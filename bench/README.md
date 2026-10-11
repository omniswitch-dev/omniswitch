# OmniSwitch competitive benchmark harness

This directory is the reproducible harness for OmniSwitch's competitive
gateway benchmarks (public beta plan items B1-B9; full design in
`/code/.plans/omniswitch-competitive-benchmark-plan.md`, sections 2.3, 3,
and 4). It measures **gateway overhead against a deterministic local mock**,
not end-to-end model latency, and publishes raw artifacts alongside every
number.

Related, adjacent docs:
- `docs/comparison/claims.yaml` + `docs/comparison/COMPARISON.md` — the
  capability claims register this harness's S8 conformance probes feed.
- `docs/comparison/README.md` — claims register review process.
- `bench/gateways/<name>/` — per-gateway pinned image/digest, `forwarding`
  and `production-default` configs, and a `NOTES.md` of parity decisions
  and unverified fields.

## Status / TODO (read this before trusting any number from this harness)

This pass (Track B Week 1, plan items B1-B4) delivers the **harness itself**
and a **local smoke test**, not a measurement campaign. Specifically done:

- [x] Claims register (`docs/comparison/claims.yaml`, `COMPARISON.md`).
- [x] Ten gateway pins with forwarding/production-default configs and
      parity notes (`bench/gateways/`).
- [x] Deterministic mock upstream (`bench/cmd/mock-upstream`): OpenAI chat
      completions + embeddings, Anthropic Messages, streaming with
      configurable chunk cadence, fault injection (429/500/503/reset/
      stall/slow-first-byte/mid-stream-disconnect), request counters,
      `/healthz` and `/stats`. Has unit tests (`go test ./...` from
      `bench/`).
- [x] SSE probe (`bench/cmd/sse-probe`): custom per-chunk timing client for
      TTFT/inter-chunk measurement, and the fallback driver when k6 is not
      installed.
- [x] k6 scripts for S1, S2, S4, S5, S6, S8 (`bench/k6/`) — **not run in
      this environment**: `k6` is not installed here (`which k6` exit
      code 1). They are written against the documented k6 API and the
      scenario matrix below but are unexecuted in this session. Treat
      them as reviewed-but-unverified until someone runs them.
- [x] Support scripts (`bench/scripts/`): environment capture, cgroup
      sampler, baseline noise gate, and a single-cell orchestrator
      (`run-cell.sh`) that falls back to `sse-probe` when k6 is absent.
- [x] Local, non-containerized smoke test: mock upstream + an OmniSwitch
      binary built from this worktree, forwarding cell, both non-streaming
      and streaming chat completions round-tripped successfully (see
      "Harness self-test" below).

Explicitly **not done** in this pass (real TODO for whoever picks up
plan items B5-B9):

- [ ] No campaign has been run: no five-block interleaved trials, no CoV
      gate enforcement in practice, no `results/<run-id>/` artifact tree
      with real data, no HDR histograms.
- [ ] `bench/docker-compose.yml` only wires `mock-upstream` + `omniswitch`;
      the other nine gateways need service blocks added (image from their
      `pin.yaml`, config mount from their `forwarding.yaml`/
      `production-default.yaml`, cpuset 4-7) before they can be run through
      `run-cell.sh`.
- [ ] `bench/scripts/run-cell.sh` does not yet implement the five-block
      randomized interleaving in plan section 3.9, only a single trial per
      invocation. It also does not yet drive `POST /admin/fault` windows
      for S5, or open/verify cache config for S6 — both need a per-scenario
      wrapper on top of it.
- [ ] `k6` has never actually executed any script in this repository. The
      scripts have not been schema-validated against a real k6 binary.
- [ ] No pre-registration tag (`bench-prereg-v1`) has been cut yet — do
      that once the full scenario set is believed stable, per plan section
      3.10, before the first *measured* (not smoke) run.
- [ ] Envoy AI Gateway (`bench/gateways/envoy-ai-gateway/`) has no runnable
      cell at all (K8s-only deployment model; see its `NOTES.md`).
