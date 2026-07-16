package main

import (
	"log"
	"net/http"
	"os"

	"github.com/omniswitch-dev/omniswitch/internal/audit"
	"github.com/omniswitch-dev/omniswitch/internal/policy"
	"github.com/omniswitch-dev/omniswitch/internal/proxy"
)

func main() {
	listenAddr := env("OMNISWITCH_LISTEN", ":8080")
	upstreamURL := env("OMNISWITCH_UPSTREAM", "http://127.0.0.1:8090/mcp")
	policyPath := env("OMNISWITCH_POLICY", "policies/production-delete.yaml")

	engine, err := policy.NewEngineFromFiles(policyPath)
	if err != nil {
		log.Fatalf("failed to load policy: %v", err)
	}

	handler, err := proxy.NewHandler(engine, audit.NewStdoutLogger(os.Stdout), upstreamURL)
	if err != nil {
		log.Fatalf("failed to initialize proxy: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)

	log.Printf("omniswitch proxy listening on %s, forwarding allowed requests to %s", listenAddr, upstreamURL)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatalf("proxy server failed: %v", err)
	}
}

func env(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
