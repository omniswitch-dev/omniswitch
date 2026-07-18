# OmniSwitch Architecture

OmniSwitch is a local policy enforcement layer for AI tool calls. Protocol adapters convert external payloads into the canonical `ToolRequest`; the policy engine evaluates that model with CEL and returns an explainable decision.

The MCP adapter is intentionally thin: it parses JSON-RPC, maps `tools/call` into `ToolRequest`, and formats MCP-compatible denial responses. It does not contain authorization policy.

The proxy evaluates every request, writes a JSON audit event to stdout or the
gateway audit store, forwards allowed requests to the configured MCP upstream,
and returns a structured denial for blocked requests. Gateway MCP targets can
federate HTTP, streamable HTTP/SSE, and stdio servers behind one policy surface.

Local defaults:

```bash
go run ./cmd/proxy
```

The proxy listens on `:8080`, loads `policies/production-delete.yaml`, and forwards allowed calls to `http://127.0.0.1:8090/mcp`. Override those with `OMNISWITCH_LISTEN`, `OMNISWITCH_POLICY`, and `OMNISWITCH_UPSTREAM`.

OmniSwitch policy and decision trace documents use `apiVersion: omniswitch.dev/v1`. A decision trace is the portable artifact for time travel: it stores the request, policy reference, result, evaluation latency, and per-rule match graph.

Useful commands:

```bash
go run ./cmd/omniswitch validate policies/production-delete.yaml
go run ./cmd/omniswitch trace policies/production-delete.yaml examples/requests/delete-prod.json
go run ./cmd/omniswitch replay trace.yaml
go run ./cmd/omniswitch diff before.yaml after.yaml
```

The `cmd/gateway` binary combines the policy standard with an OpenAI-compatible AI gateway. It adds exact and semantic caching, SSE streaming, trace/session IDs, persisted per-key spend budgets, circuit breakers, weighted A/B routing, shadow routing, OpenAI-compatible moderation, A2A discovery/direct messaging, and an embedded MCP policy gateway at `/mcp` and `/v1/mcp/tools/call`.

Gateway runtime behavior can be configured with `OMNISWITCH_CONFIG`, which loads a `omniswitch.dev/v1` `GatewayConfig` YAML or JSON document. The config file can define listen/data settings, cache settings, rate limits, OIDC identity, authorization rules, guardrail webhooks, MCP targets, fallback routes, retry counts, and weighted provider/model variants. Explicit environment variables are applied after the file, so deployments can override checked-in defaults without editing the config artifact.

Provider accounts in `GatewayConfig` create virtual provider aliases such as `@openai-prod`. The alias wraps a concrete provider adapter, keeps secrets in environment variables, and exposes virtual model IDs like `@openai-prod/gpt-4o-mini` through `/v1/models` and `/api/providers`.

The gateway accepts OpenAI-compatible multimodal message content arrays. Text parts are extracted for guardrails, cache keys, and text-only provider adapters, while OpenAI-compatible providers receive the original content parts.

Human feedback can be posted to `/api/feedback` with a `trace_id` or `request_id`. This gives application UIs a simple quality loop without changing the chat-completions contract.

Gateway request logs persist capped raw request and response bodies alongside cost, latency, cache status, trace IDs, and session IDs. This supports incident review and later replay without requiring a separate logging pipeline.

The management plane includes organizations, workspaces, users, workspace memberships, roles, and workspace-scoped API keys. OIDC/JWKS workload identity and CEL authorization rules extend that foundation to external identity providers and route/model-level policy enforcement. SAML, mTLS, and a shared database control plane remain roadmap items.

Prompt records are immutable by version. Creating a prompt with an existing name creates the next version, and `/api/prompts/versions` returns version history for review or rollback workflows.

Policy replay evals live at `/api/evals/policy`. They load one or more OmniSwitch policy files and evaluate a batch of canonical `ToolRequest` objects, returning aggregate allow/deny counts and per-request decision traces.
