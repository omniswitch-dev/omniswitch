package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/omniswitch-dev/omniswitch/internal/cache"
	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

const (
	// persistTimeout bounds store writes made after (or independently of) the
	// client request. They run on a context detached from the request so that
	// a client disconnect cannot drop the request log or the spend update.
	persistTimeout = 5 * time.Second

	// maxConcurrentShadowRequests bounds in-flight shadow traffic. Shadow
	// requests beyond the bound are dropped rather than queued.
	maxConcurrentShadowRequests = 16

	// cachedStreamChunkRunes is the size of each content delta when a cached
	// or redacted response is replayed as a stream.
	cachedStreamChunkRunes = 16

	redactedContent = "[REDACTED BY OMNISWITCH]"

	statusSuccess  = "success"
	statusRedacted = "redacted"
	statusDenied   = "denied"
	statusError    = "error"

	settingsContextKey contextKey = "gateway_settings"
)

// handlerSettings is the hot-reloadable part of the handler configuration.
// A published value is never mutated: setters copy it, change the copy and
// swap the pointer, and each request loads it once so it never sees a mix
// of old and new values.
type handlerSettings struct {
	cacheThreshold        float64
	cacheTTL              time.Duration
	cacheScope            string
	logPayloads           bool
	streamGuardrailBuffer bool
	maxRequestBytes       int64
	shadowProvider        string
	alerts                *AlertManager
	logBroadcaster        func(store.RequestLog)
}

// Handler serves the OpenAI-compatible /v1/chat/completions API.
type Handler struct {
	registry   *provider.Registry
	router     *router.Router
	store      *store.Store
	guardrails atomic.Pointer[guardrail.Engine]

	settings   atomic.Pointer[handlerSettings]
	settingsMu sync.Mutex

	// maxRequestBytes mirrors the request body limit that was configured
	// before the handler started serving (the first SetMaxRequestBytes call).
	// It is never written after that, so handlers that read it without
	// synchronisation stay race-free. Request handlers in this file use
	// requestBodyLimit, which also follows hot reloads.
	maxRequestBytes  int64
	initialLimitOnce sync.Once

	configRouters sync.Map

	shadowSlots   chan struct{}
	shadowBreaker *router.CircuitBreaker
}

type cachedConfigRouter struct {
	router    *router.Router
	updatedAt time.Time
}

// New creates a new gateway handler.
func New(registry *provider.Registry, rtr *router.Router, st *store.Store, gr *guardrail.Engine) *Handler {
	h := &Handler{
		registry:        registry,
		router:          rtr,
		store:           st,
		maxRequestBytes: 10 << 20,
		shadowSlots:     make(chan struct{}, maxConcurrentShadowRequests),
		shadowBreaker:   router.NewCircuitBreaker(5, 60*time.Second),
	}
	h.settings.Store(&handlerSettings{
		cacheThreshold: 0.95,
		cacheTTL:       24 * time.Hour,
		cacheScope:     "api_key",
		// Preserve the previous package-level behavior for embedders. The gateway
		// binary explicitly disables this unless configured otherwise.
		logPayloads:           true,
		streamGuardrailBuffer: true,
		maxRequestBytes:       10 << 20,
	})
	if gr != nil {
		h.guardrails.Store(gr)
	}
	return h
}

// updateSettings publishes a modified copy of the current settings.
func (h *Handler) updateSettings(change func(*handlerSettings)) {
	h.settingsMu.Lock()
	defer h.settingsMu.Unlock()
	next := *h.settings.Load()
	change(&next)
	h.settings.Store(&next)
}

// loadSettings returns the settings snapshot for the request in ctx, or the
// current settings when the request has not loaded a snapshot yet.
func (h *Handler) loadSettings(ctx context.Context) *handlerSettings {
	if ctx != nil {
		if cfg, ok := ctx.Value(settingsContextKey).(*handlerSettings); ok && cfg != nil {
			return cfg
		}
	}
	return h.settings.Load()
}

// withSettings pins one settings snapshot to a request so every stage of the
// request (including nested compat calls) uses the same values.
func (h *Handler) withSettings(r *http.Request) (*http.Request, *handlerSettings) {
	if cfg, ok := r.Context().Value(settingsContextKey).(*handlerSettings); ok && cfg != nil {
		return r, cfg
	}
	cfg := h.settings.Load()
	return r.WithContext(context.WithValue(r.Context(), settingsContextKey, cfg)), cfg
}

// requestBodyLimit returns the current maximum request body size.
func (h *Handler) requestBodyLimit() int64 {
	return h.settings.Load().maxRequestBytes
}

// SetGuardrails atomically swaps the guardrail engine; in-flight requests keep
// using their loaded engine until completion. Used by config hot-reload.
func (h *Handler) SetGuardrails(engine *guardrail.Engine) {
	if engine == nil {
		h.guardrails.Store(&guardrail.Engine{})
		return
	}
	h.guardrails.Store(engine)
}

func (h *Handler) SetSemanticCache(threshold float64) {
	h.updateSettings(func(s *handlerSettings) { s.cacheThreshold = threshold })
}

func (h *Handler) SetCacheTTL(ttl time.Duration) {
	h.updateSettings(func(s *handlerSettings) { s.cacheTTL = ttl })
}

func (h *Handler) SetCacheScope(scope string) {
	h.updateSettings(func(s *handlerSettings) {
		switch strings.TrimSpace(scope) {
		case "workspace", "organization", "global", "api_key":
			s.cacheScope = strings.TrimSpace(scope)
		default:
			s.cacheScope = "api_key"
		}
	})
}

func (h *Handler) SetLogPayloads(enabled bool) {
	h.updateSettings(func(s *handlerSettings) { s.logPayloads = enabled })
}

// SetStreamGuardrailBuffer prevents output from being emitted before output
// guardrails have inspected it. Disabling it trades protection for lower
// first-token latency and is therefore only appropriate for trusted traffic.
func (h *Handler) SetStreamGuardrailBuffer(enabled bool) {
	h.updateSettings(func(s *handlerSettings) { s.streamGuardrailBuffer = enabled })
}

func (h *Handler) SetMaxRequestBytes(limit int64) {
	if limit <= 0 {
		return
	}
	h.initialLimitOnce.Do(func() { h.maxRequestBytes = limit })
	h.updateSettings(func(s *handlerSettings) { s.maxRequestBytes = limit })
}

func (h *Handler) SetShadowProvider(providerName string) {
	h.updateSettings(func(s *handlerSettings) { s.shadowProvider = providerName })
}

func (h *Handler) SetAlerts(config AlertConfig) {
	var manager *AlertManager
	if len(config.Thresholds) != 0 || config.WebhookURL != "" || config.SlackURL != "" {
		manager = NewAlertManager(config)
	}
	h.updateSettings(func(s *handlerSettings) { s.alerts = manager })
}

