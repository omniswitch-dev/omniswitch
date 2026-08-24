# Roadmap

OmniSwitch's goal is to become a local-first, open-source control plane for AI gateways and agent systems.

## Shipped

- Config hot-reload: routes, guardrails, cache posture, circuit breaker, shadow provider (no restart).
- Per-request trace waterfalls persisted with logs and rendered in the dashboard.
- Rust-accelerated guardrail scanning via embedded WASM, with pure-Go fallback.
- Release automation: goreleaser binaries, GHCR multi-arch images, Homebrew tap refresh.
- CI hardening: golangci-lint, race detector, coverage, CodeQL.

## Near Term

- Shared storage or external control-plane support for horizontally scaled key, log, budget, and config state.
- SAML/mTLS identity options on top of the current API key and OIDC/JWKS model.
- OpenAPI-to-MCP conversion and richer MCP OAuth client flows.
- Prompt rollback and playground workflows on top of the current prompt version API.
- Guardrail retry, reroute, and fallback actions after policy violations.
- Dashboard cost charts and eval report views.
- MCP spec conformance pass (stateless servers, `_meta` trace context propagation).
- One native MCP OAuth provider integration (Keycloak or Auth0).

## Medium Term

- Evaluation datasets and model-quality simulations on top of the current policy replay endpoint.
- Visual policy and routing editor.
- Fuller A2A task lifecycle, streaming, push notifications, and outbound agent calls.
- Image, audio, batch, and file endpoints.
- Postgres storage driver option alongside SQLite.

## Long Term

- Agent identity registry.
- Multi-agent execution graph observability.
- Compliance evidence reports.
- Policy marketplace and signed policy bundles.

Roadmap items are not commitments. They are intended to help contributors find useful places to push.
