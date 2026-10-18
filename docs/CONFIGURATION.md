# Configuration

OmniSwitch can be configured with environment variables or a declarative config file.

## Gateway Config

Set `OMNISWITCH_CONFIG` to a YAML or JSON file:

```bash
OMNISWITCH_CONFIG=examples/gateway-config.yaml go run ./cmd/gateway
```

Environment variables are applied after the config file and override file values.

The gateway checks the config file every 2 seconds and applies some changes
without a restart. See
[`GET /api/config/raw` and `POST /api/config/raw`](API.md#get-apiconfigraw-and-post-apiconfigraw)
for the list of settings that reload.

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
  budget_fail_open: false
  budget_refresh_interval: 5s

rate_limit:
  requests: 120
  window: 1m
  # Set to share quotas across gateway replicas.
  redis_url: redis://redis:6379/0
  prefix: omniswitch:ratelimit
  # false returns 503 if Redis is unavailable; true preserves availability.
  fail_open: false

authorization:
  rules:
    - name: member-model-restriction
      when: 'role == "member" && model == "production-model"'
      effect: deny
      message: This model requires an administrator key

identity:
  oidc:
    jwks_url: https://issuer.example.com/.well-known/jwks.json
    issuer: https://issuer.example.com/
    audience: omniswitch
    role_claim: roles
    workspace_claim: workspace_id
    organization_claim: organization_id
    cache_ttl: 5m
    stale_if_error: 24h

guardrails:
  actions:
    injection: deny
    pii: redact
  stream_buffer: true
  webhooks:
    - name: managed-moderation
      url: https://guardrails.example.com/v1/check
      stage: input
      action: deny
      headers:
        authorization: "Bearer ${GUARDRAIL_API_KEY}"
      timeout: 3s
      fail_open: false
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
      # Forward an OIDC bearer token only; OmniSwitch API keys never leave the gateway.
      forward_bearer_token: false

    - name: crm
      upstream: https://crm.example.com/mcp
      timeout: 30s
      metadata:
        environment: production
      # Swap the OIDC caller's token for a CRM token (RFC 8693).
      token_exchange:
        token_url: https://idp.example.com/oauth2/token
        client_id: omniswitch-gateway
        client_secret: "${CRM_EXCHANGE_SECRET}"
        audience: crm-api

    - name: filesystem
      transport: stdio
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "${WORKSPACE_ROOT}"]
      environment:
        LOG_LEVEL: warn

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

Retries, fallbacks and timeouts:

- `max_retries` retries the same provider. Without `retry_codes`, only
  transient failures are retried: 408, 409, 425, 429, 5xx and errors with no
  HTTP status (for example a refused connection). With `retry_codes`, only
  the listed statuses are retried.
- An error that is not retried moves the request to the next provider in
  `fallbacks`. An unsupported parameter is never retried on the same provider.
- `retry_backoff` is the minimum wait between retries. A `Retry-After`
  header from the provider extends the wait up to 10 seconds. When the
  provider asks for a longer wait, the router moves to the next fallback.
- `timeout` limits one attempt. For a streaming request it limits the time
  to the first chunk, and after that it is an idle timeout between chunks,
  so a long generation is not cut off.
- The circuit breaker counts 5xx responses, 408, 429, timeouts and network
  errors. Other 4xx responses and client disconnects do not open it. After
  `circuit_breaker_failures` consecutive failures, the provider's circuit
  opens for `circuit_breaker_cooldown`, and requests skip the provider.
- When the cooldown ends, the circuit is half-open: one request at a time
  (the probe) goes to the provider, and other requests use the fallback
  chain as for an open circuit. A successful probe closes the circuit. A
  failed probe opens it again for another cooldown. A probe that ends because
  the client cancelled frees the slot without a verdict. A probe that reports
  nothing within the cooldown counts as lost, and the next request becomes
  the probe. A retry against the same provider stops when its circuit opens.
- `circuit_breaker_failures` and `circuit_breaker_cooldown` reload with the
  config file. A change to one of them installs a new breaker with all
  circuits closed. A reload that does not change them keeps the current
  breaker and its open circuits. Requests that use a stored config share
  this breaker. Shadow traffic (`shadow_provider`) has a separate breaker
  with the same settings, so shadow failures never open circuits for client
  traffic. The same reload rule applies to the shadow breaker.
