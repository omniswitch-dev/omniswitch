package provider

// EstimateCost returns the provider-specific estimated cost for a token usage.
func EstimateCost(providerName, model string, usage Usage) float64 {
	return pricingFor(providerName, model).Cost(usage.PromptTokens, usage.CompletionTokens)
}

func InputPricePerMillion(providerName, model string) float64 {
	return pricingFor(providerName, model).InputPerMillion
}

func pricingFor(providerName, model string) ModelPricing {
	switch providerName {
	case "anthropic":
		return anthropicPricing(model)
	case "azure":
		return openAIPricing(model)
	case "bedrock":
		return bedrockPricing(model)
	case "google":
		return geminiPricing(model)
	case "groq":
		return groqPricing(model)
	case "openai":
		return openAIPricing(model)
	default:
		return ModelPricing{}
	}
}
