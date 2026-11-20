package router

import (
	"context"
	"errors"
)

var ErrProviderAccessDenied = errors.New("API key is not allowed to use a provider for this request")

type providerAccessKey struct{}

// WithProviderAccess restricts a request to the provider names accepted by allows.
func WithProviderAccess(ctx context.Context, allows func(string) bool) context.Context {
	return context.WithValue(ctx, providerAccessKey{}, allows)
}

func hasProviderAccessPolicy(ctx context.Context) bool {
	_, ok := ctx.Value(providerAccessKey{}).(func(string) bool)
	return ok
}

func providerAllowed(ctx context.Context, name string) bool {
	allows, ok := ctx.Value(providerAccessKey{}).(func(string) bool)
	return !ok || allows(name)
}
