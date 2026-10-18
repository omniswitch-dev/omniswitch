package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/adapter/mcp"
	"github.com/omniswitch-dev/omniswitch/internal/audit"
	"github.com/omniswitch-dev/omniswitch/internal/model"
	"github.com/omniswitch-dev/omniswitch/internal/policy"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	// maxRequestBytes caps one incoming MCP message.
	maxRequestBytes = 1 << 20
	// maxBufferedResponseBytes caps an upstream response the gateway has to
	// read fully (tools/list federation).
	maxBufferedResponseBytes = 16 << 20
	// maxListPages caps tools/list pagination per target.
	maxListPages = 20
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
	// OpenAPISpec optionally points at an OpenAPI 3.x document (local path or
	// URL, JSON or YAML). Its operations are exposed as synthetic MCP tools on
	// this target, executed as plain HTTP calls with policy and audit
	// enforcement.
	OpenAPISpec string

	// Metadata holds operator-trusted attributes for policy input, for
	// example "environment" or "resource_type". They take precedence over
	// values inferred from caller arguments and are exposed as
	// resource.environment, resource.type and metadata in CEL.
	Metadata map[string]string

	// Timeout bounds one forwarded call. Zero uses defaultCallTimeout.
	Timeout time.Duration

	// TokenExchange configures RFC 8693 token exchange for this target.
	// When configured, the caller's OIDC bearer token is exchanged for a
	// target-specific token.
	TokenExchange *TokenExchangeConfig
}

type target struct {
	name               string
	engine             policy.Engine
	upstream           *url.URL
	stdio              *stdioClient
	headers            map[string]string
	metadata           map[string]string
	timeout            time.Duration
	forwardBearerToken bool
	openAPITools       []openAPITool
	tokenExchange      *TokenExchangeConfig
}

func (t *target) callTimeout() time.Duration {
	if t.timeout > 0 {
		return t.timeout
	}
	return defaultCallTimeout
}

type Handler struct {
	engine      policy.Engine
	auditor     audit.Logger
	targets     map[string]*target
	targetOrder []string
	defaultName string
	client      *http.Client
	tokens      *tokenCache
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
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// A hung upstream must not hold a request forever, but a client-wide
	// timeout would terminate valid long-lived SSE responses, so only the
	// wait for response headers is bounded here.
	transport.ResponseHeaderTimeout = defaultCallTimeout
	handler := &Handler{
		engine:  engine,
		auditor: auditor,
		targets: make(map[string]*target, len(configs)),
		client:  &http.Client{Transport: transport},
		tokens:  newTokenCache(),
	}
	for index, config := range configs {
		name := strings.TrimSpace(config.Name)
		if name == "" {
			handler.Close()
			return nil, fmt.Errorf("MCP target %d name is required", index)
		}
		key := strings.ToLower(name)
		if _, exists := handler.targets[key]; exists {
			handler.Close()
			return nil, fmt.Errorf("duplicate MCP target %q", name)
		}
		configured, err := newTarget(name, config, engine)
		if err != nil {
			handler.Close()
			return nil, err
		}
		handler.targets[key] = configured
		handler.targetOrder = append(handler.targetOrder, key)
		if index == 0 {
			handler.defaultName = key
		}
	}
	return handler, nil
}

