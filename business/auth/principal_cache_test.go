package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	"doexam/pkg/errr"
)

const cacheTTL = 30 * time.Second

// countingStore counts session lookups, i.e. requests that reached the database.
type countingStore struct {
	*fakeStore
	lookups int
}

func (c *countingStore) FindSessionByHash(ctx context.Context, hash []byte) (modelAuth.AuthSessionEntity, error) {
	c.lookups++
	return c.fakeStore.FindSessionByHash(ctx, hash)
}

// cachedFixture is newFixture with the principal cache on and lookups counted.
func cachedFixture(t *testing.T) (*fixture, *countingStore) {
	t.Helper()
	f := newFixture(t)
	cs := &countingStore{fakeStore: f.store}
	f.svc.storer = cs
	f.svc.principals = newPrincipalCache(cacheTTL)
	return f, cs
}

func (f *fixture) suspend() {
	u := f.store.users[userID]
	u.Status = modelAuth.UserStatusSuspended
	f.store.users[userID] = u
}

func TestAuthCacheServesRepeatedRequests(t *testing.T) {
	f, cs := cachedFixture(t)
	token := f.login(t).Token
	for range 5 {
		p, err := f.svc.Authenticate(context.Background(), token)
		if err != nil || p.UserID != userID || !p.Has("user.manage") {
			t.Fatalf("p = %+v, err = %v", p, err)
		}
		f.now = f.now.Add(time.Second)
	}
	if cs.lookups != 1 {
		t.Fatalf("database read %d times, want 1", cs.lookups)
	}
}

func TestAuthCacheTTLBoundsUnnotifiedChanges(t *testing.T) {
	// a change made behind the service's back (e.g. directly in the database) is
	// served from the cache only until the TTL ends
	f, _ := cachedFixture(t)
	token := f.login(t).Token
	if _, err := f.svc.Authenticate(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	f.suspend()
	f.now = f.now.Add(cacheTTL - time.Second)
	if _, err := f.svc.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("within the TTL the cached login still answers: %v", err)
	}
	f.now = f.now.Add(2 * time.Second)
	if _, err := f.svc.Authenticate(context.Background(), token); !errors.Is(err, errr.ErrUnauthorized) {
		t.Fatalf("after the TTL: err = %v, want ErrUnauthorized", err)
	}
}

