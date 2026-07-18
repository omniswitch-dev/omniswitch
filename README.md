# <p align="center"><img src="https://raw.githubusercontent.com/omniswitch-dev/omniswitch-website/main/public/favicon.svg" width="48" height="48" alt="OmniSwitch Logo" /><br/>OmniSwitch</p>

<p align="center">
  <strong>Open-source AI gateway, guardrail, and agent-protocol layer for production teams</strong>
</p>

<p align="center">
  <a href="https://github.com/omniswitch-dev/omniswitch/actions"><img src="https://img.shields.io/github/actions/workflow/status/omniswitch-dev/omniswitch/build.yml?branch=main&style=flat-square" alt="Build Status" /></a>
  <a href="https://golang.org"><img src="https://img.shields.io/github/go-mod/go-version/omniswitch-dev/omniswitch?style=flat-square&color=blue" alt="Go Version" /></a>
  <a href="https://github.com/omniswitch-dev/omniswitch/blob/main/LICENSE"><img src="https://img.shields.io/github/license/omniswitch-dev/omniswitch?style=flat-square&color=emerald" alt="License" /></a>
  <a href="https://omniswitch.dev"><img src="https://img.shields.io/badge/website-omniswitch.dev-purple?style=flat-square" alt="Website" /></a>
</p>

OmniSwitch is a self-hosted AI gateway for routing, securing, caching, and
observing LLM traffic across OpenAI-compatible clients, provider backends, MCP
tools, and basic A2A agent discovery. It runs as a single Go binary with SQLite
as the built-in store; Redis is optional when you need shared request limits
across multiple gateway replicas.

## Key Capabilities

- **OpenAI-compatible gateway:** `/v1/chat/completions`, `/v1/responses`, `/v1/messages`, `/v1/embeddings`, `/v1/rerank`, `/v1/moderations`, and `/v1/models`.
- **Provider routing:** Native OpenAI, Anthropic, Google, and Groq adapters plus any OpenAI-compatible custom endpoint.
- **Reliability controls:** Fallback chains, weighted variants, CEL route conditions, retries, retryable status codes, timeouts, circuit breakers, and shadow routing.
- **Tenant-safe cache and quotas:** Exact and semantic cache with API key, workspace, organization, or global scope; per-key budgets; local or Redis-backed request limits.
- **Identity and authorization:** Hashed API keys, bootstrap owner key, role gates, OIDC/JWKS workload identity, and CEL allow/deny authorization policies.
- **Guardrails:** Built-in PII, injection, SQL, toxic, and secret checks; regex rules; webhook guardrail connectors; local OpenAI-compatible moderation; output redaction and buffered SSE protection.
- **MCP gateway:** HTTP and streamable HTTP/SSE pass-through, persistent stdio targets, federated `tools/list`, policy-gated `tools/call`, target headers, and explicit OIDC bearer delegation.
- **A2A gateway:** Public Agent Card discovery plus authenticated JSON-RPC `SendMessage` through the governed chat pipeline.
- **Observability and operations:** SQLite logs, traces/sessions, feedback, provider metrics, OTLP trace export, Prometheus `/metrics`, Docker, and Kubernetes Kustomize manifests.

## Quickstart

Build and run locally:

```bash
git clone https://github.com/omniswitch-dev/omniswitch.git
cd omniswitch
OPENAI_API_KEY=your_key_here go run ./cmd/gateway
```

The gateway starts on `http://localhost:8080` and serves the dashboard at
`http://localhost:8080/`.

Run with Docker Compose:

```bash
docker compose up -d
```

Run on Kubernetes:

```bash
kubectl apply -k deploy/kubernetes
```

Replace the example secrets in `deploy/kubernetes/secret.example.yaml` before
using the Kubernetes manifests outside a throwaway environment.

## Example Configuration

```yaml
apiVersion: omniswitch.dev/v1
kind: GatewayConfig

gateway:
  listen: ":8080"
  auth: true
  cache_scope: api_key
  log_payloads: false

rate_limit:
  requests: 120
  window: 1m
  redis_url: redis://redis:6379/0

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

providers:
  - name: openai-prod
    type: openai
    api_key_env: OPENAI_API_KEY

guardrails:
  stream_buffer: true
  actions:
    injection: deny
    pii: redact

mcp:
  enabled: true
  policy: policies/production-delete.yaml
  targets:
    - name: github
      upstream: http://127.0.0.1:8091/mcp
      headers:
        x-api-key: "${GITHUB_MCP_TOKEN}"

routes:
  logical-chat:
    fallbacks: ["@anthropic-prod"]
    max_retries: 2
    retry_codes: [429, 500, 502, 503, 504]
    timeout: 30s
    variants:
      - provider: "@openai-prod"
        model: "@openai-prod/gpt-4o-mini"
        weight: 100
```

Run with the config file:

```bash
OMNISWITCH_CONFIG=examples/gateway-config.yaml go run ./cmd/gateway
```

## Compatibility Snapshot

| Area | OmniSwitch today |
| --- | --- |
| Inference APIs | Chat, Responses subset, Anthropic Messages subset, embeddings, rerank, models, local moderation |
| Routing | Fallbacks, weighted variants, CEL conditions, retries, timeouts, circuit breaker, shadow traffic |
| Security | API keys, OIDC/JWKS, CEL authorization, workspace/org scoping, encrypted provider vault |
| Guardrails | Built-in checks, regex rules, webhooks, redaction, buffered output checks |
| MCP | HTTP, streamable HTTP/SSE, stdio, tool federation, policy and audit |
| A2A | Agent Card, `SendMessage`, `GetExtendedAgentCard` |
| Deployment | Binary, Docker Compose, Kubernetes manifests, optional Redis for shared request limits |

See [docs/PORTKEY_COMPARISON.md](docs/PORTKEY_COMPARISON.md) for the full
Portkey and AgentGateway comparison.

## Client Integrations

OmniSwitch works with normal OpenAI-compatible clients by setting the base URL
to your gateway and using an OmniSwitch API key when authentication is enabled.
The lightweight SDK wrappers in `sdk/` add convenience headers for traces,
sessions, provider overrides, and virtual key routing.

## Repository Map

- `cmd/gateway`: AI gateway, dashboard, MCP, A2A, and management API server.
- `internal/gateway`: OpenAI-compatible handlers, identity, quotas, cache, guardrails, and A2A.
- `internal/router`: Provider routing, fallbacks, retries, variants, and request shaping.
- `internal/proxy`: MCP federation, stdio transport, and policy-enforced tool forwarding.
- `internal/store`: SQLite persistence for keys, logs, prompts, feedback, budgets, and cache.
- `deploy/kubernetes`: Kustomize baseline for self-hosted Kubernetes deployment.
- `docs`: API, configuration, deployment, architecture, and comparison docs.

## License

OmniSwitch is distributed under the Apache-2.0 License. See [LICENSE](LICENSE).
