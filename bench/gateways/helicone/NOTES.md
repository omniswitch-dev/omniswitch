# Helicone AI Gateway -- feature-parity decisions and caveats

- **License caveat (important, verify before publishing).** GitHub
  repository metadata for `Helicone/ai-gateway` reports **GPL-3.0**.
  Multiple third-party blog posts/comparison articles describe it as
  Apache-2.0 or MIT -- those are wrong, or conflating it with a different
  Helicone package. `docs/comparison/claims.yaml` records GPL-3.0 with
  the GitHub API as the source. Re-verify the LICENSE file content
  directly (not just the API metadata) before publishing.
- **Repo staleness caveat (important, verify before publishing).** GitHub
  API `pushed_at` for this repo was 2025-11-21 at research time, roughly
  ten months before this pin (2026-09-11). This is a signal, not proof,
  of reduced maintenance -- re-check commit activity immediately before
  the first measured run and flag in the published report if it hasn't
  moved.
- **Config format is YAML router config** (`routers.<name>.providers.<id>.
  base-url` + `load-balance.chat`), per
  https://docs.helicone.ai/gateway/overview. The exact field name for a
  fully custom/OpenAI-compatible provider override (`base-url` under a
  named provider block) was pieced together from documentation examples
  and blog posts, not from a schema reference or a live container in this
  session. **Action before the first measured run:** run the container
  with `--validate`/`-h` (or consult the JSON schema if one is published)
  to confirm this key name, and fix `forwarding.yaml` /
  `production-default.yaml` if it differs.
- **Cache** is exercised only in the S6 overlay cell (`cache-store` +
  `global.cache.directive`, as shown in Helicone's own example), not in
  either baseline cell -- matching the pattern used for the other nine
  gateways.
- **Auth/retry.** No gateway-level API-key enforcement or retry field was
  confirmed for the production-default cell; `n/a` in the results matrix
  until verified against a running container.
- **Not yet execution-validated** against a live `helicone/ai-gateway`
  container with the mock upstream in this environment.
