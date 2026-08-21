package principal

import "context"

type Principal struct {
	UserID      int64
	Role        string
	SessionID   string
	AuthVersion int64
}

type contextKey struct{}

func WithContext(ctx context.Context, value Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, value)
}

func FromContext(ctx context.Context) (Principal, bool) {
	value, ok := ctx.Value(contextKey{}).(Principal)
	return value, ok
}

func IsAdmin(value Principal) bool { return value.Role == "admin" }
