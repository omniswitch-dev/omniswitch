# Portkey and AgentGateway Comparison

This is a capability map, not a claim of commercial or protocol parity. It
captures the options exposed by [Portkey's AI Gateway](https://portkey.ai/docs/product/ai-gateway),
[Portkey Guardrails](https://portkey.ai/docs/product/guardrails), and the
[AgentGateway project](https://github.com/agentgateway/agentgateway), then
states exactly what OmniSwitch implements today.

## Feature Availability Matrix

| Area | Portkey options | AgentGateway options | OmniSwitch status and option |
| --- | --- | --- | --- |
| Unified inference API | Universal REST/SDK API across models and multimodal endpoints | OpenAI-compatible routing to cloud and local providers | **Partial:** `/v1/chat/completions`, `/v1/models`, `/v1/responses` text subset, `/v1/messages` core subset, and `/v1/embeddings` |
| Provider coverage | Large managed provider/model catalog, virtual keys, custom hosts | OpenAI, Anthropic, Gemini, Bedrock and provider backends | **Partial:** native OpenAI, Anthropic, Google, Groq; any OpenAI-compatible `custom` endpoint and aliases. No native Bedrock/Vertex/Cohere adapter yet |
| Routing | Fallback, load balancing, canary, conditional routing, retries, timeouts, circuit breaker | Load balancing, failover, prompt enrichment, inference-aware Kubernetes routing | **Implemented (core):** `fallbacks`, weighted `variants`, CEL `condition`, retry/backoff/status codes, per-attempt `timeout`, circuit breaker, `shadow_provider` |
| Request shaping | Defaults, overrides, drop/forward parameters, config via header/API key | Route policies and prompt enrichment | **Implemented (subset):** `default_params`, `override_params`, `drop_params` for model, temperature, max tokens, top-p, stream, and stop |
| Cache | Simple and semantic cache; config-managed policies | Not a primary advertised cache surface | **Implemented:** exact + semantic cache with `api_key`/`workspace`/`organization`/`global` isolation and TTL |
| Budgets and quotas | Cost/token budgets and time-window rate limits | Budget/spend controls and token-aware rate limiting | **Partial:** per-key cost/token budgets and local sliding-window request limits; no distributed or token-based limiter |
| Guardrails | Deterministic, AI/partner, custom webhook checks with deny/log/dataset/retry/fallback actions | Regex, OpenAI moderation, Bedrock Guardrails, Model Armor, custom webhooks | **Partial:** built-in PII/injection/SQL/toxic/secret checks plus regex rules; `deny`, `redact`, `warn`, `log`; structured audit events and buffered SSE output checks. No external/webhook/LLM guardrails or guardrail-driven retry/fallback |
| Authentication and authorization | Managed project/workspace controls and key management | JWT, API keys, OAuth, CEL RBAC, TLS | **Partial:** hashed API keys, bootstrap owner, fixed viewer/member/admin/owner role gates, workspace scope, vault encryption. No JWT/OIDC/OAuth/mTLS or CEL RBAC |
| Observability | Detailed logs, traces, analytics, feedback, OTel ingestion | OTel metrics/logs/traces and agent/protocol telemetry | **Partial:** SQLite logs, traces/sessions, feedback, provider metrics, OTLP export, Prometheus `/metrics`; no distributed trace waterfall or external log store |
| Prompt management | Templates, partials, releases/publish workflow, experiments | Prompt enrichment at route level | **Partial:** templates, versions, rendering. No approval, rollback/promotion, experiments, or partials |
| MCP | Remote MCP connectivity | Federation; stdio, HTTP, SSE/streamable HTTP, OpenAPI, OAuth | **Partial:** HTTP JSON-RPC MCP target federation, server-side headers, `tools/list` and policy-gated `tools/call`; no stdio/SSE/streamable HTTP, OpenAPI conversion, or OAuth delegation |
| Agent-to-agent | Agent-framework integrations | Native A2A discovery, negotiation, and task collaboration | **Missing:** A2A is not implemented |
| Kubernetes/control plane | Hosted and self-hosted gateway configuration | Standalone plus Kubernetes Gateway API/controller and inference extensions | **Missing:** single-process self-hosted runtime only; no controller, CRDs, or inference-aware routing |
| High availability/data plane | Managed platform plus self-hosted gateway | Rust proxy/control plane deployment model | **Missing:** SQLite and local in-memory rate limiting; no shared database, config hot reload, or HA control plane |

## OmniSwitch Configuration Options Added for This Comparison

```yaml
gateway:
  auth: true
  cache_scope: api_key          # api_key | workspace | organization | global
  log_payloads: false
  cors_origins: [https://app.example.com]
  circuit_breaker_failures: 5
  circuit_breaker_cooldown: 60s
  max_request_bytes: 10485760
  read_header_timeout: 5s
  read_timeout: 30s
  write_timeout: 0s             # preserves SSE streaming
  idle_timeout: 60s

guardrails:
  stream_buffer: true
  actions: {injection: deny, pii: redact}
  rules:
    - name: no-secret-marker
      stage: both                # input | output | both
      pattern: '(?i)internal-secret'
      action: deny               # deny | redact | warn | log

routes:
  logical-model:
    fallbacks: ["@anthropic-prod"]
    max_retries: 2
    retry_backoff: 500ms
    retry_codes: [429, 500, 502, 503, 504]
    timeout: 30s
    shadow_provider: "@openai-shadow"
    default_params: {temperature: 0.2}
    override_params: {max_tokens: 1000}
    drop_params: [logprobs]
    variants:
      - provider: "@openai-prod"
        model: "@openai-prod/gpt-4o-mini"
        weight: 90
        condition: 'model == "logical-model"'

mcp:
  targets:
    - name: github
      upstream: http://github-mcp.internal/mcp
      policy: policies/production-delete.yaml
      headers: {x-api-key: "${GITHUB_MCP_TOKEN}"}
```

Environment-only production options include `OMNISWITCH_BOOTSTRAP_API_KEY`,
`OMNISWITCH_CACHE_SCOPE`, `OMNISWITCH_LOG_PAYLOADS`, `OMNISWITCH_CORS_ORIGINS`,
`OMNISWITCH_GUARDRAIL_STREAM_BUFFER`, request/server timeout variables, circuit
breaker variables, and `OMNISWITCH_PROMETHEUS_ENABLED`. See
[Configuration](CONFIGURATION.md) for the complete list.

## Important Parity Boundaries

OmniSwitch now covers the highest-leverage gateway baseline—safe cache tenancy,
auth bootstrap/RBAC, declarative routing and request shaping, core compatibility
endpoints, deterministic guardrails, and HTTP MCP federation. It is still not a
drop-in replacement for either larger platform. The largest gaps are native
provider and endpoint breadth, external/distributed infrastructure, full MCP
transport and OAuth, A2A, external guardrails, and Kubernetes control-plane
features.

Recommended next investments, in order:

1. Shared storage and distributed rate limits for HA deployments.
2. JWT/OIDC/mTLS authentication plus CEL authorization.
3. Streamable HTTP/stdio MCP and OAuth delegation, then A2A.
4. Native Bedrock, Vertex, Cohere, rerank, image, audio, batch, and file APIs.
5. Webhook/LLM guardrails and guardrail actions that retry or reroute.
6. Trace waterfalls, prompt release/experiment workflows, and evaluation datasets.
