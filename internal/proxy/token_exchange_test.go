package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestTokenExchangeIsPerSubjectAndCached(t *testing.T) {
	var mu sync.Mutex
	exchanges := map[string]int{}
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		subject := r.PostForm.Get("subject_token")
		mu.Lock()
		exchanges[subject]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"exchanged-%s","expires_in":3600}`, subject)
	}))
	defer tokenServer.Close()

	var seenMu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		seenMu.Unlock()
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer upstream.Close()

	h, err := NewMultiHandler(defaultEngine(t), nil, []TargetConfig{{
		Name: "default", Upstream: upstream.URL,
		TokenExchange: &TokenExchangeConfig{TokenURL: tokenServer.URL, ClientID: "c", ClientSecret: "s", Audience: "mcp"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, method string) {
		serve(h, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "Authorization", "Bearer "+token, "x-omniswitch-auth-method", method)
	}
	call("alice", "oidc")
	call("bob", "oidc")
	call("alice", "oidc")
	call("gateway-api-key", "api_key")

	want := []string{"Bearer exchanged-alice", "Bearer exchanged-bob", "Bearer exchanged-alice", ""}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("upstream Authorization = %q, want %q", seen, want)
	}
	if exchanges["alice"] != 1 || exchanges["bob"] != 1 || exchanges["gateway-api-key"] != 0 {
		t.Fatalf("exchanges = %v, want one per OIDC subject and none for API keys", exchanges)
	}
}
