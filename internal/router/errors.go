package router

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

// maxRetryAfterWait is the longest upstream Retry-After hint the router waits
// for before it retries the same provider. A longer hint ends the retries
// against that provider so the next provider in the fallback chain is tried
// at once instead of holding the request open.
const maxRetryAfterWait = 10 * time.Second

// statusCodePattern reads an HTTP status from the text of an error that is
// not a *provider.UpstreamError, for example "status 429" or "status code
// (503)".
var statusCodePattern = regexp.MustCompile(`(?i)status\s*(?:code\s*)?\(?([0-9]{3})\)?`)

// upstreamStatus returns the upstream HTTP status carried by err. Typed
// provider errors are read directly; other errors fall back to the status
// text in the message.
func upstreamStatus(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var upstream *provider.UpstreamError
	if errors.As(err, &upstream) && upstream.StatusCode > 0 {
		return upstream.StatusCode, true
	}
	match := statusCodePattern.FindStringSubmatch(err.Error())
	if len(match) != 2 {
		return 0, false
	}
	code, convErr := strconv.Atoi(match[1])
	if convErr != nil {
		return 0, false
	}
	return code, true
}

// transientStatus reports whether an upstream status describes a condition
// that can clear on its own: a timeout, a conflict, throttling or a server
// error. Other 4xx statuses describe the request itself.
func transientStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	}
	return code >= 500
}

// isRetryable reports whether err is worth another attempt against the same
// provider. The caller's provider loop still falls back to the next provider
// when it is not.
func isRetryable(err error, codes []int) bool {
	if err == nil {
		return false
	}
	// A request field an adapter cannot honour (e.g. seed on Anthropic) will
	// fail identically on every retry against the same provider; surface it
	// immediately instead of burning retry budget.
	var unsupported *provider.UnsupportedParameterError
	if errors.As(err, &unsupported) {
		return false
	}
	code, known := upstreamStatus(err)
	if len(codes) == 0 {
		// Without retry_codes, network failures, timeouts, throttling and
		// server errors are retried. Client errors such as 400, 401 or 404
		// fail the same way on every attempt.
		return !known || transientStatus(code)
	}
	return known && slices.Contains(codes, code)
}

// retryWait returns the delay before the next attempt against the same
// provider and whether that attempt should happen. The route backoff is the
// minimum; an upstream Retry-After hint can extend it up to
// maxRetryAfterWait. A longer hint stops the retries for this provider.
func retryWait(backoff string, attempt int, err error) (time.Duration, bool) {
	delay := retryBackoff(backoff, attempt)
	var upstream *provider.UpstreamError
	if errors.As(err, &upstream) && upstream.RetryAfter > 0 {
		if upstream.RetryAfter > maxRetryAfterWait {
			return 0, false
		}
		if upstream.RetryAfter > delay {
			delay = upstream.RetryAfter
		}
	}
	return delay, true
}

// countsAsProviderFailure reports whether err says something about the
// provider's health and so should count toward its circuit breaker.
//
// Client cancellations do not count. Neither do errors caused by the request
// itself (bad parameters, an unknown model, a model the account cannot use,
// an oversized prompt): any caller could otherwise open the circuit for every
// tenant with a few malformed requests. Network failures, timeouts,
// throttling (429) and server errors (5xx) count.
func countsAsProviderFailure(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var unsupported *provider.UnsupportedParameterError
	if errors.As(err, &unsupported) {
		return false
	}
	if code, known := upstreamStatus(err); known && code >= 400 && code < 500 {
		return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests
	}
	return true
}
