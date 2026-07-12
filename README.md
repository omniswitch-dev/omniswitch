# Sentinel

Sentinel is an open-source AI gateway and policy enforcement layer. It provides a unified OpenAI-compatible API across any LLM provider, including OpenAI, Anthropic, Google Gemini, Groq, and **any OpenAI-compatible endpoint** (Ollama, vLLM, DeepSeek, Together AI, Mistral, Azure OpenAI, and more).

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
- [Portkey Comparison](docs/PORTKEY_COMPARISON.md)
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
- `SENTINEL_VAULT_KEY`: passphrase used to encrypt stored provider credentials
- `SENTINEL_OTEL_ENABLED`: set to `true` to enable OpenTelemetry trace export
- `SENTINEL_OTEL_ENDPOINT`: OTLP HTTP traces endpoint, for example `http://localhost:4318/v1/traces`
- `SENTINEL_OTEL_SERVICE_NAME`: OpenTelemetry service name, default `sentinel-gateway`
- `SENTINEL_OTEL_HEADERS`: comma-separated OTLP headers, for example `x-api-key=...`
- `SENTINEL_OTEL_INSECURE`: allow insecure OTLP transport when needed
- `SENTINEL_OTEL_TIMEOUT`: OTLP exporter timeout, default `10s`
- `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GOOGLE_API_KEY`, `GROQ_API_KEY`: built-in provider credentials
- Any custom provider keys (e.g. `DEEPSEEK_API_KEY`, `TOGETHER_API_KEY`) referenced via `api_key_env` in config

### Custom Providers (Ollama, vLLM, DeepSeek, etc.)

Connect any OpenAI-compatible endpoint by adding it to your config:

```yaml
providers:
  - name: ollama
    type: custom
    base_url: http://localhost:11434/v1
    models: [llama3.2, codellama, mistral]

  - name: deepseek
    type: custom
    base_url: https://api.deepseek.com/v1
    api_key_env: DEEPSEEK_API_KEY
    models: [deepseek-chat, deepseek-coder]

  - name: together
    type: custom
    base_url: https://api.together.xyz/v1
    api_key_env: TOGETHER_API_KEY
    models: [meta-llama/Llama-3.3-70B-Instruct-Turbo]

  - name: azure-gpt4o
    type: custom
    base_url: https://YOUR_RESOURCE.openai.azure.com/openai/deployments/gpt-4o/chat/completions?api-version=2024-10-21
    api_key_env: AZURE_OPENAI_API_KEY
    extra_headers:
      api-key: "${AZURE_OPENAI_API_KEY}"
    models: [gpt-4o]
```

Then query them via the unified API:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "x-sentinel-provider: ollama" \
  -d '{"model": "llama3.2", "messages": [{"role": "user", "content": "Hello!"}]}'
```

## SDKs

### Python

```bash
pip install openai  # sentinel-ai wraps the official openai package
```

```python
from sentinel import Sentinel

client = Sentinel(gateway_url="http://localhost:8080")
response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Hello!"}],
)
print(response.choices[0].message.content)

# Force a provider
client = Sentinel(provider="anthropic")

# With observability
client = Sentinel(trace_id="agent-run-001", session_id="conv-abc")

# Streaming
stream = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Tell a story"}],
    stream=True,
)
for chunk in stream:
    print(chunk.choices[0].delta.content or "", end="")
```

### Node.js / TypeScript

```bash
npm install openai  # sentinel-ai wraps the official openai package
```

```javascript
const { Sentinel } = require('./sdk/node');

const client = new Sentinel({ gatewayUrl: 'http://localhost:8080' });
const response = await client.chat.completions.create({
  model: 'gpt-4o-mini',
  messages: [{ role: 'user', content: 'Hello!' }],
});
console.log(response.choices[0].message.content);

// Streaming
const stream = await client.chat.completions.create({
  model: 'gpt-4o-mini',
  messages: [{ role: 'user', content: 'Tell a story' }],
  stream: true,
});
for await (const chunk of stream) {
  process.stdout.write(chunk.choices[0]?.delta?.content || '');
}
```

Gateway endpoints:

- `POST /v1/chat/completions`
- `GET /v1/models`
- `GET /api/health`
- `GET /api/logs`
- `GET /api/metrics`
- `POST /api/keys`
- `POST /api/orgs`
- `POST /api/workspaces`
- `POST /api/users`
- `POST /api/workspace-members`
- `GET /api/providers`
- `GET /api/virtual-keys`
- `POST /api/virtual-keys`
- `POST /api/virtual-keys/rotate`
- `POST /api/feedback`
- `POST /api/prompts`
- `POST /api/evals/policy`

The dashboard is served from `/`.

Advanced gateway behavior:

- **Streaming:** `POST /v1/chat/completions` supports `stream: true` and emits OpenAI-compatible SSE chunks.
- **Caching:** exact prompt/provider/model matches are hashed and cached first; semantic similarity is used as a fallback. Hits return `x-sentinel-cache: HIT`.
- **Agent observability:** pass `x-sentinel-trace-id` and `x-sentinel-session-id` to group calls across an agent run.
- **Raw observability logs:** request and response payloads are persisted with size-capped raw log entries for incident review and replay.
- **Budgets:** API keys can carry `budget_usd`, `monthly_cost_budget`, and `monthly_token_budget`; exceeded keys receive `budget_exceeded`.
- **Workspace governance:** organizations, workspaces, users, roles, workspace memberships, and workspace-scoped API keys are available through the management API.
- **Circuit breakers:** failing providers are opened after consecutive errors and skipped until their cooldown expires.
- **Config-as-code:** use `SENTINEL_CONFIG` with `examples/gateway-config.yaml` to define cache, MCP, fallback, retry, and weighted routing behavior.
- **Provider catalog:** define provider accounts in config without embedding secrets. Accounts are exposed as virtual providers such as `@openai-prod/gpt-4o-mini`.
- **Feedback loop:** `POST /api/feedback` records thumbs-up/down style feedback against a `trace_id` or `request_id`.
- **Prompt versions:** creating a prompt with an existing name creates the next immutable version; `/api/prompts/versions` returns version history.
- **Policy eval replay:** `/api/evals/policy` replays batches of tool requests against one or more policy files to estimate future allow/deny impact.
- **Multimodal compatibility:** OpenAI-style message content arrays with text and image URL parts are accepted and preserved for OpenAI-compatible providers.
- **A/B routing:** use config routes or `SENTINEL_AB_TEST` to split a logical model across provider/model variants.
- **Shadow routing:** use `SENTINEL_SHADOW_PROVIDER` or `x-sentinel-shadow-provider` to compare a second provider without affecting the user response.
- **MCP Gateway:** `/mcp` and `/v1/mcp/tools/call` use Sentinel CEL policies to govern agent tool calls in the same process as LLM traffic.
- **OpenTelemetry:** export gateway and provider spans to any OTLP-compatible backend.
- **Provider credential vault:** store encrypted provider credentials and expose them as virtual providers with rotation and revoke workflows.

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
