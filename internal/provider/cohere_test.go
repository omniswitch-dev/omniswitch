package provider

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCohereRerankSendsV2PayloadAndBillsSearchUnits(t *testing.T) {
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{"id":"rr-1","results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}],"meta":{"api_version":{"version":"2"},"billed_units":{"search_units":3}}}`)
	}))
	defer server.Close()
	c := NewCohere("key")
	c.baseURL = server.URL
	topN, maxChunks, maxTokens := 2, 5, 512
	resp, meta, err := c.Rerank(context.Background(), RerankRequest{
		Model: "rerank-v3.5", Query: "q",
		Documents:       []any{"doc a", map[string]any{"text": "doc b"}},
		TopN:            &topN,
		ReturnDocuments: true,
		MaxChunksPerDoc: &maxChunks,
		MaxTokensPerDoc: &maxTokens,
		User:            "u1",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"return_documents", "max_chunks_per_doc", "user"} {
		if _, ok := sent[field]; ok {
			t.Errorf("v2 payload contains unsupported field %q: %v", field, sent)
		}
	}
	docs, _ := sent["documents"].([]any)
	if len(docs) != 2 || docs[0] != "doc a" || docs[1] != `{"text":"doc b"}` || sent["top_n"] != float64(2) || sent["max_tokens_per_doc"] != float64(512) {
		t.Fatalf("payload = %v", sent)
	}
	if resp.Usage.SearchUnits != 3 || math.Abs(meta.Cost-0.006) > 1e-12 {
		t.Fatalf("usage = %+v cost = %v, want 3 search units / $0.006", resp.Usage, meta.Cost)
	}
	if len(resp.Results) != 2 || resp.Results[0].Index != 1 || resp.Results[0].Document == nil || resp.Results[1].Document != "doc a" {
		t.Fatalf("results = %+v, want original documents attached", resp.Results)
	}
	if resp.Model != "rerank-v3.5" || resp.Object != "list" || resp.ID != "rr-1" {
		t.Fatalf("response = %+v", resp)
	}
}
