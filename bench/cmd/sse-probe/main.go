// Command sse-probe is the harness's custom SSE client for scenario S4
// (streaming TTFT and inter-chunk timing) and for the B4 harness self-test
// when k6 is not available. k6/wrk2 do not expose per-chunk timing, so this
// is a small, published, independently testable program per the benchmark
// plan section 3.4: it must reproduce the mock's chunk cadence within 1ms
// against the direct baseline before its numbers are trusted for a gateway
// comparison.
//
// Usage:
//
//	sse-probe -url http://localhost:PORT/v1/chat/completions \
//	  -shape openai -n 20 -header 'Authorization: Bearer x'
//
// Output is newline-delimited JSON, one object per request, to stdout:
//
//	{"ttfb_ms":1.2,"chunks":40,"inter_chunk_ms":[20.1,19.8,...],"total_ms":812.3,"error":""}
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type probeResult struct {
	TTFBMillis       float64   `json:"ttfb_ms"`
	Chunks           int       `json:"chunks"`
	InterChunkMillis []float64 `json:"inter_chunk_ms"`
	TotalMillis      float64   `json:"total_ms"`
	Error            string    `json:"error,omitempty"`
}

func main() {
	url := flag.String("url", "http://localhost:4000/v1/chat/completions", "target URL")
	shape := flag.String("shape", "openai", "request shape: openai or anthropic")
	n := flag.Int("n", 1, "number of sequential requests to run")
	model := flag.String("model", "bench-model", "model name to send")
	var headers headerList
	flag.Var(&headers, "header", "extra request header, may be repeated (Name: value)")
	flag.Parse()

	client := &http.Client{Timeout: 90 * time.Second}
	for i := 0; i < *n; i++ {
		res := runOnce(client, *url, *shape, *model, headers)
		b, _ := json.Marshal(res)
		fmt.Println(string(b))
	}
}

type headerList []string

func (h *headerList) String() string { return strings.Join(*h, ",") }
func (h *headerList) Set(v string) error {
	*h = append(*h, v)
	return nil
}

func runOnce(client *http.Client, url, shape, model string, headers headerList) probeResult {
	body := chatBody(shape, model)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return probeResult{Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if shape == "anthropic" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	for _, h := range headers {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) != 2 {
			continue
		}
		req.Header.Set(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return probeResult{Error: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return probeResult{Error: fmt.Sprintf("status %d", resp.StatusCode)}
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var ttfb time.Duration
	var last time.Time
	interChunk := make([]float64, 0, 64)
	chunkCount := 0

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			continue
		}
		now := time.Now()
		if chunkCount == 0 {
			ttfb = now.Sub(start)
			last = now
		} else {
			interChunk = append(interChunk, float64(now.Sub(last).Microseconds())/1000.0)
			last = now
		}
		chunkCount++
	}
	if err := scanner.Err(); err != nil {
		return probeResult{Error: err.Error()}
	}

	return probeResult{
		TTFBMillis:       float64(ttfb.Microseconds()) / 1000.0,
		Chunks:           chunkCount,
		InterChunkMillis: interChunk,
		TotalMillis:      float64(time.Since(start).Microseconds()) / 1000.0,
	}
}

func chatBody(shape, model string) string {
	if shape == "anthropic" {
		return fmt.Sprintf(`{"model":%q,"max_tokens":64,"stream":true,"messages":[{"role":"user","content":"bench probe"}]}`, model)
	}
	return fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"bench probe"}]}`, model)
}
