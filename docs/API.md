# API Reference

OmniSwitch exposes OpenAI-compatible inference endpoints and OmniSwitch management endpoints.

## OpenAI-Compatible Gateway

### `POST /v1/chat/completions`

Accepts OpenAI-style chat completion requests.

Supported request fields, and how each is handled per adapter:

| Field | openai / azure / groq / custom (OpenAI-compatible) | anthropic | google |
|---|---|---|---|
| `model`, `messages`, `temperature`, `max_tokens`, `top_p`, `stream`, `stop` | forwarded | forwarded/translated | forwarded/translated |
| `tools`, `tool_choice` | forwarded | translated | translated |
| `message.name` (per-message) | forwarded | dropped (no Anthropic equivalent) | dropped (no Gemini equivalent) |
| OpenAI content arrays with `text` and `image_url` parts | forwarded | translated | translated |
| `response_format` (`text`, `json_object`, `json_schema`) | forwarded | translated to `output_config.format` (`json_schema`); any other `type` is rejected | translated to `responseMimeType`/`responseSchema`; any other `type` is rejected |
| `max_completion_tokens` | forwarded | translated to `max_tokens` (wins over `max_tokens` if both set) | translated to `maxOutputTokens` (wins over `max_tokens` if both set) |
| `parallel_tool_calls` | forwarded | translated to `tool_choice.disable_parallel_tool_use` | **rejected** (400 `unsupported_parameter`) |
| `stream_options.include_usage` | forwarded | honored: an extra chunk with empty `choices` and populated `usage` is emitted before `[DONE]` | honored: same synthesized final-chunk behavior |
| `store`, `metadata` | forwarded | ignored (platform bookkeeping only; no effect on generation, not forwarded upstream) | ignored (same as anthropic) |
| `n` (only `1` is safe) | forwarded | **rejected** if `n > 1` | **rejected** if `n > 1` |
| `seed` | forwarded | **rejected** | **rejected** |
| `presence_penalty`, `frequency_penalty` | forwarded | **rejected** | **rejected** |
| `logit_bias` | forwarded | **rejected** if non-empty | **rejected** if non-empty |
| `reasoning_effort` | forwarded | **rejected** if non-empty | **rejected** if non-empty |
| `service_tier` | forwarded | **rejected** if non-empty | **rejected** if non-empty |
| `user` | forwarded | dropped (no Anthropic equivalent) | dropped (no Gemini equivalent) |
| any other/future top-level OpenAI field (e.g. `prediction`, `modalities`, `audio`) | forwarded byte-for-byte (captured via an internal `Extra` map, not silently dropped) | dropped (adapter-specific request shape has no passthrough slot) | dropped (same) |

"Rejected" means the gateway returns HTTP 400 with an OpenAI-style error body
instead of silently ignoring the field or forwarding a request that would
behave differently than requested:

```json
{"error": {"message": "anthropic does not support the \"seed\" parameter; remove it or use an OpenAI-compatible provider", "type": "invalid_request_error", "param": "seed", "code": "unsupported_parameter"}}
```

This applies to both the non-streaming and streaming (`stream: true`) paths.
Requests to OpenAI-compatible upstreams (`openai`, `azure`, `groq`, `custom`,
and any preset built on `custom`) never hit this path: every field above,
plus any field OmniSwitch does not model by name, is forwarded verbatim.

Cache keys (exact-match cache) and guardrail evaluation see the full request:
the cache key is derived from the request's complete JSON encoding (so two
requests differing only in, say, `response_format` or `seed` never collide on
the same cached response), and guardrails evaluate the actual `messages`
content, which is unaffected by any of the fields above.

Headers:

