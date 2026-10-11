package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

type fakeHealthProvider struct{ name string }

func (f fakeHealthProvider) Name() string { return f.name }
func (f fakeHealthProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	return provider.ChatResponse{}, provider.ProviderMeta{}, nil
}
func (f fakeHealthProvider) Models() []provider.ModelInfo { return nil }

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestHealthLivenessAlwaysOK(t *testing.T) {
	h := newHealthState(nil, nil, false)
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	h.liveness(rec, req)
	if rec.Code != 200 {
		t.Fatalf("liveness status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("body = %v, want status ok", body)
	}
}

func TestHealthLivenessRejectsPost(t *testing.T) {
	h := newHealthState(nil, nil, false)
	req := httptest.NewRequest("POST", "/healthz", nil)
	rec := httptest.NewRecorder()
	h.liveness(rec, req)
	if rec.Code != 405 {
		t.Fatalf("liveness POST status = %d, want 405", rec.Code)
	}
}

func TestHealthReadinessReadyWithStoreAndProvider(t *testing.T) {
	st := newTestStore(t)
	registry := provider.NewRegistry()
	registry.Register(fakeHealthProvider{name: "test"})

	h := newHealthState(st, registry, false)
	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	h.readiness(rec, req)
	if rec.Code != 200 {
		t.Fatalf("readiness status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHealthReadinessNotReadyWithoutProviders(t *testing.T) {
	st := newTestStore(t)
	registry := provider.NewRegistry()

	h := newHealthState(st, registry, false)
	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	h.readiness(rec, req)
	if rec.Code != 503 {
		t.Fatalf("readiness status = %d, want 503", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["status"] != "not ready" {
		t.Fatalf("body = %v, want status not ready", body)
	}
	reasons, ok := body["reasons"].([]any)
	if !ok || len(reasons) == 0 {
		t.Fatalf("body reasons = %v, want at least one reason", body["reasons"])
	}
}

func TestHealthReadinessAllowNoProvidersBypassesProviderCheck(t *testing.T) {
	st := newTestStore(t)
	registry := provider.NewRegistry()

	h := newHealthState(st, registry, true)
	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	h.readiness(rec, req)
	if rec.Code != 200 {
		t.Fatalf("readiness status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHealthReadinessNotReadyWithUnreachableStore(t *testing.T) {
	st := newTestStore(t)
	st.Close() // closing the underlying db makes Ping fail.
	registry := provider.NewRegistry()
	registry.Register(fakeHealthProvider{name: "test"})

	h := newHealthState(st, registry, false)
	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	h.readiness(rec, req)
	if rec.Code != 503 {
		t.Fatalf("readiness status = %d, want 503, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	reasons, _ := body["reasons"].([]any)
	if len(reasons) == 0 {
		t.Fatalf("body reasons = %v, want a store failure reason", body["reasons"])
	}
}