func TestAuthCacheLogoutAppliesAtOnce(t *testing.T) {
	f, _ := cachedFixture(t)
	token := f.login(t).Token
	p, err := f.svc.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Logout(context.Background(), p, modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(context.Background(), token); !errors.Is(err, errr.ErrUnauthorized) {
		t.Fatalf("after logout: err = %v, want ErrUnauthorized", err)
	}
}

func TestAuthCacheChangePasswordDropsOtherSessions(t *testing.T) {
	f, _ := cachedFixture(t)
	other := f.login(t).Token // another device
	current := f.login(t).Token
	if _, err := f.svc.Authenticate(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	p, err := f.svc.Authenticate(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	err = f.svc.ChangePassword(context.Background(), p,
		modelAuth.ChangePasswordInput{CurrentPassword: "rahasia123", NewPassword: "rahasiaBaru456"}, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(context.Background(), other); !errors.Is(err, errr.ErrUnauthorized) {
		t.Fatalf("other session after password change: err = %v, want ErrUnauthorized", err)
	}
	if _, err := f.svc.Authenticate(context.Background(), current); err != nil {
		t.Fatalf("the current session stays signed in: %v", err)
	}
}

func TestAuthCacheForgetUserAppliesAtOnce(t *testing.T) {
	f, _ := cachedFixture(t)
	token := f.login(t).Token
	if _, err := f.svc.Authenticate(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	f.suspend() // what UserService.Update commits
	f.svc.ForgetUser(tenantID, userID)
	if _, err := f.svc.Authenticate(context.Background(), token); !errors.Is(err, errr.ErrUnauthorized) {
		t.Fatalf("after suspension: err = %v, want ErrUnauthorized", err)
	}
}

func TestAuthCacheNeverOutlivesSession(t *testing.T) {
	f, _ := cachedFixture(t)
	token := f.login(t).Token
	for _, s := range f.store.sessions {
		s.ExpiresAt = f.now.Add(10 * time.Second) // ends well before the cache TTL
	}
	if _, err := f.svc.Authenticate(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(11 * time.Second)
	if _, err := f.svc.Authenticate(context.Background(), token); !errors.Is(err, errr.ErrUnauthorized) {
		t.Fatalf("after the session expired: err = %v, want ErrUnauthorized", err)
	}
}

func TestAuthCacheDisabled(t *testing.T) {
	f := newFixture(t) // cache TTL 0
	cs := &countingStore{fakeStore: f.store}
	f.svc.storer = cs
	token := f.login(t).Token
	for range 3 {
		if _, err := f.svc.Authenticate(context.Background(), token); err != nil {
			t.Fatal(err)
		}
	}
	if cs.lookups != 3 {
		t.Fatalf("database read %d times, want 3", cs.lookups)
	}
}

func TestPrincipalCacheRejectsReadRacingInvalidation(t *testing.T) {
	now := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	c := newPrincipalCache(cacheTTL)
	p := modelAuth.Principal{TenantID: "t1", UserID: "u1", Permissions: []string{"x"}}

	// request reads the database (old state), the change commits and invalidates,
	// then the request tries to store what it read
	st := c.stamp(now)
	c.forgetUser("t1", "u1", now)
	c.put("k", p, st, now.Add(time.Hour), now)
	if _, ok := c.get("k", now); ok {
		t.Fatal("a principal read before an invalidation must not be cached")
	}

	// an entry cached before the invalidation is rejected after it
	st = c.stamp(now)
	c.put("k", p, st, now.Add(time.Hour), now)
	if _, ok := c.get("k", now); !ok {
		t.Fatal("fresh entry missing")
	}
	c.forgetUser("t1", "u1", now)
	if _, ok := c.get("k", now); ok {
		t.Fatal("invalidated entry still served")
	}

	// another user's invalidation does not touch this user's entries
	st = c.stamp(now)
	c.put("k", p, st, now.Add(time.Hour), now)
	c.forgetUser("t1", "u2", now)
	c.forgetUser("t2", "u1", now)
	if _, ok := c.get("k", now); !ok {
		t.Fatal("unrelated invalidation dropped the entry")
	}
}

func TestPrincipalCacheForgetsGenerationsSafely(t *testing.T) {
	now := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	c := newPrincipalCache(cacheTTL)
	p := modelAuth.Principal{TenantID: "t1", UserID: "u1"}

	st := c.stamp(now) // a slow request starts reading
	c.forgetUser("t1", "u1", now)
	later := now.Add(cacheTTL + time.Second)
	c.put("other", modelAuth.Principal{TenantID: "t9", UserID: "u9"}, c.stamp(later), later.Add(time.Hour), later) // sweeps
	if len(c.gens) != 0 {
		t.Fatalf("generations older than the TTL should be dropped, have %d", len(c.gens))
	}
	// the slow request finishes only now: its read began before the invalidation,
	// so it must not be stored even though the generation is gone
	c.put("k", p, st, later.Add(time.Hour), later)
	if _, ok := c.get("k", later); ok {
		t.Fatal("a read stamped before a dropped generation was cached")
	}
}

func TestPrincipalCacheReturnsCopies(t *testing.T) {
	now := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	c := newPrincipalCache(cacheTTL)
	c.put("k", modelAuth.Principal{TenantID: "t1", UserID: "u1", Permissions: []string{"exam.read"}}, c.stamp(now), now.Add(time.Hour), now)
	got, _ := c.get("k", now)
	got.Permissions[0] = "tenant.manage"
	again, _ := c.get("k", now)
	if again.Permissions[0] != "exam.read" {
		t.Fatal("a caller changed the cached principal")
	}
	if c.len() != 1 {
		t.Fatalf("len = %d", c.len())
	}
}
