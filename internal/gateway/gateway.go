package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"sentinel/internal/cache"
	"sentinel/internal/guardrail"
	"sentinel/internal/provider"
	"sentinel/internal/router"
	"sentinel/internal/store"
)

// Handler serves the OpenAI-compatible /v1/chat/completions API.
type Handler struct {
	registry              *provider.Registry
	router                *router.Router
	store                 *store.Store
	guardrails            *guardrail.Engine
	cacheThreshold        float64
	cacheTTL              time.Duration
	cacheScope            string
	logPayloads           bool
	streamGuardrailBuffer bool
	maxRequestBytes       int64
	shadowProvider        string
}

// New creates a new gateway handler.
func New(registry *provider.Registry, rtr *router.Router, st *store.Store, gr *guardrail.Engine) *Handler {
	return &Handler{
		registry:       registry,
		router:         rtr,
		store:          st,
		guardrails:     gr,
		cacheThreshold: 0.95,
		cacheTTL:       24 * time.Hour,
		cacheScope:     "api_key",
		// Preserve the previous package-level behavior for embedders. The gateway
		// binary explicitly disables this unless configured otherwise.
		logPayloads:           true,
		streamGuardrailBuffer: true,
		maxRequestBytes:       10 << 20,
	}
}

func (h *Handler) SetSemanticCache(threshold float64) {
	h.cacheThreshold = threshold
}

func (h *Handler) SetCacheTTL(ttl time.Duration) {
	h.cacheTTL = ttl
}

func (h *Handler) SetCacheScope(scope string) {
	switch strings.TrimSpace(scope) {
	case "workspace", "organization", "global", "api_key":
		h.cacheScope = scope
	default:
		h.cacheScope = "api_key"
	}
}

func (h *Handler) SetLogPayloads(enabled bool) {
	h.logPayloads = enabled
}

// SetStreamGuardrailBuffer prevents output from being emitted before output
// guardrails have inspected it. Disabling it trades protection for lower
// first-token latency and is therefore only appropriate for trusted traffic.
func (h *Handler) SetStreamGuardrailBuffer(enabled bool) {
	h.streamGuardrailBuffer = enabled
}

func (h *Handler) SetMaxRequestBytes(limit int64) {
	if limit > 0 {
		h.maxRequestBytes = limit
	}
}

func (h *Handler) SetShadowProvider(providerName string) {
	h.shadowProvider = providerName
}