- `Authorization: Bearer <omniswitch-api-key>` when `OMNISWITCH_AUTH=true`
- `x-omniswitch-provider`: force a provider or provider alias
- `x-omniswitch-config`: use a dynamic gateway config by name or ID (create
  one with [`POST /api/configs`](#post-apiconfigs)); an unknown or disabled
  config returns `400`
- `x-omniswitch-shadow-provider`: run a secondary provider asynchronously
- `x-omniswitch-trace-id`: group calls into a trace
- `x-omniswitch-session-id`: group calls into an agent session

Streaming is available on this endpoint with `stream: true`. OmniSwitch's default
guardrail mode buffers stream output before emitting SSE so output policies can
be enforced.

### `POST /v1/responses`

Implements the practical text/messages subset of the OpenAI Responses API:
`model`, a string or message-array `input`, `instructions`, `temperature`,
`max_output_tokens`, and `top_p`. It uses the same routing, cache, budget,
guardrail, and logging path as chat completions. Responses streaming, tools,
background mode, and other advanced Responses features are not yet supported.

### `POST /v1/messages`

Accepts the core Anthropic Messages shape (`model`, `messages`, `system`,
`max_tokens`, `temperature`, `top_p`) and returns an Anthropic-style message
result through the common OmniSwitch path. Streaming and Anthropic tool use are
not yet supported.

### `POST /v1/embeddings`

Forwards OpenAI-compatible embedding requests (`model`, `input`, optional
`encoding_format`, `dimensions`, and `user`) to OpenAI or a compatible custom
provider. Input budget and guardrail checks apply; providers without an
embeddings implementation are skipped in a configured fallback chain.

### `POST /v1/rerank`

Accepts a common RAG rerank request shape (`model`, `query`, `documents`,
optional `top_n`, `return_documents`, `max_chunks_per_doc`, and `user`) and
routes it to providers that implement reranking. Custom OpenAI-compatible
providers forward rerank traffic to `/rerank`; fallback chains skip providers
without rerank support. Query and document text are inspected by input
guardrails before the provider call.

### `GET /v1/models`

Returns all models registered through provider adapters and provider catalog aliases.
For OpenAI, Anthropic, Google, and Groq, the advertised model list and the
per-1M-token pricing used for cost estimation and cost-optimal routing both
come from the consolidated model catalog at
`internal/provider/catalog/models.json` (see "Model catalog" in
[docs/CONFIGURATION.md](CONFIGURATION.md)).

## A2A Gateway

### `GET /.well-known/agent-card.json`

Publishes a public A2A v1 Agent Card for OmniSwitch's JSON-RPC interface. The
card advertises `/a2a` as a governed text inference agent and is cacheable by
A2A clients with `Cache-Control` and `ETag` validators.

### `POST /a2a`

Accepts A2A v1 JSON-RPC requests. `SendMessage` supports direct text message
responses by converting message text into the same chat-completions path used
by OpenAI-compatible clients. Provide the model with `metadata.model` or the
`x-omniswitch-model` header.

Supported A2A methods:

- `SendMessage`
- `GetExtendedAgentCard`

Task lifecycle methods, streaming, push notifications, and outbound A2A client
calls are not yet implemented.

## Health Checks

`/healthz` and `/readyz` are served ahead of the authentication, authorization
and rate-limit middleware, so orchestrators can probe them without a key.
Both accept `GET` and `HEAD`.

### `GET /healthz`

Liveness. Returns `200 {"status":"ok"}` whenever the process is serving HTTP;
it never touches the store or providers.

### `GET /readyz`

Readiness. Returns `200 {"status":"ready"}` when the store answers a ping and
at least one provider is registered (or `OMNISWITCH_ALLOW_NO_PROVIDERS=true`).
Otherwise returns `503 {"status":"not ready","reasons":[...]}`.

## Management API

### `GET /api/health`

Returns health information.

### `GET /api/version`

Returns the build metadata of the running binary:

```json
{"version": "0.2.0-beta", "commit": "abc1234", "built": "2026-01-01T00:00:00Z"}
```

Builds without release metadata report `dev`, `none` and `unknown`.

### `GET /api/logs`

Query recent request logs.

Query parameters:

- `limit`
- `offset`
- `provider`
- `status`

Raw `request_body` and `response_body` are present only when
`log_payloads: true` is explicitly enabled; metadata, usage, cache, and status
remain available by default.

### `GET /api/metrics`

Returns gateway metrics.

Query parameters:

- `window`: `1h`, `6h`, `7d`, or `30d`

### `GET /api/metrics/providers`

Returns per-provider aggregate metrics.

### `GET /metrics`

Returns Prometheus text-format gateway metrics for the last 24 hours when
`prometheus_enabled` is true. With gateway authentication enabled, a viewer,
member, admin, or owner key is required.

### `GET /api/providers`

Returns registered provider names and exposed models.

### `GET /api/config`

Returns a redacted runtime configuration posture summary for authenticated
operators. It includes gateway settings, rate-limit backend, identity mode,
authorization rule names/effects, guardrail rule/webhook names, provider counts,
route summaries, MCP target posture, and A2A capability flags. It does not
return bootstrap keys, provider credentials, target headers, webhook URLs,
provider base URLs, Redis URLs, or policy expressions.

### `GET /api/analytics/cost`

Aggregated spend from the request logs. Query parameters:

- `window`: `1h`, `6h`, `24h` (default), `7d`, or `30d`
- `groupBy`: `day` (default), `provider`, `model`, `api_key_id`, `session_id`,
  or `status`; any other value returns `400`

```json
{"groupBy": "model", "rows": [{"group": "gpt-4o-mini", "request_count": 12, "total_cost": 0.0042, "total_tokens": 9150, "avg_latency_ms": 640.5}]}
```

Admins and owners see all keys; other roles see only their own key's rows.

### `GET /api/traces/{trace_id}`

Returns everything recorded for one `x-omniswitch-trace-id`:
`{"trace_id", "logs", "guardrail_events", "shadow_logs", "feedback"}`.
Returns `404` when the trace does not exist or is not visible to the caller's
key (non-admin roles see only their own key's requests).

### `GET /api/configs`

Lists stored dynamic gateway configs as `{"configs": [...]}`. Disabled configs
are included only with `include_disabled=true`. With `id=<id>` or
`name=<name>`, returns a single enabled config (`404` if it is missing or
disabled). Requires the `admin` role, because config bodies can contain
secrets.

### `POST /api/configs`

Creates or replaces a dynamic config that requests can select with the
`x-omniswitch-config` header. Requires `admin`.

```json
{
  "id": "cfg_optional",
  "name": "cheap-routing",
  "description": "optional",
  "format": "yaml",
  "body": "routes:\n  gpt-4o:\n    provider: groq\n    model: llama-3.3-70b-versatile\n",
  "enabled": true
}
```

`name` and `body` are required. `body` uses the same schema as the gateway
config file (see [CONFIGURATION.md](CONFIGURATION.md)); `format` is `yaml`
(default) or `json`. The body is parsed before it is stored, and a parse
error returns `400`. An omitted `id` is generated; posting an existing `id`
replaces that record. Returns `201` with the stored record.

### `PATCH /api/configs?id=<id>` (or `?name=<name>`)

Body `{"enabled": true|false}` enables or disables a config. Requires `admin`.

### `DELETE /api/configs?id=<id>` (or `?name=<name>`)

Deletes a config and returns `{"deleted": true}`. Requires `admin`.

### `GET /api/config/raw` and `POST /api/config/raw`

Reads or replaces the gateway config file named by `OMNISWITCH_CONFIG`
(`404` when no config file is configured). `GET` returns
`{"path", "format", "body"}`. `POST` takes `{"body": "...", "format": "yaml"}`,
validates it, writes the file, and returns `{"saved": true, "path": ...}`; the
running gateway applies the reloadable parts (routes, guardrails, cache,
shaping limits, shadow provider) within a few seconds; providers, identity and
MCP targets need a restart. Both methods
require `admin`.

### `POST /api/virtual-keys`

Stores an encrypted provider credential and exposes it as a virtual provider on next gateway startup.

```json
{
  "name": "azure-prod",
  "provider_type": "custom",
  "provider_name": "azure-prod",
  "base_url": "https://YOUR_RESOURCE.openai.azure.com/openai/deployments/gpt-4o/chat/completions?api-version=2024-10-21",
  "provider_key": "real-provider-secret",
  "metadata": {
    "auth_header": "api-key",
    "models": "gpt-4o,gpt-4o-mini"
  }
}
```

The response redacts `provider_key`. Use `OMNISWITCH_VAULT_KEY` to keep stored credentials decryptable across restarts.

### `GET /api/virtual-keys`

Lists virtual provider keys without decrypted provider credentials.

### `POST /api/virtual-keys/rotate`

Rotates the encrypted provider credential for an existing virtual key.

```json
{
  "name": "azure-prod",
  "provider_key": "new-real-provider-secret"
}
```

### `DELETE /api/virtual-keys?name=<virtual_key_name>`

Revokes a virtual key without deleting its audit history.

### `GET /api/feedback`

Lists feedback entries.

Query parameters:

- `limit`
- `trace_id`
- `request_id`

### `POST /api/feedback`

Records human feedback.

```json
{
  "trace_id": "trace_123",
  "score": 1,
  "rating": "up",
  "comment": "Useful response",
  "user_id": "user_123",
  "metadata": {
    "screen": "chat"
  }
}
```

`score` must be `-1`, `0`, or `1`.

### `POST /api/keys`

Creates a OmniSwitch API key.

```json
{
  "name": "production-app",
  "workspace_id": "ws_123",
  "role": "admin",
  "rate_limit": 120,
  "budget_usd": 500,
  "monthly_cost_budget": 50,
  "monthly_token_budget": 1000000,
  "expires_in": "720h"
}
```

All limits are optional; zero or a missing field means no limit.

- `budget_usd` is a lifetime cap. It is compared with the key's total spend
  and never resets.
- `monthly_cost_budget` (USD) and `monthly_token_budget` are compared with
  the key's usage since the start of the current UTC month, so they reset
  every month. Usage counts every request that incurred upstream cost,
  including a response that a guardrail denied after generation. Cache hits
  do not count.
- `expires_in` is a Go duration such as `720h`. Day units such as `30d` are
  rejected.

A request over a budget gets HTTP 402 with a message that names the budget.

### `GET /api/keys`

Lists API keys.

### `DELETE /api/keys?id=<key_id>`

Disables an API key.

### `POST /api/orgs`

Creates an organization.

```json
{
  "name": "Acme",
  "metadata": {
    "tier": "enterprise"
  }
}
```

### `GET /api/orgs`

Lists organizations.

### `POST /api/workspaces`

Creates a workspace inside an organization.

```json
{
  "organization_id": "org_123",
  "name": "Production"
}
```

### `GET /api/workspaces`

Lists workspaces. Use `organization_id` to filter.

### `POST /api/users`

Creates or updates a user.

```json
{
  "email": "ada@example.com",
  "name": "Ada Lovelace"
}
```

### `GET /api/users`

Lists users.

### `POST /api/workspace-members`

Creates or updates a workspace membership.

```json
{
  "workspace_id": "ws_123",
  "user_id": "user_123",
  "role": "admin"
}
```

### `GET /api/workspace-members`

Lists workspace memberships. Use `workspace_id` to filter.

### `POST /api/prompts`

Creates a prompt template. Reusing an existing `name` creates the next version.

### `GET /api/prompts`

Lists prompt templates.

### `GET /api/prompts/versions?name=<prompt_name>`

Lists all versions for a prompt name, newest version first.

### `POST /api/prompts/render`

Renders a prompt template with variables.

### `POST /api/evals/policy`

Replays a batch of tool requests against one or more OmniSwitch policy files.

```json
{
  "policy_paths": ["policies/production-delete.yaml"],
  "requests": [
    {
      "agent": {"id": "coder", "department": "engineering"},
      "tool": {"name": "github"},
      "action": {"name": "delete"},
      "resource": {
        "type": "repo",
        "name": "payments",
        "environment": "production"
      }
    }
  ]
}
```

The response includes aggregate `allowed`, `denied`, and `errors` counts plus each decision trace.

## Moderation

### `POST /v1/moderations`

Provides an OpenAI-compatible moderation response using OmniSwitch's configured
local input guardrails. `input` accepts a string or an array of strings. Each
result includes `flagged`, `categories`, and `category_scores`; no request text
is sent to an external moderation provider.

## MCP Gateway

### `POST /mcp`

Evaluates MCP JSON-RPC `tools/call` requests through OmniSwitch policy and forwards allowed requests upstream. With `mcp.targets` configured, `tools/list` federates tools from each target under `target__tool` names and `tools/call` dispatches the prefixed name to its owning target. Target headers are configured server-side and OmniSwitch credentials are not forwarded upstream.

### `POST /v1/mcp/tools/call`

Alias for the MCP tool-call gateway.

The MCP gateway supports JSON-RPC over HTTP, including streaming
`text/event-stream` responses from streamable HTTP/SSE-capable upstreams.
Targets can opt into OIDC bearer-token delegation with
`forward_bearer_token: true`, or exchange the OIDC caller's token for a
target token with `token_exchange` (RFC 8693). Local OmniSwitch API keys are
never forwarded or exchanged. A call that exceeds the target `timeout` (default
2 minutes, measured to the upstream response headers) gets HTTP 502 with a
JSON-RPC `-32603` error.
It supports persistent stdio targets, HTTP and streamable HTTP, and targets
that set `openapi_spec` (see "OpenAPI-to-MCP conversion" in
[`CONFIGURATION.md`](CONFIGURATION.md)). A2A is exposed separately through
the `/.well-known/agent-card.json` and `/a2a` endpoints.

## Roles and Scopes

When `OMNISWITCH_AUTH=true`, API-key roles are `viewer`, `member`, `admin`, and
`owner` (`editor` is an alias for `member`). All valid keys can call inference
(`/v1/*`) and MCP (`/mcp`). For `/api/*` routes the gateway requires:

| Route | Read (`GET`, `HEAD`, `OPTIONS`) | Write (any other method) |
|---|---|---|
| `/api/health` | none | none |
| `/api/keys`, `/api/virtual-keys`, `/api/virtual-keys/rotate`, `/api/users`, `/api/workspace-members`, `/api/configs`, `/api/config/raw` | `admin` | `admin` |
| `/api/orgs`, `/api/workspaces` | `viewer` | `admin` |
| `/api/prompts`, `/api/prompts/versions`, `/api/prompts/render`, `/api/evals/policy` | `viewer` | `member` |
| `/api/feedback` | `viewer` | `viewer` |
| Any other `/api/*` route | `viewer` | `admin` |

Key, user, membership, and config routes need `admin` even to read, because
they expose credential metadata, user emails, or raw configuration that can
contain secrets. Admins and owners see global dashboard aggregates, while other
roles are limited to their key's logs and metrics.
