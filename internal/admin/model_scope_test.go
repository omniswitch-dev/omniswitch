package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
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
