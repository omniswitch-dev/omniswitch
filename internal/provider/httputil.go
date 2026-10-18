package provider

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultRequestTimeout bounds a whole non-streaming upstream exchange
	// and, for streaming requests, the wait for the response headers.
	defaultRequestTimeout = 120 * time.Second

	// maxErrorBodyBytes caps how much of a non-2xx upstream body is read
	// and copied into errors, logs and ProviderMeta.Error.
	maxErrorBodyBytes = 64 << 10

	// maxResponseBodyBytes caps a successful non-streaming upstream body.
	// It is deliberately generous (large embedding batches and base64
	// images are tens of MiB) and exists only to stop a misbehaving
	// upstream from exhausting gateway memory.
	maxResponseBodyBytes = 256 << 20
)

// newProviderClient returns the HTTP client an adapter uses. Its overall
// Timeout bounds non-streaming calls; its transport's ResponseHeaderTimeout
// bounds the wait for response headers. Streaming calls must go through
// streamingClient, which drops the overall Timeout: http.Client.Timeout also
// covers reading the body and would cut long SSE streams off mid-answer.
func newProviderClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	return &http.Client{Transport: newProviderTransport(timeout), Timeout: timeout}
}

func newProviderTransport(headerTimeout time.Duration) *http.Transport {
	var transport *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	} else {
		transport = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		}
	}
	transport.ResponseHeaderTimeout = headerTimeout
	return transport
}

// streamingClient returns a copy of plain without the overall Timeout, for
// streaming requests. Cancellation then comes from the request context
// (client disconnect, gateway deadlines) and the transport's
// ResponseHeaderTimeout still bounds the time to first byte. plain must not
// be nil; every adapter constructor sets its client.
func streamingClient(plain *http.Client) *http.Client {
	if plain.Timeout == 0 {
		return plain
	}
	copied := *plain
	copied.Timeout = 0
	return &copied
}

// UpstreamError is returned when an upstream provider answers with a
// non-2xx status. Error() keeps the historical "<op> (status N): <body>"
// text so status detection based on the message keeps working, while
// callers can use errors.As to read the status code and Retry-After hint.
type UpstreamError struct {
	// Provider is the adapter or account name (e.g. "openai", "azure-eu").
	Provider string
	// Op is the message prefix, e.g. "openai error" or "groq stream error".
	Op string
	// StatusCode is the upstream HTTP status code.
	StatusCode int
	// RetryAfter is the server's back-off hint (Retry-After or
	// retry-after-ms headers); zero when absent.
	RetryAfter time.Duration
	// Body is the upstream error body, truncated to maxErrorBodyBytes.
	Body string
	// Truncated reports whether Body was cut short.
	Truncated bool
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("%s (status %d): %s", e.Op, e.StatusCode, e.Body)
}

// newUpstreamError reads at most maxErrorBodyBytes of resp.Body and builds an
// UpstreamError. The caller still owns closing resp.Body.
func newUpstreamError(providerName, op string, resp *http.Response) *UpstreamError {
	body, truncated := readErrorBody(resp.Body)
	return &UpstreamError{
		Provider:   providerName,
		Op:         op,
		StatusCode: resp.StatusCode,
		RetryAfter: parseRetryAfter(resp.Header, time.Now()),
		Body:       body,
		Truncated:  truncated,
	}
}

func readErrorBody(r io.Reader) (string, bool) {
	if r == nil {
		return "", false
	}
	data, _ := io.ReadAll(io.LimitReader(r, maxErrorBodyBytes+1))
	if len(data) > maxErrorBodyBytes {
		return string(data[:maxErrorBodyBytes]) + "...(truncated)", true
	}
	return string(data), false
}

// parseRetryAfter understands Retry-After as delta-seconds or an HTTP date,
// and the millisecond variant retry-after-ms sent by OpenAI and Azure.
func parseRetryAfter(header http.Header, now time.Time) time.Duration {
	if header == nil {
		return 0
	}
	if value := strings.TrimSpace(header.Get("Retry-After-Ms")); value != "" {
		if ms, err := strconv.ParseFloat(value, 64); err == nil && ms > 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds * float64(time.Second))
	}
	if when, err := http.ParseTime(value); err == nil {
		if d := when.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// errResponseTooLarge is returned when a successful upstream body exceeds
// maxResponseBodyBytes.
var errResponseTooLarge = fmt.Errorf("upstream response exceeds %d bytes", maxResponseBodyBytes)

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errResponseTooLarge
	}
	return data, nil
}
