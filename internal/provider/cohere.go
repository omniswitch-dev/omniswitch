package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Cohere implements native Cohere rerank support. Chat completions are not
// exposed through this provider; register a custom OpenAI-compatible Cohere
// proxy if chat compatibility is required.
type Cohere struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

func NewCohere(apiKey string) *Cohere {
	return &Cohere{
		apiKey:  apiKey,
		baseURL: "https://api.cohere.com/v2",
		client:  newProviderClient(defaultRequestTimeout),
	}
}

func (c *Cohere) Name() string { return "cohere" }

func (c *Cohere) Models() []ModelInfo {
	return catalogModelInfos("cohere", "cohere")
}

func (c *Cohere) ChatCompletion(context.Context, ChatRequest) (ChatResponse, ProviderMeta, error) {
	return ChatResponse{}, ProviderMeta{Provider: "cohere", Timestamp: time.Now().UTC()}, fmt.Errorf("cohere provider supports rerank only")
}

func (c *Cohere) Rerank(ctx context.Context, req RerankRequest) (RerankResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: "cohere", Model: req.Model, Timestamp: start}
	body, err := json.Marshal(cohereRerankRequest{
		Model:           req.Model,
		Query:           req.Query,
		Documents:       cohereRerankDocuments(req.Documents),
		TopN:            req.TopN,
		MaxTokensPerDoc: req.MaxTokensPerDoc,
	})
	if err != nil {
		return RerankResponse{}, meta, fmt.Errorf("marshal cohere rerank request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rerank", bytes.NewReader(body))
	if err != nil {
		return RerankResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		meta.Latency = time.Since(start)
		return RerankResponse{}, meta, fmt.Errorf("cohere rerank request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		meta.Latency = time.Since(start)
		upstreamErr := newUpstreamError(meta.Provider, "cohere rerank error", resp)
		meta.Error = upstreamErr.Body
		return RerankResponse{}, meta, upstreamErr
	}
	payload, err := readLimited(resp.Body, maxResponseBodyBytes)
	if err != nil {
		meta.Latency = time.Since(start)
		return RerankResponse{}, meta, fmt.Errorf("read cohere rerank response: %w", err)
	}
	meta.Latency = time.Since(start)
	var upstream cohereRerankResponse
	if err := json.Unmarshal(payload, &upstream); err != nil {
		return RerankResponse{}, meta, fmt.Errorf("decode cohere rerank response: %w", err)
	}
	response := RerankResponse{ID: upstream.ID, Results: upstream.Results}
	if response.Results == nil {
		response.Results = []RerankResult{}
	}
	if req.ReturnDocuments {
		// v2 no longer echoes documents; attach the caller's originals.
		for i := range response.Results {
			if idx := response.Results[i].Index; idx >= 0 && idx < len(req.Documents) {
				response.Results[i].Document = req.Documents[idx]
			}
		}
	}
	if upstream.Meta.BilledUnits != nil {
		units := int(upstream.Meta.BilledUnits.SearchUnits)
		response.Usage.SearchUnits = units
		meta.Cost = float64(units) * cohereRerankPricePerSearchUnit
	}
	if response.Model == "" {
		response.Model = req.Model
	}
	if response.Object == "" {
		response.Object = "list"
	}
	return response, meta, nil
}

// cohereRerankPricePerSearchUnit is Cohere's rerank price ($2.00 per 1,000
// search units, https://cohere.com/pricing, verified 2026-10-18).
const cohereRerankPricePerSearchUnit = 0.002

// cohereRerankRequest is the Cohere v2 /rerank payload. v2 dropped
// return_documents and max_chunks_per_doc (replaced by max_tokens_per_doc)
// and has no user field, so those are not forwarded.
type cohereRerankRequest struct {
	Model           string `json:"model"`
	Query           string `json:"query"`
	Documents       []any  `json:"documents"`
	TopN            *int   `json:"top_n,omitempty"`
	MaxTokensPerDoc *int   `json:"max_tokens_per_doc,omitempty"`
}

type cohereRerankResponse struct {
	ID      string         `json:"id"`
	Results []RerankResult `json:"results"`
	Meta    struct {
		BilledUnits *struct {
			SearchUnits float64 `json:"search_units"`
		} `json:"billed_units"`
	} `json:"meta"`
}

func cohereRerankDocuments(documents []any) []any {
	converted := make([]any, 0, len(documents))
	for _, document := range documents {
		if text, ok := document.(string); ok {
			converted = append(converted, text)
			continue
		}
		payload, err := json.Marshal(document)
		if err != nil {
			converted = append(converted, fmt.Sprint(document))
			continue
		}
		converted = append(converted, string(payload))
	}
	return converted
}
