# Roadmap

OmniSwitch's goal is to become a local-first, open-source control plane for AI gateways and agent systems.

OmniSwitch is currently in **public beta** (`v0.2.0-beta.x`). See "Beta scope and explicit deferrals" below for what is and isn't in scope before `v1.0.0`.

## Shipped

- Config hot-reload: routes, guardrails, cache posture, circuit breaker, shadow provider (no restart).
- Per-request trace waterfalls persisted with logs and rendered in the dashboard.
- Rust-accelerated guardrail scanning via embedded WASM, with pure-Go fallback.
- Release automation: goreleaser binaries, GHCR multi-arch images, Homebrew tap refresh.
- CI hardening: golangci-lint, race detector, coverage, CodeQL.
- OpenAPI-to-MCP conversion, exposing any OpenAPI-described service as MCP tools.
- A2A v1 task lifecycle (create, poll, cancel) with SSE streaming for task updates.
- Guardrail fallback reroute action and automatic retries for guardrail webhook calls.
- MCP OAuth token exchange (RFC 8693) with RFC 7523 JWT Bearer Grant, for delegating identity into federated MCP targets.
- Evals/experiments framework with dataset management and policy replay.
- PostgreSQL shared-state storage driver alongside SQLite.
- Helm chart for Kubernetes deployments (`deploy/helm/omniswitch`).

## Near Term

- Shared storage or external control-plane support for horizontally scaled key, log, budget, and config state (Postgres driver ships; multi-writer HA and migration tooling are still open).
- SAML/mTLS identity options on top of the current API key and OIDC/JWKS model.
- Additional native MCP OAuth provider integrations (Keycloak, Auth0) beyond generic RFC 8693 token exchange.
- Prompt rollback and playground workflows on top of the current prompt version API.
- Dashboard cost charts and eval report views.
- MCP spec conformance pass (stateless servers, `_meta` trace context propagation).

## Medium Term

- Model-quality simulations on top of the current evals/policy-replay framework.
- Visual policy and routing editor.
- A2A streaming push notifications and outbound agent calls (task lifecycle itself has shipped).
- Image, audio, batch, and file endpoints.
- Multi-writer, horizontally scaled Postgres control plane (single-writer Postgres has shipped).

## Long Term

- Agent identity registry.
- Multi-agent execution graph observability.
- Compliance evidence reports.
- Policy marketplace and signed policy bundles.

## Beta scope and explicit deferrals

The public beta (`v0.2.0-beta.x`) is scoped to make the OpenAI-compatible gateway, MCP tool proxy, and core guardrail/caching/routing features reliable to install and trust. The following are explicitly **deferred past the beta** and are not planned before `v1.0.0` unless called out otherwise:

- Image, TTS, and transcription HTTP endpoints.
- Full Responses API and Anthropic Messages tool-use parity (both currently support a documented subset; see `docs/KNOWN_LIMITATIONS.md`).
- A2A push notifications and an outbound A2A client (task lifecycle and SSE streaming have shipped).
- SAML, SCIM, and mTLS identity/transport options.
- A multi-writer, highly-available control plane (the current Postgres driver is single-writer/experimental; SQLite remains the default).
- A token-based (as opposed to request-count-based) rate limiter.
- A visual policy/config editor.
- An agent identity registry.
- An evals UI beyond the existing policy replay endpoint.
- A Kubernetes controller or CRDs (the Kustomize manifests and Helm chart remain the supported path).
- A public, versioned Go SDK (Python and Node SDKs exist; Go users currently call the HTTP API directly).
- Windows arm64 builds.
- A Kubernetes-based benchmark tier in the public benchmark harness.
- Publishing managed-service (hosted competitor) latency numbers in benchmarks.
- Artifact Hub listing, apt/rpm packages, Scoop/winget, and a Terraform module.
- GenAI OpenTelemetry semantic conventions (current tracer attributes are OmniSwitch-specific).
- A dedicated documentation-site framework with search.
- A formal contributor ladder and recurring community call.

Roadmap items above this section are not commitments. They are intended to help contributors find useful places to push. Feedback and voting on priorities happens in [GitHub Discussions → Ideas](https://github.com/omniswitch-dev/omniswitch/discussions/categories/ideas); see [`GOVERNANCE.md`](GOVERNANCE.md) for how roadmap decisions are actually made.
