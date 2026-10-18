package provider

import "testing"

func TestPricingForFallbacks(t *testing.T) {
	cases := []struct {
		provider, model string
		in, out         float64
	}{
		{"openai", "gpt-4o-mini", 0.15, 0.60},
		{"openai", "gpt-4o-2024-08-06", 2.50, 10.00},
		{"openai", "gpt-4o-mini-2024-07-18", 0.15, 0.60},
		{"openai", "gpt-4.1-mini", 0.40, 1.60},
		{"openai", "gpt-5-mini", 0.25, 2.00},
		{"openai", "o1-mini", 1.10, 4.40},
		{"azure", "gpt-4o", 2.50, 10.00},
		{"openai", "@prod/gpt-4o", 2.50, 10.00},
		{"anthropic", "claude-opus-4-1-20250805", 15.00, 75.00},
		{"anthropic", "claude-sonnet-4-5-20250929", 3.00, 15.00},
		{"anthropic", "claude-3-5-sonnet-latest", 3.00, 15.00},
		{"google", "gemini-2.5-flash", 0.30, 2.50},
		{"bedrock", "us.anthropic.claude-sonnet-4-20250514-v1:0", 3.00, 15.00},
		{"bedrock", "anthropic.claude-3-7-sonnet-20250219-v1:0", 3.00, 15.00},
		{"bedrock", "meta.llama3-1-70b-instruct-v1:0", 0.99, 0.99},
		{"bedrock", "eu.anthropic.claude-3-5-haiku-20241022-v1:0", 0.80, 4.00},
		{"bedrock", "arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-3-5-sonnet-20241022-v2:0", 3.00, 15.00},
		{"mistral", "mistral-large-latest", 2.00, 6.00},
		{"custom", "mistral-large-latest", 2.00, 6.00},
		{"deepseek", "deepseek-chat", 0.28, 0.42},
		{"xai", "grok-4", 3.00, 15.00},
	}
	for _, c := range cases {
		got := pricingFor(c.provider, c.model)
		if got.InputPerMillion != c.in || got.OutputPerMillion != c.out {
			t.Errorf("pricingFor(%q, %q) = %+v, want %v/%v", c.provider, c.model, got, c.in, c.out)
		}
	}
}

func TestPricingForDoesNotGuessAcrossModelFamilies(t *testing.T) {
	for _, c := range [][2]string{
		{"openai", "gpt-4o-audio-preview"}, // must not price as gpt-4o
		{"google", "gemini-2.5-flash-lite"},
		{"groq", "mixtral-8x7b-32768"}, // decommissioned, removed from catalog
		{"openai", "mistral-large-latest"},
		{"unknown", "model"},
	} {
		if got := pricingFor(c[0], c[1]); got != (ModelPricing{}) {
			t.Errorf("pricingFor(%q, %q) = %+v, want zero", c[0], c[1], got)
		}
	}
}
