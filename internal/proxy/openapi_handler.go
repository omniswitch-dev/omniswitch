package proxy

import (
	"context"
	"io"
	"net/http"

	"github.com/omniswitch-dev/omniswitch/internal/adapter/mcp"
)

// handleOpenAPICall executes a synthetic OpenAPI-derived tool: policy gate,
// audit event, HTTP execution, and MCP-formatted response.
func (h *Handler) handleOpenAPICall(w http.ResponseWriter, r *http.Request, tgt *target, tool *openAPITool, rpcReq mcp.Request) {
	tc := h.toolContext(r, tgt)
	tc.HTTPMethod = tool.Method
	toolReq := mcp.BuildToolRequest(tool.Name, rpcReq.Params.Arguments, tc)
	toolReq.Metadata["mcp.openapi"] = tool.Method + " " + tool.Path
	decision, evalErr := tgt.engine.Evaluate(r.Context(), toolReq)
	h.audit(r, rpcReq.Method, toolReq, decision)
	if evalErr != nil || !decision.Allowed {
		writeJSON(w, http.StatusOK, mcp.DeniedResponse(rpcReq.ID, decision))
		return
	}

	baseURL := ""
	if tgt.upstream != nil {
		baseURL = tgt.upstream.String()
	}
	ctx, cancel := context.WithTimeout(r.Context(), tgt.callTimeout())
	defer cancel()
	resp, err := executeOpenAPICall(ctx, h.client, baseURL, tgt.headers, tool, rpcReq.Params.Arguments)
	if err != nil {
		writeJSON(w, http.StatusOK, mcp.Response{JSONRPC: "2.0", ID: rpcReq.ID, Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": "upstream error: " + err.Error()}},
			"isError": true,
		}})
		return
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	isError := resp.StatusCode >= 400
	text := string(payload)
	if text == "" {
		text = resp.Status
	}
	result := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
	writeJSON(w, http.StatusOK, mcp.Response{JSONRPC: "2.0", ID: rpcReq.ID, Result: result})
}
