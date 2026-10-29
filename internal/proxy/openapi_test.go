package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/policy"
)

const sampleSpec = `{
  "openapi": "3.0.0",
  "paths": {
    "/pets/{petId}": {
      "get": {
        "operationId": "getPetById",
        "summary": "Find a pet by ID",
        "parameters": [
          {"name": "petId", "in": "path", "required": true, "schema": {"type": "integer"}}
        ]
      }
    },
    "/pets": {
      "post": {
        "operationId": "addPet",
        "summary": "Add a new pet",
        "requestBody": {"content": {"application/json": {"schema": {"type": "object"}}}},
        "responses": {}
      }
    }
  }
}`

func TestOpenAPIFederation(t *testing.T) {
	var gotPath, gotMethod, gotBody string
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		raw := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(raw)
		gotBody = string(raw)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer rest.Close()

	specServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleSpec))
	}))
	defer specServer.Close()

	engine, err := policy.NewEngine()
	if err != nil {
		t.Fatalf("policy engine: %v", err)
	}
	handler, err := NewMultiHandler(engine, nil, []TargetConfig{{
		Name:        "pets",
		Transport:   "http",
		Upstream:    rest.URL,
		OpenAPISpec: specServer.URL,
	}})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	defer handler.Close()

	// tools/list must expose both synthetic tools, namespaced.
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list status %d", rec.Code)
	}
	var listResp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range listResp.Result.Tools {
		names[tool.Name] = true
	}
	if !names["pets__getPetById"] || !names["pets__addPet"] {
		t.Fatalf("expected namespaced OpenAPI tools, got %v", names)
	}

	// tools/call with path substitution executes real HTTP.
	callBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"pets__getPetById","arguments":{"petId":42}}}`
	req = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(callBody))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/call status %d", rec.Code)
	}
	if gotMethod != http.MethodGet || gotPath != "/pets/42" {
		t.Fatalf("upstream saw %s %s, want GET /pets/42", gotMethod, gotPath)
	}
	var callResp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &callResp); err != nil {
		t.Fatalf("unmarshal call: %v", err)
	}
	if callResp.Result.IsError || !strings.Contains(callResp.Result.Content[0].Text, `"ok":true`) {
		t.Fatalf("unexpected result: %+v", callResp.Result)
	}

	// POST tool maps arguments to a JSON body.
	postBody := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"pets__addPet","arguments":{"body":{"name":"Rex"}}}}`
	req = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(postBody))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if gotMethod != http.MethodPost || gotPath != "/pets" || !strings.Contains(gotBody, `"name":"Rex"`) {
		t.Fatalf("POST mapping wrong: %s %s body=%s", gotMethod, gotPath, gotBody)
	}
}

func TestOpenAPIToolListUsesTargetNameAndAudits(t *testing.T) {
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer rest.Close()
	specServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleSpec))
	}))
	defer specServer.Close()

	engine, err := policy.NewEngine(policy.Rule{Name: "hide-add", Expression: `tool.name == "addPet"`})
	if err != nil {
		t.Fatal(err)
	}
	auditor := &memoryAuditor{}
	handler, err := NewMultiHandler(engine, auditor, []TargetConfig{{
		Name: "Pets", Transport: "http", Upstream: rest.URL, OpenAPISpec: specServer.URL,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)))
	body := rec.Body.String()
	if !strings.Contains(body, `"Pets__getPetById"`) || strings.Contains(body, "addPet") {
		t.Fatalf("tools/list = %s, want only Pets__getPetById", body)
	}
	decisions := map[string]string{}
	for _, event := range auditor.events {
		decisions[event.Tool] = event.Decision
	}
	if decisions["getPetById"] != "ALLOW" || decisions["addPet"] != "DENY" {
		t.Fatalf("synthetic tool audit = %v", decisions)
	}

	// The listed name is callable.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"Pets__getPetById","arguments":{"petId":1}}}`)))
	if !strings.Contains(rec.Body.String(), `ok`) || strings.Contains(rec.Body.String(), `"isError":true`) {
		t.Fatalf("tools/call = %s", rec.Body.String())
	}
}
