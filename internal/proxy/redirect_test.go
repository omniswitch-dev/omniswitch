package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type hitRecorder struct {
	mu    sync.Mutex
	hits  []string
	extra []string
}

func (h *hitRecorder) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hits = append(h.hits, r.URL.Path+" key="+r.Header.Get("X-Api-Key"))
	h.extra = append(h.extra, string(body))
}

func (h *hitRecorder) snapshot() ([]string, []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.hits...), append([]string(nil), h.extra...)
}

func TestUpstreamRedirectsStayOnOrigin(t *testing.T) {
	other := &hitRecorder{}
	otherServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other.record(r)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stolen":true}}`))
	}))
	t.Cleanup(otherServer.Close)
	origin := &hitRecorder{}
	originServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin.record(r)
		switch r.URL.Path {
		case "/cross":
			http.Redirect(w, r, otherServer.URL+"/steal", http.StatusTemporaryRedirect)
		case "/same":
			http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
		}
	}))
	t.Cleanup(originServer.Close)

	for _, tc := range []struct {
		path      string
		wantError bool
	}{{"/cross", true}, {"/same", false}} {
		h, err := NewMultiHandler(defaultEngine(t), nil, []TargetConfig{{
			Name: "default", Upstream: originServer.URL + tc.path, Headers: map[string]string{"X-Api-Key": "operator-secret"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		rec := serve(h, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
		if tc.wantError != (rec.Code == http.StatusBadGateway) {
			t.Fatalf("%s: status %d body %s", tc.path, rec.Code, rec.Body.String())
		}
		if !tc.wantError && !strings.Contains(rec.Body.String(), `"ok":true`) {
			t.Fatalf("same-origin redirect not followed: %s", rec.Body.String())
		}
	}
	if hits, _ := other.snapshot(); len(hits) != 0 {
		t.Fatalf("cross-origin redirect reached another host: %v", hits)
	}
	if hits, _ := origin.snapshot(); len(hits) != 3 || hits[2] != "/final key=operator-secret" {
		t.Fatalf("origin hits = %v", hits)
	}
}

func TestTokenExchangeDoesNotFollowCrossOriginRedirects(t *testing.T) {
	other := &hitRecorder{}
	otherServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other.record(r)
		_, _ = w.Write([]byte(`{"access_token":"stolen","expires_in":3600}`))
	}))
	t.Cleanup(otherServer.Close)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, otherServer.URL+"/token", http.StatusPermanentRedirect)
	}))
	t.Cleanup(tokenServer.Close)
	upstream := &recordingUpstream{}
	h, err := NewMultiHandler(defaultEngine(t), nil, []TargetConfig{{
		Name: "default", Upstream: upstream.server(t).URL,
		TokenExchange: &TokenExchangeConfig{TokenURL: tokenServer.URL, ClientID: "c", ClientSecret: "very-secret"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(h, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "Authorization", "Bearer alice", "x-omniswitch-auth-method", "oidc")
	if rec.Code != http.StatusBadGateway || upstream.count() != 0 {
		t.Fatalf("status %d upstream calls %d: %s", rec.Code, upstream.count(), rec.Body.String())
	}
	if _, bodies := other.snapshot(); len(bodies) != 0 {
		t.Fatalf("client secret sent to another host: %v", bodies)
	}
}

func TestSameOrigin(t *testing.T) {
	parse := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"https://api.example.com/a", "https://API.example.com:443/b", true},
		{"http://api.example.com/a", "https://api.example.com/a", false},
		{"https://api.example.com/a", "https://evil.example.com/a", false},
		{"https://api.example.com:8443/a", "https://api.example.com/a", false},
	} {
		if got := sameOrigin(parse(tc.a), parse(tc.b)); got != tc.want {
			t.Errorf("sameOrigin(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
