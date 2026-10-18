package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/policy"
)

type openAPIRequest struct {
	method, rawPath, path, query, body string
	header                             http.Header
}

func openAPIUpstream(t *testing.T) (*httptest.Server, func() []openAPIRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []openAPIRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, openAPIRequest{r.Method, r.URL.RawPath, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Clone()})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	return server, func() []openAPIRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]openAPIRequest(nil), seen...)
	}
}

func writeSpec(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const yamlSpec = `openapi: 3.0.0
components:
  parameters:
    Org:
      name: org
      in: header
      required: true
      schema:
        type: string
  schemas:
    Pet:
      type: object
      properties:
        name:
          type: string
        owner:
          $ref: '#/components/schemas/Pet'
paths:
  /users/{id}:
    parameters:
      - name: id
        in: path
        required: true
        schema:
          type: string
    get:
      operationId: getUser
      parameters:
        - $ref: '#/components/parameters/Org'
        - name: verbose
          in: query
          schema:
            type: boolean
    delete:
      operationId: deleteUser
  /pets:
    post:
      operationId: createPet
      requestBody:
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/Pet'
`

func TestOpenAPIYAMLRefsAndParameterRouting(t *testing.T) {
	upstream, seen := openAPIUpstream(t)
	h, err := NewMultiHandler(defaultEngine(t), nil, []TargetConfig{{Name: "api", Upstream: upstream.URL, OpenAPISpec: writeSpec(t, "spec.yaml", yamlSpec)}})
	if err != nil {
		t.Fatalf("NewMultiHandler() error = %v", err)
	}
	defer h.Close()

	tools := h.targets["api"].openAPITools
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "createPet,deleteUser,getUser" {
		t.Fatalf("tools = %v, want sorted createPet,deleteUser,getUser", names)
	}
	get := findOpenAPITool(tools, "getUser")
	if get == nil || len(get.Params) != 3 {
		t.Fatalf("getUser params = %+v, want path-level id plus org and verbose", get)
	}

	serve(h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"api__getUser","arguments":{"id":"u 1","org":"acme","verbose":true}}}`)
	requests := seen()
	if len(requests) != 1 {
		t.Fatalf("upstream calls = %d", len(requests))
	}
	got := requests[0]
	if got.method != http.MethodGet || got.path != "/users/u 1" || got.query != "verbose=true" || got.header.Get("org") != "acme" {
		t.Fatalf("GET routing = %+v", got)
	}

	serve(h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"api__createPet","arguments":{"body":{"name":"rex"}}}}`)
	if got := seen()[1]; got.method != http.MethodPost || strings.TrimSpace(got.body) != `{"name":"rex"}` {
		t.Fatalf("POST body = %+v, want unwrapped body", got)
	}
}

func TestOpenAPIRejectsPathTraversalAndMissingParams(t *testing.T) {
	upstream, seen := openAPIUpstream(t)
	h, err := NewMultiHandler(defaultEngine(t), nil, []TargetConfig{{Name: "api", Upstream: upstream.URL, OpenAPISpec: writeSpec(t, "spec.yaml", yamlSpec)}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	for _, args := range []string{`{"id":"../admin/delete?all=true","org":"a"}`, `{"id":"a/b","org":"a"}`, `{"id":"x"}`, `{"id":"x","org":"a\r\nX-Evil: 1"}`} {
		rec := serve(h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"api__getUser","arguments":`+args+`}}`)
		if !strings.Contains(rec.Body.String(), `"isError":true`) {
			t.Fatalf("args %s: response %s, want tool error", args, rec.Body.String())
		}
	}
	if requests := seen(); len(requests) != 0 {
		t.Fatalf("unsafe calls reached upstream: %+v", requests)
	}
}

func TestOpenAPIDeleteOperationIsPolicyAction(t *testing.T) {
	upstream, seen := openAPIUpstream(t)
	engine, err := policy.NewEngine(policy.Rule{
		Name:       "no-openapi-deletes",
		Effect:     "deny",
		Expression: `action.name == "delete" && target.name == "api"`,
		Reason:     "deletes are blocked",
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	h, err := NewMultiHandler(engine, nil, []TargetConfig{{Name: "api", Upstream: upstream.URL, OpenAPISpec: writeSpec(t, "spec.yaml", yamlSpec)}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	rec := serve(h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"api__deleteUser","arguments":{"id":"u1"}}}`)
	if !strings.Contains(rec.Body.String(), "deletes are blocked") || len(seen()) != 0 {
		t.Fatalf("DELETE operation not denied: %s", rec.Body.String())
	}
	serve(h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"api__getUser","arguments":{"id":"u1","org":"a"}}}`)
	if len(seen()) != 1 {
		t.Fatalf("GET operation was not forwarded")
	}
}

func TestOpenAPIDuplicateToolNamesAreRejected(t *testing.T) {
	_, err := parseOpenAPISpec([]byte(`{"openapi":"3.0.0","paths":{"/a":{"get":{"operationId":"same"}},"/b":{"get":{"operationId":"same"}}}}`))
	if err == nil || !strings.Contains(err.Error(), "same") {
		t.Fatalf("parseOpenAPISpec() error = %v, want duplicate tool error", err)
	}
}
