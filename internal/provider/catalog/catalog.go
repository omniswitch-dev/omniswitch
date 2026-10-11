// Package catalog is the single source of truth for OmniSwitch's model list
// and pricing data. It is deliberately provider-agnostic (no dependency on
// package provider) so it can be consumed by internal/provider (for
// Models()/pricing) as well as docs generation without creating an import
// cycle.
//
// Data lives in models.json, embedded at build time via go:embed, and is
// hand-maintained: each entry records the USD price per 1M tokens, the date
// it was last checked against the provider's public pricing page
// (verified_on), and the source_url it was checked against. There is no live
// pricing fetch; keeping this file current is a manual, periodic task (see
// docs/CONFIGURATION.md's "Model catalog" section).
package catalog

import (
	"embed"
	"encoding/json"
	"strings"
	"sync"
)

//go:embed models.json
var embedded embed.FS

// Model is one catalog entry: a specific model ID under a specific provider,
// with its per-1M-token pricing and provenance.
type Model struct {
	Provider         string  `json:"provider"`
	ID               string  `json:"id"`
	OwnedBy          string  `json:"owned_by"`
	InputPerMillion  float64 `json:"input_per_million"`
	OutputPerMillion float64 `json:"output_per_million"`
	SourceURL        string  `json:"source_url"`
	VerifiedOn       string  `json:"verified_on"`
	Notes            string  `json:"notes,omitempty"`
}

type catalogFile struct {
	VerifiedOn string  `json:"verified_on"`
	Models     []Model `json:"models"`
}

var (
	loadOnce   sync.Once
	loadErr    error
	all        []Model
	verifiedOn string
)

func load() {
	loadOnce.Do(func() {
		data, err := embedded.ReadFile("models.json")
		if err != nil {
			loadErr = err
			return
		}
		var parsed catalogFile
		if err := json.Unmarshal(data, &parsed); err != nil {
			loadErr = err
			return
		}
		all = parsed.Models
		verifiedOn = parsed.VerifiedOn
	})
}

// All returns every catalog entry across all providers. It never returns an
// error; a malformed embedded catalog is a build-time bug, not a runtime
// condition callers need to handle, so this logs nothing and returns an
// empty slice if the embedded file somehow fails to parse.
func All() []Model {
	load()
	if loadErr != nil {
		return nil
	}
	return all
}

// VerifiedOn returns the catalog-wide default verification date recorded in
// models.json.
func VerifiedOn() string {
	load()
	return verifiedOn
}

// ForProvider returns every catalog entry for the given provider name
// (case-insensitive), in the order they appear in models.json.
func ForProvider(provider string) []Model {
	provider = strings.ToLower(strings.TrimSpace(provider))
	var out []Model
	for _, m := range All() {
		if strings.ToLower(m.Provider) == provider {
			out = append(out, m)
		}
	}
	return out
}

// Lookup finds a single model by provider and model ID (case-insensitive on
// provider, exact match on model ID since IDs are case-sensitive upstream).
func Lookup(provider, model string) (Model, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, m := range All() {
		if strings.ToLower(m.Provider) == provider && m.ID == model {
			return m, true
		}
	}
	return Model{}, false
}