func (h *Handler) SetLogBroadcaster(broadcast func(store.RequestLog)) {
	h.updateSettings(func(s *handlerSettings) { s.logBroadcaster = broadcast })
}

// requestRouter returns the router for the request: the base router, or the
// per-request dynamic config named by the x-omniswitch-config header.
func (h *Handler) requestRouter(ctx context.Context, r *http.Request) (*router.Router, string, error) {
	configRef := strings.TrimSpace(r.Header.Get("x-omniswitch-config"))
	if configRef == "" {
		return h.router, "", nil
	}
	configRouter, err := h.routerForConfig(ctx, configRef)
	if err != nil {
		return nil, configRef, err
	}
	return configRouter, configRef, nil
}

// persistContext detaches store writes from request cancellation while
// keeping request-scoped values, and bounds them with a short timeout.
func persistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
}

// ChatCompletions handles POST /v1/chat/completions.
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	r, cfg := h.withSettings(r)
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

	r.Body = http.MaxBytesReader(w, r.Body, cfg.maxRequestBytes)
	var req provider.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid request body")
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	activeRouter, configRef, err := h.requestRouter(ctx, r)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid dynamic config")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if configRef != "" {
		span.SetAttributes(attribute.String("sentinel.config", configRef))
	}
	transformed, err := activeRouter.TransformRequest(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid route transformation")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req = transformed

	reqID := newRequestID()
	traceID := requestHeaderOrNew(r, "x-omniswitch-trace-id", "trace")
	sessionID := r.Header.Get("x-omniswitch-session-id")
	userID := r.Header.Get("x-omniswitch-user-id")
	providerHint := r.Header.Get("x-omniswitch-provider")
	shadowHint := firstNonEmpty(r.Header.Get("x-omniswitch-shadow-provider"), activeRouter.ShadowProviderForModel(req.Model), cfg.shadowProvider)
	apiKeyID := r.Header.Get("x-omniswitch-key-id")
	tenantCacheScope := h.tenantCacheScope(r, cfg)
	traceRecorder := newTraceRecorder()
	ctx = WithRequestID(r.Context(), reqID)
	ctx = WithTraceID(ctx, traceID)
	ctx = WithSessionID(ctx, sessionID)
	ctx = WithUserID(ctx, userID)
	ctx = WithMetadataJSON(ctx, r.Header.Get("x-omniswitch-metadata"))
	ctx = WithTags(ctx, tagsHeader(r.Header.Get("x-omniswitch-tags")))
	ctx = WithTraceRecorder(ctx, traceRecorder)
	span.SetAttributes(
		attribute.String("sentinel.request_id", reqID),
		attribute.String("sentinel.trace_id", traceID),
		attribute.String("sentinel.session_id", sessionID),
		attribute.String("sentinel.user_id", userID),
		attribute.String("llm.request.model", req.Model),
		attribute.Bool("llm.request.stream", req.Stream),
	)

	stageStart := time.Now()
	if denied, reason := h.budgetExceeded(ctx, apiKeyID); denied {
		recordTraceSpan(ctx, "budget", "", "denied", stageStart, map[string]any{"reason": reason})
		span.SetAttributes(
			attribute.String("sentinel.decision", "DENY"),
			attribute.String("sentinel.decision_reason", reason),
		)
		h.logRequest(ctx, logContext{
			ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, APIKeyID: apiKeyID,
			Status: statusDenied, ErrorMessage: reason,
		})
		w.Header().Set("x-omniswitch-trace-id", traceID)
		w.Header().Set("x-omniswitch-session-id", sessionID)
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"error": map[string]string{"message": reason, "type": "budget_exceeded", "code": "budget_exceeded"},
		})
		return
	}
	recordTraceSpan(ctx, "budget", "", "ok", stageStart, nil)

	// Input guardrails.
	var inputRedaction string
	if gr := h.guardrails.Load(); gr != nil {
		stageStart = time.Now()
		results := gr.EvaluateInputContext(ctx, req.Messages)
		h.recordGuardrailResults(ctx, reqID, results)
		var rerouteProvider string
		var fallbackTarget string
		for _, result := range results {
			if result.Action == "deny" {
				recordTraceSpan(ctx, "guardrail_input", "", "denied", stageStart, map[string]any{"type": result.Type, "message": result.Message})
				span.SetAttributes(
					attribute.String("sentinel.decision", "DENY"),
					attribute.String("sentinel.guardrail.type", result.Type),
					attribute.String("sentinel.decision_reason", result.Message),
				)
				h.logRequest(ctx, logContext{
					ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, APIKeyID: apiKeyID,
					Status: statusDenied, ErrorMessage: result.Message,
				})
				w.Header().Set("x-omniswitch-trace-id", traceID)
				w.Header().Set("x-omniswitch-session-id", sessionID)
				writeJSON(w, http.StatusForbidden, map[string]any{
					"error": map[string]string{"message": result.Message, "type": result.Type, "code": "guardrail_triggered"},
				})
				return
			}
			if result.Action == "reroute" && rerouteProvider == "" && result.RerouteProvider != "" {
				rerouteProvider = result.RerouteProvider
			}
			if result.Action == "fallback" && fallbackTarget == "" && result.Fallback != "" {
				fallbackTarget = result.Fallback
			}
		}
		if redacted, types := redactInputMessages(ctx, gr, req.Messages, results); len(types) > 0 {
			req.Messages = redacted
			inputRedaction = "input redacted by guardrail: " + strings.Join(types, ", ")
			recordTraceSpan(ctx, "guardrail_redact", "", "redacted", stageStart, map[string]any{"types": types})
			span.SetAttributes(attribute.String("sentinel.guardrail.redacted", strings.Join(types, ",")))
		}
		if rerouteProvider != "" {
			providerHint = rerouteProvider
			recordTraceSpan(ctx, "guardrail_reroute", providerHint, "rerouted", stageStart, map[string]any{"target": rerouteProvider})
		}
		if fallbackTarget != "" {
			providerName, modelID := parseFallbackTarget(fallbackTarget)
			if providerName != "" {
				providerHint = providerName
			}
			if modelID != "" {
				req.Model = modelID
			}
			recordTraceSpan(ctx, "guardrail_fallback", providerHint, "rerouted", stageStart,
				map[string]any{"target": fallbackTarget})
			span.SetAttributes(
				attribute.String("sentinel.guardrail.fallback_target", fallbackTarget),
			)
		}
		recordTraceSpan(ctx, "guardrail_input", "", "ok", stageStart, nil)
	}

	backendReq := req
	backendReq.Stream = false
	// The cache entry describes the completion, not the transport, so the
	// stream options never take part in the cache key.
	cacheReq := backendReq
	cacheReq.StreamOptions = nil
	cacheProvider := firstNonEmpty(providerHint, "auto")
	if configRef != "" {
		cacheProvider = "config:" + configRef + "|" + cacheProvider
	}
	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	stageStart = time.Now()
	cachedResp, cacheHit := h.cachedResponse(ctx, cfg, tenantCacheScope, cacheProvider, cacheReq)
	recordTraceSpan(ctx, "cache_lookup", cacheProvider, map[bool]string{true: "hit", false: "miss"}[cacheHit], stageStart, nil)
	var cacheDisp guardrailDisposition
	if cacheHit {
		cacheDisp = h.outputGuardrailDisposition(ctx, reqID, &cachedResp)
		if cacheDisp.violation() && !cacheDisp.Denied {
			// The cached answer violates a retry/reroute guardrail (the
			// guardrails changed after it was stored): ask the provider again.
			cacheHit = false
		}
	}
	if cacheHit {
		disp := cacheDisp
		if disp.Denied {
			reason := disp.denyReason()
			h.logRequest(ctx, logContext{
				ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, Response: &cachedResp,
				APIKeyID: apiKeyID, Status: statusDenied, ErrorMessage: reason, Cached: true,
			})
			w.Header().Set("x-omniswitch-trace-id", traceID)
			w.Header().Set("x-omniswitch-session-id", sessionID)
			writeGuardrailDenied(w, reason)
			return
		}
		meta := provider.ProviderMeta{Provider: "semantic-cache", Model: req.Model, Cached: true, Timestamp: time.Now().UTC()}
		span.SetAttributes(
			attribute.String("sentinel.decision", "ALLOW"),
			attribute.Bool("sentinel.cache_hit", true),
			attribute.String("llm.response.provider", meta.Provider),
		)
		status, reason := allowStatus(inputRedaction, disp)
		h.logRequest(ctx, logContext{
			ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, Response: &cachedResp, Meta: &meta,
			APIKeyID: apiKeyID, Status: status, DecisionReason: reason, Cached: true,
		})
		w.Header().Set("x-omniswitch-trace-id", traceID)
		w.Header().Set("x-omniswitch-session-id", sessionID)
		w.Header().Set("x-omniswitch-cache", "HIT")
		if req.Stream {
			writeStream(w, cachedResp, includeUsage)
			return
		}
		writeJSON(w, http.StatusOK, cachedResp)
		return
	}

	if req.Stream {
		h.streamProviderResponse(w, ctx, streamContext{
			ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, BackendRequest: backendReq, CacheRequest: cacheReq,
			Router: activeRouter, ProviderHint: providerHint, ShadowHint: shadowHint, APIKeyID: apiKeyID, CacheProvider: cacheProvider, CacheScope: tenantCacheScope,
			InputRedaction: inputRedaction, IncludeUsage: includeUsage,
		})
		return
	}

	var resp provider.ChatResponse
	var meta provider.ProviderMeta
	var totals attemptTotals
	var disp guardrailDisposition

	maxAttempts := 1
	for attempt := 0; attempt < maxAttempts; attempt++ {
		stageStart = time.Now()
		resp, meta, err = activeRouter.Execute(ctx, backendReq, providerHint)
		if err != nil {
			recordTraceSpan(ctx, "provider_call", meta.Provider, "error", stageStart, map[string]any{"error": err.Error()})
			span.RecordError(err)
			span.SetStatus(codes.Error, "provider error")
			span.SetAttributes(attribute.String("llm.response.provider", meta.Provider))
			totals.add(meta.Cost, provider.Usage{})
			logMeta := totals.meta(meta)
			usage := totals.usage
			h.logRequest(ctx, logContext{
				ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, Meta: &logMeta, Usage: &usage,
				APIKeyID: apiKeyID, Status: statusError, ErrorMessage: err.Error(),
			})
			w.Header().Set("x-omniswitch-trace-id", traceID)
			w.Header().Set("x-omniswitch-session-id", sessionID)
			if upErr, ok := asUnsupportedParameterError(err); ok {
				writeUnsupportedParameterError(w, upErr)
				return
			}
			writeError(w, http.StatusBadGateway, "provider error: "+err.Error())
			return
		}
		recordTraceSpan(ctx, "provider_call", meta.Provider, "ok", stageStart, nil)
		totals.add(meta.Cost, resp.Usage)

		// Output guardrails inspect every completion choice.
		disp = h.outputGuardrailDisposition(ctx, reqID, &resp)
		if disp.Denied {
			break
		}
		if disp.violation() {
			maxAttempts = retryAttempts(maxAttempts, disp)
			if attempt+1 < maxAttempts {
				providerHint, backendReq.Model = applyRetryTarget(disp, providerHint, backendReq.Model)
				continue
			}
		}
		break
	}

	logMeta := totals.meta(meta)
	usage := totals.usage
	if disp.Denied || disp.violation() {
		// Either a deny rule matched, or every retry/reroute attempt still
		// violated the output guardrails. Never return a violating response.
		reason := disp.denyReason()
		span.SetAttributes(
			attribute.String("sentinel.decision", "DENY"),
			attribute.String("sentinel.decision_reason", reason),
			attribute.String("llm.response.provider", meta.Provider),
		)
		h.logRequest(ctx, logContext{
			ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, Response: &resp, Meta: &logMeta, Usage: &usage,
			APIKeyID: apiKeyID, Status: statusDenied, ErrorMessage: reason,
		})
		w.Header().Set("x-omniswitch-trace-id", traceID)
		w.Header().Set("x-omniswitch-session-id", sessionID)
		writeGuardrailDenied(w, reason)
		return
	}

	h.storeCache(ctx, cfg, tenantCacheScope, cacheProvider, cacheReq, resp)
	status, reason := allowStatus(inputRedaction, disp)
	h.logRequest(ctx, logContext{
		ID: reqID, TraceID: traceID, SessionID: sessionID, Request: req, Response: &resp, Meta: &logMeta, Usage: &usage,
		APIKeyID: apiKeyID, Status: status, DecisionReason: reason, Cached: meta.Cached,
	})
	span.SetStatus(codes.Ok, "")
	span.SetAttributes(
		attribute.String("sentinel.decision", "ALLOW"),
		attribute.Bool("sentinel.cache_hit", meta.Cached),
		attribute.String("llm.response.provider", meta.Provider),
		attribute.String("llm.response.model", meta.Model),
		attribute.Int("llm.usage.prompt_tokens", usage.PromptTokens),
		attribute.Int("llm.usage.completion_tokens", usage.CompletionTokens),
		attribute.Int("llm.usage.total_tokens", usage.TotalTokens),
		attribute.Float64("llm.usage.cost_usd", logMeta.Cost),
	)
	h.shadow(activeRouter, reqID, traceID, meta.Provider, backendReq, shadowHint)

	w.Header().Set("x-omniswitch-trace-id", traceID)
	w.Header().Set("x-omniswitch-session-id", sessionID)
	w.Header().Set("x-omniswitch-cache", "MISS")
	writeJSON(w, http.StatusOK, resp)
}

