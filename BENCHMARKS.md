# OmniSwitch Benchmarks

Honest numbers, reproducible methodology. No cherry-picking. Re-run everything
below on your own hardware before drawing conclusions.

## Gateway overhead (micro-benchmarks)

Run from the repository root:

```bash
go test ./internal/accel/ -bench "First|Regexp" -benchtime=2s
go test ./internal/guardrail/ -bench . -benchtime=2s
```

### Guardrail pattern scanning (8 rules, Rust-WASM vs pure Go)

Measured on AMD Ryzen 5 5500U (12 threads), Windows, Go 1.26, wazero 1.9.
The accelerator executes all rules in a single pass inside an embedded WASM
module compiled from Rust (`accel/`); the Go path runs each rule as a separate
`regexp.Match` call. Both paths return identical trigger decisions.

| Payload | Pure-Go path | Rust-WASM path | Speedup |
| --- | --- | --- | --- |
| 8 KB | ~1.48 ms | ~1.24 ms | 1.19x |
| 64 KB | ~12.8 ms | ~9.3 ms | 1.38x |
| 256 KB | ~53.5 ms | ~37.8 ms | 1.42x |

Interpretation:

- The accelerator pays off for large payloads and many rules; at small sizes
  the WASM call overhead roughly cancels the engine advantage.
- The bigger real-world win in this codebase came from **precompiling rule
  regexes once** instead of per request: `guardrail.NewEngineWithConfig` now
  compiles every custom rule a single time, removing `regexp.Compile` from the
  request path entirely.
- Native (non-WASM) execution of the same Rust scanner would be faster still;
  WASM keeps the single static-binary distribution story intact.

## Load testing against a running gateway

Use any HTTP load generator pointed at your gateway. Example with
[vegeta](https://github.com/tsenart/vegeta):

```bash
export BASE=http://localhost:8080
export KEY=sk-omniswitch-...
printf 'POST %s/v1/chat/completions\nAuthorization: Bearer %s\n@payload.json\n' \
  "$BASE" "$KEY" > target.txt

echo '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Say hi"}],"max_tokens":5}' > payload.json

vegeta attack -targets target.txt -duration 30s -rate 50/s | vegeta report
```

Compare against LiteLLM proxy and Bifrost on the same machine, same mock or
real backend, streaming disabled first. Report p50/p95/p99 latency and error
rate, not just mean throughput.

## Reproducibility checklist

1. Pin CPU frequency governor to performance where possible.
2. Use a mock OpenAI backend (e.g. WireMock) to isolate gateway overhead from
   provider latency.
3. Run each scenario for >= 30s and discard the first 5s as warm-up.
4. Disable payload logging (`gateway.log_payloads: false`) for overhead tests;
   measure it separately as a feature cost.

## Competitive benchmark harness (in progress)

The guidance above is a quick manual checklist. A full, pre-registered,
reproducible competitive benchmark harness against ten other AI gateways
(LiteLLM, LiteLLM Rust mode, Solo.io agentgateway, Bifrost, Portkey OSS,
Helicone AI Gateway, Kong AI Gateway, TensorZero, Envoy AI Gateway/Agent
Router, plus OmniSwitch itself) is being built under
[`bench/`](bench/README.md): a deterministic mock upstream, k6 scenario
scripts, cgroup CPU/RSS sampling, and an anti-bias/statistical protocol.
Capability claims for every gateway (including OmniSwitch) live in a single
source of truth register, [`docs/comparison/claims.yaml`](docs/comparison/claims.yaml),
rendered to [`docs/comparison/COMPARISON.md`](docs/comparison/COMPARISON.md).

As of this writing the harness itself is built and locally smoke-tested
(see `bench/README.md` "Harness self-test" and "Status / TODO"), but no
measurement campaign has been run yet — treat any number from `bench/` as
unverified until it ships with a `results/<run-id>/` artifact directory and
a pre-registration tag. `bench/README.md` is the canonical source for
methodology, current status, and open decisions; this section will be
replaced with a results summary once a campaign is published.

