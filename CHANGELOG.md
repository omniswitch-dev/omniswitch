# Changelog

All notable changes to OmniSwitch will be documented in this file.

The format is based on Keep a Changelog, and this project follows semantic versioning once tagged releases begin.

## [Unreleased]

Nothing yet.

## [0.2.0-beta.1] - Unreleased

First public beta. Focused on making the install and first-run path trustworthy, filling in agent-protocol and storage gaps from 0.1.0, and standing up community/security scaffolding ahead of the public announcement.

### Added

- PostgreSQL shared-state storage driver, provider presets, MCP trace context propagation, and a Helm chart for Kubernetes deployments.
- OpenAPI-to-MCP conversion so any OpenAPI-described service can be exposed as MCP tools, a `bench` tool, and a proxy-overhead benchmark.
- A2A v1 task lifecycle (create, poll, cancel) with SSE streaming for task updates.
- Guardrail fallback reroute action and automatic retries for guardrail webhook calls.
- MCP OAuth token exchange (RFC 8693) with RFC 7523 JWT Bearer Grant support, for delegating identity into federated MCP targets.
- Evals/experiments framework with dataset management on top of the existing policy replay endpoint.
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
