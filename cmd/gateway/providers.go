package main

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/vault"
)

// buildProviderRegistry registers providers from environment variables,
// the config file, the vault, and the OpenAI-compatible presets, in that
// order.
func buildProviderRegistry(settings runtimeSettings, vaultManager *vault.Vault) *provider.Registry {
	registry := provider.NewRegistry()
	registerEnvProvider(registry, "openai", "OPENAI_API_KEY")
	registerEnvProvider(registry, "anthropic", "ANTHROPIC_API_KEY")
	registerEnvProvider(registry, "google", "GOOGLE_API_KEY")
	registerEnvProvider(registry, "groq", "GROQ_API_KEY")
	registerEnvProvider(registry, "cohere", "COHERE_API_KEY")
	registerAzureEnvProvider(registry)
	registerBedrockEnvProvider(registry)
	for _, account := range settings.providerAccounts {
		registerProviderAccount(registry, account)
	}
	registerVaultProviders(registry, vaultManager)
	registerOpenAICompatiblePresets(registry)
	return registry
}

// openAICompatiblePreset describes a hosted provider whose API is
// OpenAI-compatible. Setting the environment variable is enough to enable it;
// a config account with the same type can override the base URL or models.
type openAICompatiblePreset struct {
	Type    string
	EnvKey  string
	BaseURL string
	Models  []string
}

var openAICompatiblePresets = []openAICompatiblePreset{
	{"mistral", "MISTRAL_API_KEY", "https://api.mistral.ai/v1", []string{"mistral-large-latest", "mistral-medium-latest", "mistral-small-latest", "codestral-latest", "open-mistral-nemo"}},
	{"xai", "XAI_API_KEY", "https://api.x.ai/v1", []string{"grok-4", "grok-4-fast", "grok-3", "grok-3-mini"}},
	{"deepseek", "DEEPSEEK_API_KEY", "https://api.deepseek.com/v1", []string{"deepseek-chat", "deepseek-reasoner"}},
	{"together", "TOGETHER_API_KEY", "https://api.together.xyz/v1", []string{"meta-llama/Llama-3.3-70B-Instruct-Turbo", "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo", "Qwen/Qwen2.5-72B-Instruct-Turbo", "deepseek-ai/DeepSeek-V3"}},
	{"fireworks", "FIREWORKS_API_KEY", "https://api.fireworks.ai/inference/v1", []string{"accounts/fireworks/models/llama-v3p3-70b-instruct", "accounts/fireworks/models/deepseek-v3"}},
	{"perplexity", "PERPLEXITY_API_KEY", "https://api.perplexity.ai", []string{"sonar", "sonar-pro", "sonar-reasoning-pro"}},
	{"cerebras", "CEREBRAS_API_KEY", "https://api.cerebras.ai/v1", []string{"llama-3.3-70b", "llama3.1-8b"}},
	{"openrouter", "OPENROUTER_API_KEY", "https://openrouter.ai/api/v1", nil}, // dynamic catalog; route by full model id
}

// registerOpenAICompatiblePresets lights up hosted providers whose credentials
// are present in the environment, giving broad provider coverage without any
// per-provider adapter code.
func registerOpenAICompatiblePresets(registry *provider.Registry) {
	for _, preset := range openAICompatiblePresets {
		apiKey := os.Getenv(preset.EnvKey)
		if apiKey == "" {
			continue
		}
		var opts []provider.CustomOption
		if len(preset.Models) > 0 {
			opts = append(opts, provider.WithCustomModels(preset.Models))
		}
		p := provider.NewCustom(preset.Type, preset.BaseURL, apiKey, opts...)
		registry.Register(p)
		log.Printf("registered %s provider: %s (%s)", preset.Type, p.Name(), preset.BaseURL)
	}
}

func presetForType(providerType string) *openAICompatiblePreset {
	normalized := strings.ToLower(strings.TrimSpace(providerType))
	for i := range openAICompatiblePresets {
		if openAICompatiblePresets[i].Type == normalized {
			return &openAICompatiblePresets[i]
		}
	}
	return nil
}

func registerEnvProvider(registry *provider.Registry, providerType string, envKey string) {
	if key := os.Getenv(envKey); key != "" {
		if p := newProvider(providerType, key); p != nil {
			registry.Register(p)
			log.Printf("registered provider: %s", p.Name())
		}
	}
}