type streamContext struct {
	ID             string
	TraceID        string
	SessionID      string
	Request        provider.ChatRequest
	BackendRequest provider.ChatRequest
	CacheRequest   provider.ChatRequest
	Router         *router.Router
	ProviderHint   string
	ShadowHint     string
	APIKeyID       string
	CacheProvider  string
	CacheScope     string
	InputRedaction string
	IncludeUsage   bool
}

// attemptTotals sums upstream cost and token usage across every provider
// attempt of one request (guardrail retries and reroutes included), so the
// request log and the API-key spend reflect everything that was billed.
type attemptTotals struct {
	cost  float64
	usage provider.Usage
}

func (t *attemptTotals) add(cost float64, usage provider.Usage) {
	t.cost += cost
	t.usage.PromptTokens += usage.PromptTokens
	t.usage.CompletionTokens += usage.CompletionTokens
	t.usage.TotalTokens += usage.TotalTokens
}

// meta returns the last attempt's metadata with the summed cost.
func (t *attemptTotals) meta(last provider.ProviderMeta) provider.ProviderMeta {
	last.Cost = t.cost
	return last
}

// retryAttempts returns the attempt budget for a retry/reroute/fallback
// disposition: 1 + max_retries, or one retry by default.
func retryAttempts(current int, disp guardrailDisposition) int {
	if current != 1 {
		return current
	}
	if disp.MaxRetries > 0 {
		return 1 + disp.MaxRetries
	}
	return 2
}

