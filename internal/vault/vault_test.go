package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func TestVaultLifecycle(t *testing.T) {
	st := newTestStore(t)
	v := New(st, "test-master-key")
	ctx := context.Background()
	now := time.Now().UTC()

	key := VirtualKey{
		ID:           "vk_1",
		Name:         "prod-azure",
		ProviderType: "custom",
		ProviderName: "azure-prod",
		BaseURL:      "https://example.openai.azure.com/openai/deployments/gpt-4o",
		ProviderKey:  "secret-old",
		CreatedAt:    now,
		UpdatedAt:    now,
		Enabled:      true,
		Metadata:     map[string]string{"auth_header": "api-key", "models": "gpt-4o,gpt-4o-mini"},
	}
	if err := v.Store(ctx, key); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	keys, err := v.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(keys) != 1 || keys[0].ProviderKey != "" || keys[0].Metadata["auth_header"] != "api-key" {
		t.Fatalf("List() = %+v, want redacted key with metadata", keys)
	}

	providerKey, providerType, baseURL, err := v.Resolve(ctx, "prod-azure")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if providerKey != "secret-old" || providerType != "custom" || baseURL != key.BaseURL {
		t.Fatalf("Resolve() = %q/%q/%q, want stored mapping", providerKey, providerType, baseURL)
	}

	if err := v.Rotate(ctx, "prod-azure", "secret-new"); err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	providerKey, _, _, err = v.Resolve(ctx, "prod-azure")
	if err != nil {
		t.Fatalf("Resolve(rotated) error = %v", err)
	}
	if providerKey != "secret-new" {
		t.Fatalf("rotated provider key = %q, want secret-new", providerKey)
	}

	if err := v.Revoke(ctx, "prod-azure"); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if _, _, _, err := v.Resolve(ctx, "prod-azure"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("Resolve(revoked) error = %v, want disabled error", err)
	}
}

func TestHandlerValidatesAndRedactsVirtualKeys(t *testing.T) {
	st := newTestStore(t)
	handler := NewHandler(New(st, "test-master-key"))

	body := bytes.NewBufferString(`{"name":"bad","provider_type":"custom","provider_key":"secret"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/virtual-keys", body)
	w := httptest.NewRecorder()
	handler.CreateVirtualKey(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("CreateVirtualKey(custom without base_url) status = %d, want 400", w.Code)
	}

	body = bytes.NewBufferString(`{"name":"local","provider_type":"custom","provider_name":"ollama","base_url":"http://localhost:11434/v1","metadata":{"models":"llama3.2"}}`)
	req = httptest.NewRequest(http.MethodPost, "/api/virtual-keys", body)
	w = httptest.NewRecorder()
	handler.CreateVirtualKey(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateVirtualKey() status = %d, body = %s, want 201", w.Code, w.Body.String())
	}
	var created VirtualKey
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("Unmarshal(created) error = %v", err)
	}
	if created.ProviderKey != "" || created.ProviderName != "ollama" || !created.Enabled {
		t.Fatalf("created key = %+v, want redacted enabled key", created)
	}
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	return st
}
