package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func TestAPIKeyModelAccessAcrossInferenceEndpoints(t *testing.T) {
	st := newGatewayTestStore(t)
	const rawKey = "sk-model-access"
	hash := sha256.Sum256([]byte(rawKey))
	if err := st.InsertAPIKey(context.Background(), store.APIKey{ID: "restricted", Name: "restricted", KeyHash: hex.EncodeToString(hash[:]), CreatedAt: time.Now(), Enabled: true, AllowedModels: []string{"allowed"}}); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"chat/completions", "responses", "messages", "embeddings", "rerank", "moderations"} {
		for _, model := range []string{"allowed", "blocked"} {
			t.Run(endpoint+"/"+model, func(t *testing.T) {
				body := `{"model":"` + model + `","stream":true}`
				next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					data, _ := io.ReadAll(r.Body)
					if string(data) != body {
						t.Errorf("body = %s", data)
					}
					identity, _ := IdentityFromContext(r.Context())
					if len(identity.AllowedModels) != 1 || identity.AllowedModels[0] != "allowed" {
						t.Errorf("identity = %+v", identity)
					}
					w.WriteHeader(204)
				})
				handler := NewAuthMiddleware(st, true).Wrap(NewAuthorizationMiddleware(true).Wrap(next))
				req := httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+rawKey)
				req.Header.Set("x-omniswitch-allowed-models", "blocked")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				want := 204
				if model == "blocked" {
					want = 403
				}
				if rec.Code != want {
					t.Fatalf("status/body = %d/%s; want %d", rec.Code, rec.Body, want)
				}
			})
		}
	}
}

func TestModelDiscoveryFiltersKeysAndIncludesAliases(t *testing.T) {
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "test", model: "test-model"})
	rtr := router.New(registry)
	rtr.SetRoute("public-alias", router.Route{Provider: "test"})
	h := New(registry, rtr, nil, nil)
	for _, tc := range []struct {
		allowed []string
		want    []string
	}{
		{nil, []string{"public-alias", "test-model"}},
		{[]string{"public-alias"}, []string{"public-alias"}},
		{[]string{"unavailable"}, []string{}},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req = req.WithContext(withIdentity(req.Context(), Identity{AllowedModels: tc.allowed}))
		rec := httptest.NewRecorder()
		h.ListModels(rec, req)
		var result struct {
			Data []provider.ModelInfo `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Data) != len(tc.want) {
			t.Fatalf("models = %s", rec.Body)
		}
		for i, model := range result.Data {
			if model.ID != tc.want[i] {
				t.Fatalf("models = %s", rec.Body)
			}
		}
	}
}