// ChatCompletions handles POST /v1/chat/completions.
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	ctx, span := otel.Tracer("sentinel/gateway").Start(
		r.Context(),
		"POST /v1/chat/completions",
		oteltrace.WithSpanKind(oteltrace.SpanKindServer),
		oteltrace.WithAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("http.route", "/v1/chat/completions"),
		),
	)
	defer span.End()
	r = r.WithContext(ctx)

	if r.Method != http.MethodPost {
		span.SetStatus(codes.Error, "method not allowed")
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxRequestBytes)
	var req provider.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid request body")
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	transformed, err := h.router.TransformRequest(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid route transformation")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req = transformed

	reqID := newRequestID()
	traceID := requestHeaderOrNew(r, "x-sentinel-trace-id", "trace")
	sessionID := r.Header.Get("x-sentinel-session-id")
	providerHint := r.Header.Get("x-sentinel-provider")
	shadowHint := firstNonEmpty(r.Header.Get("x-sentinel-shadow-provider"), h.router.ShadowProviderForModel(req.Model), h.shadowProvider)
	apiKeyID := r.Header.Get("x-sentinel-key-id")
	tenantCacheScope := h.tenantCacheScope(r)
	ctx = WithRequestID(r.Context(), reqID)
	ctx = WithTraceID(ctx, traceID)
	ctx = WithSessionID(ctx, sessionID)
	span.SetAttributes(
		attribute.String("sentinel.request_id", reqID),
		attribute.String("sentinel.trace_id", traceID),
		attribute.String("sentinel.session_id", sessionID),
		attribute.String("llm.request.model", req.Model),
		attribute.Bool("llm.request.stream", req.Stream),
	)

	if denied, reason := h.budgetExceeded(ctx, apiKeyID); denied {
		span.SetAttributes(
			attribute.String("sentinel.decision", "DENY"),
			attribute.String("sentinel.decision_reason", reason),
		)
		h.logRequest(ctx, logContext{
			ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, APIKeyID: apiKeyID,
			Status: "denied", ErrorMessage: reason,
		})
		w.Header().Set("x-sentinel-trace-id", traceID)
		w.Header().Set("x-sentinel-session-id", sessionID)
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"error": map[string]string{"message": reason, "type": "budget_exceeded", "code": "budget_exceeded"},
		})
		return
	}

	// Input guardrails.
	if h.guardrails != nil {
		results := h.guardrails.EvaluateInput(req.Messages)
		h.recordGuardrailResults(ctx, reqID, results)
		for _, gr := range results {
			if gr.Action == "deny" {
				span.SetAttributes(
					attribute.String("sentinel.decision", "DENY"),
					attribute.String("sentinel.guardrail.type", gr.Type),
					attribute.String("sentinel.decision_reason", gr.Message),
				)
				h.logRequest(ctx, logContext{
					ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, APIKeyID: apiKeyID,
					Status: "denied", ErrorMessage: gr.Message,
				})
				w.Header().Set("x-sentinel-trace-id", traceID)
				w.Header().Set("x-sentinel-session-id", sessionID)
				writeJSON(w, http.StatusForbidden, map[string]any{
					"error": map[string]string{"message": gr.Message, "type": gr.Type, "code": "guardrail_triggered"},
				})
				return
			}
		}
	}

	backendReq := req
	backendReq.Stream = false
	cacheProvider := firstNonEmpty(providerHint, "auto")
	if cachedResp, ok := h.cachedResponse(ctx, tenantCacheScope, cacheProvider, backendReq); ok {
		if denied, _, reason := h.outputGuardrailDisposition(ctx, reqID, cachedResp); denied {
			h.logRequest(ctx, logContext{
				ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, Response: &cachedResp,
				APIKeyID: apiKeyID, Status: "denied", ErrorMessage: reason, Cached: true,
			})
			w.Header().Set("x-sentinel-trace-id", traceID)
			w.Header().Set("x-sentinel-session-id", sessionID)
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": map[string]string{"message": reason, "type": "guardrail", "code": "guardrail_triggered"},
			})
			return
		}
		meta := provider.ProviderMeta{Provider: "semantic-cache", Model: req.Model, Cached: true, Timestamp: time.Now().UTC()}
		span.SetAttributes(
			attribute.String("sentinel.decision", "ALLOW"),
			attribute.Bool("sentinel.cache_hit", true),
			attribute.String("llm.response.provider", meta.Provider),
		)
		h.logRequest(ctx, logContext{
			ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, Response: &cachedResp, Meta: &meta,
			APIKeyID: apiKeyID, Status: "success", Cached: true,
		})
		w.Header().Set("x-sentinel-trace-id", traceID)
		w.Header().Set("x-sentinel-session-id", sessionID)
		w.Header().Set("x-sentinel-cache", "HIT")
		if req.Stream {
			writeStream(w, cachedResp)
			return
		}
		writeJSON(w, http.StatusOK, cachedResp)
		return
	}

	if req.Stream {
		h.streamProviderResponse(w, ctx, streamContext{
			ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, BackendRequest: backendReq,
			ProviderHint: providerHint, ShadowHint: shadowHint, APIKeyID: apiKeyID, CacheProvider: cacheProvider, CacheScope: tenantCacheScope,
		})
		return
	}

	// Route to provider.
	resp, meta, err := h.router.Execute(ctx, backendReq, providerHint)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "provider error")
		span.SetAttributes(attribute.String("llm.response.provider", meta.Provider))
		h.logRequest(ctx, logContext{
			ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, Meta: &meta,
			APIKeyID: apiKeyID, Status: "error", ErrorMessage: err.Error(),
		})
		w.Header().Set("x-sentinel-trace-id", traceID)
		w.Header().Set("x-sentinel-session-id", sessionID)
		writeError(w, http.StatusBadGateway, "provider error: "+err.Error())
		return
	}

	// Output guardrails inspect every completion choice. Redacted output is what
	// gets cached and logged, so a later cache hit cannot restore sensitive data.
	if denied, _, reason := h.outputGuardrailDisposition(ctx, reqID, resp); denied {
		span.SetAttributes(
			attribute.String("sentinel.decision", "DENY"),
			attribute.String("sentinel.decision_reason", reason),
			attribute.String("llm.response.provider", meta.Provider),
		)
		h.logRequest(ctx, logContext{
			ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, Response: &resp, Meta: &meta,
			APIKeyID: apiKeyID, Status: "denied", ErrorMessage: reason,
		})
		w.Header().Set("x-sentinel-trace-id", traceID)
		w.Header().Set("x-sentinel-session-id", sessionID)
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": map[string]string{"message": reason, "type": "guardrail", "code": "guardrail_triggered"},
		})
		return
	}

	h.storeCache(ctx, tenantCacheScope, cacheProvider, backendReq, resp)
	h.logRequest(ctx, logContext{
		ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, Response: &resp, Meta: &meta,
		APIKeyID: apiKeyID, Status: "success", Cached: meta.Cached,
	})
	span.SetStatus(codes.Ok, "")
	span.SetAttributes(
		attribute.String("sentinel.decision", "ALLOW"),
		attribute.Bool("sentinel.cache_hit", meta.Cached),
		attribute.String("llm.response.provider", meta.Provider),
		attribute.String("llm.response.model", meta.Model),
		attribute.Int("llm.usage.prompt_tokens", resp.Usage.PromptTokens),
		attribute.Int("llm.usage.completion_tokens", resp.Usage.CompletionTokens),
		attribute.Int("llm.usage.total_tokens", resp.Usage.TotalTokens),
		attribute.Float64("llm.usage.cost_usd", meta.Cost),
	)
	h.shadow(ctx, reqID, traceID, meta.Provider, backendReq, shadowHint)

	w.Header().Set("x-sentinel-trace-id", traceID)
	w.Header().Set("x-sentinel-session-id", sessionID)
	w.Header().Set("x-sentinel-cache", "MISS")
	if req.Stream {
		writeStream(w, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type streamContext struct {
	ID             string
	TraceID        string
	SessionID      string
	Request        provider.ChatRequest
	BackendRequest provider.ChatRequest
	ProviderHint   string
	ShadowHint     string
	APIKeyID       string
	CacheProvider  string
	CacheScope     string
}

// ListModels handles GET /v1/models.
func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	models := h.registry.AllModels()
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": models})
}

type logContext struct {
	ID           string
	TraceID      string
	SessionID    string
	Request      provider.ChatRequest
	Response     *provider.ChatResponse
	Meta         *provider.ProviderMeta
	APIKeyID     string
	Status       string
	ErrorMessage string
	Cached       bool
}

func (h *Handler) logRequest(ctx context.Context, logCtx logContext) {
	if h.store == nil {
		return
	}

	entry := store.RequestLog{
		ID:        logCtx.ID,
		Timestamp: time.Now().UTC(),
		TraceID:   logCtx.TraceID,
		SessionID: logCtx.SessionID,
		Model:     logCtx.Request.Model,
		APIKeyID:  logCtx.APIKeyID,
		Status:    logCtx.Status,
		Decision:  "ALLOW",
		Cached:    logCtx.Cached,
	}
	if h.logPayloads {
		entry.RequestBody = marshalLogBody(logCtx.Request)
	}

	if logCtx.Status == "denied" {
		entry.Decision = "DENY"
		entry.DecisionReason = logCtx.ErrorMessage
	}
	if logCtx.Status == "error" {
		entry.ErrorMessage = logCtx.ErrorMessage
	}

	if logCtx.Meta != nil {
		entry.Provider = logCtx.Meta.Provider
		if logCtx.Meta.Model != "" {
			entry.Model = logCtx.Meta.Model
		}
		entry.LatencyMs = float64(logCtx.Meta.Latency.Microseconds()) / 1000
		entry.Cost = logCtx.Meta.Cost
	}

	if logCtx.Response != nil {
		entry.InputTokens = logCtx.Response.Usage.PromptTokens
		entry.OutputTokens = logCtx.Response.Usage.CompletionTokens
		entry.TotalTokens = logCtx.Response.Usage.TotalTokens
		if h.logPayloads {
			entry.ResponseBody = marshalLogBody(logCtx.Response)
		}
	}

	if err := h.store.InsertLog(ctx, entry); err != nil {
		log.Printf("failed to log request: %v", err)
	}
	if entry.APIKeyID != "" && entry.Status == "success" && entry.Cost > 0 && !entry.Cached {
		if err := h.store.IncrementAPIKeySpend(ctx, entry.APIKeyID, entry.Cost); err != nil {
			log.Printf("failed to increment API key spend: %v", err)
		}
	}
}

func (h *Handler) streamProviderResponse(w http.ResponseWriter, ctx context.Context, streamCtx streamContext) {
	span := oteltrace.SpanFromContext(ctx)
	chunks, meta, err := h.router.ExecuteStream(ctx, streamCtx.BackendRequest, streamCtx.ProviderHint)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "provider stream error")
		span.SetAttributes(attribute.String("llm.response.provider", meta.Provider))
		h.logRequest(ctx, logContext{
			ID: streamCtx.ID, TraceID: streamCtx.TraceID, SessionID: streamCtx.SessionID, Request: streamCtx.Request, Meta: &meta,
			APIKeyID: streamCtx.APIKeyID, Status: "error", ErrorMessage: err.Error(),
		})
		w.Header().Set("x-sentinel-trace-id", streamCtx.TraceID)
		w.Header().Set("x-sentinel-session-id", streamCtx.SessionID)
		writeError(w, http.StatusBadGateway, "provider stream error: "+err.Error())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported by this server")
		return
	}
	bufferOutput := h.streamGuardrailBuffer && h.guardrails != nil
	if !bufferOutput {
		setStreamHeaders(w, streamCtx.TraceID, streamCtx.SessionID)
		w.WriteHeader(http.StatusOK)
	}

	aggregated := provider.ChatResponse{
		ID:      newID("chat"),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   firstNonEmpty(meta.Model, streamCtx.BackendRequest.Model),
		Choices: []provider.Choice{{
			Index:        0,
			Message:      provider.Message{Role: "assistant"},
			FinishReason: "stop",
		}},
	}
	var buffered []provider.ChatResponseChunk
	for chunk := range chunks {
		if chunk.ID != "" {
			aggregated.ID = chunk.ID
		}
		if chunk.Model != "" {
			aggregated.Model = chunk.Model
		}
		for _, choice := range chunk.Choices {
			if choice.Index == 0 {
				aggregated.Choices[0].Message.Content += choice.Delta.Content
				if choice.FinishReason != "" {
					aggregated.Choices[0].FinishReason = choice.FinishReason
				}
			}
		}
		if chunk.Usage != nil {
			aggregated.Usage = *chunk.Usage
			meta.Cost = provider.EstimateCost(firstNonEmpty(meta.ProviderType, meta.Provider), aggregated.Model, aggregated.Usage)
		}
		if bufferOutput {
			buffered = append(buffered, chunk)
		} else {
			writeSSE(w, chunk)
			flusher.Flush()
		}
	}

	if bufferOutput {
		denied, redacted, reason := h.outputGuardrailDisposition(ctx, streamCtx.ID, aggregated)
		if denied {
			span.SetStatus(codes.Error, "stream output guardrail denied")
			h.logRequest(ctx, logContext{
				ID: streamCtx.ID, TraceID: streamCtx.TraceID, SessionID: streamCtx.SessionID, Request: streamCtx.Request, Response: &aggregated, Meta: &meta,
				APIKeyID: streamCtx.APIKeyID, Status: "denied", ErrorMessage: reason,
			})
			w.Header().Set("x-sentinel-trace-id", streamCtx.TraceID)
			w.Header().Set("x-sentinel-session-id", streamCtx.SessionID)
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": map[string]string{"message": reason, "type": "guardrail", "code": "guardrail_triggered"},
			})
			return
		}
		setStreamHeaders(w, streamCtx.TraceID, streamCtx.SessionID)
		if redacted {
			writeStream(w, aggregated)
		} else {
			w.WriteHeader(http.StatusOK)
			for _, chunk := range buffered {
				writeSSE(w, chunk)
				flusher.Flush()
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		}
	} else {
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}

	h.storeCache(ctx, streamCtx.CacheScope, streamCtx.CacheProvider, streamCtx.BackendRequest, aggregated)
	h.logRequest(ctx, logContext{
		ID: streamCtx.ID, TraceID: streamCtx.TraceID, SessionID: streamCtx.SessionID, Request: streamCtx.Request, Response: &aggregated, Meta: &meta,
		APIKeyID: streamCtx.APIKeyID, Status: "success",
	})
	span.SetStatus(codes.Ok, "")
	span.SetAttributes(
		attribute.String("sentinel.decision", "ALLOW"),
		attribute.String("llm.response.provider", meta.Provider),
		attribute.String("llm.response.model", aggregated.Model),
		attribute.Int("llm.usage.prompt_tokens", aggregated.Usage.PromptTokens),
		attribute.Int("llm.usage.completion_tokens", aggregated.Usage.CompletionTokens),
		attribute.Int("llm.usage.total_tokens", aggregated.Usage.TotalTokens),
		attribute.Float64("llm.usage.cost_usd", meta.Cost),
	)
	h.shadow(ctx, streamCtx.ID, streamCtx.TraceID, meta.Provider, streamCtx.BackendRequest, streamCtx.ShadowHint)
}

