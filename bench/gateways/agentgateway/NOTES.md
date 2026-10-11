# agentgateway (Solo.io) -- feature-parity decisions for the harness

- **Caching disabled.** Per `docs/comparison/claims.yaml`, agentgateway's
  own materials do not advertise a semantic/exact cache as a primary
  surface (a prior claim on this project's own homepage that agentgateway
  has semantic caching was found unsupported). S6 (cache scenario) is
  therefore `n/a` for agentgateway unless a caching mechanism is confirmed
  before that scenario runs; do not invent a cache config to force a
  comparison cell to populate.
- **Auth/RBAC.** Production cell uses agentgateway's documented key-auth
  policy plus a permissive CEL rule (`"true"`), matching "auth on, no extra
  restrictions" across every gateway's production cell. CEL RBAC is a
  flagship agentgateway feature (per `docs/PORTKEY_COMPARISON.md`'s
  competitor column); S8 conformance should add a real CEL condition to
  verify enforcement, not just presence.
- **Retry/circuit-breaker field names unverified.** The
  `policies.retry.*` fields in `production-default.yaml` are a best-effort
  match to the harness's 2-retry/10s-timeout/500ms-backoff/429-504 parity
  rule, adapted from Envoy/xDS-style retry policy conventions that
  agentgateway's Rust proxy is known to follow, but were not checked
  against the live `agentgateway.dev/schema/config` JSON schema in this
  session. **Action before the first measured run:** fetch the schema (or
  run agentgateway with `--validate` / a syntax-check dry run) and correct
  field names if they differ.
- **MCP/A2A features are out of scope for the latency scenarios.**
  agentgateway's flagship MCP federation, OpenAPI-to-MCP, and A2A features
  are exercised only by S8 conformance probes against a separate MCP/A2A
  config, not the chat-completions latency config above -- mixing them in
  would not be apples-to-apples with gateways that lack those features.
- **Not yet execution-validated.** These configs have not been run against
  a live agentgateway container with the mock upstream in this environment.
