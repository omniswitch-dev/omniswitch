// Command proxy runs the MCP policy gateway as a standalone, local proxy.
//
// It has no authentication of its own, so it listens on loopback by default
// and discards client-supplied x-omniswitch-* identity headers: identity-based
// policy rules see an anonymous caller. Use the gateway binary for
// authenticated, multi-tenant deployments.
package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/audit"
	"github.com/omniswitch-dev/omniswitch/internal/policy"
	"github.com/omniswitch-dev/omniswitch/internal/proxy"
)

const defaultListenAddr = "127.0.0.1:8080"

func main() {
	listenAddr := env("OMNISWITCH_LISTEN", defaultListenAddr)
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

	server := newServer(listenAddr, handler)
	log.Printf("omniswitch proxy listening on %s, forwarding allowed requests to %s", listenAddr, upstreamURL)
	if err := server.ListenAndServe(); err != nil {
		log.Printf("proxy server failed: %v", err)
		handler.Close()
		os.Exit(1)
	}
}

// newServer mounts handler at /mcp behind the identity-header filter with
// bounded header and idle timeouts. There is no overall write timeout because
// MCP responses may be long-lived SSE streams.
func newServer(addr string, handler http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/mcp", stripIdentityHeaders(handler))
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// stripIdentityHeaders removes every x-omniswitch-* request header. The
// policy handler trusts these headers as gateway-authenticated identity, and
// this binary authenticates nobody, so a client must not be able to set them.
func stripIdentityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for key := range r.Header {
			if strings.HasPrefix(strings.ToLower(key), "x-omniswitch-") {
				r.Header.Del(key)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func env(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
