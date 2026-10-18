package dashboard

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// GW-19: /api/configs reports every change to the config change hook, so the
// gateway can drop its cached config routers at once.
func TestConfigChangeHookCalledOnWrites(t *testing.T) {
	st := newDashboardTestStore(t)
	h := New(st)
	var calls [][]string
	h.SetConfigChangeHook(func(refs ...string) { calls = append(calls, refs) })
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	serve := func(method, path, body string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec.Code
	}
	body := "apiVersion: omniswitch.dev/v1\nkind: GatewayConfig\nroutes:\n  m:\n    provider: openai\n"
	if code := serve(http.MethodPost, "/api/configs", `{"id":"cfg_hook","name":"hooked","format":"yaml","body":`+strconvQuote(body)+`}`); code != http.StatusCreated {
		t.Fatalf("create status = %d", code)
	}
	if code := serve(http.MethodPatch, "/api/configs?name=hooked", `{"enabled":false}`); code != http.StatusOK {
		t.Fatalf("patch status = %d", code)
	}
	if code := serve(http.MethodDelete, "/api/configs?id=cfg_hook", ""); code != http.StatusOK {
		t.Fatalf("delete status = %d", code)
	}
	if code := serve(http.MethodDelete, "/api/configs?id=missing", ""); code != http.StatusNotFound {
		t.Fatalf("delete missing status = %d", code)
	}
	want := [][]string{{"cfg_hook", "hooked"}, {"hooked"}, {"cfg_hook"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("hook calls = %v, want %v", calls, want)
	}
}