func newTarget(name string, config TargetConfig, engine policy.Engine) (*target, error) {
	configured := &target{
		name:               name,
		headers:            cloneHeaders(config.Headers),
		metadata:           cloneHeaders(config.Metadata),
		timeout:            config.Timeout,
		forwardBearerToken: config.ForwardBearerToken,
		tokenExchange:      config.TokenExchange,
		engine:             config.Engine,
	}
	if configured.engine == nil {
		configured.engine = engine
	}
	transport := strings.ToLower(strings.TrimSpace(config.Transport))
	if transport == "" {
		transport = "http"
	}
	switch transport {
	case "http", "streamable_http":
		parsed, err := url.Parse(config.Upstream)
		if err != nil {
			return nil, fmt.Errorf("parse MCP target %q upstream: %w", name, err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("MCP target %q upstream must include scheme and host", name)
		}
		configured.upstream = parsed
	case "stdio":
		client, err := newStdioClient(config.Command, config.Args, config.Environment)
		if err != nil {
			return nil, fmt.Errorf("configure MCP target %q stdio transport: %w", name, err)
		}
		if config.Timeout > 0 {
			client.timeout = config.Timeout
		}
		configured.stdio = client
	default:
		return nil, fmt.Errorf("MCP target %q uses unsupported transport %q", name, config.Transport)
	}
	if spec := strings.TrimSpace(config.OpenAPISpec); spec != "" {
		tools, err := loadOpenAPISpec(spec)
		if err != nil {
			if configured.stdio != nil {
				configured.stdio.close()
			}
			return nil, fmt.Errorf("load OpenAPI spec for MCP target %q: %w", name, err)
		}
		configured.openAPITools = tools
		log.Printf("MCP target %q exposes %d OpenAPI-derived tools", name, len(tools))
	}
	return configured, nil
}

// Close stops persistent transports owned by the handler.
func (h *Handler) Close() {
	if h == nil {
		return
	}
	for _, target := range h.targets {
		if target.stdio != nil {
			target.stdio.close()
		}
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, mcp.ErrorResponse(nil, mcp.CodeInvalidRequest, "Invalid Request", fmt.Errorf("only POST is supported")))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, mcp.ErrorResponse(nil, mcp.CodeParseError, "Parse error", err))
		return
	}
	rpcReq, err := mcp.Parse(body)
	if err != nil {
		var parseErr *mcp.ParseError
		if !errors.As(err, &parseErr) {
			parseErr = &mcp.ParseError{Code: mcp.CodeInvalidRequest, Message: "Invalid Request", Err: err}
		}
		// JSON-RPC errors for well-formed JSON are application-level and use
		// HTTP 200; only unparseable bodies are an HTTP-level failure.
		status := http.StatusOK
		if parseErr.Code == mcp.CodeParseError {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, mcp.ErrorResponse(parseErr.ID, parseErr.Code, parseErr.Message, parseErr.Err))
		return
	}

	ctx, span := startServerSpan(r, rpcReq)
	defer span.End()
	r = r.WithContext(ctx)

	switch rpcReq.Method {
	case "tools/call":
		h.handleToolCall(w, r, rpcReq)
	case "tools/list":
		h.handleToolList(w, r, rpcReq)
	default:
		h.handlePassthrough(w, r, rpcReq)
	}
}