func setStreamHeaders(w http.ResponseWriter, traceID, sessionID string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("x-sentinel-trace-id", traceID)
	w.Header().Set("x-sentinel-session-id", sessionID)
	w.Header().Set("x-sentinel-cache", "MISS")
}

// outputGuardrailDisposition evaluates every generated choice. A redaction
// replaces the affected output before it is logged, cached, or returned.
func (h *Handler) outputGuardrailDisposition(ctx context.Context, requestID string, resp provider.ChatResponse) (denied, redacted bool, reason string) {
	if h.guardrails == nil {
		return false, false, ""
	}
	for index := range resp.Choices {
		results := h.guardrails.EvaluateOutput(resp.Choices[index].Message.Content)
		h.recordGuardrailResults(ctx, requestID, results)
		for _, result := range results {
			switch result.Action {
			case "deny":
				return true, false, result.Message
			case "redact":
				resp.Choices[index].Message.Content = "[REDACTED BY SENTINEL]"
				redacted = true
			}
		}
	}
	return false, redacted, ""
}

// recordGuardrailResults keeps a structured audit trail for both enforced and
// non-blocking results without persisting the prompt or completion itself.
func (h *Handler) recordGuardrailResults(ctx context.Context, requestID string, results []guardrail.Result) {
	if h.store == nil || requestID == "" {
		return
	}
	for _, result := range results {
		if !result.Triggered {
			continue
		}
		if err := h.store.InsertGuardrailEvent(ctx, store.GuardrailEvent{
			ID:        newID("gr"),
			RequestID: requestID,
			Timestamp: time.Now().UTC(),
			Type:      result.Type,
			Action:    result.Action,
			Matched:   true,
			Details:   result.Details,
		}); err != nil {
			log.Printf("failed to store guardrail event: %v", err)
		}
	}
}

