package provider

import (
	"regexp"
	"strings"

	"github.com/omniswitch-dev/omniswitch/internal/provider/catalog"
)

// EstimateCost returns the provider-specific estimated cost for a token usage.
func EstimateCost(providerName, model string, usage Usage) float64 {
	return pricingFor(providerName, model).Cost(usage.PromptTokens, usage.CompletionTokens)
}

func InputPricePerMillion(providerName, model string) float64 {
	return pricingFor(providerName, model).InputPerMillion
}

// presetPricingProvider holds catalog rows for OpenAI-compatible services
// reached through the Custom adapter (Mistral, DeepSeek, xAI, ...).
const presetPricingProvider = "custom-preset-reference"

// bedrockRegionPrefixes are the cross-region inference-profile prefixes
// Bedrock puts in front of a base model ID ("us.anthropic.claude-...").
var bedrockRegionPrefixes = []string{"us-gov.", "us.", "eu.", "apac.", "global.", "jp.", "au.", "ca."}

// bedrockVersionSuffix matches the "-v1:0" style suffix of Bedrock model IDs.
var bedrockVersionSuffix = regexp.MustCompile(`-v\d+(:\d+)?$`)

// catalogSnapshotDate matches a trailing snapshot date ("-20250514").
var catalogSnapshotDate = regexp.MustCompile(`-\d{8}$`)

// pricingFor resolves per-token pricing from the consolidated model catalog
// (internal/provider/catalog/models.json).
//
//   - An "@alias/" model prefix (AliasProvider) is stripped first.
//   - "azure" deployments bill at the underlying OpenAI model's rate.
//   - "bedrock" IDs have their region/inference-profile prefix removed;
//     Anthropic models not listed under bedrock fall back to the
//     first-party Anthropic rate, which Bedrock on-demand pricing matches.
//   - Dated or versioned snapshots ("gpt-4o-2024-08-06",
//     "claude-opus-4-1-20250805") fall back to the longest catalog ID they
//     extend, as long as the remaining suffix is a version/date (starts
//     with a digit) or "latest"; "gpt-4o-mini" is never priced as "gpt-4o".
//   - Providers with no catalog rows of their own ("mistral", "deepseek",
//     "xai", or the "custom" provider type) are looked up against the
//     custom-preset reference rows.
//
// Models still not found return a zero ModelPricing.
func pricingFor(providerName, model string) ModelPricing {
	model = stripAliasModelPrefix(strings.TrimSpace(model))
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	switch providerName {
	case "azure":
		providerName = "openai"
	case "bedrock":
		return bedrockPricing(model)
	}
	if p, ok := catalogPricing(providerName, model); ok {
		return p
	}
	if len(catalog.ForProvider(providerName)) == 0 {
		if p, ok := catalogPricing(presetPricingProvider, model); ok {
			return p
		}
	}
	return ModelPricing{}
}

// bedrockPricing resolves a Bedrock model ID (optionally with a
// cross-region prefix or given as an inference-profile ARN) to catalog
// pricing.
func bedrockPricing(model string) ModelPricing {
	if i := strings.LastIndex(model, "/"); i >= 0 && strings.HasPrefix(model, "arn:") {
		model = model[i+1:]
	}
	for _, prefix := range bedrockRegionPrefixes {
		if strings.HasPrefix(model, prefix) {
			model = strings.TrimPrefix(model, prefix)
			break
		}
	}
	if p, ok := catalogPricing("bedrock", model); ok {
		return p
	}
	if base, ok := strings.CutPrefix(model, "anthropic."); ok {
		base = bedrockVersionSuffix.ReplaceAllString(base, "")
		if p, ok := catalogPricing("anthropic", base); ok {
			return p
		}
	}
	return ModelPricing{}
}

// catalogPricing looks up model under provider exactly, then by the
// longest catalog ID that model extends with a version/date suffix.
func catalogPricing(provider, model string) (ModelPricing, bool) {
	if m, ok := catalog.Lookup(provider, model); ok {
		return ModelPricing{InputPerMillion: m.InputPerMillion, OutputPerMillion: m.OutputPerMillion}, true
	}
	var best catalog.Model
	found := false
	bestLen := 0
	for _, m := range catalog.ForProvider(provider) {
		// Match against the catalog ID and against its family name with
		// any snapshot date removed ("claude-opus-4-20250514" ->
		// "claude-opus-4"), so "claude-opus-4-1-20250805" and
		// "claude-3-5-sonnet-latest" resolve to their family's price.
		for _, key := range []string{m.ID, catalogSnapshotDate.ReplaceAllString(m.ID, "")} {
			rest, ok := strings.CutPrefix(model, key+"-")
			if !ok || !isVersionSuffix(rest) {
				continue
			}
			if !found || len(key) > bestLen {
				best, bestLen, found = m, len(key), true
			}
		}
	}
	if !found {
		return ModelPricing{}, false
	}
	return ModelPricing{InputPerMillion: best.InputPerMillion, OutputPerMillion: best.OutputPerMillion}, true
}

func isVersionSuffix(rest string) bool {
	if rest == "latest" {
		return true
	}
	return rest != "" && rest[0] >= '0' && rest[0] <= '9'
}

// stripAliasModelPrefix turns "@alias/model" into "model".
func stripAliasModelPrefix(model string) string {
	if !strings.HasPrefix(model, "@") {
		return model
	}
	if i := strings.Index(model, "/"); i >= 0 {
		return model[i+1:]
	}
	return model
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