// handlePassthrough forwards initialization, notifications, prompts,
// resources and client responses to the default target. Tool calls and tool
// listing are the operations that need virtual-target routing and policy
// mediation; everything else is audited and forwarded intact.
func (h *Handler) handlePassthrough(w http.ResponseWriter, r *http.Request, rpcReq mcp.Request) {
	tgt := h.targets[h.defaultName]
	method := rpcReq.Method
	if method == "" {
		method = "response"
	}
	h.audit(r, method, model.ToolRequest{
		Agent:   requestIdentity(r),
		Tool:    model.Tool{Name: tgt.name},
		Action:  model.Action{Name: method},
		Target:  model.Target{Name: tgt.name},
		Session: model.Session{ID: r.Header.Get("Mcp-Session-Id")},
	}, model.Decision{Allowed: true, Rule: "passthrough", Reason: "forwarded without tool policy evaluation"})

	if tgt.stdio != nil {
		switch {
		case rpcReq.Method == "":
			// Responses to server-initiated requests are answered by the
			// stdio bridge itself, so the client's reply is acknowledged.
			w.WriteHeader(http.StatusAccepted)
			return
		case rpcReq.Method == "initialize" && rpcReq.HasID:
			// The bridge owns the stdio lifecycle and has already performed
			// the handshake; answer with the server's cached result.
			result, err := tgt.stdio.InitializeResult(r.Context())
			if err != nil {
				writeJSON(w, http.StatusBadGateway, mcp.ErrorResponse(rpcReq.ID, mcp.CodeInternalError, "Forwarding failed", err))
				return
			}
			writeJSON(w, http.StatusOK, mcp.Response{JSONRPC: "2.0", ID: rpcReq.ID, Result: result})
			return
		case rpcReq.Method == "notifications/initialized":
			// Already sent by the bridge during its own handshake.
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}
	h.forwardToTarget(w, r, tgt, rpcReq, h.forwardBody(r.Context(), rpcReq, ""))
}

func (h *Handler) handleToolCall(w http.ResponseWriter, r *http.Request, rpcReq mcp.Request) {
	tgt, toolName := h.targetForTool(rpcReq.Params.Name)
	rpcReq.Params.Name = toolName

	// Synthetic OpenAPI-derived tool: execute as plain HTTP with policy+audit.
	if tool := findOpenAPITool(tgt.openAPITools, toolName); tool != nil {
		h.handleOpenAPICall(w, r, tgt, tool, rpcReq)
		return
	}
	toolReq := mcp.BuildToolRequest(toolName, rpcReq.Params.Arguments, h.toolContext(r, tgt))
	decision, evalErr := tgt.engine.Evaluate(r.Context(), toolReq)
	h.audit(r, rpcReq.Method, toolReq, decision)
	if evalErr != nil || !decision.Allowed {
		writeJSON(w, http.StatusOK, mcp.DeniedResponse(rpcReq.ID, decision))
		return
	}
	h.forwardToTarget(w, r, tgt, rpcReq, h.forwardBody(r.Context(), rpcReq, toolName))
}

// toolContext returns the trusted, gateway-derived context for policy input.
func (h *Handler) toolContext(r *http.Request, tgt *target) mcp.ToolContext {
	return mcp.ToolContext{
		Agent:          requestIdentity(r),
		SessionID:      r.Header.Get("Mcp-Session-Id"),
		Target:         tgt.name,
		TargetMetadata: tgt.metadata,
	}
}

// forwardBody re-serializes the parsed message for the upstream. Members are
// copied as raw JSON, so unknown fields survive and numbers keep their exact
// text; toolName (when set) replaces params.name, and the active trace
// context is added to params._meta.
func (h *Handler) forwardBody(ctx context.Context, rpcReq mcp.Request, toolName string) []byte {
	fields := make(map[string]json.RawMessage, len(rpcReq.Fields))
	for key, value := range rpcReq.Fields {
		fields[key] = value
	}
	if rpcReq.ParamFields != nil {
		params := make(map[string]json.RawMessage, len(rpcReq.ParamFields)+1)
		for key, value := range rpcReq.ParamFields {
			params[key] = value
		}
		changed := false
		if toolName != "" {
			encoded, _ := json.Marshal(toolName)
			params["name"] = encoded
			changed = true
		}
		if injectMetaFields(ctx, params) {
			changed = true
		}
		if changed {
			encoded, err := json.Marshal(params)
			if err == nil {
				fields["params"] = encoded
			}
		}
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	return out
}

// audit records one gateway decision. Audit failures are logged, not fatal.
func (h *Handler) audit(r *http.Request, method string, req model.ToolRequest, decision model.Decision) {
	if h.auditor == nil {
		return
	}
	event := audit.NewEvent(req, decision)
	event.Method = method
	event.KeyID = r.Header.Get("x-omniswitch-key-id")
	event.Workspace = r.Header.Get("x-omniswitch-workspace-id")
	if sc := oteltrace.SpanContextFromContext(r.Context()); sc.IsValid() {
		event.TraceID = sc.TraceID().String()
	}
	if err := h.auditor.Log(r.Context(), event); err != nil {
		log.Printf("mcp audit log failed (method=%s target=%s): %v", method, event.Target, err)
	}
}

func (h *Handler) handleToolList(w http.ResponseWriter, r *http.Request, rpcReq mcp.Request) {
	tools := make([]map[string]json.RawMessage, 0)
	for _, key := range h.targetOrder {
		tgt := h.targets[key]
		// Synthetic OpenAPI tools need no upstream tools/list round-trip.
		for _, synthetic := range tgt.openAPITools {
			listRequest := h.listRequest(r, tgt, synthetic.Name)
			decision, err := tgt.engine.Evaluate(r.Context(), listRequest)
			if err != nil || !decision.Allowed {
				continue
			}
			encoded, err := json.Marshal(synthetic)
			if err != nil {
				continue
			}
			var entry map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &entry); err != nil {
				continue
			}
			entry["name"], _ = json.Marshal(key + "__" + synthetic.Name)
			tools = append(tools, entry)
		}
		listRequest := h.listRequest(r, tgt, tgt.name)
		decision, err := tgt.engine.Evaluate(r.Context(), listRequest)
		h.audit(r, rpcReq.Method, listRequest, decision)
		if err != nil || !decision.Allowed {
			continue
		}
		upstreamTools, err := h.listTargetTools(r, tgt, rpcReq, key == h.defaultName)
		if err != nil {
			log.Printf("mcp tools/list: target %q failed: %v", tgt.name, err)
		}
		for _, tool := range upstreamTools {
			var name string
			if json.Unmarshal(tool["name"], &name) == nil {
				tool["name"], _ = json.Marshal(tgt.name + "__" + name)
			}
			tools = append(tools, tool)
		}
	}
	writeJSON(w, http.StatusOK, mcp.Response{JSONRPC: "2.0", ID: rpcReq.ID, Result: map[string]any{"tools": tools}})
}

func (h *Handler) listRequest(r *http.Request, tgt *target, toolName string) model.ToolRequest {
	return model.ToolRequest{
		Agent:    requestIdentity(r),
		Tool:     model.Tool{Name: toolName, FullName: toolName},
		Action:   model.Action{Name: "list", Operation: "list"},
		Target:   model.Target{Name: tgt.name},
		Session:  model.Session{ID: r.Header.Get("Mcp-Session-Id")},
		Metadata: map[string]string{"mcp.target": tgt.name},
	}
}

// listTargetTools collects every page of an upstream tools/list. The client's
// Mcp-Session-Id belongs to the default target and is only sent there.
func (h *Handler) listTargetTools(r *http.Request, tgt *target, rpcReq mcp.Request, isDefault bool) ([]map[string]json.RawMessage, error) {
	id := rpcReq.ID
	if len(id) == 0 || string(id) == "null" {
		id = json.RawMessage(`"omniswitch-tools-list"`)
	}
	var tools []map[string]json.RawMessage
	cursor := ""
	for page := 0; page < maxListPages; page++ {
		params := map[string]json.RawMessage{}
		if cursor != "" {
			params["cursor"], _ = json.Marshal(cursor)
		}
		injectMetaFields(r.Context(), params)
		encodedParams, _ := json.Marshal(params)
		body, _ := json.Marshal(map[string]json.RawMessage{
			"jsonrpc": json.RawMessage(`"2.0"`),
			"id":      id,
			"method":  json.RawMessage(`"tools/list"`),
			"params":  encodedParams,
		})
		status, headers, payload, err := h.callTarget(r.Context(), tgt, body, r.Header, isDefault)
		if err != nil {
			return tools, err
		}
		if status < 200 || status >= 300 {
			return tools, fmt.Errorf("upstream returned HTTP %d", status)
		}
		message, err := decodeRPCResponse(payload, headers.Get("Content-Type"), id)
		if err != nil {
			return tools, err
		}
		var response struct {
			Result *struct {
				Tools      []map[string]json.RawMessage `json:"tools"`
				NextCursor string                       `json:"nextCursor"`
			} `json:"result"`
			Error *mcp.Error `json:"error"`
		}
		if err := json.Unmarshal(message, &response); err != nil {
			return tools, fmt.Errorf("decode tools/list response: %w", err)
		}
		if response.Error != nil {
			return tools, fmt.Errorf("upstream error %d: %s", response.Error.Code, response.Error.Message)
		}
		if response.Result == nil {
			return tools, fmt.Errorf("tools/list response has no result")
		}
		tools = append(tools, response.Result.Tools...)
		if response.Result.NextCursor == "" || response.Result.NextCursor == cursor {
			return tools, nil
		}
		cursor = response.Result.NextCursor
	}
	return tools, fmt.Errorf("stopped after %d tools/list pages", maxListPages)
}

// decodeRPCResponse returns the JSON-RPC response in payload. Streamable
// HTTP servers may answer with text/event-stream, in which case the data
// frame whose id matches is returned.
func decodeRPCResponse(payload []byte, contentType string, id json.RawMessage) ([]byte, error) {
	if !strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return payload, nil
	}
	wantID := compactJSON(id)
	var data bytes.Buffer
	check := func() []byte {
		defer data.Reset()
		if data.Len() == 0 {
			return nil
		}
		var message struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(data.Bytes(), &message) != nil || message.ID == nil {
			return nil
		}
		if wantID != "" && compactJSON(message.ID) != wantID {
			return nil
		}
		return append([]byte(nil), data.Bytes()...)
	}
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	scanner.Buffer(make([]byte, 64*1024), maxBufferedResponseBytes)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if message := check(); message != nil {
				return message, nil
			}
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read event stream: %w", err)
	}
	if message := check(); message != nil {
		return message, nil
	}
	return nil, fmt.Errorf("event stream has no response for id %s", wantID)
}