func (h *Handler) budgetExceeded(ctx context.Context, apiKeyID string) (bool, string) {
	if h.store == nil || apiKeyID == "" {
		return false, ""
	}
	apiKey, err := h.store.GetAPIKeyByID(ctx, apiKeyID)
	if err != nil {
		return false, ""
	}
	costBudget := apiKey.BudgetUSD
	if costBudget <= 0 {
		costBudget = apiKey.MonthlyCostBudget
	}
	if costBudget <= 0 && apiKey.MonthlyTokenBudget <= 0 {
		return false, ""
	}
	now := time.Now().UTC()
	since := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	usage, err := h.store.GetBudgetUsage(ctx, apiKeyID, since)
	if err != nil {
		log.Printf("failed to check budget: %v", err)
		return false, ""
	}
	spendUSD := apiKey.SpendUSD
	if usage.Cost > spendUSD {
		spendUSD = usage.Cost
	}
	if costBudget > 0 && spendUSD >= costBudget {
		return true, fmt.Sprintf("monthly cost budget exceeded: %.4f of %.4f used", spendUSD, costBudget)
	}
	if apiKey.MonthlyTokenBudget > 0 && usage.Tokens >= apiKey.MonthlyTokenBudget {
		return true, fmt.Sprintf("monthly token budget exceeded: %d of %d used", usage.Tokens, apiKey.MonthlyTokenBudget)
	}
	return false, ""
}

