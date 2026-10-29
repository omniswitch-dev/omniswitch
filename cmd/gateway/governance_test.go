package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
)

func TestExternalAuthorizationRuntimeWiring(t *testing.T) {
	var calls atomic.Int32
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"allowed":false,"message":"external denial"}`)
	}))
	defer policy.Close()
	st := newTestStore(t)
	settings := runtimeSettings{authEnabled: true, bootstrapAPIKey: routeTestKey, rateLimitRequests: 100, rateLimitWindow: time.Minute}
	applyGatewayConfig(&settings, gatewayconfig.Config{Authorization: gatewayconfig.Authorization{External: gatewayconfig.ExternalAuthZ{URL: policy.URL, Timeout: &gatewayconfig.Duration{Duration: time.Second}}}})
	if err := ensureBootstrapKey(context.Background(), st, settings); err != nil {
		t.Fatal(err)
	}
	h, closeLimiter, err := wrapGatewayMiddleware(settings, st, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("external denial reached handler") }))
	if err != nil {
		t.Fatal(err)
	}
	defer closeLimiter()
	rec := serveRoute(h, http.MethodGet, "/v1/models", true)
	if rec.Code != 403 || calls.Load() != 1 {
		t.Fatalf("status/calls = %d/%d; body=%s", rec.Code, calls.Load(), rec.Body)
	}
}

func TestMergeRouteEnrichmentCanBeCleared(t *testing.T) {
	base := router.Route{Provider: "test", PrependMessages: []provider.Message{{Role: "system", Content: "base"}}, AppendMessages: []provider.Message{{Role: "user", Content: "base"}}}
	merged := mergeRoute(base, router.Route{PrependMessages: []provider.Message{}})
	if len(merged.PrependMessages) != 0 || len(merged.AppendMessages) != 1 {
		t.Fatalf("merged = %+v", merged)
	}
}
