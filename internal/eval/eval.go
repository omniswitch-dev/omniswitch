package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/omniswitch-dev/omniswitch/internal/model"
	"github.com/omniswitch-dev/omniswitch/internal/policy"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// Handler manages evaluation operations
type Handler struct {
	store *store.Store
}

func NewHandler(st *store.Store) *Handler {
	return &Handler{store: st}
}

// Dataset management

type CreateDatasetRequest struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Metadata    map[string]string      `json:"metadata"`
	Examples    []store.DatasetExample `json:"examples"`
}

type UpdateDatasetRequest struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Metadata    map[string]string `json:"metadata"`
}

type AddExamplesRequest struct {
	DatasetID string                 `json:"dataset_id"`
	Examples  []store.DatasetExample `json:"examples"`
}

func (h *Handler) CreateDataset(w http.ResponseWriter, r *http.Request) {
	var req CreateDatasetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	dataset := &store.Dataset{
		ID:          uuid.New().String(),
		Name:        req.Name,
		Description: req.Description,
		Version:     1,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
		Metadata:    req.Metadata,
		Examples:    req.Examples,
	}

	if err := h.store.CreateDataset(r.Context(), dataset); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, dataset)
}

func (h *Handler) GetDataset(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	dataset, err := h.store.GetDataset(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "dataset not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, dataset)
}

func (h *Handler) ListDatasets(w http.ResponseWriter, r *http.Request) {
	limit := parseInt(r.URL.Query().Get("limit"), 50)
	offset := parseInt(r.URL.Query().Get("offset"), 0)

	datasets, total, err := h.store.ListDatasets(r.Context(), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"datasets": datasets,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
	})
}

func (h *Handler) UpdateDataset(w http.ResponseWriter, r *http.Request) {
	var req UpdateDatasetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	dataset, err := h.store.GetDataset(r.Context(), req.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "dataset not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	dataset.Name = req.Name
	dataset.Description = req.Description
	dataset.Metadata = req.Metadata
	dataset.Version++
	dataset.UpdatedAt = time.Now().UTC()

	if err := h.store.UpdateDataset(r.Context(), dataset); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, dataset)
}

func (h *Handler) DeleteDataset(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	if err := h.store.DeleteDataset(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "dataset not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) AddExamples(w http.ResponseWriter, r *http.Request) {
	var req AddExamplesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.store.AddExamples(r.Context(), req.DatasetID, req.Examples); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "added"})
}

// Experiment management

type CreateExperimentRequest struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	DatasetID   string                 `json:"dataset_id"`
	Config      store.ExperimentConfig `json:"config"`
}

func (h *Handler) CreateExperiment(w http.ResponseWriter, r *http.Request) {
	var req CreateExperimentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	exp := &store.Experiment{
		ID:          uuid.New().String(),
		Name:        req.Name,
		Description: req.Description,
		DatasetID:   req.DatasetID,
		Config:      req.Config,
		Status:      "pending",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
		CreatedBy:   r.Header.Get("x-omniswitch-user-id"),
	}

	if err := h.store.CreateExperiment(r.Context(), exp); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Start experiment asynchronously
	go h.runExperiment(r.Context(), exp)

	writeJSON(w, http.StatusCreated, exp)
}

func (h *Handler) GetExperiment(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	exp, err := h.store.GetExperiment(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "experiment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, exp)
}

func (h *Handler) ListExperiments(w http.ResponseWriter, r *http.Request) {
	limit := parseInt(r.URL.Query().Get("limit"), 50)
	offset := parseInt(r.URL.Query().Get("offset"), 0)
	status := r.URL.Query().Get("status")

	experiments, total, err := h.store.ListExperiments(r.Context(), limit, offset, status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"experiments": experiments,
		"total":       total,
		"limit":       limit,
		"offset":      offset,
	})
}

func (h *Handler) GetExperimentResults(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	exp, err := h.store.GetExperiment(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "experiment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, exp.Results)
}

func (h *Handler) runExperiment(ctx context.Context, exp *store.Experiment) {
	// Update status to running
	exp.Status = "running"
	exp.UpdatedAt = time.Now().UTC()
	if err := h.store.UpdateExperiment(ctx, exp); err != nil {
		return
	}

	// Load dataset
	dataset, err := h.store.GetDataset(ctx, exp.DatasetID)
	if err != nil {
		exp.Status = "failed"
		exp.UpdatedAt = time.Now().UTC()
		_ = h.store.UpdateExperiment(ctx, exp)
		return
	}

	// Run evaluation for each model
	modelResults := make(map[string]store.ModelResult)
	for _, model := range exp.Config.Models {
		result := h.evaluateModel(ctx, exp.Config, dataset, model)
		modelResults[model] = result
	}

	exp.Results = &store.ExperimentResult{
		ModelResults:   modelResults,
		Comparison:     h.computeComparison(modelResults),
		CompletedAt:    time.Now().UTC(),
		TotalEvaluated: len(modelResults),
		Errors:         len(modelResults),
	}
	exp.Status = "completed"
	exp.UpdatedAt = time.Now().UTC()

	_ = h.store.UpdateExperiment(ctx, exp)
}

// Evaluation methods

func (h *Handler) evaluateModel(ctx context.Context, config store.ExperimentConfig, dataset *store.Dataset, model string) store.ModelResult {
	// Implementation would call the gateway's chat completions endpoint
	// for each example in the dataset and compute metrics
	result := store.ModelResult{
		Model:      model,
		Metrics:    make(map[string]float64),
		PerExample: []store.ExampleResult{},
	}

	// This is a simplified implementation
	// Real implementation would call the gateway's chat completion endpoint
	// for each example in the dataset

	return result
}

func (h *Handler) compareModels(config store.ExperimentConfig, ctx context.Context, dataset *store.Dataset) *store.ComparisonResult {
	// Compute statistical comparisons between models
	return &store.ComparisonResult{
		BestModel:     "model_a",
		MetricWinners: map[string]string{},
		Significance:  map[string]float64{},
	}
}

func (h *Handler) computeComparison(modelResults map[string]store.ModelResult) *store.ComparisonResult {
	// Compute statistical comparisons
	return &store.ComparisonResult{
		BestModel:     "",
		MetricWinners: map[string]string{},
		Significance:  map[string]float64{},
	}
}

// PolicyReplayRequest is the request for policy replay evaluation
type PolicyReplayRequest struct {
	PolicyPaths []string            `json:"policy_paths"`
	Requests    []model.ToolRequest `json:"requests"`
}

// PolicyReplayResult is a single result from policy replay
type PolicyReplayResult struct {
	Index    int               `json:"index"`
	Request  model.ToolRequest `json:"request"`
	Decision model.Decision    `json:"decision"`
	Error    string            `json:"error,omitempty"`
}

// PolicyReplayResponse is the response for policy replay
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

func parseInt(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	var v int
	_, _ = fmt.Sscanf(s, "%d", &v)
	return v
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": msg}})
}