func (h *Handler) cachedResponse(ctx context.Context, tenantScope, providerName string, req provider.ChatRequest) (provider.ChatResponse, bool) {
	if h.store == nil || h.cacheThreshold <= 0 || len(req.Messages) == 0 {
		return provider.ChatResponse{}, false
	}
	key, err := cache.KeyWithScope(tenantScope, providerName, req)
	if err != nil {
		log.Printf("exact cache key failed: %v", err)
	} else if entry, ok, err := h.store.GetExactCache(ctx, key); err != nil {
		log.Printf("exact cache lookup failed: %v", err)
	} else if ok {
		return entry.Response, true
	}

	vector := cache.Vectorize(cache.PromptText(req.Messages))
	entry, ok, err := h.store.FindSemanticCache(ctx, semanticCacheScope(tenantScope, providerName, req), vector, h.cacheThreshold)
	if err != nil {
		log.Printf("semantic cache lookup failed: %v", err)
		return provider.ChatResponse{}, false
	}
	if !ok {
		return provider.ChatResponse{}, false
	}
	return entry.Response, true
}

func (h *Handler) storeCache(ctx context.Context, tenantScope, providerName string, req provider.ChatRequest, resp provider.ChatResponse) {
	if h.store == nil || h.cacheThreshold <= 0 || len(req.Messages) == 0 || len(resp.Choices) == 0 {
		return
	}
	key, err := cache.KeyWithScope(tenantScope, providerName, req)
	if err != nil {
		log.Printf("exact cache key failed: %v", err)
	}
	promptText := cache.PromptText(req.Messages)
	var expiresAt *time.Time
	if h.cacheTTL > 0 {
		value := time.Now().UTC().Add(h.cacheTTL)
		expiresAt = &value
	}
	if err := h.store.InsertSemanticCache(ctx, store.CacheEntry{
		ID:        newID("cache"),
		Key:       key,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: expiresAt,
		Model:     semanticCacheScope(tenantScope, providerName, req),
		Prompt:    promptText,
		Vector:    cache.Vectorize(promptText),
		Response:  resp,
	}); err != nil {
		log.Printf("semantic cache insert failed: %v", err)
	}
}

