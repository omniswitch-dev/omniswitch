// Command bench is a dependency-free HTTP load generator tuned for AI
// gateways: it drives OpenAI-compatible chat completions endpoints and reports
// throughput plus percentile latencies.
//
// Usage:
//
//	go run ./cmd/bench -url http://localhost:8080/v1/chat/completions \
//	  -key sk-omniswitch-... -model gpt-4o-mini -duration 30s -concurrency 8 -rate 50
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	url := flag.String("url", "http://localhost:8080/v1/chat/completions", "target endpoint")
	key := flag.String("key", os.Getenv("OPENAI_API_KEY"), "bearer token")
	model := flag.String("model", "gpt-4o-mini", "model name")
	prompt := flag.String("prompt", "Say hi", "user prompt")
	maxTokens := flag.Int("max-tokens", 16, "max output tokens")
	duration := flag.Duration("duration", 30*time.Second, "test duration")
	concurrency := flag.Int("concurrency", 8, "parallel workers")
	rate := flag.Float64("rate", 0, "target requests per second (0 = unlimited)")
	timeout := flag.Duration("timeout", 120*time.Second, "per-request timeout")
	flag.Parse()

	payload, err := json.Marshal(map[string]any{
		"model": *model,
		"messages": []map[string]string{
			{"role": "user", "content": *prompt},
		},
		"max_tokens": *maxTokens,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal payload:", err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: *timeout}
	var (
		stop       = make(chan struct{})
		wg         sync.WaitGroup
		total      atomic.Int64
		errors     atomic.Int64
		mu         sync.Mutex
		latencies  []float64
		ticketChan chan struct{}
	)
	if *rate > 0 {
		ticketChan = make(chan struct{}, 1)
		go func() {
			interval := time.Duration(float64(time.Second) / *rate)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					select {
					case ticketChan <- struct{}{}:
					default:
					}
				}
			}
		}()
	}

	worker := func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if ticketChan != nil {
				select {
				case <-ticketChan:
				case <-stop:
					return
				}
			}
			req, err := http.NewRequest(http.MethodPost, *url, bytes.NewReader(payload))
			if err != nil {
				errors.Add(1)
				continue
			}
			req.Header.Set("Content-Type", "application/json")
			if *key != "" {
				req.Header.Set("Authorization", "Bearer "+*key)
			}
			start := time.Now()
			resp, err := client.Do(req)
			latency := time.Since(start).Seconds() * 1000
			if err != nil {
				errors.Add(1)
				continue
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			mu.Lock()
			latencies = append(latencies, latency)
			mu.Unlock()
			if resp.StatusCode >= 400 {
				errors.Add(1)
				continue
			}
			total.Add(1)
		}
	}

	fmt.Printf("Target %s | model=%s concurrency=%d rate=%.0f/s duration=%s\n\n",
		*url, *model, *concurrency, *rate, *duration)
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go worker()
	}
	time.Sleep(*duration)
	close(stop)
	wg.Wait()

	n := len(latencies)
	if n == 0 {
		fmt.Println("No completed requests.")
		os.Exit(1)
	}
	sort.Float64s(latencies)
	percentile := func(p float64) float64 {
		idx := int(float64(n-1) * p)
		return latencies[idx]
	}
	elapsed := *duration
	fmt.Printf("Requests  : %d ok, %d failed (%.2f%%)\n", total.Load(), errors.Load(), 100*float64(errors.Load())/float64(total.Load()+errors.Load()))
	fmt.Printf("Throughput: %.1f req/s\n", float64(total.Load())/elapsed.Seconds())
	fmt.Printf("Latency   : p50=%.1fms  p90=%.1fms  p95=%.1fms  p99=%.1fms  max=%.1fms  mean=%.1fms\n",
		percentile(0.50), percentile(0.90), percentile(0.95), percentile(0.99), latencies[n-1], func() float64 {
			sum := 0.0
			for _, l := range latencies {
				sum += l
			}
			return sum / float64(n)
		}())
}