func compactJSON(raw json.RawMessage) string {
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, raw); err != nil {
		return string(raw)
	}
	return buffer.String()
}

func (h *Handler) targetForTool(name string) (*target, string) {
	if targetName, toolName, ok := strings.Cut(name, "__"); ok {
		if target, exists := h.targets[strings.ToLower(targetName)]; exists && strings.TrimSpace(toolName) != "" {
			return target, toolName
		}
	}
	return h.targets[h.defaultName], name
}

func (h *Handler) forwardToTarget(w http.ResponseWriter, r *http.Request, tgt *target, rpcReq mcp.Request, body []byte) {
	isDefault := tgt == h.targets[h.defaultName]
	if tgt.stdio != nil {
		if !rpcReq.HasID {
			if err := tgt.stdio.Notify(r.Context(), body); err != nil {
				writeJSON(w, http.StatusBadGateway, mcp.ErrorResponse(nil, mcp.CodeInternalError, "Forwarding failed", err))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		payload, err := tgt.stdio.Call(r.Context(), body)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, mcp.ErrorResponse(rpcReq.ID, mcp.CodeInternalError, "Forwarding failed", err))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
		return
	}
	response, err := h.targetRequest(r.Context(), tgt, body, r.Header, isDefault)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, mcp.ErrorResponse(rpcReq.ID, mcp.CodeInternalError, "Forwarding failed", err))
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

// callTarget performs one buffered request/response exchange with a target.
func (h *Handler) callTarget(ctx context.Context, tgt *target, body []byte, incoming http.Header, forwardSession bool) (int, http.Header, []byte, error) {
	if tgt.stdio != nil {
		payload, err := tgt.stdio.Call(ctx, body)
		if err != nil {
			return 0, nil, nil, err
		}
		return http.StatusOK, http.Header{"Content-Type": {"application/json"}}, payload, nil
	}
	ctx, cancel := context.WithTimeout(ctx, tgt.callTimeout())
	defer cancel()
	response, err := h.targetRequest(ctx, tgt, body, incoming, forwardSession)
	if err != nil {
		return 0, nil, nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxBufferedResponseBytes+1))
	if err != nil {
		return 0, nil, nil, err
	}
	if len(payload) > maxBufferedResponseBytes {
		return 0, nil, nil, fmt.Errorf("upstream response exceeds %d bytes", maxBufferedResponseBytes)
	}
	return response.StatusCode, response.Header, payload, nil
}

