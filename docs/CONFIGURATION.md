# Configuration

Sentinel can be configured with environment variables or a declarative config file.

## Gateway Config

Set `SENTINEL_CONFIG` to a YAML or JSON file:

```bash
SENTINEL_CONFIG=examples/gateway-config.yaml go run ./cmd/gateway
```

Environment variables are applied after the config file and override file values.

## Example

```yaml
apiVersion: sentinel.dev/v1
kind: GatewayConfig

gateway:
  listen: ":8080"
  data_dir: ".sentinel"
  auth: false
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
      - provider: "@openai-prod"
        model: "@openai-prod/gpt-4o-mini"
        weight: 90
      - provider: anthropic
        model: claude-3-5-haiku-20241022
        weight: 10
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

## Environment Variables

- `SENTINEL_LISTEN`
- `SENTINEL_DATA`
- `SENTINEL_AUTH`
- `SENTINEL_CACHE_THRESHOLD`
- `SENTINEL_CACHE_TTL`
- `SENTINEL_SHADOW_PROVIDER`
- `SENTINEL_AB_TEST`
- `SENTINEL_MCP_ENABLED`
- `SENTINEL_MCP_POLICY`
- `SENTINEL_MCP_UPSTREAM`
- `OPENAI_API_KEY`
- `ANTHROPIC_API_KEY`
- `GOOGLE_API_KEY`
- `GROQ_API_KEY`
