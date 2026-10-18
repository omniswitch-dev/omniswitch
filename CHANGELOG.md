# Changelog

All notable changes to OmniSwitch will be documented in this file.

The format is based on Keep a Changelog, and this project follows semantic versioning once tagged releases begin.

## [Unreleased]

A hardening release: security fixes, verified bug fixes, new regression tests and safer defaults. Read "Breaking and behavior changes" before you upgrade.

### Breaking and behavior changes

- **Go 1.26.** A source build needs Go 1.26 or newer. Dependencies with known vulnerabilities are upgraded (pgx, gRPC, `golang.org/x/net`, `golang.org/x/text`, OpenTelemetry and others), and `govulncheck ./...` reports no vulnerabilities. The lint job uses golangci-lint v2, because v1 cannot type-check Go 1.26 code.
- **Loopback bind when auth is off.** When authentication is off and neither `OMNISWITCH_LISTEN` nor `gateway.listen` is set, the gateway listens on `127.0.0.1:8080`. An explicit non-loopback address with auth off logs a warning. Docker images set `OMNISWITCH_LISTEN=:8080`. The standalone `cmd/proxy` also defaults to `127.0.0.1:8080`.
- **`/metrics` follows gateway authentication.** Set the new `OMNISWITCH_METRICS_PUBLIC=true` to serve it without authentication.
- **Stricter roles.** Viewers can no longer read `/api/keys`, `/api/virtual-keys`, `/api/users`, `/api/workspace-members`, `/api/configs` or `/api/config/raw`. Organizations and workspaces need `viewer` to read and `admin` to write. Writes (any method other than `GET`, `HEAD` or `OPTIONS`) to other `/api/*` routes need `admin`, except `/api/feedback`, which every role can use. A caller cannot create or delete a key that ranks above its own role, and a workspace-scoped caller manages only the keys of its own workspace.
- **Control-plane limits.** `/api/*` request bodies are limited to 1 MiB (8 MiB for `/api/configs` and `/api/config/raw`); a larger body gets 413. `/api/prompts/render` accepts only `POST`. Prompt rendering is single-pass, with limits on output and variable size.
- **Client identity headers are removed.** The gateway deletes client-supplied `x-omniswitch-key-id`, `-rate-limit`, `-workspace-id`, `-organization-id`, `-role`, `-auth-method` and `-user-id` headers. `x-omniswitch-user-id` comes only from the OIDC subject.
- **OIDC.** Tokens without `exp` are rejected (30 seconds of clock leeway). An unknown `kid` refreshes the JWKS at most once per 30 seconds. EC keys are validated on their curve.
- **Strict environment parsing.** An invalid boolean, integer, number or duration stops startup and names the variable. Integer settings must be positive, except `OMNISWITCH_LOG_RETENTION_DAYS`, which accepts `0`.
- **Storage DSN validation.** `OMNISWITCH_DATABASE_URL` and `gateway.storage_url` must be `postgres://` or `postgresql://` URLs. Any other value, or `gateway.storage: postgres` without a DSN, stops startup. Before, the gateway silently used SQLite.
- **Policy replay files.** `POST /api/evals/policy` reads policies only from the new `OMNISWITCH_POLICY_DIR`. By default this is the directory of the MCP policy, but only when that directory is named `policies` (the default MCP policy gives `policies`). When the MCP policy is next to the gateway config or in the working directory, replay is off until you set `OMNISWITCH_POLICY_DIR`. Set it to `off` or `-` to turn replay off. Absolute paths, `..` segments and other extensions are rejected.
- **Bootstrap key.** `OMNISWITCH_BOOTSTRAP_API_KEY` no longer re-enables a disabled `bootstrap-admin` key or resets its spend. A changed secret rotates the stored hash. A missing bootstrap key is created only when no enabled owner or admin key exists.
- **Key budgets.** `budget_usd` is a lifetime cap, compared with the key's total spend. `monthly_cost_budget` and `monthly_token_budget` reset at the start of each UTC month. Before, key creation copied `monthly_cost_budget` into `budget_usd`, so a monthly budget also acted as a lifetime cap and never reset. A one-time data migration (recorded in the new `schema_migrations` table) clears those copied values.
- **Spend.** Key spend is charged whenever the upstream provider charged for the request, including a response that a guardrail denied after generation. Cache hits stay free.
- **Retries and the circuit breaker.** Without `retry_codes`, the router retries a provider only for 408, 409, 425, 429 and 5xx responses. Other 4xx errors go to the next fallback provider at once and do not open the circuit breaker. `Retry-After` is honored up to 10 seconds; a longer value moves the request to the next provider. In non-streaming chat, a non-retryable error now falls back to the next provider instead of failing the request. Client disconnects do not count as provider failures.
- **Streaming timeouts.** A route `timeout` on a streaming request limits the time to the first chunk, and after that it is an idle timeout between chunks. Long generations are no longer cut off. Provider streams no longer have an overall HTTP timeout.
- **Stream errors.** When an upstream stream fails, the gateway sends `data: {"error":{"message":"...","type":"upstream_stream_error"}}` and no `data: [DONE]`. `/v1/responses` sends `response.failed`, and `/v1/messages` sends `event: error`.
- **Stream usage.** The gateway asks every OpenAI-compatible upstream for stream usage (except Mistral and Azure On Your Data), so streamed requests are billed. The usage-only chunk goes to the client only when the client sets `stream_options.include_usage`.
- **Finish reasons** are normalized to OpenAI values. Refusals and safety blocks become `content_filter`, and unknown values become `stop`.
- **Guardrails.** When guardrail retries, reroutes or fallbacks are exhausted, the request fails with 403 `guardrail_triggered`. Redacted input is logged with the new `redacted` status. A config file with only `tool_rules`, `webhooks` or `llm_judges` is now enforced from startup; before, it took effect only after the first hot reload. A hot reload now removes the rules that were removed from the file.
- **Cache.** Semantic-cache keys include every request parameter other than the messages, and vectors are role-, order- and media-aware. Existing entries miss once after the upgrade.
- **A2A.** SSE frames are bare `data:` JSON-RPC responses with `status-update` and `artifact-update` results, without `event:` names. Tasks are visible only to the key and workspace that created them, and `CancelTask` stops the running inference.
- **MCP JSON-RPC.** Batches, duplicate or case-variant members, invalid ids and a wrong `jsonrpc` version return `-32600`. Invalid `tools/call` params return `-32602`. Malformed JSON returns `-32700` with HTTP 400. Tool arguments can no longer lower the inferred environment or action.
- **OpenAPI-to-MCP.** A `body` argument is sent as the whole JSON request body. Before, it was wrapped as `{"body": ...}`.
- **SDK environment variables.** `OMNISWITCH_CONFIG` and `OMNISWITCH_SHADOW_PROVIDER` are renamed to `OMNISWITCH_CLIENT_CONFIG` and `OMNISWITCH_CLIENT_SHADOW_PROVIDER`, so they no longer collide with the gateway's own variables. An explicit SDK argument now wins over headers and environment variables.
- **Providers.** Gemini API keys are sent in the `x-goog-api-key` header, not in the URL. A config-file provider account now wins over an environment-variable preset with the same name or type. Groq's non-streaming timeout is 120 seconds. Cohere rerank uses the v2 API and bills search units.
- **Catalog prices.** `gemini-2.5-flash` and `o1-mini` prices are corrected. `gpt-5-mini`, `gpt-5-nano`, `gpt-4.1-mini`, `gpt-4.1-nano` and `claude-3-7-sonnet-20250219` are added. The decommissioned Groq `mixtral-8x7b-32768` and `gemma2-9b-it` are removed. Dated snapshots, Bedrock inference profiles and `@alias/` names resolve to their family price. OpenAI image prices follow the published per-image table.
- **Deployment.** Docker Compose requires `OMNISWITCH_BOOTSTRAP_API_KEY` and restarts the gateway unless it is stopped. The Helm chart generates a bootstrap key when auth is on and none is given (`bootstrap.generate`, default `true`), reads OTLP headers from a Secret (`observability.otel.headersSecret`) and sets `config.gateway.listen` to `:8080`. `.env.example` no longer turns auth off or enables MCP.