func registerAzureEnvProvider(registry *provider.Registry) {
	apiKey := os.Getenv("AZURE_OPENAI_API_KEY")
	endpoint := os.Getenv("AZURE_OPENAI_ENDPOINT")
	if apiKey == "" || endpoint == "" {
		return
	}
	models := parseCSV(os.Getenv("AZURE_OPENAI_MODELS"))
	azure := provider.NewAzureOpenAI("azure", endpoint, apiKey, os.Getenv("AZURE_OPENAI_API_VERSION"), models)
	registry.Register(azure)
	log.Printf("registered provider: %s", azure.Name())
}

func registerBedrockEnvProvider(registry *provider.Registry) {
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	if accessKey == "" || secretKey == "" {
		return
	}
	region := firstNonEmpty(os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION"), "us-east-1")
	bedrock := provider.NewBedrock("bedrock", region, accessKey, secretKey, os.Getenv("AWS_SESSION_TOKEN"), parseCSV(os.Getenv("OMNISWITCH_BEDROCK_MODELS"))).
		WithGuardrail(os.Getenv("OMNISWITCH_BEDROCK_GUARDRAIL_ID"), os.Getenv("OMNISWITCH_BEDROCK_GUARDRAIL_VERSION"))
	registry.Register(bedrock)
	log.Printf("registered provider: %s", bedrock.Name())
}

func registerProviderAccount(registry *provider.Registry, account gatewayconfig.ProviderAccount) {
	providerType := strings.ToLower(strings.TrimSpace(account.Type))

	switch providerType {
	case "azure", "azure-openai":
		envKey := strings.TrimSpace(account.APIKeyEnv)
		if envKey == "" {
			envKey = "AZURE_OPENAI_API_KEY"
		}
		apiKey := os.Getenv(envKey)
		if apiKey == "" {
			log.Printf("skipping Azure provider account %q: env %s is not set", account.Name, envKey)
			return
		}
		endpoint := firstNonEmpty(account.Endpoint, account.BaseURL, os.Getenv("AZURE_OPENAI_ENDPOINT"))
		if endpoint == "" {
			log.Printf("skipping Azure provider account %q: endpoint is required", account.Name)
			return
		}
		azure := provider.NewAzureOpenAI(account.Name, endpoint, apiKey, account.APIVersion, account.Models)
		registry.Register(azure)
		log.Printf("registered Azure OpenAI provider: %s (%s)", azure.Name(), endpoint)
		return
	case "bedrock", "aws-bedrock":
		accessEnv := firstNonEmpty(account.AWSAccessKeyEnv, "AWS_ACCESS_KEY_ID")
		secretEnv := firstNonEmpty(account.AWSSecretKeyEnv, "AWS_SECRET_ACCESS_KEY")
		sessionEnv := firstNonEmpty(account.AWSSessionTokenEnv, "AWS_SESSION_TOKEN")
		accessKey := os.Getenv(accessEnv)
		secretKey := os.Getenv(secretEnv)
		if accessKey == "" || secretKey == "" {
			log.Printf("skipping Bedrock provider account %q: env %s/%s must be set", account.Name, accessEnv, secretEnv)
			return
		}
		region := firstNonEmpty(account.Region, os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION"))
		bedrock := provider.NewBedrock(account.Name, region, accessKey, secretKey, os.Getenv(sessionEnv), account.Models).
			WithGuardrail(account.GuardrailID, account.GuardrailVersion)
		registry.Register(bedrock)
		log.Printf("registered Bedrock provider: %s (%s)", bedrock.Name(), region)
		return
	}

	// Custom or generic OpenAI-compatible provider with explicit base_url.
	if providerType == "custom" || account.BaseURL != "" || presetForType(providerType) != nil {
		envKey := strings.TrimSpace(account.APIKeyEnv)
		apiKey := ""
		if envKey != "" {
			apiKey = os.Getenv(envKey)
		}
		var opts []provider.CustomOption
		models := account.Models
		baseURL := account.BaseURL
		if preset := presetForType(providerType); preset != nil {
			if baseURL == "" {
				baseURL = preset.BaseURL
			}
			if len(models) == 0 {
				models = preset.Models
			}
			if envKey == "" {
				envKey = preset.EnvKey
				apiKey = os.Getenv(envKey)
			}
		}
		if len(models) > 0 {
			opts = append(opts, provider.WithCustomModels(models))
		}
		if len(account.ExtraHeaders) > 0 {
			opts = append(opts, provider.WithCustomHeaders(expandHeaderValues(account.ExtraHeaders)))
		}
		if baseURL == "" {
			log.Printf("skipping custom provider %q: base_url is required for type=custom", account.Name)
			return
		}
		custom := provider.NewCustom(account.Name, baseURL, apiKey, opts...)
		registry.Register(custom)
		log.Printf("registered custom provider: %s (%s)", custom.Name(), baseURL)
		return
	}

	// Standard provider account with alias.
	envKey := strings.TrimSpace(account.APIKeyEnv)
	if envKey == "" {
		envKey = defaultProviderKeyEnv(account.Type)
	}
	apiKey := os.Getenv(envKey)
	if apiKey == "" {
		log.Printf("skipping provider account %q: env %s is not set", account.Name, envKey)
		return
	}
	inner := newProvider(account.Type, apiKey)
	if inner == nil {
		log.Printf("skipping provider account %q: unsupported provider type %q", account.Name, account.Type)
		return
	}
	alias := provider.NewAlias(account.Name, inner)
	registry.Register(alias)
	log.Printf("registered provider account: %s (%s)", alias.Name(), inner.Name())
}

func registerVaultProviders(registry *provider.Registry, vaultManager *vault.Vault) {
	if vaultManager == nil {
		return
	}
	keys, err := vaultManager.List(context.Background())
	if err != nil {
		log.Printf("failed to list virtual provider keys: %v", err)
		return
	}
	for _, key := range keys {
		if !key.Enabled {
			continue
		}
		providerKey, providerType, baseURL, err := vaultManager.Resolve(context.Background(), key.Name)
		if err != nil {
			log.Printf("skipping virtual key %q: %v", key.Name, err)
			continue
		}
		providerName := key.ProviderName
		if providerName == "" {
			providerName = key.Name
		}
		providerType = strings.ToLower(strings.TrimSpace(providerType))
		if baseURL != "" || providerType == "custom" {
			opts := []provider.CustomOption{}
			if models := parseCSV(key.Metadata["models"]); len(models) > 0 {
				opts = append(opts, provider.WithCustomModels(models))
			}
			if authHeader := strings.TrimSpace(key.Metadata["auth_header"]); authHeader != "" && providerKey != "" {
				opts = append(opts, provider.WithCustomHeaders(map[string]string{authHeader: providerKey}))
			}
			custom := provider.NewCustom(providerName, baseURL, providerKey, opts...)
			registry.Register(custom)
			log.Printf("registered vaulted provider: %s (%s)", custom.Name(), baseURL)
			continue
		}
		inner := newProvider(providerType, providerKey)
		if inner == nil {
			log.Printf("skipping virtual key %q: unsupported provider type %q", key.Name, providerType)
			continue
		}
		alias := provider.NewAlias(providerName, inner)
		registry.Register(alias)
		log.Printf("registered vaulted provider account: %s (%s)", alias.Name(), inner.Name())
	}
}

func newProvider(providerType string, apiKey string) provider.Provider {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case "openai":
		return provider.NewOpenAI(apiKey)
	case "anthropic":
		return provider.NewAnthropic(apiKey)
	case "google", "gemini":
		return provider.NewGoogle(apiKey)
	case "groq":
		return provider.NewGroq(apiKey)
	case "cohere":
		return provider.NewCohere(apiKey)
	default:
		return nil
	}
}

func defaultProviderKeyEnv(providerType string) string {
	if preset := presetForType(providerType); preset != nil {
		return preset.EnvKey
	}
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "google", "gemini":
		return "GOOGLE_API_KEY"
	case "groq":
		return "GROQ_API_KEY"
	case "cohere":
		return "COHERE_API_KEY"
	case "azure", "azure-openai":
		return "AZURE_OPENAI_API_KEY"
	default:
		return ""
	}
}
