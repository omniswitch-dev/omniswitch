package vault

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
)

// TestVirtualKeysAreGlobalOnly covers SEC-3: virtual keys register
// providers in the gateway-wide registry, so tenant-scoped callers may not
// list, create, rotate or revoke them.
func TestVirtualKeysAreGlobalOnly(t *testing.T) {
	h := NewHandler(New(newTestStore(t), "test-master-key"))
	ops := []struct {
		name   string
		fn     http.HandlerFunc
		method string
		target string
		body   string
		ok     int
	}{
		{"create", h.CreateVirtualKey, http.MethodPost, "/api/virtual-keys", `{"name":"k","provider_type":"openai","provider_key":"sk-1"}`, http.StatusCreated},
		{"list", h.ListVirtualKeys, http.MethodGet, "/api/virtual-keys", "", http.StatusOK},
		{"rotate", h.RotateVirtualKey, http.MethodPost, "/api/virtual-keys/rotate", `{"name":"k","provider_key":"sk-2"}`, http.StatusOK},
		{"revoke", h.RevokeVirtualKey, http.MethodDelete, "/api/virtual-keys?name=k", "", http.StatusOK},
	}
	callers := []struct {
		name     string
		identity *gateway.Identity
		global   bool
	}{
		{"organization", &gateway.Identity{Role: "owner", OrganizationID: "org_a"}, false},
		{"workspace", &gateway.Identity{Role: "owner", WorkspaceID: "ws_a", OrganizationID: "org_a"}, false},
		{"global", &gateway.Identity{Role: "admin"}, true},
	}
	for _, caller := range callers {
		for _, op := range ops {
			t.Run(caller.name+"/"+op.name, func(t *testing.T) {
				r := httptest.NewRequest(op.method, op.target, strings.NewReader(op.body))
				r = r.WithContext(gateway.ContextWithIdentity(r.Context(), *caller.identity))
				rec := httptest.NewRecorder()
				op.fn(rec, r)
				want := http.StatusForbidden
				if caller.global {
					want = op.ok
				}
				if rec.Code != want {
					t.Fatalf("status = %d, want %d; body = %s", rec.Code, want, rec.Body.String())
				}
				if !caller.global && !strings.Contains(rec.Body.String(), "only global callers") {
					t.Fatalf("403 body = %s, want a clear message", rec.Body.String())
				}
			})
		}
	}
}
