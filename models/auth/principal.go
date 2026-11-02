package auth

import (
	"context"
	"slices"
)

// Principal is the authenticated caller of a request.
// Services use it for authorization (role + tenant + ownership checks).
type Principal struct {
	TenantID    string
	UserID      string
	SessionID   string
	Roles       []string
	Permissions []string
}

// Has reports whether the principal holds perm.
func (p Principal) Has(perm string) bool {
	return slices.Contains(p.Permissions, perm)
}

type principalCtxKey struct{}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// PrincipalFrom returns the principal stored in ctx, if any.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalCtxKey{}).(Principal)
	return p, ok
}