- An invalid CEL `condition` is logged once when routes load, and that
  variant never matches.

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
- `google` (alias `gemini`)
- `groq`
- `cohere` (native Cohere v2 chat adapter)
- `azure` (alias `azure-openai`, native Azure OpenAI adapter)
- `bedrock` (alias `aws-bedrock`, native AWS Bedrock Converse adapter with SigV4 signing)
- `custom` (any OpenAI-compatible endpoint)
- the OpenAI-compatible presets listed below (`mistral`, `xai`, ...)

Every account needs `name` and `type`. For `openai`, `anthropic`, `google`,
`groq`, and `cohere`, `api_key_env` names the variable that holds the key
(default: the provider's standard variable such as `OPENAI_API_KEY`). Setting
`base_url` on any non-Azure, non-Bedrock account turns it into an
OpenAI-compatible custom provider, which also honors `models` and
`extra_headers`. An account whose credentials are missing is skipped with a
log line rather than failing startup.

Providers can also be registered from the environment alone, with no config
entry: setting `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GOOGLE_API_KEY`,
`GROQ_API_KEY`, or `COHERE_API_KEY` registers that provider, and the Azure and
Bedrock variables below register `azure` and `bedrock`.

### Azure OpenAI

```yaml
providers:
  - name: azure-prod
    type: azure
    endpoint: https://YOUR_RESOURCE.openai.azure.com   # or base_url
    api_key_env: AZURE_OPENAI_API_KEY                   # default
    api_version: "2024-10-21"                           # default
    models: [gpt-4o, gpt-4o-mini]                       # deployment names
```

Requests go to `{endpoint}/openai/deployments/{model}/chat/completions`, so
each model name is an Azure deployment name. `endpoint` is required (config
validation rejects an Azure account without an absolute HTTP(S) endpoint); it
falls back to `AZURE_OPENAI_ENDPOINT` at registration time. Without a config
entry, setting both `AZURE_OPENAI_API_KEY` and `AZURE_OPENAI_ENDPOINT`
registers a provider named `azure`, with optional `AZURE_OPENAI_MODELS`
(comma-separated deployment names, default `gpt-4o,gpt-4o-mini`) and
`AZURE_OPENAI_API_VERSION`.

### AWS Bedrock

```yaml
providers:
  - name: bedrock-us
    type: bedrock
    region: us-east-1                              # required
    aws_access_key_env: AWS_ACCESS_KEY_ID          # default
    aws_secret_key_env: AWS_SECRET_ACCESS_KEY      # default
    aws_session_token_env: AWS_SESSION_TOKEN       # default, optional
    models: [anthropic.claude-3-5-sonnet-20240620-v1:0]
    guardrail_id: abc123                           # optional Bedrock Guardrail
    guardrail_version: "1"
```

The adapter calls the Bedrock Converse API with SigV4-signed requests using
static credentials read from the named environment variables. Without a config
entry, setting `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` registers a
provider named `bedrock` in `AWS_REGION` (then `AWS_DEFAULT_REGION`, then
`us-east-1`), with optional `AWS_SESSION_TOKEN`, `OMNISWITCH_BEDROCK_MODELS`
(comma-separated model or inference-profile IDs; defaults come from the model
catalog), and `OMNISWITCH_BEDROCK_GUARDRAIL_ID` /
`OMNISWITCH_BEDROCK_GUARDRAIL_VERSION`.

### Claude sampling parameters

Anthropic Claude models reject a request that sets both `temperature` and
`top_p`. When a request sets both, the `anthropic` adapter sends only
`temperature`. The `bedrock` adapter applies the same rule to Claude model
IDs (IDs that contain `anthropic.` or `claude`). Other Bedrock models get
both values. A request that sets only `top_p` keeps it.

### Cohere

```yaml
providers:
  - name: cohere-prod
    type: cohere
    api_key_env: COHERE_API_KEY   # default
```

### Reserved: `inference` replicas

Config validation accepts `type: inference` with `replicas` (each with `url`,
optional `runtime`, `models`, and `priority`) and `health_interval`, but the
gateway does not register `inference` accounts in this release; such an
account is skipped at startup.

### OpenAI-compatible presets

Setting an environment variable enables these providers instantly - no config entry required. Each preset ships a default endpoint and a curated model list; a config `providers` entry with the same `type` can override `base_url` or `models`.

| Type | Environment variable | Default endpoint |
| --- | --- | --- |
| `mistral` | `MISTRAL_API_KEY` | `https://api.mistral.ai/v1` |
| `xai` | `XAI_API_KEY` | `https://api.x.ai/v1` |
| `deepseek` | `DEEPSEEK_API_KEY` | `https://api.deepseek.com/v1` |
| `together` | `TOGETHER_API_KEY` | `https://api.together.xyz/v1` |
| `fireworks` | `FIREWORKS_API_KEY` | `https://api.fireworks.ai/inference/v1` |
| `perplexity` | `PERPLEXITY_API_KEY` | `https://api.perplexity.ai` |
| `cerebras` | `CEREBRAS_API_KEY` | `https://api.cerebras.ai/v1` |
| `openrouter` | `OPENROUTER_API_KEY` | `https://openrouter.ai/api/v1` |

```yaml
providers:
  # Explicit account form (overrides preset defaults)
  - name: mistral-eu
    type: mistral
    api_key_env: MISTRAL_EU_KEY
    models: [mistral-large-latest]
```

`custom` connects any OpenAI-compatible endpoint. Use `base_url`, optional `models`, and optional `extra_headers`. Header values support environment expansion. The example below reaches
Azure through the generic adapter; prefer the native [`type: azure`](#azure-openai)
account unless you need a fixed URL (for example, behind an API proxy).

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

## Model catalog

`/v1/models` and cost-based routing (the `cost-optimal` strategy) for OpenAI,
Anthropic, Google, and Groq are driven by a single consolidated catalog file,
embedded at build time via `go:embed`:
`internal/provider/catalog/models.json`.

Each entry records the model ID, `owned_by`, USD price per 1M input/output
tokens, a `source_url`, and the `verified_on` date it was last checked against
the provider's public pricing page. There is no live pricing fetch; updating
prices/models is a manual edit to this file (add a row, keep `verified_on`
current) followed by `go test ./internal/provider/...`. Bedrock and Cohere
also draw their default model lists from the catalog; Bedrock pricing then
matches by substring against the catalog's canonical Claude model IDs because
Bedrock model IDs vary by region/cross-region-inference-profile prefix.
Models not present in the catalog return zero estimated cost rather than a
guessed default. Mistral, DeepSeek, and xAI are reachable through the OpenAI-compatible
presets (see above) and have reference-only catalog rows under provider
`custom-preset-reference` for documentation purposes; they are not read by any
adapter.

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
endpoint requires an authenticated viewer role or higher. The metrics cover
all tenants, so a workspace- or organization-scoped caller gets `403`. With
`OMNISWITCH_METRICS_PUBLIC=true`, the endpoint needs no key and any caller
can read it.

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

`gateway.storage` selects the store backend: `sqlite` (default) or `postgres`.
When set to `postgres`, `gateway.storage_url` (a `postgres://` or
`postgresql://` DSN) is required and config validation rejects the file
without it. The `OMNISWITCH_DATABASE_URL` environment variable always takes
precedence over `gateway.storage_url`, so an operator can override storage
without editing the config file.

Upgrade note: on its first start after an upgrade, the gateway migrates the
schema:

- It adds `prompts.workspace_id` (empty for existing prompts, which then
  belong to the global namespace) and `workspace_id` and `organization_id`
  to `request_logs` and `feedback`. It does not backfill existing rows, so
  only global callers see them.
- It replaces the unique index `idx_prompts_name_version` with
  `idx_prompts_workspace_name_version` on `(workspace_id, name, version)`. If
  duplicate rows exist, the gateway logs a warning, keeps the old index and
  starts. Find the duplicates with
  `SELECT workspace_id, name, version, COUNT(*) FROM prompts GROUP BY workspace_id, name, version HAVING COUNT(*) > 1;`,
  remove them, and restart. The gateway then creates the index.
- It adds tenant indexes on `request_logs`, `feedback` and `workspaces`. On
  PostgreSQL, the gateway builds non-unique indexes on large tables with
  `CREATE INDEX CONCURRENTLY` in the background after startup.

`gateway.alerts` sends key budget notifications to a webhook and/or Slack
incoming webhook URL:

```yaml
gateway:
  alerts:
    webhook_url: https://hooks.example.com/omniswitch
    slack_url: https://hooks.slack.com/services/...
    thresholds: [0.5, 0.9]
```

`webhook_url` and `slack_url` must be absolute `http://` or `https://` URLs
when set; `thresholds` are fractions in `(0, 1]`. The gateway checks the
thresholds each time it records the cost of a request. An alert fires when a
key's lifetime spend reaches a threshold of `budget_usd`, or when its spend in
the current UTC month reaches a threshold of `monthly_cost_budget`. Monthly
alerts name the key as `<key id> (monthly YYYY-MM)`. Each process sends an
alert for one key and threshold once. A restart or a config file reload
clears this record, so the alert can fire again. Alerts reload alongside
guardrails when `OMNISWITCH_CONFIG` hot-reload is enabled.

`authorization.external` (an out-of-process HTTP authorization callout) is not
implemented in this release. Configuring `authorization.external.url` or
`authorization.external.timeout` fails config validation with a clear error;
use `authorization.rules` (CEL) instead.

## Authorization and Rate Limits

`authorization.rules` are CEL policies evaluated after API-key authentication.
They receive `method`, `path`, `model`, `api_key_id`, `workspace_id`,
`organization_id`, `role`, `subject`, and `claims`. A matching `deny` always
wins. When any `allow` rule exists, a request must also match at least one
allow rule. This composes with the built-in role checks for the control plane.

`rate_limit` controls the default request quota. Every API key can still set
its own quota in the control plane. Without `redis_url`, the gateway uses a
local sliding window, suitable for one replica. With Redis, OmniSwitch uses an
atomic fixed-window counter shared by all replicas. The startup check fails
closed by default if Redis cannot be reached; `fail_open: true` lets traffic
continue if Redis later becomes unavailable.

## Key Budgets

An API key can have a lifetime cost budget, a monthly cost budget and a
monthly token budget (see [`POST /api/keys`](API.md#post-apikeys)). Before
the gateway calls a provider, it reserves an estimate of the request's cost
and tokens. The input estimate comes from the request body. The output
estimate comes from `max_completion_tokens` or `max_tokens`, else 1024
tokens. A request gets HTTP 402 `budget_exceeded` when its estimate does
not fit in the remaining budget, less the estimates of requests in progress. When the request completes, its actual cost
replaces the estimate.

- `gateway.budget_refresh_interval` (`OMNISWITCH_BUDGET_REFRESH_INTERVAL`)
  sets how often the gateway reads a key's committed usage from the store.
  The default is `5s`. The value must be between `100ms` and `10m`; other
  values fail config validation or stop startup.
- `gateway.budget_fail_open` (`OMNISWITCH_BUDGET_FAIL_OPEN`) defaults to
  `false`. When the gateway cannot read the budget state of a key that has a
  budget, the request gets HTTP 503 `budget_unavailable`. With `true`, the
  request continues without a budget check, and the gateway logs this at
  most once every 30 seconds.

Both settings reload with the config file.

With several replicas, each replica enforces budgets exactly for its own
requests and sees the spend of other replicas within one refresh interval.
Together, the replicas can overspend a budget by about one estimate error
per replica plus what the other replicas spend in one refresh interval.
When refreshes fail, a replica uses its last usage snapshot for at most 12
times the refresh interval, and at least 1 minute. After that, requests for
keys with a budget get `budget_unavailable`, or continue when
`budget_fail_open` is `true`.

## OIDC Workload Identity

`identity.oidc` accepts JWTs signed by the public keys at `jwks_url`, alongside
locally issued API keys. It supports RSA, ECDSA, and Ed25519 signing keys and
verifies expiration, issuer, and audience when those values are configured.
The JWT `sub` becomes the request subject and quota/cache key; role, workspace,
and organization claims are mapped with the configurable claim names. JWT
claims are also available to authorization CEL rules as `subject` and
`claims`. Supplying a JWKS URL enables gateway authentication automatically.

The gateway caches the JWKS signing keys for `cache_ttl` (default `5m`). When
the cache expires, requests keep using the cached keys while one background
fetch refreshes them, so a slow JWKS endpoint does not delay requests. If the
refresh fails, the cached keys stay usable for `stale_if_error` (default `24h`,
`0` to `168h`) after `cache_ttl`, so an identity provider outage does not
reject every OIDC caller. After a failed fetch, the next fetch waits 1 second,
and the wait doubles up to 1 minute until a fetch succeeds. During that wait,
a token with an unknown `kid` does not start a fetch. When the grace ends, the
gateway drops the cached keys and rejects OIDC tokens with 401 until the JWKS
endpoint answers again. A successful fetch replaces the key set at once.

Security trade-off: during an outage the gateway accepts tokens signed with
the cached keys for at most `stale_if_error`, also a key that the identity
provider revoked during the outage. Token `exp` and `nbf` are still checked.
Set `stale_if_error: 0` to reject OIDC tokens as soon as `cache_ttl` ends and
a refresh fails.

When `auth: true` is used on an empty database, set
`OMNISWITCH_BOOTSTRAP_API_KEY` for the first process start. OmniSwitch stores only
its SHA-256 hash and creates the `bootstrap-admin` owner key. Store the secret
in an environment-injection or secret-manager mechanism, then rotate it into a
normal key and remove the bootstrap variable. `OMNISWITCH_BOOTSTRAP_ROLE` may be
`owner` (default) or `admin`; `OMNISWITCH_BOOTSTRAP_WORKSPACE` is optional.

## Routing

A route may set `strategy`: `direct`, `fallback`, `round-robin`,
`least-latency`, or `cost-optimal` (lowest catalog input-token price). Routes
support direct providers, ordered `fallbacks`, weighted `variants`, a
CEL `condition` on a variant (`model` and `prompt` variables), retry count and
backoff, retryable HTTP status codes, per-attempt timeouts, a shadow provider,
and request shaping. The route-level `shadow_provider` sends a copy of each
request to that provider asynchronously for comparison; the client response
always comes from the primary route. `gateway.shadow_provider` (or
`OMNISWITCH_SHADOW_PROVIDER`) sets a gateway-wide shadow provider for routes
that do not set their own. `default_params` fills absent values,
`override_params` always wins, and `drop_params` is applied last. The current
request-shaping fields are `model`, `temperature`, `max_tokens`,
`max_completion_tokens`, `top_p`, `stream`, `stop`, `n`, `seed`,
`presence_penalty`, `frequency_penalty`, `user`, and `parallel_tool_calls`.

### Stored configs

A request can select a stored config with the `x-omniswitch-config` header
(create one with [`POST /api/configs`](API.md#post-apiconfigs)). Only the
`routes` of a stored config apply. They override the file routes with the
same model name, and the other file routes stay in effect. File-route hot
reloads apply to stored-config requests too.

The gateway caches each resolved stored config in memory for 5 seconds and
remembers unknown config names for 2 seconds. The cache holds at most 512
entries (each config uses one entry for its name and one for its ID). A
change made through `/api/configs` takes effect at once on the replica that
handled it. Other replicas see the change within 5 seconds.

## Guardrails

Built-in input/output checks cover PII, prompt injection, SQL patterns, toxic
content, and code leakage. `guardrails.actions` can set their action; regex
rules can run at `input`, `output`, or `both` stages. Supported actions are
`deny`, `redact`, `warn`, `log`, `retry`, `reroute`, and `fallback`. Every trigger produces a structured
guardrail audit event; `deny` and `redact` are enforced in the local request
path. `retry` evaluates up to `max_retries` (default 1) times. `reroute` overrides
the provider selection to a specified `reroute_provider`, and `fallback` overrides
the requested model to a specified `fallback` model (e.g. `anthropic/claude-3`). `stream_buffer: true` (default) buffers an SSE response before emitting
it, allowing output enforcement; turning it off reduces latency but makes
stream output a trusted-provider trade-off.

`guardrails.tool_rules` inspect tool calls rather than text. Each rule has a
`name`, an optional `stage` (`input`, `output` (default), or `both`), a
`tool_name` regex matched against the function name, an `arguments_pattern`
regex matched against the raw JSON arguments, an `action` (default `deny`),
and an optional `message`. A rule needs at least one of the two patterns and
triggers only when every pattern it sets matches.

```yaml
guardrails:
  tool_rules:
    - name: no-shell-rm
      tool_name: '^(shell|exec)$'
      arguments_pattern: 'rm\s+-rf'
      action: deny
```

`guardrails.llm_judges` call an OpenAI-compatible chat completions `url` with
`model` and ask it for a JSON verdict (`triggered`, `message`, `details`)
about the content and tool calls. Optional keys: `stage` (default `both`),
`action` (default `deny`), `system_prompt` (replaces the built-in evaluator
prompt), `api_key_env` (sent as a bearer token), `headers`, `timeout` (default
`10s`), and `fail_open`.

`guardrails.webhooks` adds an opt-in connector for a managed or in-house
moderation service. OmniSwitch sends `{stage, text, messages}` and expects
`{triggered, message, details}` or `{allowed}`. The configured `action`
controls enforcement; `fail_open: false` (the safer default) treats an
unavailable webhook as a guardrail trigger, while `true` ignores its failure.

## MCP Targets

The legacy `upstream` is a default HTTP MCP upstream. `targets` adds federated
HTTP MCP servers. `tools/list` combines allowed target tools under stable
`target__tool` names, and `tools/call` dispatches the prefixed tool to the
matching upstream with the target's policy and configured headers. HTTP
responses with `text/event-stream` are streamed through without gateway
buffering, so streamable HTTP and SSE-capable MCP servers work through the same
endpoint. Set `forward_bearer_token: true` only for targets that require
OAuth/OIDC delegation: it forwards the authenticated OIDC bearer token and
never forwards a local OmniSwitch API key.

Stdio targets use a persistent child process that speaks newline-delimited
JSON-RPC. Concurrent calls share the process: the gateway gives each call
its own request ID and restores the client's ID in the response. A client's
`notifications/cancelled` goes to the matching call. When the gateway stops
waiting for a call (for example, at the timeout), it sends
`notifications/cancelled` to the process. On shutdown, the gateway kills the
whole process group.

The gateway does the MCP handshake with a stdio server itself, with protocol
version `2025-06-18`. It does this on first use and after each restart of
the process. A client `initialize` request to a stdio target gets the
server's stored initialize result, and a client `notifications/initialized`
gets HTTP 202.

### Per-target timeout, metadata and token exchange

| Field | Targets | Effect |
|---|---|---|
| `timeout` | All | Limits one call. The default is `2m`. For an HTTP target, it limits the wait for the response headers, including the token exchange, so a long event stream is not cut. `tools/list` federation and OpenAPI tool calls are limited as a whole. For a stdio target, it limits each request. |
| `metadata` | All | Operator-trusted policy attributes. `resource_type` replaces the type that OmniSwitch infers from the tool. `environment` comes before the environments inferred from tool arguments, but `production` from any source always wins. CEL rules read them as `resource.type`, `resource.environment` and `metadata`. Keys that start with `mcp.` are reserved. |
| `token_exchange` | HTTP targets without `openapi_spec` | Exchanges the OIDC caller's bearer token for a token issued for this target (RFC 8693, `grant_type=urn:ietf:params:oauth:grant-type:token-exchange`) before each call. |

`token_exchange` fields:

| Field | Required | Meaning |
|---|---|---|
| `token_url` | Yes | Absolute `http://` or `https://` URL of the token endpoint. |
| `client_id` | Yes | OAuth client of the gateway at the token endpoint. |
| `client_secret` | No | Client secret, sent in the form body. |
| `audience`, `resource`, `scope` | No | Sent as the RFC 8693 parameters of the same names. |
| `subject_token_type`, `requested_token_type` | No | Default `urn:ietf:params:oauth:token-type:access_token`. |
| `actor_token`, `actor_token_type` | No | An actor token for delegation. `actor_token_type` needs `actor_token` and has the same default. |
| `token_cache_ttl` | No | Upper limit for the cache lifetime of an exchanged token. Tokens are cached per caller token and target until one minute before `expires_in` (default 1h). |

`client_id`, `client_secret` and `actor_token` expand `${VAR}` references at
startup. A reference that expands to an empty value stops startup. Use
references so that secrets are not in the file that `GET /api/config/raw`
returns.

Only OIDC-authenticated callers have a token to exchange. A call from an
OmniSwitch API key goes upstream without an `Authorization` header, and an
API key is never exchanged. If the token endpoint fails, the call gets a
JSON-RPC error and is not sent upstream. Validation rejects these
combinations:

- `token_exchange` on a stdio target, or together with `openapi_spec`.
- `token_exchange` together with `forward_bearer_token: true`.
- `token_exchange` together with an `Authorization` entry in `headers`,
  because the header would replace the exchanged token.

### OpenAPI-to-MCP conversion

Set `openapi_spec` on a target to expose an OpenAPI 3.x document's operations
as synthetic MCP tools (local path or URL). Each operation becomes a tool
named by its `operationId`, or by its method and path when it has no
`operationId`. `tools/list` shows the tool as `<target>__<name>`. Every call
passes through the target policy, audit trail, and headers before plain HTTP
execution.

Tool arguments map to the HTTP request as follows:

- Arguments for declared `path`, `query`, `header` and `cookie` parameters go
  to that location. A path value that is empty or `.`, or that contains `/`,
  `\` or `..`, is rejected. A header value that contains CR or LF is
  rejected.
- The `body` argument is the whole JSON request body. A spec that declares a
  parameter named `body` fails to load.
- Other arguments become query parameters, for all methods.
- The target's configured `headers` replace header arguments with the same
  name.
- The gateway sends W3C `traceparent` and `tracestate` HTTP headers.
- The gateway follows only redirects to the same scheme, host and port.

```yaml
mcp:
  targets:
    - name: pets-api
      upstream: https://petstore.example.com   # REST base URL
      openapi_spec: ./specs/petstore.yaml      # or https://...
      headers:
        authorization: "Bearer ${PETSTORE_TOKEN}"
```

On forwarded MCP calls, distributed trace context propagates into
`params._meta.traceparent` (SEP-414), so downstream MCP servers can continue
the gateway's OTel trace. A2A is not an MCP target type; it is
served separately through public Agent Card discovery and `/a2a` JSON-RPC.

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

Environment variables override the matching config-file keys. An empty value
is treated as unset. An invalid boolean, integer, number or duration stops
startup with an error that names the variable. Booleans accept `true`/`false`,
`1`/`0`, `t`/`f`, `y`/`n`, `yes`/`no` and `on`/`off` in any case. Integers must be
positive (`OMNISWITCH_LOG_RETENTION_DAYS` also accepts `0`), and durations use
Go syntax such as `30s` or `5m`.

- `OMNISWITCH_CONFIG` - path of the YAML or JSON gateway config file.
- `OMNISWITCH_LISTEN` - listen address. When neither this nor
  `gateway.listen` is set, the gateway binds `127.0.0.1:8080` while
  authentication is off and `:8080` (all interfaces) while it is on.
- `OMNISWITCH_DATA`
- `OMNISWITCH_DATABASE_URL` - a `postgres://` or `postgresql://` DSN enables PostgreSQL storage so multiple gateway replicas share keys, logs, budgets, and configuration. When it and `gateway.storage_url` are unset, SQLite in the data directory is used. Any other URL scheme stops startup with an error.
- `OMNISWITCH_AUTH` - defaults to `false` (no authentication) unless
  `gateway.auth` is set in the config file or an OIDC JWKS URL is configured.
  With auth on and an empty key store, set `OMNISWITCH_BOOTSTRAP_API_KEY` for
  the first start.
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
- `OMNISWITCH_BUDGET_FAIL_OPEN` - defaults to `false`. See [Key Budgets](#key-budgets).
- `OMNISWITCH_BUDGET_REFRESH_INTERVAL` - defaults to `5s`; allowed values are `100ms` to `10m`. See [Key Budgets](#key-budgets).
- `OMNISWITCH_RATE_LIMIT_REQUESTS`
- `OMNISWITCH_RATE_LIMIT_WINDOW`
- `OMNISWITCH_RATE_LIMIT_REDIS_URL`
- `OMNISWITCH_RATE_LIMIT_PREFIX`
- `OMNISWITCH_RATE_LIMIT_FAIL_OPEN`
- `OMNISWITCH_OIDC_JWKS_URL`
- `OMNISWITCH_OIDC_ISSUER`
- `OMNISWITCH_OIDC_AUDIENCE`
- `OMNISWITCH_OIDC_ROLE_CLAIM`
- `OMNISWITCH_OIDC_WORKSPACE_CLAIM`
- `OMNISWITCH_OIDC_ORGANIZATION_CLAIM`
- `OMNISWITCH_OIDC_CACHE_TTL`
- `OMNISWITCH_OIDC_STALE_IF_ERROR` (a Go duration from `0` to `168h`; another value stops startup)
- `OMNISWITCH_SHADOW_PROVIDER`
- `OMNISWITCH_AB_TEST`
- `OMNISWITCH_OTEL_ENABLED`
- `OMNISWITCH_OTEL_ENDPOINT`
- `OMNISWITCH_OTEL_SERVICE_NAME`
- `OMNISWITCH_OTEL_HEADERS`
- `OMNISWITCH_OTEL_INSECURE`
- `OMNISWITCH_OTEL_TIMEOUT`
- `OMNISWITCH_PROMETHEUS_ENABLED`
- `OMNISWITCH_METRICS_PUBLIC` - defaults to `false`. When authentication is on, `/metrics` needs a valid key of a global caller (no workspace and no organization) unless this is `true`. With `true`, any caller can read `/metrics`, including workspace- and organization-scoped callers.
- `OMNISWITCH_VAULT_KEY`
- `OMNISWITCH_MCP_ENABLED` - defaults to `false`. When enabled without `OMNISWITCH_MCP_POLICY` (or `mcp.policy`) set, the gateway falls back to a conservative embedded default policy rather than failing to start.
- `OMNISWITCH_MCP_POLICY`
- `OMNISWITCH_MCP_UPSTREAM`
- `OMNISWITCH_POLICY_DIR` - the only directory that `POST /api/evals/policy` reads policy files from. Requests may name only relative `.cel`, `.yaml` or `.yml` files inside it. The default is the directory of `OMNISWITCH_MCP_POLICY` when that directory is named `policies` (the default MCP policy gives `policies`). For any other MCP policy location, for example next to the gateway config, policy replay is off until you set this variable, so replay cannot read other YAML files there. Set it to `off` or `-` to turn policy replay off.
- `OMNISWITCH_SHUTDOWN_TIMEOUT` - defaults to `30s`. On SIGINT/SIGTERM, `omniswitch serve` stops accepting new connections, waits up to this long for in-flight requests (including SSE streams) to finish, then exits `0`.
- `OMNISWITCH_ALLOW_NO_PROVIDERS` - defaults to `false`. When `false`, `GET /readyz` reports not-ready if no provider is registered; set to `true` for local smoke-testing without any provider configured.
- `OMNISWITCH_LOG_RETENTION_DAYS` - defaults to `0` (keep forever). When positive, the gateway deletes request logs, guardrail events and shadow requests older than this many days, once at startup and then every hour. Records from the current UTC month are always kept, because monthly key budgets are computed from them. Lifetime key spend is stored on the key and is not affected. Expired semantic-cache entries are deleted on the same hourly schedule whatever this value is.
- `OPENAI_API_KEY`
- `ANTHROPIC_API_KEY`
- `GOOGLE_API_KEY`
- `GROQ_API_KEY`
- `COHERE_API_KEY`
- `AZURE_OPENAI_API_KEY`, `AZURE_OPENAI_ENDPOINT`, `AZURE_OPENAI_MODELS`,
  `AZURE_OPENAI_API_VERSION` - see [Azure OpenAI](#azure-openai).
- `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`,
  `AWS_REGION`, `AWS_DEFAULT_REGION`, `OMNISWITCH_BEDROCK_MODELS`,
  `OMNISWITCH_BEDROCK_GUARDRAIL_ID`, `OMNISWITCH_BEDROCK_GUARDRAIL_VERSION` -
  see [AWS Bedrock](#aws-bedrock).
- `MISTRAL_API_KEY`, `XAI_API_KEY`, `DEEPSEEK_API_KEY`, `TOGETHER_API_KEY`,
  `FIREWORKS_API_KEY`, `PERPLEXITY_API_KEY`, `CEREBRAS_API_KEY`,
  `OPENROUTER_API_KEY` - see [OpenAI-compatible presets](#openai-compatible-presets).

## Command Line and Health Checks

See [`docs/CLI.md`](CLI.md) for `omniswitch serve`, `omniswitch validate-config`,
and `omniswitch version`. `GET /healthz` (liveness) and `GET /readyz`
(readiness: store reachability and provider registration) are always
unauthenticated, independent of `gateway.auth`, and safe to point Kubernetes
or load-balancer health checks at.
