# Portkey and AgentGateway Comparison

This is a capability map, not a claim of commercial or protocol parity. It
captures the options exposed by [Portkey's AI Gateway](https://portkey.ai/docs/product/ai-gateway),
[Portkey Guardrails](https://portkey.ai/docs/product/guardrails), and the
[AgentGateway project](https://github.com/agentgateway/agentgateway), then
states exactly what OmniSwitch implements today.

## Feature Availability Matrix

| Area | Portkey options | AgentGateway options | OmniSwitch status and option |
| --- | --- | --- | --- |
| Unified inference API | Universal REST/SDK API across models and multimodal endpoints | OpenAI-compatible routing to cloud and local providers | **Partial:** `/v1/chat/completions`, `/v1/models`, `/v1/responses` text subset, `/v1/messages` core subset, `/v1/embeddings`, `/v1/rerank`, and local `/v1/moderations` |
| Provider coverage | Large managed provider/model catalog, virtual keys, custom hosts | OpenAI, Anthropic, Gemini, Bedrock and provider backends | **Partial:** native OpenAI, Anthropic, Google, Groq; any OpenAI-compatible `custom` endpoint and aliases. No native Bedrock/Vertex/Cohere adapter yet |
| Routing | Fallback, load balancing, canary, conditional routing, retries, timeouts, circuit breaker | Load balancing, failover, prompt enrichment, inference-aware Kubernetes routing | **Implemented (core):** `fallbacks`, weighted `variants`, CEL `condition`, retry/backoff/status codes, per-attempt `timeout`, circuit breaker, `shadow_provider` |
| Request shaping | Defaults, overrides, drop/forward parameters, config via header/API key | Route policies and prompt enrichment | **Implemented (subset):** `default_params`, `override_params`, `drop_params` for model, temperature, max tokens, top-p, stream, and stop |
| Cache | Simple and semantic cache; config-managed policies | Not a primary advertised cache surface | **Implemented:** exact + semantic cache with `api_key`/`workspace`/`organization`/`global` isolation and TTL |
| Budgets and quotas | Cost/token budgets and time-window rate limits | Budget/spend controls and token-aware rate limiting | **Partial:** per-key cost/token budgets, local sliding-window request limits, and optional Redis-coordinated fixed windows; no token-based limiter |
| Guardrails | Deterministic, AI/partner, custom webhook checks with deny/log/dataset/retry/fallback actions | Regex, OpenAI moderation, Bedrock Guardrails, Model Armor, custom webhooks | **Partial:** built-in PII/injection/SQL/toxic/secret checks, regex rules, local OpenAI-compatible moderation, and HTTP webhook connectors; `deny`, `redact`, `warn`, `log`; structured audit events and buffered SSE output checks. No native Bedrock/Model Armor connector or guardrail-driven retry/fallback |
| Authentication and authorization | Managed project/workspace controls and key management | JWT, API keys, OAuth, CEL RBAC, TLS | **Implemented (core):** hashed API keys, bootstrap owner, OIDC JWT/JWKS identity, CEL allow/deny policies, fixed role gates, workspace scope, and vault encryption. OAuth is available for explicitly delegated MCP OIDC bearer tokens; no mTLS |
| Observability | Detailed logs, traces, analytics, feedback, OTel ingestion | OTel metrics/logs/traces and agent/protocol telemetry | **Partial:** SQLite logs, traces/sessions, feedback, provider metrics, OTLP export, Prometheus `/metrics`; no distributed trace waterfall or external log store |
| Prompt management | Templates, partials, releases/publish workflow, experiments | Prompt enrichment at route level | **Partial:** templates, versions, rendering. No approval, rollback/promotion, experiments, or partials |
| MCP | Remote MCP connectivity | Federation; stdio, HTTP, SSE/streamable HTTP, OpenAPI, OAuth | **Partial:** HTTP federation with streamed SSE/streamable-HTTP responses, persistent stdio targets, server-side headers, OIDC bearer delegation, `tools/list`, and policy-gated `tools/call`; no OpenAPI conversion |
| Agent-to-agent | Agent-framework integrations | Native A2A discovery, negotiation, and task collaboration | **Partial:** public A2A v1 Agent Card discovery plus authenticated JSON-RPC `SendMessage` and `GetExtendedAgentCard`; no task lifecycle, streaming, push notifications, or outbound A2A client |
| Kubernetes/control plane | Hosted and self-hosted gateway configuration | Standalone plus Kubernetes Gateway API/controller and inference extensions | **Partial:** redacted `/api/config` posture endpoint plus Kustomize manifests for Deployment, Service, ConfigMap, Secret example, PVCs, and Redis; no controller, CRDs, or inference-aware routing |
| High availability/data plane | Managed platform plus self-hosted gateway | Rust proxy/control plane deployment model | **Partial:** gateway replicas can share Redis request limits, but logs/keys remain SQLite-local; no shared database, config hot reload, or HA control plane |

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
  webhooks:
    - name: managed-moderation
      url: https://guardrails.example.com/v1/check
      stage: input
      action: deny
      headers: {authorization: "Bearer ${GUARDRAIL_API_KEY}"}
      timeout: 3s
      fail_open: false
  rules:
    - name: no-secret-marker
      stage: both                # input | output | both
      pattern: '(?i)internal-secret'
      action: deny               # deny | redact | warn | log

rate_limit:
  requests: 120
  window: 1m
  redis_url: redis://redis:6379/0
  fail_open: false

identity:
  oidc:
    jwks_url: https://issuer.example.com/.well-known/jwks.json
    issuer: https://issuer.example.com/
    audience: omniswitch

authorization:
  rules:
    - name: member-model-restriction
      when: 'role == "member" && model == "production-model"'
      effect: deny

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
      forward_bearer_token: false

    - name: filesystem
      transport: stdio
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "${WORKSPACE_ROOT}"]
```

Environment-only production options include `OMNISWITCH_BOOTSTRAP_API_KEY`,
`OMNISWITCH_CACHE_SCOPE`, `OMNISWITCH_LOG_PAYLOADS`, `OMNISWITCH_CORS_ORIGINS`,
`OMNISWITCH_GUARDRAIL_STREAM_BUFFER`, OIDC settings, Redis rate-limit settings,
request/server timeout variables, circuit breaker variables, and
`OMNISWITCH_PROMETHEUS_ENABLED`. See
[Configuration](CONFIGURATION.md) for the complete list.

## Important Parity Boundaries

OmniSwitch now covers the highest-leverage gateway baseline: safe cache tenancy,
auth bootstrap/RBAC, OIDC workload identity, CEL authorization, declarative
routing and request shaping, redacted runtime config posture, core compatibility
endpoints, deterministic and webhook guardrails, Redis-coordinated request
limits, MCP federation, stdio MCP, and basic A2A discovery/direct messaging. It
is still not a drop-in replacement for either larger platform. The largest gaps
are native provider and endpoint breadth, shared database/config reload
infrastructure, OpenAPI-to-MCP conversion, full A2A task lifecycle, and
Kubernetes controller features.

Recommended next investments, in order:

1. Shared database or external control plane for API keys, logs, budgets, and config hot reload.
2. Native Bedrock, Vertex, Cohere, image, audio, batch, and file APIs.
3. OpenAPI-to-MCP conversion and fuller MCP OAuth client flows.
4. A2A task lifecycle, streaming, push notifications, and outbound agent calls.
5. Guardrail actions that retry, reroute, or fallback after a violation.
6. Trace waterfalls, prompt release/experiment workflows, and evaluation datasets.
