package provider

// EstimateCost returns the provider-specific estimated cost for a token usage.
func EstimateCost(providerName, model string, usage Usage) float64 {
	switch providerName {
	case "anthropic":
		return anthropicPricing(model).Cost(usage.PromptTokens, usage.CompletionTokens)
	case "google":
		return geminiPricing(model).Cost(usage.PromptTokens, usage.CompletionTokens)
	case "groq":
		return groqPricing(model).Cost(usage.PromptTokens, usage.CompletionTokens)
	case "openai":
		return openAIPricing(model).Cost(usage.PromptTokens, usage.CompletionTokens)
	default:
		return 0
	}
}
