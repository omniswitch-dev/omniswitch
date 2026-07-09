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

	"sentinel/internal/cache"
	"sentinel/internal/guardrail"
	"sentinel/internal/provider"
	"sentinel/internal/router"
	"sentinel/internal/store"
)

// Handler serves the OpenAI-compatible /v1/chat/completions API.
type Handler struct {
	registry       *provider.Registry
	router         *router.Router
	store          *store.Store
	guardrails     *guardrail.Engine
	cacheThreshold float64
	cacheTTL       time.Duration
	shadowProvider string
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
	}
}

func (h *Handler) SetSemanticCache(threshold float64) {
	h.cacheThreshold = threshold
}

func (h *Handler) SetCacheTTL(ttl time.Duration) {
	h.cacheTTL = ttl
}

func (h *Handler) SetShadowProvider(providerName string) {
	h.shadowProvider = providerName
}

// ChatCompletions handles POST /v1/chat/completions.
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}

	var req provider.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	reqID := newRequestID()
	traceID := requestHeaderOrNew(r, "x-sentinel-trace-id", "trace")
	sessionID := r.Header.Get("x-sentinel-session-id")
	providerHint := r.Header.Get("x-sentinel-provider")
	shadowHint := firstNonEmpty(r.Header.Get("x-sentinel-shadow-provider"), h.shadowProvider)
	apiKeyID := r.Header.Get("x-sentinel-key-id")
	ctx := WithRequestID(r.Context(), reqID)
	ctx = WithTraceID(ctx, traceID)
	ctx = WithSessionID(ctx, sessionID)

	if denied, reason := h.budgetExceeded(ctx, apiKeyID); denied {
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
		for _, gr := range results {
			if gr.Action == "deny" {
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
	if cachedResp, ok := h.cachedResponse(ctx, cacheProvider, backendReq); ok {
		meta := provider.ProviderMeta{Provider: "semantic-cache", Model: req.Model, Cached: true, Timestamp: time.Now().UTC()}
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
			ProviderHint: providerHint, ShadowHint: shadowHint, APIKeyID: apiKeyID, CacheProvider: cacheProvider,
		})
		return
	}

	// Route to provider.
	resp, meta, err := h.router.Execute(ctx, backendReq, providerHint)
	if err != nil {
		h.logRequest(ctx, logContext{
			ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, Meta: &meta,
			APIKeyID: apiKeyID, Status: "error", ErrorMessage: err.Error(),
		})
		w.Header().Set("x-sentinel-trace-id", traceID)
		w.Header().Set("x-sentinel-session-id", sessionID)
		writeError(w, http.StatusBadGateway, "provider error: "+err.Error())
		return
	}

	// Output guardrails.
	if h.guardrails != nil && len(resp.Choices) > 0 {
		results := h.guardrails.EvaluateOutput(resp.Choices[0].Message.Content)
		for _, gr := range results {
			if gr.Action == "deny" {
				h.logRequest(ctx, logContext{
					ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, Response: &resp, Meta: &meta,
					APIKeyID: apiKeyID, Status: "denied", ErrorMessage: gr.Message,
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

	h.storeCache(ctx, cacheProvider, backendReq, resp)
	h.logRequest(ctx, logContext{
		ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, Response: &resp, Meta: &meta,
		APIKeyID: apiKeyID, Status: "success", Cached: meta.Cached,
	})
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
		ID:          logCtx.ID,
		Timestamp:   time.Now().UTC(),
		TraceID:     logCtx.TraceID,
		SessionID:   logCtx.SessionID,
		Model:       logCtx.Request.Model,
		APIKeyID:    logCtx.APIKeyID,
		Status:      logCtx.Status,
		RequestBody: marshalLogBody(logCtx.Request),
		Decision:    "ALLOW",
		Cached:      logCtx.Cached,
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
		entry.ResponseBody = marshalLogBody(logCtx.Response)
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
	chunks, meta, err := h.router.ExecuteStream(ctx, streamCtx.BackendRequest, streamCtx.ProviderHint)
	if err != nil {
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
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("x-sentinel-trace-id", streamCtx.TraceID)
	w.Header().Set("x-sentinel-session-id", streamCtx.SessionID)
	w.Header().Set("x-sentinel-cache", "MISS")
	w.WriteHeader(http.StatusOK)

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
		writeSSE(w, chunk)
		flusher.Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()

	h.storeCache(ctx, streamCtx.CacheProvider, streamCtx.BackendRequest, aggregated)
	h.logRequest(ctx, logContext{
		ID: streamCtx.ID, TraceID: streamCtx.TraceID, SessionID: streamCtx.SessionID, Request: streamCtx.Request, Response: &aggregated, Meta: &meta,
		APIKeyID: streamCtx.APIKeyID, Status: "success",
	})
	h.shadow(ctx, streamCtx.ID, streamCtx.TraceID, meta.Provider, streamCtx.BackendRequest, streamCtx.ShadowHint)
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

func (h *Handler) cachedResponse(ctx context.Context, providerName string, req provider.ChatRequest) (provider.ChatResponse, bool) {
	if h.store == nil || h.cacheThreshold <= 0 || len(req.Messages) == 0 {
		return provider.ChatResponse{}, false
	}
	key, err := cache.Key(providerName, req)
	if err != nil {
		log.Printf("exact cache key failed: %v", err)
	} else if entry, ok, err := h.store.GetExactCache(ctx, key); err != nil {
		log.Printf("exact cache lookup failed: %v", err)
	} else if ok {
		return entry.Response, true
	}

	vector := cache.Vectorize(cache.PromptText(req.Messages))
	entry, ok, err := h.store.FindSemanticCache(ctx, cacheScope(req), vector, h.cacheThreshold)
	if err != nil {
		log.Printf("semantic cache lookup failed: %v", err)
		return provider.ChatResponse{}, false
	}
	if !ok {
		return provider.ChatResponse{}, false
	}
	return entry.Response, true
}

func (h *Handler) storeCache(ctx context.Context, providerName string, req provider.ChatRequest, resp provider.ChatResponse) {
	if h.store == nil || h.cacheThreshold <= 0 || len(req.Messages) == 0 || len(resp.Choices) == 0 {
		return
	}
	key, err := cache.Key(providerName, req)
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
		Model:     cacheScope(req),
		Prompt:    promptText,
		Vector:    cache.Vectorize(promptText),
		Response:  resp,
	}); err != nil {
		log.Printf("semantic cache insert failed: %v", err)
	}
}

func cacheScope(req provider.ChatRequest) string {
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
