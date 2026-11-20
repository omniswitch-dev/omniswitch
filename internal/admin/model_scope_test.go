package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func TestCreatedKeyCannotBroadenModelAccess(t *testing.T) {
	h := New(newAdminTestStore(t))
	for _, tc := range []struct {
		models string
		status int
		want   int
	}{
		{``, 201, 2}, {`,"allowed_models":[]`, 201, 2},
		{`,"allowed_models":["a"]`, 201, 1}, {`,"allowed_models":["blocked"]`, 403, 0},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(`{"name":"child","role":"member"`+tc.models+`}`))
		req = req.WithContext(gateway.ContextWithIdentity(req.Context(), gateway.Identity{Role: "admin", AllowedModels: []string{"a", "b"}}))
		rec := httptest.NewRecorder()
		h.CreateAPIKey(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("create %s = %d/%s", tc.models, rec.Code, rec.Body)
		}
		if tc.status == 201 {
			var created CreateKeyResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			if len(created.APIKey.AllowedModels) != tc.want {
				t.Fatalf("child access = %v", created.APIKey.AllowedModels)
			}
		}
	}
}

func TestCreatedKeyCannotBroadenProviderAccess(t *testing.T) {
	st := newAdminTestStore(t)
	h := New(st)
	for _, tc := range []struct {
		providers string
		status    int
		want      []string
	}{
		{``, 201, []string{"@approved", "openai"}},
		{`,"allowed_providers":[]`, 201, []string{"@approved", "openai"}},
		{`,"allowed_providers":["@Approved","@approved"]`, 201, []string{"@approved"}},
		{`,"allowed_providers":["private"]`, 403, nil},
		{`,"allowed_providers":["approved"]`, 403, nil},
		{`,"allowed_providers":[" openai"]`, 400, nil},
		{`,"allowed_providers":[""]`, 400, nil},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(`{"name":"child","role":"member"`+tc.providers+`}`))
		req = req.WithContext(gateway.ContextWithIdentity(req.Context(), gateway.Identity{
			Role: "admin", AllowedProviders: []string{"@approved", "openai"},
		}))
		rec := httptest.NewRecorder()
		h.CreateAPIKey(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("create %s = %d/%s", tc.providers, rec.Code, rec.Body)
		}
		if rec.Code != 201 {
			continue
		}
		var created CreateKeyResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(created.APIKey.AllowedProviders, tc.want) {
			t.Fatalf("created key providers = %v, want %v", created.APIKey.AllowedProviders, tc.want)
		}
		saved, err := st.GetAPIKeyByID(req.Context(), created.APIKey.ID)
		if err != nil || !slices.Equal(saved.AllowedProviders, tc.want) {
			t.Fatalf("saved key providers = %v, error = %v, want %v", saved.AllowedProviders, err, tc.want)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	rec := httptest.NewRecorder()
	h.ListAPIKeys(rec, req)
	var list struct {
		Keys []store.APIKey
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Keys) != 3 {
		t.Fatalf("listed %d keys, want 3", len(list.Keys))
	}
	for _, key := range list.Keys {
		if !slices.Contains(key.AllowedProviders, "@approved") {
			t.Fatalf("listed key %s lost provider scope", key.ID)
		}
	}
}
