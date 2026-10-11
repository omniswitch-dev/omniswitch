# Envoy AI Gateway (now "Agent Router") -- deferral rationale

- **No `forwarding.yaml` / `production-default.yaml` in this pass.** This
  gateway is `tier: harness-deferred` in `docs/comparison/claims.yaml`.
  The loopback harness used by every other gateway in `bench/gateways/`
  assumes a single container process reachable on a fixed port; Envoy AI
  Gateway's primary deployment model is Kubernetes-native (a controller
  reconciling `AIGatewayRoute`/`AIServiceBackend`-style CRDs plus an
  Envoy + extproc data plane), which does not fit that model without
  either standing up a `kind` cluster (plan's optional S11) or verifying
  that the `aigw run` standalone CLI mode is a genuine single-process
  equivalent (not independently confirmed in this session).
- **Rename.** The upstream project renamed to "Agent Router" in 2026; see
  `pin.yaml` `rename_caveat` for the verification trail (HTTP 301 +
  GitHub API redirect target). All artifact/image names, CLI name
  (`aigw`), and license are stated by the project to be unchanged.
- **What a human needs to decide.** Per plan section 8's risk table and
  section 10's decision points: (a) whether to invest in the `kind`-based
  K8s tier (S11, optional/time-permits) before the beta, or (b) drop this
  gateway from the published beta comparison with an explicit note like
  the one in this file. This session did not make that call --
  `harness-deferred` in claims.yaml reflects "not yet decided / not
  measured", not a decision to exclude permanently.
- Three image digests (cli, controller, extproc) are pinned in `pin.yaml`
  so that whichever path is chosen later starts from a reproducible base
  rather than re-resolving `:latest` at that time.
