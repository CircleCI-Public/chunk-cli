package session

import "context"

// key is unexported so no other package can construct it, guaranteeing no collisions.
type key struct{}

// WithID returns a new context carrying the given Claude session ID.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, key{}, id)
}

// IDFromCtx returns the Claude session ID stored in ctx, or "" if not set.
func IDFromCtx(ctx context.Context) string {
	id, _ := ctx.Value(key{}).(string)
	return id
}
