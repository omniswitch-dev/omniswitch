package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Provider struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Note      string `json:"note"`
	Detected  bool   `json:"detected"`
	Env       string `json:"-"`
	BaseURL   string `json:"-"`
	Preferred string `json:"-"`
}

var providers = []Provider{
	{ID: "openai", Name: "OpenAI", Note: "GPT models", Env: "OPENAI_API_KEY", BaseURL: "https://api.openai.com/v1", Preferred: "gpt-4o-mini"},
	{ID: "anthropic", Name: "Anthropic", Note: "Claude models", Env: "ANTHROPIC_API_KEY", BaseURL: "https://api.anthropic.com/v1", Preferred: "claude-haiku-4-5"},
	{ID: "google", Name: "Google", Note: "Gemini models", Env: "GOOGLE_API_KEY", BaseURL: "https://generativelanguage.googleapis.com/v1beta", Preferred: "gemini-2.5-flash"},
	{ID: "groq", Name: "Groq", Note: "Fast open models", Env: "GROQ_API_KEY", BaseURL: "https://api.groq.com/openai/v1", Preferred: "llama-3.3-70b-versatile"},
	{ID: "openrouter", Name: "OpenRouter", Note: "Multiple providers", Env: "OPENROUTER_API_KEY", BaseURL: "https://openrouter.ai/api/v1", Preferred: "openai/gpt-4o-mini"},
	{ID: "local", Name: "Local model", Note: "Ollama, vLLM, or compatible endpoint", BaseURL: "http://127.0.0.1:11434/v1"},
	{ID: "demo", Name: "Try the demo", Note: "No key. No cost. Simulated responses.", Preferred: "demo-model"},
}

func findProvider(id string) (Provider, bool) {
	for _, p := range providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

func providerKey(p Provider, env func(string) string) string {
	if env == nil {
		return ""
	}
	key := env(p.Env)
	if key == "" && p.ID == "google" {
		key = env("GEMINI_API_KEY")
	}
	return strings.TrimSpace(key)
}

// Probe verifies model discovery only. It never makes a paid inference call,
// follows redirects, or returns an upstream error body that might echo a key.
func Probe(ctx context.Context, connection Connection) (Connection, error) {
	spec, ok := findProvider(connection.Provider)
	if !ok {
		return connection, fmt.Errorf("choose a supported provider")
	}
	if spec.ID == "demo" {
		return Connection{Provider: "demo", Model: "demo-model"}, nil
	}
	if spec.ID != "local" && strings.TrimSpace(connection.Key) == "" {
		return connection, fmt.Errorf("enter your provider API key")
	}
	base := spec.BaseURL
	if spec.ID == "local" {
		base = strings.TrimRight(strings.TrimSpace(connection.BaseURL), "/")
		parsed, err := url.Parse(base)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return connection, fmt.Errorf("enter an HTTP(S) base URL without credentials or query parameters")
		}
		connection.BaseURL = base
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return connection, fmt.Errorf("invalid provider endpoint")
	}
	switch spec.ID {
	case "anthropic":
		request.Header.Set("x-api-key", connection.Key)
		request.Header.Set("anthropic-version", "2023-06-01")
	case "google":
		request.Header.Set("x-goog-api-key", connection.Key)
	default:
		if connection.Key != "" {
			request.Header.Set("Authorization", "Bearer "+connection.Key)
		}
	}
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return connection, fmt.Errorf("could not reach the provider; check your connection and endpoint")
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return connection, fmt.Errorf("the provider did not accept this key; check its permissions")
	}
	if response.StatusCode != 200 {
		return connection, fmt.Errorf("provider model discovery returned HTTP %d; try again or use another endpoint", response.StatusCode)
	}
	const max = 2 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, max+1))
	if err != nil || len(data) > max {
		return connection, fmt.Errorf("could not read the provider model list")
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name    string   `json:"name"`
			Methods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return connection, fmt.Errorf("provider returned an invalid model list")
	}
	var models []string
	for _, m := range payload.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	for _, m := range payload.Models {
		for _, method := range m.Methods {
			if method == "generateContent" {
				models = append(models, strings.TrimPrefix(m.Name, "models/"))
				break
			}
		}
	}
	if len(models) == 0 {
		return connection, fmt.Errorf("no models are available; add a model to your local server or check provider permissions")
	}
	sort.Strings(models)
	wanted := strings.TrimSpace(connection.Model)
	if wanted == "" {
		wanted = spec.Preferred
	}
	found := false
	for _, m := range models {
		if m == wanted {
			found = true
			break
		}
	}
	if !found && connection.Model != "" {
		return connection, fmt.Errorf("the selected model is not in the provider model list")
	}
	if !found {
		wanted = models[0]
	}
	connection.Model = wanted
	return connection, nil
}
