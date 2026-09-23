package agent

import (
	"context"
)

// Identity is the trusted caller identity for one run. It is set by
// framework middleware the model cannot influence; model-supplied tenant or
// user fields are data, never authorization input. See IdentityFromContext.
type Identity struct {
	UserID   string
	TenantID string
}

type identityContextKey struct{}

// WithIdentity stores trusted identity for downstream authorization.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, identityContextKey{}, identity)
}

// IdentityFromContext returns the trusted identity, or false when no
// middleware established one. Runs without identity are rejected.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	if ctx == nil {
		return Identity{}, false
	}
	identity, ok := ctx.Value(identityContextKey{}).(Identity)
	if !ok || identity.UserID == "" {
		return Identity{}, false
	}
	return identity, true
}
