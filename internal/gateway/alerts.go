package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type AlertConfig struct {
	WebhookURL string
	SlackURL   string
	Thresholds []float64
}

type AlertManager struct {
	config AlertConfig
	client *http.Client
	fired  sync.Map
}

func NewAlertManager(config AlertConfig) *AlertManager {
	return &AlertManager{
		config: config,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

func (a *AlertManager) CheckBudget(keyID string, spent, budget float64) {
	if a == nil || keyID == "" || spent <= 0 || budget <= 0 {
		return
	}
	for _, threshold := range a.config.Thresholds {
		if threshold <= 0 || threshold > 1 || spent/budget < threshold {
			continue
		}
		key := fmt.Sprintf("%s:%.4f", keyID, threshold)
		if _, loaded := a.fired.LoadOrStore(key, true); loaded {
			continue
		}
		go a.fire(keyID, spent, budget, threshold)
	}
}

func (a *AlertManager) fire(keyID string, spent, budget, threshold float64) {
	message := fmt.Sprintf("OmniSwitch budget alert: API key %s reached %.0f%% of budget ($%.4f / $%.4f)", keyID, threshold*100, spent, budget)
	payload := map[string]any{
		"text":      message,
		"key_id":    keyID,
		"spent":     spent,
		"budget":    budget,
		"threshold": threshold,
		"timestamp": time.Now().UTC(),
	}
	if a.config.WebhookURL != "" {
		a.postJSON(a.config.WebhookURL, payload)
	}
	if a.config.SlackURL != "" {
		a.postJSON(a.config.SlackURL, map[string]any{"text": message})
	}
}

// alertTarget names a webhook for logs without its path or query. Slack and
// most webhook URLs carry their secret in the path, so the full URL must
// never be logged.
func alertTarget(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "invalid alert URL"
	}
	return parsed.Scheme + "://" + parsed.Host
}

// alertError drops the URL that *url.Error embeds in its message.
func alertError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

func (a *AlertManager) postJSON(rawURL string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("failed to marshal alert payload: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		log.Printf("failed to build alert request for %s: %v", alertTarget(rawURL), alertError(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		log.Printf("failed to fire alert webhook %s: %v", alertTarget(rawURL), alertError(err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("alert webhook %s returned status %d", alertTarget(rawURL), resp.StatusCode)
	}
}