### Added

- `OMNISWITCH_LOG_RETENTION_DAYS` deletes request logs, guardrail events and shadow requests older than the given number of days, once at startup and then every hour. The current UTC month is always kept. Expired semantic-cache entries are deleted every hour.
- Retries honor the upstream `Retry-After` header, and upstream errors keep their HTTP status (`provider.UpstreamError`).
- MCP policies can use the `target`, `session`, `metadata` and `args` CEL variables, `tool.full_name` and `action.operation`. MCP requests start an OpenTelemetry span and propagate W3C trace context. Audit events record the method, target, key, workspace, session and trace.
- `/v1/responses` supports function tools and typed input items. Embeddings and rerank honor the `x-omniswitch-config` header.
- Provider responses pass through `reasoning_content`, `refusal`, `logprobs`, token details, `system_fingerprint` and `service_tier`. Gemini thinking tokens are billed and reported as `reasoning_tokens`.
- The store tests also run against PostgreSQL when `OMNISWITCH_TEST_POSTGRES_DSN` is set.

### Fixed

- Bedrock Converse calls failed with 403 `SignatureDoesNotMatch` for versioned model IDs (which contain `:`) and inference-profile ARNs, because the SigV4 canonical URI was not encoded twice.
- Anthropic requests dropped `top_p` and `stop`, and sent `temperature` above 1. Anthropic, Gemini and Bedrock kept only the last system message.
- Gemini dropped assistant tool calls and tool results from the conversation, and ignored `stop`. Bedrock rejected conversations with two consecutive turns from the same role.
- SSE parsing ignored mid-stream error events and truncated streams, which then looked like a normal end.
- Concurrent SQLite writes failed at once with `SQLITE_BUSY`, so request logs and spend were lost under load. Writes now go through one connection with WAL mode and a busy timeout.
- PostgreSQL stored money and latency in 4-byte `REAL` columns, which lost precision. The columns are now `DOUBLE PRECISION`. Migrations run in one transaction under an advisory lock, so replicas can start at the same time.
- Timestamps were stored as variable-width text, so a monthly budget skipped the requests of the first second of the month and logs from the same second were out of order. They are now written in one fixed-width UTC layout; old rows stay readable.
- An exact-cache entry could not be stored again after it expired, so every repeat of that prompt was a paid provider call.
- Every restart reset the spend of the bootstrap key.
- Concurrent prompt creates for one name could get the same version and fail.
- Creating a user with an existing email returned a new ID that was never stored, so memberships created with it pointed at no user.
- The eval `datasets` and `experiments` tables did not exist, and experiments stopped when the HTTP request ended.
- `/a2a` ignored a hot-reloaded `max_request_bytes`.
- Every hot reload leaked the previous guardrail engine and its WASM scanners. The WASM guardrail module now compiles once per process in a shared runtime, and each engine uses a small pool of scanner instances.
- Guardrail rule indexes shifted when a pattern was not supported by the accelerator, so a match could be reported against the wrong rule.
- Invalid CEL route conditions failed silently on every request. They are now logged once at load and never match.
- Webhook and alert URLs (which can contain secrets) appeared in stored guardrail details and failure logs.
- The vault used a deterministic fallback master key when `crypto/rand` failed.
- Startup banner URLs were wrong for `host:port` listen addresses.
- `install.sh` used `sudo` even when the install directory was writable.

