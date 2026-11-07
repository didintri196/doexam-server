package auth

import (
	"context"
	"testing"
	"time"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	"doexam/pkg/metrics/metricstest"
)

func TestLoginMetrics(t *testing.T) {
	r := metricstest.Install(t)
	tests := []struct {
		name   string
		in     modelAuth.LoginInput
		mutate func(*fakeStore)
		want   string
	}{
		{"ok", modelAuth.LoginInput{Tenant: "sman1", Username: "admin", Password: "rahasia123"}, nil, loginOK},
		{"missing fields", modelAuth.LoginInput{}, nil, loginInvalidInput},
		{"unknown tenant", modelAuth.LoginInput{Tenant: "nope", Username: "admin", Password: "rahasia123"}, nil, loginUnknownAccount},
		{"unknown user", modelAuth.LoginInput{Tenant: "sman1", Username: "ghost", Password: "rahasia123"}, nil, loginUnknownAccount},
		{"wrong password", modelAuth.LoginInput{Tenant: "sman1", Username: "admin", Password: "salah12345"}, nil, loginWrongPassword},
		{"suspended", modelAuth.LoginInput{Tenant: "sman1", Username: "admin", Password: "rahasia123"}, func(s *fakeStore) {
			u := s.users[userID]
			u.Status = modelAuth.UserStatusSuspended
			s.users[userID] = u
		}, loginInactiveUser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			if tt.mutate != nil {
				tt.mutate(f.store)
			}
			before := r.Value("doexam.auth.logins", map[string]string{"result": tt.want})
			_, _ = f.svc.Login(context.Background(), tt.in, modelAudit.Client{})
			if got := r.Value("doexam.auth.logins", map[string]string{"result": tt.want}) - before; got != 1 {
				t.Fatalf("logins{result=%s} grew by %v, want 1 (points %+v)", tt.want, got, r.Points("doexam.auth.logins"))
			}
		})
	}
}

func TestAuthCacheMetrics(t *testing.T) {
	r := metricstest.Install(t)
	f := newFixture(t)
	f.svc = NewAuthService(f.store, f.tx, f.hasher, 12*time.Hour, 30*time.Second, f.svc.logger)
	f.svc.now = func() time.Time { return f.now }
	dto := f.login(t)
	for range 3 {
		if _, err := f.svc.Authenticate(context.Background(), dto.Token); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.Value("doexam.auth.cache.requests", map[string]string{"result": "miss"}); got != 1 {
		t.Errorf("misses = %v, want 1", got)
	}
	if got := r.Value("doexam.auth.cache.requests", map[string]string{"result": "hit"}); got != 2 {
		t.Errorf("hits = %v, want 2", got)
	}
	if got := r.Value("doexam.auth.cache.entries", nil); got != 1 {
		t.Errorf("entries = %v, want 1", got)
	}
}
