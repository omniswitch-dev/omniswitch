# Portkey Comparison

This document tracks Sentinel against the broad Portkey-style AI gateway surface. It is not a claim of feature parity; it is a practical map for contributors.

## Covered Today

- OpenAI-compatible `/v1/chat/completions` and `/v1/models`.
- SSE streaming for chat completions.
- Provider adapters for OpenAI, Anthropic, Google Gemini, and Groq.
- Provider account aliases and config-as-code routing.
- Retries, fallbacks, weighted A/B routing, and shadow routing.
- Exact and semantic response caching.
- Circuit breakers.
- API keys with rate limits, cost budgets, and token budgets.
- Raw request/response logs, trace IDs, session IDs, metrics, and feedback.
- Organizations, workspaces, users, workspace memberships, roles, and workspace-scoped API keys.
- Prompt storage, rendering, and immutable prompt version history.
- MCP gateway for governed agent tool calls.
- CEL policies, explainable decisions, decision traces, and policy replay evals.
- OpenAI-style multimodal content array compatibility for providers that support it.

## Still Missing

- Enterprise SSO/SAML and full enforced RBAC middleware for every management endpoint.
- OpenTelemetry ingest/export with vendor-neutral trace and log backends.
- A full provider catalog comparable to Portkey's commercial catalog, including Azure OpenAI, AWS Bedrock, Mistral, Cohere, Together, Fireworks, local OpenAI-compatible endpoints, embeddings, rerank, image, and audio APIs.
- Prompt experiments, prompt rollback, prompt approvals, and production promotion workflows.
- Model-quality eval datasets, scorers, graders, and scheduled regression runs.
- Configurable guardrail chains with deny, log, retry, fallback, and dataset actions.
- Visual dashboards for traces, waterfalls, prompt comparisons, and eval reports.
- Official Python and Node SDKs.
- Encrypted provider credential vault and key rotation workflows.
- Compliance evidence packs for SOC2, ISO27001, HIPAA, PCI, and similar frameworks.

## Next Best Builds

1. Enforce workspace roles on `/api/*` routes.
2. Add OpenTelemetry export for request logs, spans, and MCP decisions.
3. Add Azure OpenAI, Mistral, Cohere, Together, and local OpenAI-compatible provider adapters.
4. Add prompt rollback and prompt promotion endpoints.
5. Add model eval datasets that replay logs against candidate providers and prompts.