// applyRetryTarget returns the provider hint and model for the next attempt.
func applyRetryTarget(disp guardrailDisposition, providerHint, model string) (string, string) {
	if disp.RerouteProvider != "" {
		providerHint = disp.RerouteProvider
	}
	if disp.Fallback != "" {
		pn, mod := parseFallbackTarget(disp.Fallback)
		if pn != "" {
			providerHint = pn
		}
		if mod != "" {
			model = mod
		}
	}
	return providerHint, model
}

// allowStatus returns the log status for an allowed response: "redacted"
// when the input or the output was redacted, otherwise "success".
func allowStatus(inputRedaction string, disp guardrailDisposition) (string, string) {
	reasons := make([]string, 0, 2)
	if inputRedaction != "" {
		reasons = append(reasons, inputRedaction)
	}
	if disp.Redacted {
		reasons = append(reasons, "output redacted by guardrail")
	}
	if len(reasons) == 0 {
		return statusSuccess, ""
	}
	return statusRedacted, strings.Join(reasons, "; ")
}

func writeGuardrailDenied(w http.ResponseWriter, reason string) {
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error": map[string]string{"message": reason, "type": "guardrail", "code": "guardrail_triggered"},
	})
}

// ListModels handles GET /v1/models.
func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	models := h.registry.AllModels()
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": models})
}

// parseFallbackTarget splits a "provider/model" reroute target. Either half
// may be empty; the caller applies only the halves present.
func parseFallbackTarget(target string) (providerName, modelID string) {
	name, model, ok := strings.Cut(target, "/")
	if !ok {
		return strings.TrimPrefix(target, "@"), ""
	}
	return strings.TrimPrefix(name, "@"), model
}

type logContext struct {
	ID        string
	TraceID   string
	SessionID string
	Request   provider.ChatRequest
	Response  *provider.ChatResponse
	Meta      *provider.ProviderMeta
	// Usage overrides Response.Usage for the logged token counts (for
	// example the sum over several attempts).
	Usage          *provider.Usage
	APIKeyID       string
	Status         string
	ErrorMessage   string
	DecisionReason string
	Cached         bool
}

func (h *Handler) logRequest(ctx context.Context, logCtx logContext) {
	if h.store == nil {
		return
	}
	cfg := h.loadSettings(ctx)

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
	if cfg.logPayloads {
		entry.RequestBody = marshalLogBody(logCtx.Request)
	}
	switch logCtx.Status {
	case statusDenied:
		entry.Decision = "DENY"
		entry.DecisionReason = logCtx.ErrorMessage
	case statusError:
		entry.ErrorMessage = logCtx.ErrorMessage
	}
	if entry.DecisionReason == "" {
		entry.DecisionReason = logCtx.DecisionReason
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
		if cfg.logPayloads {
			entry.ResponseBody = marshalLogBody(logCtx.Response)
		}
	}
	if logCtx.Usage != nil {
		entry.InputTokens = logCtx.Usage.PromptTokens
		entry.OutputTokens = logCtx.Usage.CompletionTokens
		entry.TotalTokens = logCtx.Usage.TotalTokens
	}

	if spans := spansFromContext(ctx); len(spans) > 0 {
		entry.Metadata = metadataWithSpans(MetadataJSONFromContext(ctx), spans)
	} else if meta := MetadataJSONFromContext(ctx); meta != "{}" {
		entry.Metadata = meta
	}

	writeCtx, cancel := persistContext(ctx)
	defer cancel()
	if err := h.store.InsertLog(writeCtx, entry); err != nil {
		log.Printf("failed to log request: %v", err)
	}
	// Charge every upstream cost that was actually incurred, including
	// responses that were later denied by an output guardrail or cut short
	// by a stream error or client disconnect. Cache hits cost nothing.
	if entry.APIKeyID != "" && entry.Cost > 0 && !entry.Cached {
		if err := h.store.IncrementAPIKeySpend(writeCtx, entry.APIKeyID, entry.Cost); err != nil {
			log.Printf("failed to increment API key spend: %v", err)
		}
	}
}

func (h *Handler) routerForConfig(ctx context.Context, ref string) (*router.Router, error) {
	if h.store == nil {
		return nil, fmt.Errorf("dynamic config %q requested but no store is configured", ref)
	}
	record, err := h.store.GetGatewayConfig(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("dynamic config %q not found", ref)
	}
	if cached, ok := h.configRouters.Load(record.Name); ok {
		value := cached.(cachedConfigRouter)
		if value.updatedAt.Equal(record.UpdatedAt) {
			return value.router, nil
		}
	}
	cfg, err := gatewayconfig.Parse([]byte(record.Body), "."+record.Format)
	if err != nil {
		return nil, fmt.Errorf("dynamic config %q is invalid: %w", record.Name, err)
	}
	configRouter := h.router.CloneWithRoutes(cfg.Routes)
	h.configRouters.Store(record.Name, cachedConfigRouter{router: configRouter, updatedAt: record.UpdatedAt})
	if record.ID != record.Name {
		h.configRouters.Store(record.ID, cachedConfigRouter{router: configRouter, updatedAt: record.UpdatedAt})
	}
	return configRouter, nil
}

