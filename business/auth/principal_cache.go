package auth

import (
	"slices"
	"sync"
	"time"

	modelAuth "doexam/models/auth"
)

// maxCachedPrincipals bounds memory: past it new logins are simply not cached.
const maxCachedPrincipals = 200_000

// principalCache keeps resolved principals for a short time, keyed by the SHA-256 of
// the token (DOCS/PLAN/10-BINARY-TUNGGAL.md §2.8). Invalidation bumps a per-user
// generation: an entry is valid only while its user's generation is the one read when
// the entry's database read began, so a read racing an invalidation cannot store
// stale access. Entries live ttl from that read's start, which also bounds how long
// a generation must be remembered.
type principalCache struct {
	mu        sync.Mutex
	ttl       time.Duration
	entries   map[string]principalEntry
	gens      map[string]generation // tenant:user → last invalidation
	counter   uint64                // source of generations (strictly increasing)
	lastSweep time.Time
}

type principalEntry struct {
	p       modelAuth.Principal
	userKey string
	gen     uint64
	expires time.Time
}

type generation struct {
	value  uint64
	bumped time.Time
}

// newPrincipalCache returns nil when ttl ≤ 0 (cache disabled); a nil cache misses.
func newPrincipalCache(ttl time.Duration) *principalCache {
	if ttl <= 0 {
		return nil
	}
	return &principalCache{ttl: ttl, entries: map[string]principalEntry{}, gens: map[string]generation{}}
}

func userKey(tenantID, userID string) string { return tenantID + ":" + userID }

// stamp is taken before reading the database for a token: the current generation of
// every user is unknown at that point, so it records the counter instead. Any
// invalidation after this moment gets a larger generation and so rejects the entry.
type stamp struct {
	counter uint64
	at      time.Time
}

func (c *principalCache) stamp(now time.Time) stamp {
	if c == nil {
		return stamp{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return stamp{counter: c.counter, at: now}
}

// get returns the cached principal for a token hash, if still valid at now.
func (c *principalCache) get(key string, now time.Time) (modelAuth.Principal, bool) {
	if c == nil {
		return modelAuth.Principal{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return modelAuth.Principal{}, false
	}
	if !now.Before(e.expires) || c.gens[e.userKey].value > e.gen {
		delete(c.entries, key)
		return modelAuth.Principal{}, false
	}
	return clonePrincipal(e.p), true
}

// put stores p, read from the database after st was taken, until min(st.at+ttl, sessionExpires).
func (c *principalCache) put(key string, p modelAuth.Principal, st stamp, sessionExpires, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweep(now)
	uk := userKey(p.TenantID, p.UserID)
	if c.gens[uk].value > st.counter {
		return // invalidated while we were reading: what we read may be stale
	}
	expires := st.at.Add(c.ttl)
	if sessionExpires.Before(expires) {
		expires = sessionExpires
	}
	if !now.Before(expires) {
		return
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= maxCachedPrincipals {
		return
	}
	c.entries[key] = principalEntry{p: clonePrincipal(p), userKey: uk, gen: st.counter, expires: expires}
}

// forgetUser invalidates every cached principal of the user (all their sessions).
func (c *principalCache) forgetUser(tenantID, userID string, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counter++
	c.gens[userKey(tenantID, userID)] = generation{value: c.counter, bumped: now}
}

// sweep drops expired entries and generations no live entry can predate, at most once per ttl.
func (c *principalCache) sweep(now time.Time) {
	if now.Sub(c.lastSweep) < c.ttl {
		return
	}
	c.lastSweep = now
	for k, e := range c.entries {
		if !now.Before(e.expires) || c.gens[e.userKey].value > e.gen {
			delete(c.entries, k)
		}
	}
	// an entry stamped before a bump expires within ttl of its stamp, so by bump+ttl
	// none can remain that the generation would still need to reject
	for k, g := range c.gens {
		if now.Sub(g.bumped) > c.ttl {
			delete(c.gens, k)
		}
	}
}

func (c *principalCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// clonePrincipal copies the slices so callers cannot change a cached principal.
func clonePrincipal(p modelAuth.Principal) modelAuth.Principal {
	p.Roles, p.Permissions = slices.Clone(p.Roles), slices.Clone(p.Permissions)
	return p
}
