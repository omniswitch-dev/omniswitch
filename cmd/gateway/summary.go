package main

import (
	"sort"
	"strings"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
)

func runtimeConfigSummary(settings runtimeSettings, registry *provider.Registry) map[string]any {
	providerNames := []string{}
	modelCount := 0
	if registry != nil {
		providerNames = registry.Names()
		sort.Strings(providerNames)
		modelCount = len(registry.AllModels())
	}
	return map[string]any{
		"config_path": settings.configPath,
		"gateway": map[string]any{
			"listen":                   settings.listenAddr,
			"auth_enabled":             settings.authEnabled,
			"cache_threshold":          settings.cacheThreshold,
			"cache_ttl":                settings.cacheTTL.String(),
			"cache_scope":              settings.cacheScope,
			"log_payloads":             settings.logPayloads,
			"cors_origins":             append([]string(nil), settings.corsOrigins...),
			"max_request_bytes":        settings.maxRequestBytes,
			"read_header_timeout":      settings.readHeaderTimeout.String(),
			"read_timeout":             settings.readTimeout.String(),
			"write_timeout":            settings.writeTimeout.String(),
			"idle_timeout":             settings.idleTimeout.String(),
			"circuit_breaker_failures": settings.circuitBreakerFailures,
			"circuit_breaker_cooldown": settings.circuitBreakerCooldown.String(),
			"shadow_provider":          settings.shadowProvider,
		},
		"rate_limit": map[string]any{
			"requests":  settings.rateLimitRequests,
			"window":    settings.rateLimitWindow.String(),
			"backend":   rateLimitBackendName(settings),
			"prefix":    settings.rateLimitPrefix,
			"fail_open": settings.rateLimitFailOpen,
		},
		"identity": map[string]any{
			"api_keys_enabled": settings.authEnabled,
			"oidc": map[string]any{
				"enabled":            settings.oidcJWKSURL != "",
				"issuer":             settings.oidcIssuer,
				"audience":           settings.oidcAudience,
				"role_claim":         settings.oidcRoleClaim,
				"workspace_claim":    settings.oidcWorkspaceClaim,
				"organization_claim": settings.oidcOrganizationClaim,
				"cache_ttl":          settings.oidcCacheTTL.String(),
			},
		},
		"authorization": map[string]any{
			"rule_count": len(settings.authorizationRules),
			"rules":      authorizationRuleSummaries(settings.authorizationRules),
		},
		"guardrails": map[string]any{
			"stream_buffer":   settings.guardrailStreamBuffer,
			"actions":         cloneStringMap(settings.guardrailConfig.Actions),
			"rule_count":      len(settings.guardrailConfig.Rules),
			"rules":           guardrailRuleSummaries(settings.guardrailConfig.Rules),
			"tool_rule_count": len(settings.guardrailConfig.ToolRules),
			"tool_rules":      guardrailToolRuleSummaries(settings.guardrailConfig.ToolRules),
			"webhook_count":   len(settings.guardrailConfig.Webhooks),
			"webhooks":        guardrailWebhookSummaries(settings.guardrailConfig.Webhooks),
			"llm_judge_count": len(settings.guardrailConfig.LLMJudges),
			"llm_judges":      guardrailLLMJudgeSummaries(settings.guardrailConfig.LLMJudges),
			"moderation_api":  true,
		},
		"providers": map[string]any{
			"names":       providerNames,
			"model_count": modelCount,
		},
		"routes": routeSummaries(settings.routes),
		"mcp": map[string]any{
			"enabled": settings.mcpEnabled,
			"targets": mcpTargetSummaries(settings),
		},
		"a2a": map[string]any{
			"enabled":            true,
			"agent_card_path":    "/.well-known/agent-card.json",
			"jsonrpc_path":       "/a2a",
			"methods":            []string{"SendMessage", "GetExtendedAgentCard"},
			"task_lifecycle":     false,
			"streaming":          false,
			"push_notifications": false,
		},
	}
}

func rateLimitBackendName(settings runtimeSettings) string {
	if strings.TrimSpace(settings.rateLimitRedisURL) != "" {
		return "redis"
	}
	return "local"
}

