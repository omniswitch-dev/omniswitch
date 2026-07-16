# Configuration

OmniSwitch can be configured with environment variables or a declarative config file.

## Gateway Config

Set `OMNISWITCH_CONFIG` to a YAML or JSON file:

```bash
OMNISWITCH_CONFIG=examples/gateway-config.yaml go run ./cmd/gateway
```

Environment variables are applied after the config file and override file values.

## Example

```yaml
apiVersion: omniswitch.dev/v1
kind: GatewayConfig

gateway:
  listen: ":8080"
  data_dir: ".omniswitch"
  auth: true
  cache_threshold: 0.95
  cache_ttl: 24h
  cache_scope: api_key
  log_payloads: false
  cors_origins: [https://app.example.com]
  circuit_breaker_failures: 5
  circuit_breaker_cooldown: 60s
  max_request_bytes: 10485760
  read_header_timeout: 5s
  read_timeout: 30s
  write_timeout: 0s
  idle_timeout: 60s

guardrails:
  actions:
    injection: deny
    pii: redact
  stream_buffer: true
  rules:
    - name: no-secret-marker
      stage: both
      pattern: '(?i)internal-secret'
      action: deny
      message: Sensitive content is not allowed

observability:
  otel_enabled: true
  otlp_endpoint: http://localhost:4318/v1/traces
  service_name: omniswitch-gateway
  insecure: true
  timeout: 10s
  prometheus_enabled: true

providers:
  - name: openai-prod
    type: openai
    api_key_env: OPENAI_API_KEY

  - name: ollama
    type: custom
    base_url: http://localhost:11434/v1
    models: [llama3.2]

mcp:
  enabled: true
  policy: policies/production-delete.yaml
  upstream: http://127.0.0.1:8090/mcp
  targets:
    - name: github
      upstream: http://127.0.0.1:8091/mcp
      policy: policies/production-delete.yaml
      headers:
        x-api-key: "${GITHUB_MCP_TOKEN}"

routes:
  canary-chat:
    fallbacks: ["@anthropic-prod"]
    max_retries: 2
    retry_backoff: 500ms
    retry_codes: [429, 500, 502, 503, 504]
    timeout: 30s
    default_params:
      temperature: 0.2
    override_params:
      max_tokens: 1000
    drop_params: [logprobs]
    variants:
      - provider: "@openai-prod"
        model: "@openai-prod/gpt-4o-mini"
        weight: 90
      - provider: anthropic
        model: claude-3-5-haiku-20241022
        weight: 10
        condition: 'model == "canary-chat"'
```

## Provider Accounts

Provider accounts create virtual providers without putting secrets in config.

```yaml
providers:
  - name: openai-prod
    type: openai
    api_key_env: OPENAI_API_KEY
```

This exposes models such as:

```text
@openai-prod/gpt-4o-mini
```

Supported provider types:

- `openai`
- `anthropic`
- `google`
- `groq`
- `custom`

`custom` connects any OpenAI-compatible endpoint. Use `base_url`, optional `models`, and optional `extra_headers`. Header values support environment expansion.

```yaml
providers:
  - name: azure-gpt4o
    type: custom
    base_url: https://YOUR_RESOURCE.openai.azure.com/openai/deployments/gpt-4o/chat/completions?api-version=2024-10-21
    api_key_env: AZURE_OPENAI_API_KEY
    extra_headers:
      api-key: "${AZURE_OPENAI_API_KEY}"
    models: [gpt-4o]
```

## Observability

OmniSwitch can export gateway and provider spans to any OTLP-compatible backend.

```yaml
observability:
  otel_enabled: true
  otlp_endpoint: http://localhost:4318/v1/traces
  service_name: omniswitch-gateway
  headers:
    x-api-key: observability-secret
```

Setting `OMNISWITCH_OTEL_ENDPOINT` also enables tracing.

Set `prometheus_enabled: true` (the runtime default) to expose the lightweight
Prometheus endpoint at `GET /metrics`. When authentication is enabled, this
endpoint requires an authenticated viewer role or higher.

## Security and Runtime Limits

`gateway.cache_scope` controls cache sharing: `api_key` (default), `workspace`,
`organization`, or `global`. Organization scope is derived from the
authenticated key's persisted workspace mapping, not a caller-provided header.
Use `global` only for public, tenant-independent traffic. `log_payloads` defaults to `false`, so SQLite request logs retain
metadata and usage but not raw prompts or outputs.

`cors_origins` is an explicit browser allow-list. Leaving it empty disables
cross-origin browser access. `max_request_bytes`, `read_header_timeout`,
`read_timeout`, `write_timeout`, and `idle_timeout` map to request and server
limits. The write timeout defaults to `0s` so long-running SSE streams are not
cut off; set a positive value only when streaming is not required.