func semanticCacheScope(tenantScope, providerName string, req provider.ChatRequest) string {
	parts := []string{tenantScope, providerName}
	parts = append(parts, requestCacheScope(req))
	return strings.Join(parts, "|")
}

func requestCacheScope(req provider.ChatRequest) string {
	parts := []string{req.Model}
	if req.Temperature != nil {
		parts = append(parts, fmt.Sprintf("temperature=%.4f", *req.Temperature))
	}
	if req.TopP != nil {
		parts = append(parts, fmt.Sprintf("top_p=%.4f", *req.TopP))
	}
	if req.MaxTokens != nil {
		parts = append(parts, fmt.Sprintf("max_tokens=%d", *req.MaxTokens))
	}
	if len(req.Stop) > 0 {
		parts = append(parts, "stop="+strings.Join(req.Stop, ","))
	}
	return strings.Join(parts, "|")
}

func (h *Handler) tenantCacheScope(r *http.Request) string {
	switch h.cacheScope {
	case "global":
		return "global"
	case "organization":
		if id := strings.TrimSpace(r.Header.Get("x-sentinel-organization-id")); id != "" {
			return "organization:" + id
		}
	case "workspace":
		if id := strings.TrimSpace(r.Header.Get("x-sentinel-workspace-id")); id != "" {
			return "workspace:" + id
		}
	}
	if id := strings.TrimSpace(r.Header.Get("x-sentinel-key-id")); id != "" {
		return "api_key:" + id
	}
	return "anonymous"
}

