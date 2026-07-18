package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/omniswitch-dev/omniswitch/internal/adapter/mcp"
	"github.com/omniswitch-dev/omniswitch/internal/audit"
	"github.com/omniswitch-dev/omniswitch/internal/model"
	"github.com/omniswitch-dev/omniswitch/internal/policy"
)

// TargetConfig defines a named upstream MCP server. A configured target is
// exposed through the shared OmniSwitch MCP endpoint; its tools are prefixed
// with "<target>__" in tools/list responses.
type TargetConfig struct {
	Name               string
	Transport          string
	Upstream           string
	Command            string
	Args               []string
	Environment        map[string]string
	Headers            map[string]string
	ForwardBearerToken bool
	Engine             policy.Engine
}

type target struct {
	name               string
	engine             policy.Engine
	upstream           *url.URL
	stdio              *stdioClient
	headers            map[string]string
	forwardBearerToken bool
}

type Handler struct {
	engine      policy.Engine
	auditor     audit.Logger
	targets     map[string]target
	targetOrder []string
	defaultName string
	client      *http.Client
}

func NewHandler(engine policy.Engine, auditor audit.Logger, upstream string) (*Handler, error) {
	return NewMultiHandler(engine, auditor, []TargetConfig{{Name: "default", Upstream: upstream}})
}

// NewMultiHandler constructs a virtual MCP gateway over one or more remote
// servers. Individual targets can use stricter policy engines while sharing
// the gateway's authenticated caller identity and audit trail.
func NewMultiHandler(engine policy.Engine, auditor audit.Logger, configs []TargetConfig) (*Handler, error) {
	if engine == nil {
		return nil, fmt.Errorf("policy engine is required")
	}
	if len(configs) == 0 {
		return nil, fmt.Errorf("at least one MCP target is required")
	}
	handler := &Handler{
		engine:  engine,
		auditor: auditor,
		targets: make(map[string]target, len(configs)),
		// Request contexts still control cancellation. A client-wide timeout
		// would terminate valid long-lived MCP SSE/streamable responses.
		client: &http.Client{},
	}
	for index, config := range configs {
		name := strings.TrimSpace(config.Name)
		if name == "" {
			return nil, fmt.Errorf("MCP target %d name is required", index)
		}
		key := strings.ToLower(name)
		if _, exists := handler.targets[key]; exists {
			return nil, fmt.Errorf("duplicate MCP target %q", name)
		}
		transport := strings.ToLower(strings.TrimSpace(config.Transport))
		if transport == "" {
			transport = "http"
		}
		configuredTarget := target{name: name, headers: cloneHeaders(config.Headers), forwardBearerToken: config.ForwardBearerToken}
		switch transport {
		case "http", "streamable_http":
			parsed, err := url.Parse(config.Upstream)
			if err != nil {
				return nil, fmt.Errorf("parse MCP target %q upstream: %w", name, err)
			}
			if parsed.Scheme == "" || parsed.Host == "" {
				return nil, fmt.Errorf("MCP target %q upstream must include scheme and host", name)
			}
			configuredTarget.upstream = parsed
		case "stdio":
			client, err := newStdioClient(config.Command, config.Args, config.Environment)
			if err != nil {
				return nil, fmt.Errorf("configure MCP target %q stdio transport: %w", name, err)
			}
			configuredTarget.stdio = client
		default:
			return nil, fmt.Errorf("MCP target %q uses unsupported transport %q", name, config.Transport)
		}
		engineForTarget := config.Engine
		if engineForTarget == nil {
			engineForTarget = engine
		}
		configuredTarget.engine = engineForTarget
		handler.targets[key] = configuredTarget
		handler.targetOrder = append(handler.targetOrder, key)
		if index == 0 {
			handler.defaultName = key
		}
	}
	return handler, nil
}

// Close stops persistent transports owned by the handler.
func (h *Handler) Close() {
	if h == nil {
		return
	}
	for _, target := range h.targets {
		if target.stdio != nil {
			target.stdio.stop()
		}
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, mcp.ErrorResponse(nil, -32600, "Invalid Request", fmt.Errorf("only POST is supported")))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, mcp.ErrorResponse(nil, -32700, "Parse error", err))
		return
	}
	rpcReq, err := mcp.Decode(bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, mcp.ErrorResponse(nil, -32700, "Parse error", err))
		return
	}

	switch rpcReq.Method {
	case "tools/call":
		h.handleToolCall(w, r, rpcReq, body)
	case "tools/list":
		h.handleToolList(w, r, rpcReq, body)
	default:
		// Initialization, notifications, prompts, and resources are forwarded to
		// the default target intact. Tool calls/listing are the operations that
		// require virtual-target routing and policy mediation.
		h.forwardToTarget(w, r.Context(), h.targets[h.defaultName], body, r.Header)
	}
}

func (h *Handler) handleToolCall(w http.ResponseWriter, r *http.Request, rpcReq mcp.Request, body []byte) {
	target, toolName := h.targetForTool(rpcReq.Params.Name)
	rpcReq.Params.Name = toolName
	toolReq, err := rpcReq.ToToolRequestWithIdentity(requestIdentity(r), r.Header.Get("Mcp-Session-Id"))
	if err != nil {
		writeJSON(w, http.StatusOK, mcp.ErrorResponse(rpcReq.ID, -32602, "Invalid params", err))
		return
	}
	if toolReq.Metadata == nil {
		toolReq.Metadata = map[string]string{}
	}
	toolReq.Metadata["mcp.target"] = target.name
	decision, evalErr := target.engine.Evaluate(r.Context(), toolReq)
	if h.auditor != nil {
		_ = h.auditor.Log(r.Context(), audit.NewEvent(toolReq, decision))
	}
	if evalErr != nil || !decision.Allowed {
		writeJSON(w, http.StatusOK, mcp.DeniedResponse(rpcReq.ID, decision))
		return
	}
	body, err = replaceToolName(body, toolName)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, mcp.ErrorResponse(rpcReq.ID, -32700, "Parse error", err))
		return
	}
	h.forwardToTarget(w, r.Context(), target, body, r.Header)
}