When `auth: true` is used on an empty database, set
`OMNISWITCH_BOOTSTRAP_API_KEY` for the first process start. OmniSwitch stores only
its SHA-256 hash and creates the `bootstrap-admin` owner key. Store the secret
in an environment-injection or secret-manager mechanism, then rotate it into a
normal key and remove the bootstrap variable. `OMNISWITCH_BOOTSTRAP_ROLE` may be
`owner` (default) or `admin`; `OMNISWITCH_BOOTSTRAP_WORKSPACE` is optional.

## Routing

Routes support direct providers, ordered `fallbacks`, weighted `variants`, a
CEL `condition` on a variant (`model` and `prompt` variables), retry count and
backoff, retryable HTTP status codes, per-attempt timeouts, a shadow provider,
and request shaping. `default_params` fills absent values,
`override_params` always wins, and `drop_params` is applied last. The current
request shaper supports `model`, `temperature`, `max_tokens`, `top_p`, `stream`,
and `stop`.

## Guardrails

Built-in input/output checks cover PII, prompt injection, SQL patterns, toxic
content, and code leakage. `guardrails.actions` can set their action; regex
rules can run at `input`, `output`, or `both` stages. Supported actions are
`deny`, `redact`, `warn`, and `log`. Every trigger produces a structured
guardrail audit event; `deny` and `redact` are enforced in the local request
path. `stream_buffer: true` (default) buffers an SSE response before emitting
it, allowing output enforcement; turning it off reduces latency but makes
stream output a trusted-provider trade-off.

## MCP Targets

The legacy `upstream` is a default HTTP MCP upstream. `targets` adds federated
HTTP MCP servers. `tools/list` combines allowed target tools under stable
`target__tool` names, and `tools/call` dispatches the prefixed tool to the
matching upstream with the target's policy and configured headers. OmniSwitch
currently supports HTTP MCP transport here; stdio, SSE/streamable transport,
OAuth delegation, and A2A are not implemented.

## Provider Vault

Set `OMNISWITCH_VAULT_KEY` before creating virtual keys so encrypted provider credentials remain decryptable after restart.

```bash
curl -X POST http://localhost:8080/api/virtual-keys \
  -H "Content-Type: application/json" \
  -d '{
    "name": "azure-prod",
    "provider_type": "custom",
    "provider_name": "azure-prod",
    "base_url": "https://YOUR_RESOURCE.openai.azure.com/openai/deployments/gpt-4o/chat/completions?api-version=2024-10-21",
    "provider_key": "real-provider-secret",
    "metadata": {"auth_header": "api-key", "models": "gpt-4o"}
  }'
```

## Environment Variables

- `OMNISWITCH_LISTEN`
- `OMNISWITCH_DATA`
- `OMNISWITCH_AUTH`
- `OMNISWITCH_BOOTSTRAP_API_KEY`
- `OMNISWITCH_BOOTSTRAP_WORKSPACE`
- `OMNISWITCH_BOOTSTRAP_ROLE`
- `OMNISWITCH_CACHE_THRESHOLD`
- `OMNISWITCH_CACHE_TTL`
- `OMNISWITCH_CACHE_SCOPE`
- `OMNISWITCH_LOG_PAYLOADS`
- `OMNISWITCH_CORS_ORIGINS`
- `OMNISWITCH_GUARDRAIL_STREAM_BUFFER`
- `OMNISWITCH_MAX_REQUEST_BYTES`
- `OMNISWITCH_READ_HEADER_TIMEOUT`
- `OMNISWITCH_READ_TIMEOUT`
- `OMNISWITCH_WRITE_TIMEOUT`
- `OMNISWITCH_IDLE_TIMEOUT`
- `OMNISWITCH_CIRCUIT_BREAKER_FAILURES`
- `OMNISWITCH_CIRCUIT_BREAKER_COOLDOWN`
- `OMNISWITCH_SHADOW_PROVIDER`
- `OMNISWITCH_AB_TEST`
- `OMNISWITCH_OTEL_ENABLED`
- `OMNISWITCH_OTEL_ENDPOINT`
- `OMNISWITCH_OTEL_SERVICE_NAME`
- `OMNISWITCH_OTEL_HEADERS`
- `OMNISWITCH_OTEL_INSECURE`
- `OMNISWITCH_OTEL_TIMEOUT`
- `OMNISWITCH_PROMETHEUS_ENABLED`
- `OMNISWITCH_VAULT_KEY`
- `OMNISWITCH_MCP_ENABLED`
- `OMNISWITCH_MCP_POLICY`
- `OMNISWITCH_MCP_UPSTREAM`
- `OPENAI_API_KEY`
- `ANTHROPIC_API_KEY`
- `GOOGLE_API_KEY`
- `GROQ_API_KEY`
