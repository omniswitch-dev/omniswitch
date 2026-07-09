# Sentinel Architecture

Sentinel is a local policy enforcement layer for AI tool calls. Protocol adapters convert external payloads into the canonical `ToolRequest`; the policy engine evaluates that model with CEL and returns an explainable decision.

The MCP adapter is intentionally thin: it parses JSON-RPC, maps `tools/call` into `ToolRequest`, and formats MCP-compatible denial responses. It does not contain authorization policy.

The proxy evaluates every request, writes a JSON audit event to stdout, forwards allowed requests to the configured MCP upstream, and returns a structured denial for blocked requests.

Local defaults:

```bash
go run ./cmd/proxy
```

The proxy listens on `:8080`, loads `policies/production-delete.yaml`, and forwards allowed calls to `http://127.0.0.1:8090/mcp`. Override those with `SENTINEL_LISTEN`, `SENTINEL_POLICY`, and `SENTINEL_UPSTREAM`.

Sentinel policy and decision trace documents use `apiVersion: sentinel.dev/v1`. A decision trace is the portable artifact for time travel: it stores the request, policy reference, result, evaluation latency, and per-rule match graph.

Useful commands:

```bash
go run ./cmd/sentinel validate policies/production-delete.yaml
go run ./cmd/sentinel trace policies/production-delete.yaml examples/requests/delete-prod.json
go run ./cmd/sentinel replay trace.yaml
go run ./cmd/sentinel diff before.yaml after.yaml
```

The `cmd/gateway` binary combines the policy standard with an OpenAI-compatible AI gateway. It adds exact and semantic caching, SSE streaming, trace/session IDs, persisted per-key spend budgets, circuit breakers, weighted A/B routing, shadow routing, and an embedded MCP policy gateway at `/mcp` and `/v1/mcp/tools/call`.

Gateway runtime behavior can be configured with `SENTINEL_CONFIG`, which loads a `sentinel.dev/v1` `GatewayConfig` YAML or JSON document. The config file can define listen/data settings, cache settings, MCP policy/upstream wiring, fallback routes, retry counts, and weighted provider/model variants. Explicit environment variables are applied after the file, so deployments can override checked-in defaults without editing the config artifact.

Provider accounts in `GatewayConfig` create virtual provider aliases such as `@openai-prod`. The alias wraps a concrete provider adapter, keeps secrets in environment variables, and exposes virtual model IDs like `@openai-prod/gpt-4o-mini` through `/v1/models` and `/api/providers`.

The gateway accepts OpenAI-compatible multimodal message content arrays. Text parts are extracted for guardrails, cache keys, and text-only provider adapters, while OpenAI-compatible providers receive the original content parts.

Human feedback can be posted to `/api/feedback` with a `trace_id` or `request_id`. This gives application UIs a simple quality loop without changing the chat-completions contract.