func (h *Handler) shadow(ctx context.Context, requestID, traceID, primaryProvider string, req provider.ChatRequest, shadowProvider string) {
	if h.store == nil || strings.TrimSpace(shadowProvider) == "" || shadowProvider == primaryProvider {
		return
	}
	go func() {
		shadowCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		resp, meta, err := h.router.Execute(shadowCtx, req, shadowProvider)
		if meta.Provider == "" {
			meta.Provider = shadowProvider
		}
		status := "success"
		errMsg := ""
		if err != nil {
			status = "error"
			errMsg = err.Error()
		}
		if err := h.store.InsertShadowLog(context.Background(), store.ShadowLog{
			ID:              newID("shadow"),
			RequestID:       requestID,
			TraceID:         traceID,
			Timestamp:       time.Now().UTC(),
			PrimaryProvider: primaryProvider,
			ShadowProvider:  meta.Provider,
			Model:           req.Model,
			LatencyMs:       float64(meta.Latency.Microseconds()) / 1000,
			Cost:            meta.Cost,
			Status:          status,
			ErrorMessage:    errMsg,
		}); err != nil {
			log.Printf("failed to log shadow request: %v", err)
		}
		_ = resp
		_ = ctx
	}()
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"message": message, "type": "invalid_request_error"},
	})
}

func writeStream(w http.ResponseWriter, resp provider.ChatResponse) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported by this server")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	content := ""
	finishReason := "stop"
	if len(resp.Choices) > 0 {
		content = resp.Choices[0].Message.Content
		finishReason = resp.Choices[0].FinishReason
	}
	if finishReason == "" {
		finishReason = "stop"
	}

	words := strings.Fields(content)
	if len(words) == 0 && content != "" {
		words = []string{content}
	}
	for i, word := range words {
		token := word
		if i < len(words)-1 {
			token += " "
		}
		chunk := map[string]any{
			"id":      resp.ID,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   resp.Model,
			"choices": []map[string]any{
				{
					"index": 0,
					"delta": map[string]string{"content": token},
				},
			},
		}
		writeSSE(w, chunk)
		flusher.Flush()
	}

	finalChunk := map[string]any{
		"id":      resp.ID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   resp.Model,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         map[string]string{},
				"finish_reason": finishReason,
			},
		},
	}
	writeSSE(w, finalChunk)
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeSSE(w http.ResponseWriter, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func newRequestID() string {
	return newID("req")
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s_%d_%s", prefix, time.Now().UnixNano(), hex.EncodeToString(b[:]))
}

func requestHeaderOrNew(r *http.Request, header string, prefix string) string {
	if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
		return value
	}
	return newID(prefix)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func marshalLogBody(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	const maxLoggedBodyBytes = 64 * 1024
	if len(data) <= maxLoggedBodyBytes {
		return string(data)
	}
	return string(data[:maxLoggedBodyBytes]) + "...[truncated]"
}
