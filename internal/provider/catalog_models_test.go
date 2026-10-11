package provider

import "testing"

// TestProviderModelsAreSourcedFromCatalog is a smoke test for A8: each
// static provider's Models() must be driven by the consolidated catalog
// (internal/provider/catalog/models.json), not a separate hardcoded list
// that can drift from pricing data.
func TestProviderModelsAreSourcedFromCatalog(t *testing.T) {
	cases := []struct {
		name     string
		models   []ModelInfo
		wantID   string
		wantProv string
	}{
		{"openai", NewOpenAI("").Models(), "gpt-4o-mini", "openai"},
		{"anthropic", NewAnthropic("").Models(), "claude-3-5-haiku-20241022", "anthropic"},
		{"google", NewGoogle("").Models(), "gemini-2.5-flash", "google"},
		{"groq", NewGroq("").Models(), "llama-3.1-8b-instant", "groq"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.models) == 0 {
				t.Fatalf("%s.Models() returned no models", tc.name)
			}
			var found bool
			for _, m := range tc.models {
				if m.ID == tc.wantID {
					found = true
					if m.Provider != tc.wantProv {
						t.Fatalf("model %q Provider = %q, want %q", m.ID, m.Provider, tc.wantProv)
					}
				}
			}
			if !found {
				t.Fatalf("%s.Models() = %+v, want to include %q", tc.name, tc.models, tc.wantID)
			}
		})
	}
}