func authorizationRuleSummaries(rules []gateway.AuthorizationRule) []map[string]string {
	summaries := make([]map[string]string, 0, len(rules))
	for _, rule := range rules {
		summaries = append(summaries, map[string]string{
			"name":   rule.Name,
			"effect": rule.Effect,
		})
	}
	return summaries
}

func guardrailRuleSummaries(rules []guardrail.Rule) []map[string]string {
	summaries := make([]map[string]string, 0, len(rules))
	for _, rule := range rules {
		summaries = append(summaries, map[string]string{
			"name":   rule.Name,
			"stage":  rule.Stage,
			"action": rule.Action,
		})
	}
	return summaries
}

func guardrailToolRuleSummaries(rules []guardrail.ToolRule) []map[string]string {
	summaries := make([]map[string]string, 0, len(rules))
	for _, rule := range rules {
		summaries = append(summaries, map[string]string{
			"name":      rule.Name,
			"stage":     rule.Stage,
			"tool_name": rule.ToolName,
			"action":    rule.Action,
		})
	}
	return summaries
}

func guardrailWebhookSummaries(webhooks []guardrail.Webhook) []map[string]any {
	summaries := make([]map[string]any, 0, len(webhooks))
	for _, webhook := range webhooks {
		summaries = append(summaries, map[string]any{
			"name":      webhook.Name,
			"stage":     webhook.Stage,
			"action":    webhook.Action,
			"timeout":   webhook.Timeout.String(),
			"fail_open": webhook.FailOpen,
		})
	}
	return summaries
}

func guardrailLLMJudgeSummaries(judges []guardrail.LLMJudge) []map[string]any {
	summaries := make([]map[string]any, 0, len(judges))
	for _, judge := range judges {
		summaries = append(summaries, map[string]any{
			"name":      judge.Name,
			"stage":     judge.Stage,
			"model":     judge.Model,
			"action":    judge.Action,
			"timeout":   judge.Timeout.String(),
			"fail_open": judge.FailOpen,
		})
	}
	return summaries
}

func routeSummaries(routes map[string]router.Route) []map[string]any {
	names := make([]string, 0, len(routes))
	for name := range routes {
		names = append(names, name)
	}
	sort.Strings(names)
	summaries := make([]map[string]any, 0, len(names))
	for _, name := range names {
		route := routes[name]
		summaries = append(summaries, map[string]any{
			"model":           name,
			"provider":        route.Provider,
			"fallbacks":       append([]string(nil), route.Fallbacks...),
			"max_retries":     route.MaxRetries,
			"retry_backoff":   route.RetryBackoff,
			"retry_codes":     append([]int(nil), route.RetryCodes...),
			"timeout":         route.Timeout,
			"shadow_provider": route.ShadowProvider,
			"default_params":  cloneAnyMap(route.DefaultParams),
			"override_params": cloneAnyMap(route.OverrideParams),
			"drop_params":     append([]string(nil), route.DropParams...),
			"variants":        append([]router.Variant(nil), route.Variants...),
		})
	}
	return summaries
}

func mcpTargetSummaries(settings runtimeSettings) []map[string]any {
	if len(settings.mcpTargets) == 0 {
		if strings.TrimSpace(settings.mcpUpstreamURL) == "" {
			return []map[string]any{}
		}
		return []map[string]any{{
			"name":                   "default",
			"transport":              "http",
			"enabled":                settings.mcpEnabled,
			"policy_configured":      strings.TrimSpace(settings.mcpPolicyPath) != "",
			"headers_configured":     false,
			"environment_configured": false,
			"forward_bearer_token":   false,
		}}
	}
	summaries := make([]map[string]any, 0, len(settings.mcpTargets))
	for _, target := range settings.mcpTargets {
		transport := strings.TrimSpace(target.Transport)
		if transport == "" {
			transport = "http"
		}
		enabled := true
		if target.Enabled != nil {
			enabled = *target.Enabled
		}
		forwardBearer := false
		if target.ForwardBearerToken != nil {
			forwardBearer = *target.ForwardBearerToken
		}
		summaries = append(summaries, map[string]any{
			"name":                   target.Name,
			"transport":              transport,
			"enabled":                enabled,
			"policy_configured":      strings.TrimSpace(target.Policy) != "",
			"headers_configured":     len(target.Headers) > 0,
			"environment_configured": len(target.Environment) > 0,
			"forward_bearer_token":   forwardBearer,
		})
	}
	return summaries
}