- [ ] Nine of ten gateway configs (everything except OmniSwitch) are
      written from vendor documentation but have **never been run against
      a live container** in this environment. Several config field names
      are explicitly flagged `unverified` in their `NOTES.md`
      (agentgateway retry policy, Bifrost governance/retry fields, Helicone
      custom-provider `base-url` key, Portkey OSS gateway-level auth
      toggle, TensorZero retry-status-code mapping). Validate each against
      its real container/schema before the first measured run.

## Harness self-test (2026-09-11, this session)

Ran locally (no Docker, no k6 — see below) to prove the mock upstream and
an OmniSwitch binary built from this worktree can complete a full request
round trip end to end:

1. Built `bench/cmd/mock-upstream` and `bench/cmd/sse-probe`
   (`cd bench && go build ./...`) and `cmd/gateway` from repository root.
2. Started the mock upstream on a local port; `GET /healthz` returned
   `{"status":"ok"}`.
3. Started OmniSwitch with `bench/gateways/omniswitch/forwarding.yaml`
   (base URL rewritten from `mock-upstream:4000` to `localhost:4000` for a
   non-Docker run; see `bench/docker-compose.yml` for the Docker-network
   version) via `OMNISWITCH_CONFIG=<path>` (not a `-config` flag — the CLI
   flag parsing added by a sibling workstream was not present on this
   branch at pin time; the config loader reads the `OMNISWITCH_CONFIG`
   env var).
4. `POST /v1/chat/completions` (non-streaming) returned a well-formed
   `chat.completion` body sourced from the mock.
