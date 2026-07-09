# Sentinel

Sentinel is a local policy enforcement layer for AI agent tool calls. It converts protocol requests into a canonical `ToolRequest`, evaluates `sentinel.dev/v1` policies with CEL, and returns explainable decisions.

Sentinel is open source under the Apache-2.0 license.

The repository currently exposes two runnable surfaces:

- `cmd/sentinel`: Git-like policy and decision-trace tooling.
- `cmd/gateway`: OpenAI-compatible AI gateway with providers, guardrails, API keys, prompts, SQLite logs, and a local dashboard.
- `cmd/proxy`: MCP policy proxy for tool execution requests.

## Quickstart

```bash
go run ./cmd/sentinel validate policies/production-delete.yaml
go run ./cmd/sentinel test policies/production-delete.yaml examples/requests/delete-prod.json
go run ./cmd/sentinel trace policies/production-delete.yaml examples/requests/delete-prod.json
```

Start the MCP proxy:

```bash
go run ./cmd/proxy
```

Defaults:

- Listens on `:8080`
- Loads `policies/production-delete.yaml`
- Forwards allowed MCP calls to `http://127.0.0.1:8090/mcp`

Override with `SENTINEL_LISTEN`, `SENTINEL_POLICY`, and `SENTINEL_UPSTREAM`.

## Documentation

- [API Reference](docs/API.md)
- [Configuration](docs/CONFIGURATION.md)
- [Deployment](docs/DEPLOYMENT.md)
- [Architecture](docs/architecture.md)
- [Policy Standard](docs/standard.md)
- [Roadmap](ROADMAP.md)
- [Security Policy](SECURITY.md)
- [Contributing](CONTRIBUTING.md)

## Git-Like Workflow

```bash
go run ./cmd/sentinel verify policies/production-delete.yaml
go run ./cmd/sentinel trace policies/production-delete.yaml examples/requests/delete-prod.json > trace.yaml
go run ./cmd/sentinel replay trace.yaml
go run ./cmd/sentinel diff before.yaml after.yaml
```

## AI Gateway

Start the OpenAI-compatible gateway and dashboard:

```bash
OPENAI_API_KEY=... go run ./cmd/gateway
```

Run with a declarative gateway config:

```bash
SENTINEL_CONFIG=examples/gateway-config.yaml OPENAI_API_KEY=... go run ./cmd/gateway
```

Useful environment variables:

- `SENTINEL_CONFIG`: YAML or JSON gateway config file. Explicit env vars override file values.
- `SENTINEL_LISTEN`: listen address, default `:8080`
- `SENTINEL_DATA`: directory for `sentinel.db`, default `.`
- `SENTINEL_AUTH`: set to `true` to require Sentinel API keys
- `SENTINEL_CACHE_THRESHOLD`: semantic cache similarity threshold, default `0.95`; set `0` to disable
- `SENTINEL_CACHE_TTL`: cache expiration duration, default `24h`
- `SENTINEL_SHADOW_PROVIDER`: provider to call asynchronously for shadow comparisons
- `SENTINEL_AB_TEST`: weighted model/provider split, for example `logical=openai:gpt-4o-mini:90,anthropic:claude-3-5-haiku-20241022:10`
- `SENTINEL_MCP_ENABLED`: set to `false` to disable MCP proxy endpoints in `cmd/gateway`
- `SENTINEL_MCP_POLICY`: MCP policy file mounted at `/mcp`, default `policies/production-delete.yaml`
- `SENTINEL_MCP_UPSTREAM`: MCP upstream for allowed tool calls, default `http://127.0.0.1:8090/mcp`
- `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GOOGLE_API_KEY`, `GROQ_API_KEY`: provider credentials

Gateway endpoints:

- `POST /v1/chat/completions`
- `GET /v1/models`
- `GET /api/health`
- `GET /api/logs`
- `GET /api/metrics`
- `POST /api/keys`
- `GET /api/providers`
- `POST /api/feedback`
- `POST /api/prompts`

The dashboard is served from `/`.

Advanced gateway behavior:

- **Streaming:** `POST /v1/chat/completions` supports `stream: true` and emits OpenAI-compatible SSE chunks.
- **Caching:** exact prompt/provider/model matches are hashed and cached first; semantic similarity is used as a fallback. Hits return `x-sentinel-cache: HIT`.
- **Agent observability:** pass `x-sentinel-trace-id` and `x-sentinel-session-id` to group calls across an agent run.
- **Budgets:** API keys can carry `budget_usd`, `monthly_cost_budget`, and `monthly_token_budget`; exceeded keys receive `budget_exceeded`.
- **Circuit breakers:** failing providers are opened after consecutive errors and skipped until their cooldown expires.
- **Config-as-code:** use `SENTINEL_CONFIG` with `examples/gateway-config.yaml` to define cache, MCP, fallback, retry, and weighted routing behavior.
- **Provider catalog:** define provider accounts in config without embedding secrets. Accounts are exposed as virtual providers such as `@openai-prod/gpt-4o-mini`.
- **Feedback loop:** `POST /api/feedback` records thumbs-up/down style feedback against a `trace_id` or `request_id`.
- **Multimodal compatibility:** OpenAI-style message content arrays with text and image URL parts are accepted and preserved for OpenAI-compatible providers.
- **A/B routing:** use config routes or `SENTINEL_AB_TEST` to split a logical model across provider/model variants.
- **Shadow routing:** use `SENTINEL_SHADOW_PROVIDER` or `x-sentinel-shadow-provider` to compare a second provider without affecting the user response.
- **MCP Gateway:** `/mcp` and `/v1/mcp/tools/call` use Sentinel CEL policies to govern agent tool calls in the same process as LLM traffic.

Example config:

```yaml
apiVersion: sentinel.dev/v1
kind: GatewayConfig

gateway:
  listen: ":8080"
  cache_threshold: 0.95
  cache_ttl: 24h

providers:
  - name: openai-prod
    type: openai
    api_key_env: OPENAI_API_KEY

mcp:
  enabled: true
  policy: policies/production-delete.yaml
  upstream: http://127.0.0.1:8090/mcp

routes:
  canary-chat:
    variants:
      - provider: openai
        model: gpt-4o-mini
        weight: 90
      - provider: anthropic
        model: claude-3-5-haiku-20241022
        weight: 10
```