func (h *Handler) streamProviderResponse(w http.ResponseWriter, ctx context.Context, sc streamContext) {
	span := oteltrace.SpanFromContext(ctx)
	activeRouter := sc.Router
	if activeRouter == nil {
		activeRouter = h.router
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported by this server")
		return
	}
	cfg := h.loadSettings(ctx)
	bufferOutput := cfg.streamGuardrailBuffer && h.guardrails.Load() != nil

	// upstreamCtx is cancelled as soon as the client goes away or a write to
	// it fails, so the provider stops generating instead of being drained.
	upstreamCtx, cancelUpstream := context.WithCancel(ctx)
	defer cancelUpstream()

	var (
		totals      attemptTotals
		meta        provider.ProviderMeta
		aggregated  provider.ChatResponse
		disp        guardrailDisposition
		headersSent bool
	)
	logStream := func(status, message, reason string, resp *provider.ChatResponse) {
		logMeta := totals.meta(meta)
		usage := totals.usage
		h.logRequest(ctx, logContext{
			ID: sc.ID, TraceID: sc.TraceID, SessionID: sc.SessionID, Request: sc.Request, Response: resp, Meta: &logMeta, Usage: &usage,
			APIKeyID: sc.APIKeyID, Status: status, ErrorMessage: message, DecisionReason: reason,
		})
	}

	maxAttempts := 1
	for attempt := 0; attempt < maxAttempts; attempt++ {
		chunks, attemptMeta, err := activeRouter.ExecuteStream(upstreamCtx, sc.BackendRequest, sc.ProviderHint)
		meta = attemptMeta
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "provider stream error")
			span.SetAttributes(attribute.String("llm.response.provider", meta.Provider))
			totals.add(meta.Cost, provider.Usage{})
			logStream(statusError, err.Error(), "", nil)
			w.Header().Set("x-omniswitch-trace-id", sc.TraceID)
			w.Header().Set("x-omniswitch-session-id", sc.SessionID)
			if upErr, ok := asUnsupportedParameterError(err); ok {
				writeUnsupportedParameterError(w, upErr)
				return
			}
			writeError(w, http.StatusBadGateway, "provider stream error: "+err.Error())
			return
		}

		if !bufferOutput {
			setStreamHeaders(w, sc.TraceID, sc.SessionID)
			w.WriteHeader(http.StatusOK)
			headersSent = true
		}

		result := consumeStream(ctx, w, flusher, chunks, sc, meta, bufferOutput)
		aggregated = result.response
		totals.add(result.cost, result.usage)
		if result.clientGone || result.err != nil {
			cancelUpstream()
			go drainChunks(chunks)
		}
		if result.clientGone {
			span.SetStatus(codes.Error, "client disconnected")
			logStream(statusError, "client disconnected before the stream completed", "", &aggregated)
			return
		}
		if result.err != nil {
			span.RecordError(result.err)
			span.SetStatus(codes.Error, "provider stream error")
			span.SetAttributes(attribute.String("llm.response.provider", meta.Provider))
			logStream(statusError, "provider stream error: "+result.err.Error(), "", &aggregated)
			// Nothing that was buffered for the guardrails is replayed.
			if !headersSent {
				setStreamHeaders(w, sc.TraceID, sc.SessionID)
				w.WriteHeader(http.StatusOK)
			}
			writeStreamError(w, flusher, result.err)
			return
		}

		if !bufferOutput {
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			break
		}

		disp = h.outputGuardrailDisposition(ctx, sc.ID, &aggregated)
		if disp.Denied {
			break
		}
		if disp.violation() {
			maxAttempts = retryAttempts(maxAttempts, disp)
			if attempt+1 < maxAttempts {
				sc.ProviderHint, sc.BackendRequest.Model = applyRetryTarget(disp, sc.ProviderHint, sc.BackendRequest.Model)
				continue
			}
			break
		}

		setStreamHeaders(w, sc.TraceID, sc.SessionID)
		if disp.Redacted {
			writeStream(w, aggregated, sc.IncludeUsage)
		} else {
			w.WriteHeader(http.StatusOK)
			for _, chunk := range result.buffered {
				if err := writeSSE(w, chunk); err != nil {
					break
				}
				flusher.Flush()
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		}
		break
	}

	if disp.Denied || disp.violation() {
		// A deny rule matched, or every retry/reroute attempt still violated
		// the output guardrails. Nothing has been sent to the client yet.
		reason := disp.denyReason()
		span.SetStatus(codes.Error, "stream output guardrail denied")
		logStream(statusDenied, reason, "", &aggregated)
		w.Header().Set("x-omniswitch-trace-id", sc.TraceID)
		w.Header().Set("x-omniswitch-session-id", sc.SessionID)
		writeGuardrailDenied(w, reason)
		return
	}

	h.storeCache(ctx, cfg, sc.CacheScope, sc.CacheProvider, sc.CacheRequest, aggregated)
	status, reason := allowStatus(sc.InputRedaction, disp)
	logStream(status, "", reason, &aggregated)
	span.SetStatus(codes.Ok, "")
	span.SetAttributes(
		attribute.String("sentinel.decision", "ALLOW"),
		attribute.String("llm.response.provider", meta.Provider),
		attribute.String("llm.response.model", aggregated.Model),
		attribute.Int("llm.usage.prompt_tokens", totals.usage.PromptTokens),
		attribute.Int("llm.usage.completion_tokens", totals.usage.CompletionTokens),
		attribute.Int("llm.usage.total_tokens", totals.usage.TotalTokens),
		attribute.Float64("llm.usage.cost_usd", totals.cost),
	)
	h.shadow(activeRouter, sc.ID, sc.TraceID, meta.Provider, sc.BackendRequest, sc.ShadowHint)
}

// streamResult is the outcome of reading one provider stream.
type streamResult struct {
	response   provider.ChatResponse
	usage      provider.Usage
	cost       float64
	buffered   []provider.ChatResponseChunk
	err        error
	clientGone bool
}

