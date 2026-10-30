package aisession

import "context"

type callerScopeKey struct{}

// WithCallerScope carries only the authenticated adapter's deployment/principal
// scope. Tool payloads and host metadata are not allowed to select this value.
func WithCallerScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, callerScopeKey{}, scope)
}
func CallerScopeFromContext(ctx context.Context) (string, bool) {
	value, ok := ctx.Value(callerScopeKey{}).(string)
	return value, ok
}
