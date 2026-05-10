package appkey

import "context"

type contextKey struct{}
type scopeContextKey struct{}

// WithKey returns a derived context carrying the application key.
func WithKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, contextKey{}, key)
}

// FromContext returns the application key stored in ctx, if any.
func FromContext(ctx context.Context) (string, bool) {
	key, ok := ctx.Value(contextKey{}).(string)
	return key, ok && key != ""
}

// WithScopesCtx returns a derived context carrying scope restrictions.
func WithScopesCtx(ctx context.Context, scopes *KeyScopes) context.Context {
	return context.WithValue(ctx, scopeContextKey{}, scopes)
}

// ScopesFromContext returns the scope restrictions stored in ctx, or nil.
func ScopesFromContext(ctx context.Context) *KeyScopes {
	scopes, _ := ctx.Value(scopeContextKey{}).(*KeyScopes)
	return scopes
}
