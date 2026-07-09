package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"sentinel/internal/adapter/mcp"
	"sentinel/internal/audit"
	"sentinel/internal/policy"
)

type Handler struct {
	engine   policy.Engine
	auditor  audit.Logger
	upstream *url.URL
	client   *http.Client
}

func NewHandler(engine policy.Engine, auditor audit.Logger, upstream string) (*Handler, error) {
	if engine == nil {
		return nil, fmt.Errorf("policy engine is required")
	}

	parsed, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("parse upstream URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("upstream URL must include scheme and host")
	}

	return &Handler{
		engine:   engine,
		auditor:  auditor,
		upstream: parsed,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
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

	toolReq, err := rpcReq.ToToolRequest()
	if err != nil {
		writeJSON(w, http.StatusOK, mcp.ErrorResponse(rpcReq.ID, -32602, "Invalid params", err))
		return
	}

	decision, evalErr := h.engine.Evaluate(r.Context(), toolReq)
	if h.auditor != nil {
		_ = h.auditor.Log(r.Context(), audit.NewEvent(toolReq, decision))
	}
	if evalErr != nil || !decision.Allowed {
		writeJSON(w, http.StatusOK, mcp.DeniedResponse(rpcReq.ID, decision))
		return
	}

	h.forward(w, r.Context(), rpcReq.ID, body, r.Header.Get("Content-Type"))
}

func (h *Handler) forward(w http.ResponseWriter, ctx context.Context, id []byte, body []byte, contentType string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.upstream.String(), bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, mcp.ErrorResponse(id, -32603, "Forwarding failed", err))
		return
	}
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := h.client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, mcp.ErrorResponse(id, -32603, "Forwarding failed", err))
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func writeJSON(w http.ResponseWriter, status int, response mcp.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
