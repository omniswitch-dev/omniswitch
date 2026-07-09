# API Reference

Sentinel exposes OpenAI-compatible inference endpoints and Sentinel management endpoints.

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

- `Authorization: Bearer <sentinel-api-key>` when `SENTINEL_AUTH=true`
- `x-sentinel-provider`: force a provider or provider alias
- `x-sentinel-shadow-provider`: run a secondary provider asynchronously
- `x-sentinel-trace-id`: group calls into a trace
- `x-sentinel-session-id`: group calls into an agent session

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

### `GET /api/metrics`

Returns gateway metrics.

Query parameters:

- `window`: `1h`, `6h`, `7d`, or `30d`

### `GET /api/metrics/providers`

Returns per-provider aggregate metrics.

### `GET /api/providers`

Returns registered provider names and exposed models.

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

Creates a Sentinel API key.

### `GET /api/keys`

Lists API keys.

### `DELETE /api/keys?id=<key_id>`

Disables an API key.

### `POST /api/prompts`

Creates a prompt template.

### `GET /api/prompts`

Lists prompt templates.

### `POST /api/prompts/render`

Renders a prompt template with variables.

## MCP Gateway

### `POST /mcp`

Evaluates MCP JSON-RPC `tools/call` requests through Sentinel policy and forwards allowed requests upstream.

### `POST /v1/mcp/tools/call`

Alias for the MCP tool-call gateway.