func (h *Handler) targetRequest(ctx context.Context, tgt *target, body []byte, incoming http.Header, forwardSession bool) (*http.Response, error) {
	if tgt.upstream == nil {
		return nil, fmt.Errorf("HTTP target is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tgt.upstream.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	forwarded := []string{"Accept", "Mcp-Protocol-Version", "Last-Event-ID"}
	if forwardSession {
		// The client's MCP session was negotiated with the default target;
		// it means nothing to (and must not leak to) other targets.
		forwarded = append(forwarded, "Mcp-Session-Id")
	}
	for _, key := range forwarded {
		if value := incoming.Get(key); value != "" {
			req.Header.Set(key, value)
		}
	}
	injectTraceHeaders(ctx, req.Header)

	// Bearer tokens are only ever taken from OIDC-authenticated callers.
	// OmniSwitch API keys are never forwarded or exchanged, so a gateway
	// secret cannot leak to an MCP server.
	oidcToken := ""
	if incoming.Get("x-omniswitch-auth-method") == "oidc" {
		oidcToken = bearerToken(incoming.Get("Authorization"))
	}
	if tgt.forwardBearerToken && oidcToken != "" {
		req.Header.Set("Authorization", "Bearer "+oidcToken)
	}
	if tgt.tokenExchange != nil {
		token, err := h.exchangeToken(ctx, tgt, oidcToken)
		if err != nil {
			return nil, fmt.Errorf("token exchange failed: %w", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	for key, value := range tgt.headers {
		req.Header.Set(key, value)
	}
	return h.client.Do(req)
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
