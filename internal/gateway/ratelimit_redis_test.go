package gateway

import "testing"

func TestNewRedisRateLimitBackendRejectsInvalidURL(t *testing.T) {
	if _, err := NewRedisRateLimitBackend(":// bad", ""); err == nil {
		t.Fatal("NewRedisRateLimitBackend() error = nil, want invalid URL error")
	}
}
