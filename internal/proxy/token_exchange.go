package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TokenExchangeConfig configures RFC 8693 token exchange for a target. The
// gateway sets it from mcp.targets[].token_exchange; embedders set it on
// TargetConfig.
type TokenExchangeConfig struct {
	// TokenURL is the token endpoint of the authorization server.
	TokenURL string `json:"token_url" yaml:"token_url"`

	// ClientID is the OAuth client identifier for the token exchange.
	ClientID string `json:"client_id" yaml:"client_id"`

	// ClientSecret is the OAuth client secret for the token exchange.
	ClientSecret string `json:"client_secret" yaml:"client_secret"`

	// SubjectTokenType is the type of the subject token being exchanged.
	// Defaults to "urn:ietf:params:oauth:token-type:access_token".
	SubjectTokenType string `json:"subject_token_type,omitempty" yaml:"subject_token_type,omitempty"`

	// RequestedTokenType is the type of token to request.
	// Defaults to "urn:ietf:params:oauth:token-type:access_token".
	RequestedTokenType string `json:"requested_token_type,omitempty" yaml:"requested_token_type,omitempty"`

	// Scope is the scope to request for the new token.
	Scope string `json:"scope,omitempty" yaml:"scope,omitempty"`

	// Audience is the logical name of the target service (RFC 8693 "audience").
	Audience string `json:"audience,omitempty" yaml:"audience,omitempty"`

	// ActorToken is an optional actor token for On-Behalf-Of flow.
	ActorToken string `json:"actor_token,omitempty" yaml:"actor_token,omitempty"`

	// ActorTokenType is the type of the actor token.
	// Defaults to "urn:ietf:params:oauth:token-type:access_token".
	ActorTokenType string `json:"actor_token_type,omitempty" yaml:"actor_token_type,omitempty"`

	// Resource indicates the target resource (RFC 8707).
	Resource string `json:"resource,omitempty" yaml:"resource,omitempty"`

	// TokenCacheTTL caps how long exchanged tokens are cached. The token's
	// own expires_in (minus one minute) is used when it is shorter.
	TokenCacheTTL time.Duration `json:"token_cache_ttl,omitempty" yaml:"token_cache_ttl,omitempty"`
}

const (
	tokenTypeAccessToken = "urn:ietf:params:oauth:token-type:access_token"
	maxCachedTokens      = 4096
)

type cachedToken struct {
	accessToken string
	expiresAt   time.Time
}

// tokenCache holds exchanged tokens per handler. Entries are keyed by the
// target, a hash of the subject token, and the requested audience, scope and
// resource, so one caller's exchanged token is never reused for another.
type tokenCache struct {
	mu      sync.Mutex
	entries map[string]cachedToken
}

func newTokenCache() *tokenCache {
	return &tokenCache{entries: map[string]cachedToken{}}
}

func tokenCacheKey(targetName, subjectToken string, te *TokenExchangeConfig) string {
	subject := sha256.Sum256([]byte(subjectToken))
	parts := []string{targetName, hex.EncodeToString(subject[:]), te.Audience, te.Scope, te.Resource, te.RequestedTokenType}
	key := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(key[:])
}

func (c *tokenCache) get(key string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}
	if !now.Before(entry.expiresAt) {
		delete(c.entries, key)
		return "", false
	}
	return entry.accessToken, true
}

func (c *tokenCache) put(key string, entry cachedToken, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxCachedTokens {
		for existing, value := range c.entries {
			if !now.Before(value.expiresAt) {
				delete(c.entries, existing)
			}
		}
		if len(c.entries) >= maxCachedTokens {
			c.entries = map[string]cachedToken{}
		}
	}
	c.entries[key] = entry
}

// bearerToken returns the bearer token of an Authorization header value.
func bearerToken(authorization string) string {
	scheme, token, ok := strings.Cut(strings.TrimSpace(authorization), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// exchangeToken performs (or serves from cache) an RFC 8693 token exchange
// for subjectToken. An empty subject token yields an empty result.
func (h *Handler) exchangeToken(ctx context.Context, tgt *target, subjectToken string) (string, error) {
	te := tgt.tokenExchange
	if te == nil || subjectToken == "" {
		return "", nil
	}
	now := time.Now()
	key := tokenCacheKey(tgt.name, subjectToken, te)
	if token, ok := h.tokens.get(key, now); ok {
		return token, nil
	}

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:token-exchange")
	form.Set("subject_token", subjectToken)
	form.Set("subject_token_type", firstNonEmptyStr(te.SubjectTokenType, tokenTypeAccessToken))
	form.Set("requested_token_type", firstNonEmptyStr(te.RequestedTokenType, tokenTypeAccessToken))
	if te.Scope != "" {
		form.Set("scope", te.Scope)
	}
	if te.Audience != "" {
		form.Set("audience", te.Audience)
	}
	if te.Resource != "" {
		form.Set("resource", te.Resource)
	}
	if te.ActorToken != "" {
		form.Set("actor_token", te.ActorToken)
		form.Set("actor_token_type", firstNonEmptyStr(te.ActorTokenType, tokenTypeAccessToken))
	}
	form.Set("client_id", te.ClientID)
	form.Set("client_secret", te.ClientSecret)

	tokenReq, err := http.NewRequestWithContext(ctx, http.MethodPost, te.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenReq.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(tokenReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %s", resp.Status)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		IssuedToken string `json:"issued_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	accessToken := firstNonEmptyStr(tokenResp.AccessToken, tokenResp.IssuedToken)
	if accessToken == "" {
		return "", fmt.Errorf("no access token in response")
	}

	expiresIn := time.Duration(tokenResp.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = time.Hour
	}
	lifetime := expiresIn - time.Minute
	if te.TokenCacheTTL > 0 && te.TokenCacheTTL < lifetime {
		lifetime = te.TokenCacheTTL
	}
	if lifetime > 0 {
		h.tokens.put(key, cachedToken{accessToken: accessToken, expiresAt: now.Add(lifetime)}, now)
	}
	return accessToken, nil
}
