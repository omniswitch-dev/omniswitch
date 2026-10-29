package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// ExternalAuthorizer asks an operator-configured HTTP policy service for a
// decision. It sends identity and request attributes, never bearer credentials,
// raw request headers, query strings, or prompt/response content.
type ExternalAuthorizer struct {
	url    string
	client *http.Client
}

func NewExternalAuthorizer(endpoint string, timeout time.Duration) (*ExternalAuthorizer, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("external authorization requires an absolute HTTP(S) URL without credentials or fragment")
	}
	if timeout < 0 {
		return nil, fmt.Errorf("external authorization timeout must be positive")
	}
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	return &ExternalAuthorizer{url: endpoint, client: &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (a *ExternalAuthorizer) Evaluate(r *http.Request, identity Identity) (bool, string, error) {
	model, err := modelFromRequest(r)
	if err != nil {
		return false, "", err
	}
	payload := struct {
		Method   string            `json:"method"`
		Path     string            `json:"path"`
		Model    string            `json:"model,omitempty"`
		Identity map[string]string `json:"identity"`
	}{r.Method, r.URL.Path, model, map[string]string{
		"api_key_id":      identity.APIKeyID,
		"subject":         identity.Subject,
		"workspace_id":    identity.WorkspaceID,
		"organization_id": identity.OrganizationID,
		"role":            normalizeRole(identity.Role),
		"auth_method":     identity.AuthMethod,
	}}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, "", err
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(body))
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(req)
	if err != nil {
		return false, "", fmt.Errorf("external authorization request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, "", fmt.Errorf("external authorization returned HTTP %d", response.StatusCode)
	}
	const maxResponseBytes = 64 << 10
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return false, "", fmt.Errorf("invalid external authorization response size")
	}
	var decision struct {
		Allowed *bool  `json:"allowed"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &decision); err != nil || decision.Allowed == nil {
		return false, "", fmt.Errorf("external authorization must return an explicit allowed boolean")
	}
	if decision.Message == "" {
		decision.Message = "request denied by external authorization"
	}
	return *decision.Allowed, decision.Message, nil
}
