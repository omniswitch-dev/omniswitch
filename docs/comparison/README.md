# Comparison claims register

This directory is the single source of truth for every capability claim
OmniSwitch makes about itself or a competitor in benchmark and marketing
material.

## Files

- `claims.yaml` -- hand-authored data. One row per `(gateway, capability)`
  pair. Fields per row:
  - `capability` / `gateway`: slugs matching the `capabilities` / `gateways`
    lists at the top of the file.
  - `status`: `yes` (confirmed present), `partial` (present with a
    meaningful gap or scope limit, explained in `detail`), `no` (confirmed
    absent), or `unknown` (not independently verified in available
    sources). `unknown` is never rendered or read as "no" -- it means we
    could not confirm it, not that the feature is absent.
  - `detail`: a precise, falsifiable description. State mechanism, not just
    a label -- e.g. OmniSwitch's "semantic cache" row must say it is
    normalized token-frequency cosine similarity, not an embedding-based
    cache, every time it is described.
  - `source_url`: a URL to vendor docs/blog/repo, or a path into this
    repository (e.g. `internal/cache/cache.go`) when the claim was verified
    by reading OmniSwitch's own code. Never leave a `yes`/`no`/`partial` row
    without a source; use `unknown` instead of guessing.
  - `verified_on`: ISO date the row was last checked. Re-verify (and bump
    this date) whenever the corresponding gateway ships a release that
    could plausibly change the row.
  - `version`: the pinned version/image/digest the claim was checked
    against (see `bench/gateways/<name>/pin.yaml` for the harness gateways).
- `COMPARISON.md` -- **generated**. Rendered from `claims.yaml` by
  `bench/tools/render-comparison`. Do not hand-edit; regenerate and commit
  both files together.

## Review process

1. Any change to a competitor's row must cite a primary source (vendor
   docs, changelog, or the vendor's own repository/LICENSE file) in
   `source_url`. This project's own prior comparison docs
   (`docs/PORTKEY_COMPARISON.md`, `comparison_iter5.md`, `comparison_iter7.md`)
   are acceptable as a starting point but are marked as such in `detail` and
   should be independently re-verified before a beta launch depends on them
   -- those documents have already been found to disagree with each other
   (for example, mTLS) and with the live website in prior research for this
   project.
2. Any change to an OmniSwitch row must cite the source file/test that was
   read, not just another doc. Prefer citing code over docs when they
   disagree.
3. Before publishing benchmark results that depend on a competitor row,
   open a tracking issue with that vendor's config/claims and give
   maintainers 5 business days to comment (see `bench/README.md`
   anti-bias checklist). Record their response (or lack of one) in the
   row's `detail`.
4. After editing `claims.yaml`, run:

   ```bash
   python3 bench/tools/render-comparison
   ```

   and commit the updated `COMPARISON.md` in the same change. CI/reviewers
   can verify the two files agree with:

   ```bash
   python3 bench/tools/render-comparison --check
   ```
5. Never delete a row to make a comparison look better. If a capability
   cannot be verified, set `status: unknown` and say why in `detail`.

## Scope

`claims.yaml` currently covers the 10 gateways in the benchmark harness
(`bench/gateways/`) plus the 6 managed-tier services listed in
`bench/README.md`. Managed-tier rows are capability-map only: they are
never run through the same-host latency harness because they are not
self-hostable.
