# OmniSwitch -- feature-parity decisions for the harness

- **Provider/model reference syntax (execution-validated, 2026-09-11).**
  Route variants must reference `type: custom` providers by their plain
  name (`provider: bench-mock`, `model: bench-model`), not the
  `"@provider"` / `"@provider/model"` alias syntax used in
  `examples/gateway-config.yaml` for built-in provider types
  (openai/anthropic/...). Reading `cmd/gateway/main.go`'s
  `registerProviderAccount`: standard provider accounts get wrapped in a
  `provider.NewAlias` (registered under `"@name"`), but the `type: custom`
  branch registers the raw `provider.NewCustom` under its plain name only
  and returns early -- no alias is created. Using the `@`-prefixed syntax
  for a custom provider produces `no provider found for model
  "@bench-mock/bench-model"` even though the provider registered
  successfully at startup. Confirmed by running the mock upstream +
  a binary built from this worktree locally and fixing forward until a
  request round-tripped; see bench/README.md "Harness self-test".
- **Auth key check.** `production-default.yaml` sets `gateway.auth: true`,
  which requires `OMNISWITCH_BOOTSTRAP_API_KEY` at startup and a `Bearer`
  key on every request. `forwarding.yaml` disables auth entirely, matching
  every competitor's minimal cell.
- **Logging.** `log_payloads: false` in both cells. This is the project's
  own documented safe default (see the comment in
  `examples/gateway-config.yaml`), so "production-default" here does not
  mean "log every request body" -- it means "the config OmniSwitch's own
  docs recommend for production." Per-request SQLite metadata logging
  (timestamps, status, latency, token counts) still happens even with
  `log_payloads: false`; this is a real, measurable cost and is exactly
  what plan risk item "OmniSwitch looks worse in production-default cell
  because of per-request SQLite writes" (see the benchmark plan) expects.
  Publish it rather than tuning it away.
- **Retries/timeouts.** `timeout: 10s`, `max_retries: 2`,
  `retry_backoff: 500ms`, `retry_codes: [429,500,502,503,504]` in the
  production cell, matching the parity matrix in `bench/README.md`.
- **Cache.** Not enabled by default in either cell (`cache_scope` is set
  but no cache TTL is configured, so nothing is cached). Scenario S6
  overlays a cache-specific config with `cache_ttl` and, for the
  semantic-cache cell, `cache_threshold: 0.95` (the project's own documented
  default). **State precisely:** OmniSwitch's "semantic" cache is
  normalized token-frequency cosine similarity (`internal/cache/cache.go`),
  not an embedding-based cache. Do not describe it as "vector similarity"
  in any report generated from this harness.
- **Guardrails.** Not enabled in either baseline cell (no measurable
  overhead should be attributed to a feature that is off). Scenario S8
  (conformance) and a dedicated guardrail-overhead cell turn on
  `guardrails.rules` and both `stream_buffer: true` and `stream_buffer:
  false`, reported separately, because `stream_buffer: true` (the
  project's default) buffers the entire SSE stream before releasing it,
  which dominates TTFT under guardrails.
- **Worker/process model.** OmniSwitch is a single Go binary; there is no
  worker-count knob to match (unlike LiteLLM's `--num_workers`). Note this
  explicitly in any report that compares worker-tunable competitors.
- **No published container image.** See `pin.yaml`: the harness builds from
  this worktree's `Dockerfile` at the pinned commit rather than pulling a
  tagged release, because none is published yet. Do not compare "OmniSwitch
  latest release" numbers to competitors' tagged releases until that gap
  closes; label the commit SHA in every report.
- **Not yet execution-validated end-to-end against a running mock upstream
  and full container stack in this environment.** B4 in this change runs a
  local smoke test (binary + mock upstream on loopback, no Docker) as a
  self-test of the harness wiring; it does not exercise
  `bench/docker-compose.yml` or these exact config files end-to-end. That
  is the next actionable step before any published numbers.
