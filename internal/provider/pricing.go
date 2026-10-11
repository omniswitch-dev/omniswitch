package provider

import "github.com/omniswitch-dev/omniswitch/internal/provider/catalog"

// EstimateCost returns the provider-specific estimated cost for a token usage.
func EstimateCost(providerName, model string, usage Usage) float64 {
	return pricingFor(providerName, model).Cost(usage.PromptTokens, usage.CompletionTokens)
}

func InputPricePerMillion(providerName, model string) float64 {
	return pricingFor(providerName, model).InputPerMillion
}

// pricingFor resolves per-token pricing from the consolidated model catalog
// (internal/provider/catalog/models.json). "azure" deployments bill at the
// same rate as the underlying OpenAI model, so they share the "openai"
// catalog entries. "bedrock" model IDs vary by region/cross-region
// inference-profile prefix (e.g. "us.anthropic.claude-3-5-sonnet..."), so
// bedrock pricing matches by substring against known catalog entries instead
// of an exact ID lookup; see bedrockPricing in bedrock.go. Models not present
// in the catalog (e.g. self-hosted or brand-new releases not yet added)
// return a zero ModelPricing, matching the previous fallback behavior.
func pricingFor(providerName, model string) ModelPricing {
	if providerName == "bedrock" {
		return bedrockPricing(model)
	}
	lookupProvider := providerName
	if providerName == "azure" {
		lookupProvider = "openai"
	}
	if m, ok := catalog.Lookup(lookupProvider, model); ok {
		return ModelPricing{InputPerMillion: m.InputPerMillion, OutputPerMillion: m.OutputPerMillion}
	}
	return ModelPricing{}
}

// catalogPricingOrZero looks up a single, known-exact catalog entry and
// returns its pricing, or a zero ModelPricing if the entry is missing (e.g.
// the catalog was edited without updating this reference).
func catalogPricingOrZero(catalogProvider, id string) ModelPricing {
	if m, ok := catalog.Lookup(catalogProvider, id); ok {
		return ModelPricing{InputPerMillion: m.InputPerMillion, OutputPerMillion: m.OutputPerMillion}
	}
	return ModelPricing{}
}

// catalogModelInfos returns the ModelInfo list a static provider (OpenAI,
// Anthropic, Google, Groq, Cohere) advertises via Models(), sourced from the
// consolidated catalog. catalogProvider selects the catalog rows (e.g.
// "openai"); providerLabel is stamped onto each ModelInfo.Provider field
// (normally the same value, kept distinct for providers whose registered
// name can differ, like Bedrock).
func catalogModelInfos(catalogProvider, providerLabel string) []ModelInfo {
	entries := catalog.ForProvider(catalogProvider)
	out := make([]ModelInfo, len(entries))
	for i, m := range entries {
		out[i] = ModelInfo{ID: m.ID, Object: "model", OwnedBy: m.OwnedBy, Provider: providerLabel}
	}
	return out
}
