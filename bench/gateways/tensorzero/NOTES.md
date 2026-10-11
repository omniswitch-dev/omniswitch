# TensorZero -- feature-parity decisions and caveats

- **Maintenance status caveat (critical, re-check before publishing).**
  TensorZero's own website/project statement indicated at research time
  that the project is no longer actively maintained. Any published
  comparison including TensorZero must surface this next to its numbers.
  Re-verify immediately before the first measured run.
- **Custom OpenAI-compatible endpoint.** `type = "openai"` +
  `api_base` override, confirmed via
  https://www.tensorzero.com/docs/gateway/configuration-reference/ and
  https://www.tensorzero.com/docs/gateway/guides/providers/openai. Trailing
  slash on `api_base` matches TensorZero's documented default
  (`https://api.openai.com/v1/`).
- **Retry.** `providers.<name>.retries.{num_retries,max_delay_s}` in the
  production-default cell is a best-effort match for the harness's
  2-retry parity rule; TensorZero's retry table does not appear to
  support the harness's per-status-code list (429/500/502/503/504)
  the way OmniSwitch's `retry_codes` does -- treat status-code-specific
  parity as `n/a`, not matched.
- **Observability disabled in latency scenarios.** ClickHouse store is
  off in both baseline cells to match TensorZero's own published
  benchmark methodology; this is a deliberate anti-bias decision (see
  pin.yaml `observability_note`), not an oversight. TensorZero's
  observability capability is instead exercised only in the S8
  conformance pass, run separately with ClickHouse enabled.
- **Auth.** No documented gateway-level API-key enforcement was found for
  TensorZero at research time; production-default's "auth on" cell is
  `n/a` for TensorZero rather than silently claiming parity.
- **Not yet execution-validated** against a live `tensorzero/gateway`
  container with the mock upstream in this environment.