### Changed

- Deleted the outdated `comparison_iter5.md` and `comparison_iter7.md` notes. The 27 competitor claims that cited only them are now "Unknown" in `docs/comparison/COMPARISON.md` until someone checks them against vendor sources.
- Removed the dangling `website` gitlink. The site lives in the `omniswitch-dev/omniswitch-website` repository.

## [0.2.0-beta.1] - Unreleased

First public beta. Focused on making the install and first-run path trustworthy, filling in agent-protocol and storage gaps from 0.1.0, and standing up community/security scaffolding ahead of the public announcement.

### Added

- PostgreSQL shared-state storage driver, provider presets, MCP trace context propagation, and a Helm chart for Kubernetes deployments.
- OpenAPI-to-MCP conversion so any OpenAPI-described service can be exposed as MCP tools, a `bench` tool, and a proxy-overhead benchmark.
- A2A v1 task lifecycle (create, poll, cancel) with single-shot SSE for task updates (one status and artifact sequence per request; no token streaming or resubscribe).
- Guardrail fallback reroute action and automatic retries for guardrail webhook calls.
- RFC 8693 token exchange (with an RFC 7523 JWT Bearer option) in the MCP proxy library, for delegating identity into federated MCP targets. It is available to Go embedders through `TargetConfig.TokenExchange`; gateway configuration does not expose it yet.
- Storage and handlers for eval datasets and experiments, next to the existing policy replay endpoint. Only policy replay (`POST /api/evals/policy`) is mounted in the gateway; the experiment scorer is a placeholder.
- First-run reliability, CLI, and health-check fixes so a freshly downloaded binary starts with only `OPENAI_API_KEY` set: MCP is disabled by default, an embedded default policy is used as a fallback when MCP is enabled without an explicit policy path, `omniswitch version`/`validate-config`/`--help` subcommands, and `/healthz`/`/readyz` HTTP endpoints for liveness/readiness probes (tracked in the sibling first-run workstream; see `ROADMAP.md`).
- Community, governance, and security scaffolding: `SECURITY.md` with supported-versions table and private vulnerability reporting, `SUPPORT.md`, `GOVERNANCE.md`, `MAINTAINERS.md`, `CODEOWNERS`, `.github/dependabot.yml`, a beta-feedback issue form, and an install-verification GitHub Actions workflow.

