package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
)

// SEC-3: each request log records the caller's workspace and organization
// so the dashboard can scope its reads; global callers log no tenant.
func TestRequestLogRecordsCallerTenant(t *testing.T) {
	cases := []struct {
		name          string
		identity      *Identity
		wantWorkspace string
		wantOrg       string
	}{
		{name: "no identity"},
		{name: "global identity", identity: &Identity{APIKeyID: "k0", Role: "owner"}},
		{name: "workspace", identity: &Identity{APIKeyID: "k1", WorkspaceID: "w1", OrganizationID: "o1"}, wantWorkspace: "w1", wantOrg: "o1"},
		{name: "organization", identity: &Identity{Subject: "u", OrganizationID: "o2"}, wantOrg: "o2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, st := newHardeningHandler(t, guardrail.NewEngine(), scriptedProvider{name: "openai", model: "gpt-4o-mini", content: "hello"})
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
			if tc.identity != nil {
				req = req.WithContext(ContextWithIdentity(req.Context(), *tc.identity))
			}
			rec := httptest.NewRecorder()
			h.ChatCompletions(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			entry := onlyLog(t, st, "")
			if entry.WorkspaceID != tc.wantWorkspace || entry.OrganizationID != tc.wantOrg {
				t.Fatalf("log tenant = %q/%q, want %q/%q", entry.WorkspaceID, entry.OrganizationID, tc.wantWorkspace, tc.wantOrg)
			}
		})
	}
}
