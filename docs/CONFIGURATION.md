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

observability:
  otel_enabled: true
  otlp_endpoint: http://localhost:4318/v1/traces
  service_name: sentinel-gateway
  insecure: true
  timeout: 10s

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

Sentinel can export gateway and provider spans to any OTLP-compatible backend.

```yaml
observability:
  otel_enabled: true
  otlp_endpoint: http://localhost:4318/v1/traces
  service_name: sentinel-gateway
  headers:
    x-api-key: observability-secret
```

Setting `SENTINEL_OTEL_ENDPOINT` also enables tracing.

## Provider Vault

Set `SENTINEL_VAULT_KEY` before creating virtual keys so encrypted provider credentials remain decryptable after restart.

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

- `SENTINEL_LISTEN`
- `SENTINEL_DATA`
- `SENTINEL_AUTH`
- `SENTINEL_CACHE_THRESHOLD`
- `SENTINEL_CACHE_TTL`
- `SENTINEL_SHADOW_PROVIDER`
- `SENTINEL_AB_TEST`
- `SENTINEL_OTEL_ENABLED`
- `SENTINEL_OTEL_ENDPOINT`
- `SENTINEL_OTEL_SERVICE_NAME`
- `SENTINEL_OTEL_HEADERS`
- `SENTINEL_OTEL_INSECURE`
- `SENTINEL_OTEL_TIMEOUT`
- `SENTINEL_VAULT_KEY`
- `SENTINEL_MCP_ENABLED`
- `SENTINEL_MCP_POLICY`
- `SENTINEL_MCP_UPSTREAM`
- `OPENAI_API_KEY`
- `ANTHROPIC_API_KEY`
- `GOOGLE_API_KEY`
- `GROQ_API_KEY`