### Fixed

- Resolved an `err` redeclaration bug in `ChatCompletions` that had broken the streaming response flow.

### Changed

- `gofmt` formatting cleanup across the eval, store, and gateway packages (no behavior change).

## [0.1.0] - 2026-08-24

### Added

- OpenAI-compatible AI gateway with streaming chat completions.
- Provider adapters for OpenAI, Anthropic, Google Gemini, and Groq.
- Generic OpenAI-compatible custom provider support for Azure OpenAI, Ollama, vLLM, LiteLLM, DeepSeek, Together, Fireworks, and similar endpoints.
- Provider catalog aliases such as `@openai-prod/gpt-4o-mini`.
- Encrypted provider credential vault with virtual providers, rotation, and revoke workflows.
- Exact and semantic caching backed by SQLite.
- Config-as-code with `omniswitch.dev/v1` `GatewayConfig`.
- OpenTelemetry trace export for gateway and provider spans.
- Circuit breakers, retries, fallbacks, A/B routing, and shadow routing.
- Cost and token budgets for API keys.
- Feedback API tied to `trace_id` and `request_id`.
- Raw request/response bodies in capped observability logs.
- Organization, workspace, user, membership, role, and workspace-scoped API key foundations.
- Prompt version history for immutable prompt revisions.
- Policy replay eval endpoint for batch allow/deny simulation.
- OpenAI-style multimodal message content compatibility.
- OpenAI-compatible Responses, Anthropic Messages, embeddings, rerank, and local moderation compatibility endpoints.
- MCP policy proxy mounted in the gateway.
- Federated MCP targets with streamed HTTP/SSE pass-through, persistent stdio transport, server-side target headers, and explicit OIDC bearer delegation.
- A2A Agent Card discovery and authenticated JSON-RPC direct `SendMessage`.
- Redis-backed shared request rate limits for multi-replica deployments.
- OIDC/JWKS workload identity and CEL allow/deny authorization rules.
- External guardrail webhook connectors.
- Redacted `/api/config` runtime posture endpoint for operators.
- Kubernetes Kustomize manifests for the gateway, Redis, config, secrets, service, and persistent volumes.
- CEL-backed policy engine and Git-like policy CLI.
- Local dashboard for logs, metrics, prompts, providers, and guardrails.
- Python and Node.js SDK wrappers for the OpenAI-compatible gateway.

### Security

- Apache-2.0 license, security policy, and secret-handling guidance.
