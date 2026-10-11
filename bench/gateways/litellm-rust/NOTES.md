# LiteLLM Rust gateway (beta) -- feature-parity decisions for the harness

- **Not a separate container.** Same image/digest as `bench/gateways/litellm`
  -- the Rust path is an opt-in `rust: true` field per model inside the same
  proxy process. Both entries run the identical image; only the model
  config differs. Report resource usage for both cells so a reader can see
  the marginal cost/benefit of enabling the Rust path within one binary.
- **Scenario scope is narrower than every other gateway.** Only Anthropic
  Messages-shaped payloads exercise the Rust path at pin date (see
  `pin.yaml`). Chat-completions and embeddings cells are `n/a` for this
  entry, not silently run on the Python fallback path -- running them on
  the fallback path and reporting "litellm-rust: X ms" would misattribute
  Python-path latency to the Rust feature. This is exactly the kind of
  substitution the anti-bias checklist in `bench/README.md` prohibits.
- **Router features (fallbacks, weighted routing, circuit breaker) are not
  yet on the Rust path** per LiteLLM's own launch blog roadmap, so those
  scenarios (S5 reliability) are also `n/a` for this entry until re-checked
  against a later LiteLLM release.
- **Not yet execution-validated.** Whether the mock upstream's
  `/v1/messages` responses are actually served through the Rust path
  (verifiable via the documented `x-litellm-rust: true` response header)
  has not been checked in this environment. That header check must be part
  of the S8 conformance probe before any litellm-rust number is published,
  otherwise a silent Python-path fallback could be reported as a Rust-path
  result.
