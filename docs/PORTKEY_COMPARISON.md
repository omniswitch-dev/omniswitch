# Portkey Comparison

This document tracks Sentinel against the broad Portkey-style AI gateway surface. It is not a claim of feature parity; it is a practical map for contributors.

## Covered Today

- OpenAI-compatible `/v1/chat/completions` and `/v1/models`.
- SSE streaming for chat completions.
- Provider adapters for OpenAI, Anthropic, Google Gemini, and Groq.
- Generic OpenAI-compatible custom provider support for Azure OpenAI, Ollama, vLLM, LiteLLM, DeepSeek, Together, Fireworks, Mistral-compatible gateways, and similar endpoints.
- Provider account aliases and config-as-code routing.
- Retries, fallbacks, weighted A/B routing, and shadow routing.
- Exact and semantic response caching.
- Circuit breakers.
- API keys with rate limits, cost budgets, and token budgets.
- Raw request/response logs, trace IDs, session IDs, metrics, feedback, and OpenTelemetry trace export.
- Encrypted provider credential vault with virtual provider keys, rotation, and revoke workflows.
- Organizations, workspaces, users, workspace memberships, roles, and workspace-scoped API keys.
- Prompt storage, rendering, and immutable prompt version history.
- MCP gateway for governed agent tool calls.
- CEL policies, explainable decisions, decision traces, and policy replay evals.
- OpenAI-style multimodal content array compatibility for providers that support it.
- Python and Node.js SDK wrappers around the OpenAI SDKs.

## Still Missing

- Enterprise SSO/SAML and full enforced RBAC middleware for every management endpoint.
- A full multi-protocol provider catalog comparable to Portkey's commercial catalog, including first-class AWS Bedrock, Cohere, embeddings, rerank, image, and audio APIs.
- Prompt experiments, prompt rollback, prompt approvals, and production promotion workflows.
- Model-quality eval datasets, scorers, graders, and scheduled regression runs.
- Configurable guardrail chains with deny, log, retry, fallback, and dataset actions.
- Visual dashboards for traces, waterfalls, prompt comparisons, and eval reports.
- Compliance evidence packs for SOC2, ISO27001, HIPAA, PCI, and similar frameworks.

## Next Best Builds

1. Enforce workspace roles on `/api/*` routes.
2. Add trace waterfall and eval-report views to the dashboard.
3. Add embeddings, rerank, image, and audio endpoint families.
4. Add prompt rollback and prompt promotion endpoints.
5. Add model eval datasets that replay logs against candidate providers and prompts.