// consumeStream reads a provider stream until it ends, fails, or the client
// goes away. Without buffering, chunks are forwarded as they arrive. The
// final usage-only chunk is always used for accounting but is only
// forwarded when the client asked for it with stream_options.include_usage.
func consumeStream(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, chunks <-chan provider.ChatResponseChunk, sc streamContext, meta provider.ProviderMeta, buffer bool) streamResult {
	agg := newStreamAggregator(firstNonEmpty(meta.Model, sc.BackendRequest.Model))
	var res streamResult
	done := ctx.Done()
loop:
	for {
		select {
		case <-done:
			res.clientGone = true
			break loop
		case chunk, ok := <-chunks:
			if !ok {
				// A producer that honours cancellation closes the channel
				// when the client leaves; that is not a complete stream.
				res.clientGone = ctx.Err() != nil
				break loop
			}
			if chunk.Err != nil {
				agg.add(chunk)
				res.err = chunk.Err
				break loop
			}
			agg.add(chunk)
			if isUsageOnlyChunk(chunk) && !sc.IncludeUsage {
				continue
			}
			if buffer {
				res.buffered = append(res.buffered, chunk)
				continue
			}
			if err := writeSSE(w, chunk); err != nil {
				res.clientGone = true
				break loop
			}
			flusher.Flush()
		}
	}
	res.response = agg.response()
	usage := agg.usage
	if !agg.hasUsage {
		// Upstreams are asked for usage on every stream; if none arrived,
		// estimate it so the request is never recorded as free.
		usage = estimateUsage(sc.BackendRequest, res.response)
	}
	res.response.Usage = usage
	res.usage = usage
	res.cost = provider.EstimateCost(firstNonEmpty(meta.ProviderType, meta.Provider), res.response.Model, usage)
	return res
}

func drainChunks(chunks <-chan provider.ChatResponseChunk) {
	for range chunks { //nolint:revive // draining unblocks a producer that ignores cancellation
	}
}

func isUsageOnlyChunk(chunk provider.ChatResponseChunk) bool {
	return len(chunk.Choices) == 0 && chunk.Usage != nil
}

// writeStreamError tells a streaming client that the upstream stream failed.
// No [DONE] marker follows, so clients cannot mistake the partial output for
// a complete response.
func writeStreamError(w http.ResponseWriter, flusher http.Flusher, err error) {
	message := "upstream stream failed"
	if err != nil {
		message = err.Error()
	}
	_ = writeSSE(w, map[string]any{
		"error": map[string]string{"message": message, "type": "upstream_stream_error"},
	})
	flusher.Flush()
}

// streamAggregator rebuilds a complete chat response from stream chunks,
// keeping every choice index separately.
type streamAggregator struct {
	id       string
	model    string
	choices  map[int]*choiceBuilder
	usage    provider.Usage
	hasUsage bool
}

type choiceBuilder struct {
	role         string
	content      strings.Builder
	toolCalls    []provider.ToolCall
	finishReason string
}

func newStreamAggregator(model string) *streamAggregator {
	return &streamAggregator{model: model, choices: map[int]*choiceBuilder{}}
}

func (a *streamAggregator) add(chunk provider.ChatResponseChunk) {
	if chunk.ID != "" {
		a.id = chunk.ID
	}
	if chunk.Model != "" {
		a.model = chunk.Model
	}
	for _, choice := range chunk.Choices {
		builder, ok := a.choices[choice.Index]
		if !ok {
			builder = &choiceBuilder{role: "assistant"}
			a.choices[choice.Index] = builder
		}
		if choice.Delta.Role != "" {
			builder.role = choice.Delta.Role
		}
		builder.content.WriteString(choice.Delta.Content)
		if len(choice.Delta.ToolCalls) > 0 {
			mergeToolCallDeltas(&builder.toolCalls, choice.Delta.ToolCalls)
		}
		if choice.FinishReason != "" {
			builder.finishReason = choice.FinishReason
		}
	}
	if chunk.Usage != nil {
		a.usage = *chunk.Usage
		a.hasUsage = true
	}
}

func (a *streamAggregator) response() provider.ChatResponse {
	resp := provider.ChatResponse{
		ID:      firstNonEmpty(a.id, newID("chat")),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   a.model,
		Usage:   a.usage,
	}
	indices := make([]int, 0, len(a.choices))
	for index := range a.choices {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		builder := a.choices[index]
		resp.Choices = append(resp.Choices, provider.Choice{
			Index:        index,
			Message:      provider.Message{Role: builder.role, Content: builder.content.String(), ToolCalls: builder.toolCalls},
			FinishReason: firstNonEmpty(builder.finishReason, "stop"),
		})
	}
	if len(resp.Choices) == 0 {
		resp.Choices = []provider.Choice{{Index: 0, Message: provider.Message{Role: "assistant"}, FinishReason: "stop"}}
	}
	return resp
}