5. `POST /v1/chat/completions` with `"stream": true` returned SSE chunks
   that reached the client; `bench/cmd/sse-probe` reported `chunks: 40`
   (matching the mock's configured `MOCK_STREAM_CHUNKS`), confirming 1:1
   chunk-count fidelity end to end through the gateway.
6. The mock's `/stats` endpoint showed the expected request count,
   confirming the counter mechanism used by S5/S6 correctness checks
   works.
7. **Fixed forward, not just observed:** the first attempt used the
   `"@provider/model"` alias syntax from `examples/gateway-config.yaml`
   and failed with `no provider found for model "@bench-mock/bench-model"`.
   Reading `cmd/gateway/main.go` showed `type: custom` providers are
   registered under their plain name only (no alias wrapper, unlike
   built-in provider types) — `bench/gateways/omniswitch/{forwarding,
   production-default}.yaml` and `NOTES.md` were corrected to reference
   `provider: bench-mock` / `model: bench-model` without the `@` prefix.
   This is now execution-validated, not just documentation-derived.
8. One informal observation from this single, uncontrolled local run (not
   a trial, no statistical weight): the SSE probe's inter-chunk timings
   were all near-zero after the first chunk, i.e. the default OmniSwitch
   streaming path appears to buffer and flush the mock's paced chunks
   quickly rather than forwarding each chunk's cadence transparently. This
   matches why the plan calls for measuring OmniSwitch with
   `stream_buffer` true and false as two labeled S4 cells — it is a
   real thing to verify with a proper trial, not a claim this session is
   making.

**k6 was not run.** `which k6` exits 1 in this environment; no k6 script
in `bench/k6/` has ever been executed. `run-cell.sh` and this self-test
both used `bench/cmd/sse-probe` as the fallback driver, per plan section
3.4's requirement to have a published, independently-testable alternative
when k6 is unavailable.

## Quick start

```
export PATH=$PATH:/usr/local/go/bin GOTOOLCHAIN=auto   # if go is not already on PATH

cd bench
go build ./...
go test ./...

# Terminal 1: mock upstream
MOCK_PORT=4000 ./cmd/mock-upstream/mock-upstream    # or: go run ./cmd/mock-upstream

# Terminal 2: OmniSwitch pointed at the mock (forwarding cell)
export OMNISWITCH_CONFIG=$(pwd)/gateways/omniswitch/forwarding.yaml   # rewrite mock-upstream:4000 -> localhost:4000 first, or use docker-compose.yml
export MOCK_UPSTREAM_API_KEY=bench-mock-key
../<built-omniswitch-binary>

# Terminal 3: smoke it
curl -s localhost:8080/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"bench-model","messages":[{"role":"user","content":"hi"}]}'
go run ./cmd/sse-probe -url http://localhost:8080/v1/chat/completions -n 1
```

Or via Docker (mock + OmniSwitch only today; see "Status / TODO"):

```
docker compose -f bench/docker-compose.yml up --build
```

## Methodology summary

Full detail lives in `/code/.plans/omniswitch-competitive-benchmark-plan.md`
section 3; this is the operative summary for anyone running the harness.

**Principles:** one host, one mock, one driver, one payload set, same
cpuset/memory limit per gateway, same scenario definitions. Isolate gateway
overhead by removing real-provider latency. Open-loop constant arrival
rate (avoid coordinated omission). Two configuration cells per gateway:
`forwarding` (minimum config, logging off) and `production-default` (each
vendor's own documented production quickstart, auth on, default logging).
Both published, always.

**Topology:** driver on cpuset 0-1, gateway under test on cpuset 4-7 with a
4 GiB memory limit (image pinned by digest), mock upstream on cpuset 2-3.
Loopback or a single docker bridge network; a direct-to-mock baseline is
measured in every trial block.

**Payloads:** P1 small chat (~1 KB, 1 user message), P2 medium (~10 KB, 8
turns + system prompt), P3 large context (~100 KB), P4 tool-call turn (3
tool definitions).

**Scenario matrix** (see `bench/k6/` for the driver scripts):

| ID | Scenario | Driver | Notes |
| --- | --- | --- | --- |
| S1 | Added latency, non-streaming, 0ms mock delay | k6 `s1_added_latency.js` | Primary headline table; report absolute and added, both cells, P1-P3, rates 100/500/1000 rps |
| S2 | Sustained throughput ladder | k6 `s2_throughput_ladder.js` | 500/1000/2000/4000/8000 rps steps; publish the ladder, never a single max-QPS number |
| S3 | Memory/CPU | `bench/scripts/sample-cgroup.sh` during S1/S2 | Idle/peak/growth RSS, CPU fraction |
| S4 | Streaming TTFT and chunk fidelity | k6 `s4_streaming_ttft.js` (aggregate only) + `bench/cmd/sse-probe` (per-chunk, required for the real number) | OmniSwitch measured with `stream_buffer` true and false, both labeled |
| S5 | Reliability under injected upstream failure | k6 `s5_reliability.js` + mock `POST /admin/fault` windows | 30s each of 100% 429, 50% 500, reset, stall; success %, time-to-fallback, recovery time |
| S6 | Cache behavior | k6 `s6_cache.js` + mock `/stats` request-count diff | Duplicate ratios 0/50/90%; correctness via mock request-count, not just client latency |
| S7 | Head-of-line / mixed traffic | not yet scripted (deferred; see Status/TODO) | 80% P1 fast + 20% P3 slow (2s mock delay) |
| S8 | Capability conformance | k6 `s8_conformance.js` | Feeds `docs/comparison/claims.yaml` `verified_by: ran-it` |

**Configuration parity rules** (enforced by convention in each
`bench/gateways/<name>/NOTES.md`, not yet by an automated linter): single
logical model `bench-model`; forwarding cell has no auth anywhere,
production cell uses each gateway's own documented key check; logging off
in forwarding, vendor default in production; retries/timeouts identical
numeric targets (2 retries, 500ms backoff, 10s timeout, retry on
429/500/502/503/504) where supported, `n/a` (not silently dropped) where
not; cache/guardrails only enabled in the scenarios that target them; if a
feature is on for OmniSwitch in a cell, it must be on for every competitor
that supports it in that cell.

## Pre-registration

Before the first **measured** (not smoke) run: commit this plan and every
config/scenario file, then tag `bench-prereg-v1`. No scenario, config, or
payload may change silently after that tag without a note in
`results/<run>/DEVIATIONS.md`. This has **not** been done yet in this
session — the harness is still being built, not yet run for real numbers.

## Anti-bias controls checklist

- Neutral tooling: k6 is a third-party driver; the mock and SSE probe are
  published in this same repo with unit tests
  (`bench/cmd/mock-upstream/*_test.go`) showing they behave deterministically.
- Config review: open a tracking issue per competitor with its
  `bench/gateways/<name>/` config and invite maintainers to comment for 5
  business days before publication.
- Symmetric features: enforced by the parity rules above; any deviation
  goes in `results/<run>/DEVIATIONS.md`, never silently.
- No OmniSwitch-specific tuning beyond its own documented defaults; if a
  default changes because of this benchmark, ship the change in a release
  before publishing numbers, and say so.
- Independent reproduction: before publication, someone outside the
  original author reruns S1 and S4 on a different machine using only this
  README; results must fall within stated tolerance (added p50 within 2x,
  ordering preserved) or the run does not ship.
- No superlatives in any published copy ("fastest", "Nx faster"). State
  absolute numbers, environment, and date; every chart links to its CSV.
- No result is hidden for being unfavorable to OmniSwitch. Drops are only
  for documented harness faults, recorded in `results/<run>/EXCLUDED.md`.

## Statistical protocol

- Trial = one 60s run after a 15s warm-up (discarded). Block = one trial
  per gateway plus the direct baseline, order randomized within the block.
  Minimum five blocks per cell for S1/S2/S3/S4/S5/S6; three for S7.
- Report median across trials with min/max; publish per-trial values in
  CSV. For p99, also report the 5-trial spread.
- Acceptance gate: direct-baseline p50 CoV <= 10% and p99 CoV <= 20%
  across a block's trials (`bench/scripts/baseline-noise-gate.sh`),
  otherwise discard and rerun the block (host noise).
- Latency captured as full HDR histograms per trial where the driver
  supports it, not just percentiles.
- No result dropped for being unfavorable; drops only for documented
  harness faults in `results/<run>/EXCLUDED.md`.

## Artifact layout

```
bench/
  cmd/mock-upstream/   deterministic mock (OpenAI + Anthropic shapes, fault injection, stats)
  cmd/sse-probe/        custom SSE client for TTFT/inter-chunk timing and the no-k6 fallback
  gateways/<name>/      pin.yaml, forwarding.*, production-default.*, NOTES.md per gateway
  k6/                   S1/S2/S4/S5/S6/S8 driver scripts + lib/gateways.js shared helpers
  scripts/              capture-env.sh, sample-cgroup.sh, run-cell.sh, baseline-noise-gate.sh
  docker-compose.yml    local wiring (mock-upstream + omniswitch today; see Status/TODO)
  results/<run-id>/     NOT YET POPULATED FOR REAL -- run-cell.sh writes here:
    manifest.json         host/kernel/docker/image-digest/plan-hash facts
    <gateway>/<scenario>/ run.log, summary.json (k6) or *.jsonl (sse-probe), cgroup.csv, mock_stats.json
    EXCLUDED.md, DEVIATIONS.md   (created only when a block is actually dropped or deviates)
```

## Decisions a human still needs to make

(See plan section 10 for the canonical list; restated here for anyone
picking this up.)

1. Host choice and budget for the real measurement campaign (bare-metal
   recommended by the plan).
2. Whether to build the Kubernetes (`kind`) tier for Envoy AI Gateway
   (now renamed "Agent Router"; see `bench/gateways/envoy-ai-gateway/
   NOTES.md`) in this beta, or drop it with an explicit published note.
3. Results repo location (`omniswitch-dev/gateway-bench` recommended by
   the plan; not created in this session).
4. Whether to include TensorZero (flagged as possibly unmaintained) and
   Helicone (GPL-3.0, repo staleness signal) in the Tier A published
   comparison for this beta, or register-only.
5. Who performs the required independent, second-machine reproduction of
   S1/S4 before publication.
