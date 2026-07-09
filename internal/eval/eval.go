package eval

import (
	"encoding/json"
	"net/http"

	"sentinel/internal/model"
	"sentinel/internal/policy"
)

type Handler struct{}

func New() *Handler {
	return &Handler{}
}

type PolicyReplayRequest struct {
	PolicyPaths []string            `json:"policy_paths"`
	Requests    []model.ToolRequest `json:"requests"`
}

type PolicyReplayResult struct {
	Index    int               `json:"index"`
	Request  model.ToolRequest `json:"request"`
	Decision model.Decision    `json:"decision"`
	Error    string            `json:"error,omitempty"`
}

type PolicyReplayResponse struct {
	Total   int                  `json:"total"`
	Allowed int                  `json:"allowed"`
	Denied  int                  `json:"denied"`
	Errors  int                  `json:"errors"`
	Results []PolicyReplayResult `json:"results"`
}

// ReplayPolicies handles POST /api/evals/policy.
func (h *Handler) ReplayPolicies(w http.ResponseWriter, r *http.Request) {
	var req PolicyReplayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.PolicyPaths) == 0 {
		writeError(w, http.StatusBadRequest, "policy_paths is required")
		return
	}
	if len(req.Requests) == 0 {
		writeError(w, http.StatusBadRequest, "requests is required")
		return
	}

	engine, err := policy.NewEngineFromFiles(req.PolicyPaths...)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp := PolicyReplayResponse{
		Total:   len(req.Requests),
		Results: make([]PolicyReplayResult, 0, len(req.Requests)),
	}
	for i, toolReq := range req.Requests {
		decision, err := engine.Evaluate(r.Context(), toolReq)
		result := PolicyReplayResult{
			Index:    i,
			Request:  toolReq,
			Decision: decision,
		}
		if err != nil {
			result.Error = err.Error()
			resp.Errors++
		}
		if decision.Allowed {
			resp.Allowed++
		} else {
			resp.Denied++
		}
		resp.Results = append(resp.Results, result)
	}

	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": msg}})
}
