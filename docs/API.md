# API Reference

OmniSwitch exposes OpenAI-compatible inference endpoints and OmniSwitch management endpoints.

## OpenAI-Compatible Gateway

### `POST /v1/chat/completions`

Accepts OpenAI-style chat completion requests.

Supported request features:

- `model`
- `messages`
- `temperature`
- `max_tokens`
- `top_p`
- `stream`
- `stop`
- OpenAI-style content arrays with text and image URL parts

Headers:

- `Authorization: Bearer <omniswitch-api-key>` when `OMNISWITCH_AUTH=true`
- `x-omniswitch-provider`: force a provider or provider alias
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

### `GET /v1/models`

Returns all models registered through provider adapters and provider catalog aliases.

## Management API

### `GET /api/health`

Returns health information.

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
  "monthly_cost_budget": 50,
  "monthly_token_budget": 1000000
}
```

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

## MCP Gateway

### `POST /mcp`

Evaluates MCP JSON-RPC `tools/call` requests through OmniSwitch policy and forwards allowed requests upstream. With `mcp.targets` configured, `tools/list` federates tools from each target under `target__tool` names and `tools/call` dispatches the prefixed name to its owning target. Target headers are configured server-side and OmniSwitch credentials are not forwarded upstream.

### `POST /v1/mcp/tools/call`

Alias for the MCP tool-call gateway.

The current MCP implementation is HTTP JSON-RPC federation and policy control.
It does not yet implement stdio, SSE/streamable HTTP, OAuth delegation, or A2A.

## Roles and Scopes

When `OMNISWITCH_AUTH=true`, API-key roles are `viewer`, `member`, `admin`, and
`owner`. All valid keys can call inference and MCP. Viewers can read dashboard
data (scoped to their own key); members can change prompts and run policy evals;
admins/owners can mutate keys, virtual keys, and organization/workspace data.
Admins and owners see global dashboard aggregates, while other roles are
limited to their key's logs and metrics.
