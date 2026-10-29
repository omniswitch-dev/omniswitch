package feedback

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

var (
	globalCaller = &gateway.Identity{APIKeyID: "k0", Role: "owner"}
	orgO1Caller  = &gateway.Identity{Subject: "u", OrganizationID: "o1", Role: "admin"}
	wsW1Caller   = &gateway.Identity{APIKeyID: "k1", WorkspaceID: "w1", OrganizationID: "o1", Role: "viewer"}
	wsW2Caller   = &gateway.Identity{APIKeyID: "k2", WorkspaceID: "w2", OrganizationID: "o1", Role: "viewer"}
	wsW3Caller   = &gateway.Identity{APIKeyID: "k3", WorkspaceID: "w3", OrganizationID: "o2", Role: "viewer"}
)

// newTenantFeedback returns a handler over a store with request r1 (trace
// t1) in workspace w1, r2 (t2) in w2, both in organization o1, r3 (t3) in
// w3 of organization o2, and r0 (t0) without a tenant.
func newTenantFeedback(t *testing.T) *Handler {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, row := range []store.RequestLog{
		{ID: "r0", TraceID: "t0"},
		{ID: "r1", TraceID: "t1", WorkspaceID: "w1", OrganizationID: "o1"},
		{ID: "r2", TraceID: "t2", WorkspaceID: "w2", OrganizationID: "o1"},
		{ID: "r3", TraceID: "t3", WorkspaceID: "w3", OrganizationID: "o2"},
	} {
		row.Timestamp = time.Now()
		if err := st.InsertLog(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
	return New(st)
}

func feedbackRequest(identity *gateway.Identity, method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if identity != nil {
		req = req.WithContext(gateway.ContextWithIdentity(req.Context(), *identity))
	}
	return req
}

func createAs(h *Handler, identity *gateway.Identity, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.CreateFeedback(rec, feedbackRequest(identity, http.MethodPost, "/api/feedback", body))
	return rec
}

func listAs(t *testing.T, h *Handler, identity *gateway.Identity, query string) []store.Feedback {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ListFeedback(rec, feedbackRequest(identity, http.MethodGet, "/api/feedback"+query, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("ListFeedback() = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Feedback []store.Feedback `json:"feedback"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Feedback
}

func TestCreateFeedbackChecksReferencedTenant(t *testing.T) {
	h := newTenantFeedback(t)
	cases := []struct {
		name     string
		identity *gateway.Identity
		body     string
		want     int
	}{
		{"auth off names any request", nil, `{"request_id":"r3","score":1}`, http.StatusCreated},
		{"global names an unlogged request", globalCaller, `{"request_id":"later","score":1}`, http.StatusCreated},
		{"global names a scoped trace", globalCaller, `{"trace_id":"t2","score":1}`, http.StatusCreated},
		{"workspace own request", wsW1Caller, `{"request_id":"r1","score":1}`, http.StatusCreated},
		{"workspace own trace", wsW1Caller, `{"trace_id":"t1","score":-1}`, http.StatusCreated},
		{"workspace other workspace request", wsW1Caller, `{"request_id":"r2","score":1}`, http.StatusNotFound},
		{"workspace other organization trace", wsW1Caller, `{"trace_id":"t3","score":1}`, http.StatusNotFound},
		{"workspace legacy request", wsW1Caller, `{"request_id":"r0","score":1}`, http.StatusNotFound},
		{"workspace missing request", wsW1Caller, `{"request_id":"nope","score":1}`, http.StatusNotFound},
		{"workspace own request, foreign trace", wsW1Caller, `{"request_id":"r1","trace_id":"t2","score":1}`, http.StatusNotFound},
		{"organization request in a workspace of it", orgO1Caller, `{"request_id":"r2","score":0}`, http.StatusCreated},
		{"organization request of another organization", orgO1Caller, `{"request_id":"r3","score":0}`, http.StatusNotFound},
		{"other organization own request", wsW3Caller, `{"request_id":"r3","score":1}`, http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := createAs(h, tc.identity, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("CreateFeedback() = %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
			if tc.want == http.StatusNotFound && strings.Contains(rec.Body.String(), "w2") {
				t.Fatalf("404 body %s leaks the other tenant", rec.Body.String())
			}
		})
	}

	ids := func(entries []store.Feedback) []string {
		out := []string{}
		for _, e := range entries {
			out = append(out, e.RequestID+e.TraceID)
		}
		sort.Strings(out)
		return out
	}
	// Feedback carries the submitting caller's tenant and lists stay in it.
	for _, tc := range []struct {
		name     string
		identity *gateway.Identity
		want     string
	}{
		{"global", globalCaller, "[later r1 r2 r3 r3 t1 t2]"},
		{"organization o1", orgO1Caller, "[r1 r2 t1]"},
		{"workspace w1", wsW1Caller, "[r1 t1]"},
		{"workspace w2", wsW2Caller, "[]"},
		{"workspace w3", wsW3Caller, "[r3]"},
	} {
		got := ids(listAs(t, h, tc.identity, ""))
		if strings.Join(got, " ") != strings.Trim(tc.want, "[]") {
			t.Errorf("%s ListFeedback() = %v, want %s", tc.name, got, tc.want)
		}
	}
	// A filter on another tenant's request returns nothing.
	if got := listAs(t, h, wsW2Caller, "?request_id=r1"); len(got) != 0 {
		t.Fatalf("w2 ListFeedback(request_id=r1) = %+v, want none", got)
	}
	got := listAs(t, h, wsW1Caller, "?request_id=r1")
	if len(got) != 1 || got[0].WorkspaceID != "w1" || got[0].OrganizationID != "o1" {
		t.Fatalf("w1 ListFeedback(request_id=r1) = %+v, want one entry with the caller's tenant", got)
	}
}