func (h *Handler) handleToolList(w http.ResponseWriter, r *http.Request, rpcReq mcp.Request, body []byte) {
	tools := make([]map[string]any, 0)
	for _, key := range h.targetOrder {
		target := h.targets[key]
		listRequest := model.ToolRequest{
			Agent:    requestIdentity(r),
			Tool:     model.Tool{Name: target.name},
			Action:   model.Action{Name: "list"},
			Session:  model.Session{ID: r.Header.Get("Mcp-Session-Id")},
			Metadata: map[string]string{"mcp.target": target.name},
		}
		decision, err := target.engine.Evaluate(r.Context(), listRequest)
		if h.auditor != nil {
			_ = h.auditor.Log(r.Context(), audit.NewEvent(listRequest, decision))
		}
		if err != nil || !decision.Allowed {
			continue
		}
		status, _, response, err := h.callTarget(r.Context(), target, body, r.Header)
		if err != nil || status < 200 || status >= 300 {
			continue
		}
		var result struct {
			Result struct {
				Tools []map[string]any `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(response, &result); err != nil {
			continue
		}
		for _, tool := range result.Result.Tools {
			if name, ok := tool["name"].(string); ok {
				tool["name"] = target.name + "__" + name
			}
			tools = append(tools, tool)
		}
	}
	writeJSON(w, http.StatusOK, mcp.Response{JSONRPC: "2.0", ID: rpcReq.ID, Result: map[string]any{"tools": tools}})
}

func (h *Handler) targetForTool(name string) (target, string) {
	if targetName, toolName, ok := strings.Cut(name, "__"); ok {
		if target, exists := h.targets[strings.ToLower(targetName)]; exists && strings.TrimSpace(toolName) != "" {
			return target, toolName
		}
	}
	return h.targets[h.defaultName], name
}

func (h *Handler) forwardToTarget(w http.ResponseWriter, ctx context.Context, target target, body []byte, headers http.Header) {
	if target.stdio != nil {
		status, responseHeaders, response, err := h.callTarget(ctx, target, body, headers)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, mcp.ErrorResponse(nil, -32603, "Forwarding failed", err))
			return
		}
		for key, values := range responseHeaders {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(response)
		return
	}
	response, err := h.targetRequest(ctx, target, body, headers)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, mcp.ErrorResponse(nil, -32603, "Forwarding failed", err))
		return
	}
	defer response.Body.Close()
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	streamTargetResponse(w, response.Body, response.Header.Get("Content-Type"))
}

func (h *Handler) callTarget(ctx context.Context, target target, body []byte, incoming http.Header) (int, http.Header, []byte, error) {
	if target.stdio != nil {
		payload, err := target.stdio.Call(ctx, body)
		if err != nil {
			return 0, nil, nil, err
		}
		return http.StatusOK, http.Header{"Content-Type": {"application/json"}}, payload, nil
	}
	response, err := h.targetRequest(ctx, target, body, incoming)
	if err != nil {
		return 0, nil, nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, nil, err
	}
	return response.StatusCode, response.Header, payload, nil
}

func (h *Handler) targetRequest(ctx context.Context, target target, body []byte, incoming http.Header) (*http.Response, error) {
	if target.upstream == nil {
		return nil, fmt.Errorf("HTTP target is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.upstream.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for _, key := range []string{"Accept", "Mcp-Protocol-Version", "Mcp-Session-Id", "Last-Event-ID"} {
		if value := incoming.Get(key); value != "" {
			req.Header.Set(key, value)
		}
	}
	// A target can receive an OIDC bearer token only when explicitly enabled.
	// OmniSwitch API keys are never forwarded to avoid leaking a gateway secret
	// to an MCP server.
	if target.forwardBearerToken && incoming.Get("x-omniswitch-auth-method") == "oidc" {
		if authorization := incoming.Get("Authorization"); authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
	}
	for key, value := range target.headers {
		req.Header.Set(key, value)
	}
	response, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func streamTargetResponse(w http.ResponseWriter, body io.Reader, contentType string) {
	if !strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		_, _ = io.Copy(w, body)
		return
	}
	flusher, _ := w.(http.Flusher)
	buffer := make([]byte, 32*1024)
	for {
		read, err := body.Read(buffer)
		if read > 0 {
			_, _ = w.Write(buffer[:read])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func requestIdentity(r *http.Request) model.Agent {
	return model.Agent{
		ID:         r.Header.Get("x-omniswitch-key-id"),
		Department: r.Header.Get("x-omniswitch-workspace-id"),
		Role:       r.Header.Get("x-omniswitch-role"),
	}
}

func replaceToolName(body []byte, name string) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	params, ok := request["params"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("params is required")
	}
	params["name"] = name
	return json.Marshal(request)
}

func cloneHeaders(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func writeJSON(w http.ResponseWriter, status int, response mcp.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