// estimateUsage approximates token usage (about four bytes per token) for a
// stream that ended without a usage report.
func estimateUsage(req provider.ChatRequest, resp provider.ChatResponse) provider.Usage {
	promptBytes := 0
	for _, message := range req.Messages {
		promptBytes += len(message.Text())
		for _, call := range message.ToolCalls {
			promptBytes += len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	completionBytes := 0
	for _, choice := range resp.Choices {
		completionBytes += len(choice.Message.Content)
		for _, call := range choice.Message.ToolCalls {
			completionBytes += len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	usage := provider.Usage{
		PromptTokens:     (promptBytes + 3) / 4,
		CompletionTokens: (completionBytes + 3) / 4,
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	return usage
}

func setStreamHeaders(w http.ResponseWriter, traceID, sessionID string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("x-omniswitch-trace-id", traceID)
	w.Header().Set("x-omniswitch-session-id", sessionID)
	w.Header().Set("x-omniswitch-cache", "MISS")
}

type guardrailDisposition struct {
	Denied          bool
	Redacted        bool
	Retry           bool
	MaxRetries      int
	RerouteProvider string
	Fallback        string
	Reason          string
	// ViolationReason is the message of the first retry/reroute/fallback
	// result, used when the attempts are exhausted and the response is denied.
	ViolationReason string
}

// violation reports whether the response must not be returned as is and
// should be regenerated (retry, reroute or fallback).
func (d guardrailDisposition) violation() bool {
	return d.Retry || d.RerouteProvider != "" || d.Fallback != ""
}

func (d guardrailDisposition) denyReason() string {
	if d.Reason != "" {
		return d.Reason
	}
	if d.ViolationReason != "" {
		return d.ViolationReason
	}
	return "response blocked by output guardrail"
}

// outputGuardrailDisposition evaluates every generated choice. A redaction
// replaces the affected output before it is logged, cached, or returned.
func (h *Handler) outputGuardrailDisposition(ctx context.Context, requestID string, resp *provider.ChatResponse) guardrailDisposition {
	gr := h.guardrails.Load()
	if gr == nil {
		return guardrailDisposition{}
	}
	var disp guardrailDisposition
	for index := range resp.Choices {
		results := gr.EvaluateOutputMessageContext(ctx, resp.Choices[index].Message)
		h.recordGuardrailResults(ctx, requestID, results)
		for _, result := range results {
			if !result.Triggered {
				continue
			}
			switch result.Action {
			case "deny":
				disp.Denied = true
				if disp.Reason == "" {
					disp.Reason = result.Message
				}
			case "redact":
				resp.Choices[index].Message.Content = redactedContent
				resp.Choices[index].Message.ContentParts = nil
				disp.Redacted = true
			case "retry":
				disp.Retry = true
				if result.MaxRetries > disp.MaxRetries {
					disp.MaxRetries = result.MaxRetries
				}
				disp.noteViolation(result)
			case "reroute":
				if disp.RerouteProvider == "" && result.RerouteProvider != "" {
					disp.RerouteProvider = result.RerouteProvider
					disp.noteViolation(result)
				}
			case "fallback":
				if disp.Fallback == "" && result.Fallback != "" {
					disp.Fallback = result.Fallback
					disp.noteViolation(result)
				}
			}
		}
	}
	return disp
}

func (d *guardrailDisposition) noteViolation(result guardrail.Result) {
	if d.ViolationReason != "" {
		return
	}
	d.ViolationReason = firstNonEmpty(result.Message, "output guardrail triggered: "+result.Type)
}

// redactInputMessages applies input-stage "redact" results to the request
// before it leaves the gateway. The guardrail engine reports which checks
// fired but not where, so each message with text is re-checked on its own
// and the text of every message that triggers a redact result is replaced.
// If no single message triggers (the match spans messages), every message
// with text is redacted. Non-text parts (images, audio) are kept.
// It returns the redacted copy and the triggered redact types.
func redactInputMessages(ctx context.Context, gr *guardrail.Engine, messages []provider.Message, results []guardrail.Result) ([]provider.Message, []string) {
	var types []string
	wanted := map[string]bool{}
	for _, result := range results {
		if result.Triggered && result.Action == "redact" && !wanted[result.Type] {
			wanted[result.Type] = true
			types = append(types, result.Type)
		}
	}
	if len(types) == 0 {
		return messages, nil
	}
	out := make([]provider.Message, len(messages))
	copy(out, messages)
	var withText []int
	for index := range out {
		if messageHasText(out[index]) {
			withText = append(withText, index)
		}
	}
	targets := withText
	if len(withText) > 1 {
		targets = nil
		for _, index := range withText {
			for _, result := range gr.EvaluateInputContext(ctx, []provider.Message{out[index]}) {
				if result.Triggered && result.Action == "redact" && wanted[result.Type] {
					targets = append(targets, index)
					break
				}
			}
		}
		if len(targets) == 0 {
			targets = withText
		}
	}
	for _, index := range targets {
		out[index] = redactMessageText(out[index])
	}
	return out, types
}

func messageHasText(message provider.Message) bool {
	if message.Content != "" {
		return true
	}
	for _, part := range message.ContentParts {
		if part.Text != "" {
			return true
		}
	}
	return false
}

func redactMessageText(message provider.Message) provider.Message {
	if message.Content != "" {
		message.Content = redactedContent
	}
	if len(message.ContentParts) > 0 {
		parts := make([]provider.ContentPart, len(message.ContentParts))
		copy(parts, message.ContentParts)
		for index := range parts {
			if parts[index].Text != "" {
				parts[index].Text = redactedContent
			}
		}
		message.ContentParts = parts
	}
	return message
}

// recordGuardrailResults keeps a structured audit trail for both enforced and
// non-blocking results without persisting the prompt or completion itself.
func (h *Handler) recordGuardrailResults(ctx context.Context, requestID string, results []guardrail.Result) {
	if h.store == nil || requestID == "" {
		return
	}
	var writeCtx context.Context
	for _, result := range results {
		if !result.Triggered {
			continue
		}
		if writeCtx == nil {
			var cancel context.CancelFunc
			writeCtx, cancel = persistContext(ctx)
			defer cancel()
		}
		if err := h.store.InsertGuardrailEvent(writeCtx, store.GuardrailEvent{
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

func (h *Handler) cachedResponse(ctx context.Context, cfg *handlerSettings, tenantScope, providerName string, req provider.ChatRequest) (provider.ChatResponse, bool) {
	if h.store == nil || cfg.cacheThreshold <= 0 || len(req.Messages) == 0 {
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

	scope, err := semanticCacheScope(tenantScope, providerName, req)
	if err != nil {
		log.Printf("semantic cache scope failed: %v", err)
		return provider.ChatResponse{}, false
	}
	vector := cache.Vectorize(cache.PromptText(req.Messages))
	entry, ok, err := h.store.FindSemanticCache(ctx, scope, vector, cfg.cacheThreshold)
	if err != nil {
		log.Printf("semantic cache lookup failed: %v", err)
		return provider.ChatResponse{}, false
	}
	if !ok {
		return provider.ChatResponse{}, false
	}
	return entry.Response, true
}

func (h *Handler) storeCache(ctx context.Context, cfg *handlerSettings, tenantScope, providerName string, req provider.ChatRequest, resp provider.ChatResponse) {
	if h.store == nil || cfg.cacheThreshold <= 0 || len(req.Messages) == 0 || len(resp.Choices) == 0 {
		return
	}
	key, err := cache.KeyWithScope(tenantScope, providerName, req)
	if err != nil {
		log.Printf("exact cache key failed: %v", err)
	}
	scope, err := semanticCacheScope(tenantScope, providerName, req)
	if err != nil {
		log.Printf("semantic cache scope failed: %v", err)
		return
	}
	promptText := cache.PromptText(req.Messages)
	var expiresAt *time.Time
	if cfg.cacheTTL > 0 {
		value := time.Now().UTC().Add(cfg.cacheTTL)
		expiresAt = &value
	}
	writeCtx, cancel := persistContext(ctx)
	defer cancel()
	if err := h.store.InsertSemanticCache(writeCtx, store.CacheEntry{
		ID:        newID("cache"),
		Key:       key,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: expiresAt,
		Model:     scope,
		Prompt:    promptText,
		Vector:    cache.Vectorize(promptText),
		Response:  resp,
	}); err != nil {
		log.Printf("semantic cache insert failed: %v", err)
	}
}

func semanticCacheScope(tenantScope, providerName string, req provider.ChatRequest) (string, error) {
	requestScope, err := requestCacheScope(req)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{tenantScope, providerName, requestScope}, "|"), nil
}

// requestCacheScope identifies every request field other than the messages:
// the semantic lookup only compares prompts, so all other parameters (tools,
// tool_choice, response_format, n, seed, logit_bias, sampling settings and
// unknown passthrough fields) must match exactly.
func requestCacheScope(req provider.ChatRequest) (string, error) {
	req.Messages = nil
	req.Stream = false
	req.StreamOptions = nil
	data, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return req.Model + "|params=" + hex.EncodeToString(sum[:16]), nil
}

func (h *Handler) tenantCacheScope(r *http.Request, cfg *handlerSettings) string {
	switch cfg.cacheScope {
	case "global":
		return "global"
	case "organization":
		if id := strings.TrimSpace(r.Header.Get("x-omniswitch-organization-id")); id != "" {
			return "organization:" + id
		}
	case "workspace":
		if id := strings.TrimSpace(r.Header.Get("x-omniswitch-workspace-id")); id != "" {
			return "workspace:" + id
		}
	}
	if id := strings.TrimSpace(r.Header.Get("x-omniswitch-key-id")); id != "" {
		return "api_key:" + id
	}
	return "anonymous"
}

// shadow mirrors a completed request to a shadow provider in the background.
// At most maxConcurrentShadowRequests run at once; further shadow requests
// are dropped. Shadow calls use their own circuit breaker so a failing
// shadow provider cannot open the circuit for production traffic.
func (h *Handler) shadow(activeRouter *router.Router, requestID, traceID, primaryProvider string, req provider.ChatRequest, shadowProvider string) {
	if h.store == nil || strings.TrimSpace(shadowProvider) == "" || shadowProvider == primaryProvider {
		return
	}
	if activeRouter == nil {
		activeRouter = h.router
	}
	select {
	case h.shadowSlots <- struct{}{}:
	default:
		log.Printf("shadow request to %s dropped: %d shadow requests already in flight", shadowProvider, cap(h.shadowSlots))
		return
	}
	shadowRouter := activeRouter.CloneWithRoutes(nil)
	shadowRouter.SetCircuitBreaker(h.shadowBreaker)
	go func() {
		defer func() { <-h.shadowSlots }()
		shadowCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, meta, err := shadowRouter.Execute(shadowCtx, req, shadowProvider)
		if meta.Provider == "" {
			meta.Provider = shadowProvider
		}
		status := statusSuccess
		errMsg := ""
		if err != nil {
			status = statusError
			errMsg = err.Error()
		}
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), persistTimeout)
		defer cancelWrite()
		if err := h.store.InsertShadowLog(writeCtx, store.ShadowLog{
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
	}()
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"message": message, "type": "invalid_request_error"},
	})
}

// writeUnsupportedParameterError surfaces a provider adapter's refusal to
// silently drop a request field it cannot honour, matching OpenAI's error
// body shape so SDKs decode it the same way they decode a real OpenAI 400.
func writeUnsupportedParameterError(w http.ResponseWriter, err *provider.UnsupportedParameterError) {
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]any{
			"message": err.Error(),
			"type":    "invalid_request_error",
			"param":   err.Param,
			"code":    "unsupported_parameter",
		},
	})
}

// asUnsupportedParameterError reports whether err (or a wrapped cause) is an
// UnsupportedParameterError from a provider adapter.
func asUnsupportedParameterError(err error) (*provider.UnsupportedParameterError, bool) {
	var upErr *provider.UnsupportedParameterError
	if errors.As(err, &upErr) {
		return upErr, true
	}
	return nil, false
}

// writeStream replays a complete response (a cache hit or a redacted
// response) as an SSE stream. Every choice is replayed, content is split at
// rune boundaries without touching whitespace, and the usage chunk is sent
// only when the client asked for it.
func writeStream(w http.ResponseWriter, resp provider.ChatResponse, includeUsage bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported by this server")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	created := time.Now().Unix()
	newChunk := func(choices []provider.ChunkChoice) provider.ChatResponseChunk {
		return provider.ChatResponseChunk{ID: resp.ID, Object: "chat.completion.chunk", Created: created, Model: resp.Model, Choices: choices}
	}
	send := func(chunk provider.ChatResponseChunk) bool {
		if err := writeSSE(w, chunk); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	choices := resp.Choices
	if len(choices) == 0 {
		choices = []provider.Choice{{Index: 0, Message: provider.Message{Role: "assistant"}}}
	}
	for _, choice := range choices {
		role := firstNonEmpty(choice.Message.Role, "assistant")
		for _, piece := range splitRunes(choice.Message.Content, cachedStreamChunkRunes) {
			if !send(newChunk([]provider.ChunkChoice{{Index: choice.Index, Delta: provider.Message{Role: role, Content: piece}}})) {
				return
			}
			role = ""
		}
		if len(choice.Message.ToolCalls) > 0 {
			deltas := make([]provider.ToolCall, len(choice.Message.ToolCalls))
			for index, call := range choice.Message.ToolCalls {
				i := index
				call.Index = &i
				deltas[index] = call
			}
			if !send(newChunk([]provider.ChunkChoice{{Index: choice.Index, Delta: provider.Message{Role: role, ToolCalls: deltas}}})) {
				return
			}
		}
	}
	for _, choice := range choices {
		finish := firstNonEmpty(choice.FinishReason, "stop")
		if !send(newChunk([]provider.ChunkChoice{{Index: choice.Index, FinishReason: finish}})) {
			return
		}
	}
	if includeUsage {
		usage := resp.Usage
		usageChunk := newChunk([]provider.ChunkChoice{})
		usageChunk.Usage = &usage
		if !send(usageChunk) {
			return
		}
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// splitRunes cuts text into pieces of at most size runes. Joining the pieces
// gives back the original text byte for byte.
func splitRunes(text string, size int) []string {
	if text == "" {
		return nil
	}
	if size <= 0 {
		return []string{text}
	}
	pieces := make([]string, 0, utf8.RuneCountInString(text)/size+1)
	start, count := 0, 0
	for index := range text {
		if count == size {
			pieces = append(pieces, text[start:index])
			start, count = index, 0
		}
		count++
	}
	return append(pieces, text[start:])
}

func mergeToolCallDeltas(target *[]provider.ToolCall, deltas []provider.ToolCall) {
	for _, delta := range deltas {
		index := len(*target)
		if delta.Index != nil && *delta.Index >= 0 {
			index = *delta.Index
		}
		for len(*target) <= index {
			*target = append(*target, provider.ToolCall{Type: "function"})
		}
		current := &(*target)[index]
		if delta.ID != "" {
			current.ID = delta.ID
		}
		if delta.Type != "" {
			current.Type = delta.Type
		}
		if current.Type == "" {
			current.Type = "function"
		}
		if delta.Function.Name != "" {
			current.Function.Name += delta.Function.Name
		}
		if delta.Function.Arguments != "" {
			current.Function.Arguments += delta.Function.Arguments
		}
	}
}

// writeSSE writes one SSE data event and reports write failures so callers
// can stop streaming to a client that is gone.
func writeSSE(w http.ResponseWriter, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
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
