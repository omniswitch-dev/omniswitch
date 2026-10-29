package gateway

import (
	"net/http"
	"strings"
	"testing"
)

// SF3: an upstream stream that closes without sending any chunk fails the
// request and is logged as an error, not as an empty success.
func TestEmptyUpstreamStreamIsAFailure(t *testing.T) {
	p := scriptedProvider{name: "test", model: "test-model"}
	h, st := newHardeningHandler(t, nil, p)
	rec := doRequest(h.ChatCompletions, "/v1/chat/completions", `{"model":"test-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("status = %d body = %s, want an error", rec.Code, rec.Body.String())
	}
	entry := onlyLog(t, st, statusError)
	if !strings.Contains(entry.ErrorMessage, "ended before completion") {
		t.Fatalf("error message = %q", entry.ErrorMessage)
	}
}
