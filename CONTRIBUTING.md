# Contributing to OmniSwitch

Thanks for helping make OmniSwitch better. This project aims to be a small, understandable, self-hostable AI gateway and agent policy layer.

Not sure where to start? Look for issues labeled [`good first issue`](https://github.com/omniswitch-dev/omniswitch/labels/good%20first%20issue) or [`help wanted`](https://github.com/omniswitch-dev/omniswitch/labels/help%20wanted). During the beta, [`beta-blocker`](https://github.com/omniswitch-dev/omniswitch/labels/beta-blocker) issues are the highest-value contributions.

## Development Setup

Requirements:

- Go 1.24 or newer
- Git

Run the test suite:

```bash
go test ./...
```

Run the gateway locally:

```bash
cp .env.example .env
OMNISWITCH_CONFIG=examples/gateway-config.yaml go run ./cmd/gateway
```

Run the policy CLI:

```bash
go run ./cmd/omniswitch validate policies/production-delete.yaml
go run ./cmd/omniswitch test policies/production-delete.yaml examples/requests/delete-prod.json
```

### Running the install matrix locally

Before touching `install.sh`, `.goreleaser.yaml`, or release workflows, you can exercise the same checks CI runs after a release:

```bash
go build -o /tmp/omniswitch-dev ./cmd/gateway
OMNISWITCH_BIN=/tmp/omniswitch-dev scripts/verify-install.sh
```

This starts the binary you just built, waits for `/healthz` and `/readyz`, hits `/v1/models`, and stops it — the same sequence `.github/workflows/install-verify.yml` runs against real release artifacts across binaries, Homebrew, Docker, and Kubernetes.

### Adding a provider adapter

Provider adapters live in `internal/provider/`. If the target is already an OpenAI-compatible HTTP API (Ollama, vLLM, Together, Fireworks, DeepSeek, most others), you usually don't need a new adapter at all — configure it as a `custom` provider (see `internal/provider/custom.go`) with a `base_url` and `api_key_env`.

Write a new native adapter only when the provider's wire format genuinely differs from OpenAI's (see `internal/provider/anthropic.go` or `internal/provider/google.go` for examples). A new adapter should:

- Implement the `Provider` interface in `internal/provider/provider.go`.
- Translate to/from the gateway's internal `ChatRequest`/`ChatResponse` types, including streaming.
- Come with unit tests using recorded fixtures (see `internal/provider/*_test.go` for the pattern) rather than live API calls.
- Be registered in the provider factory and documented in `docs/CONFIGURATION.md`.

## Pull Requests

Before opening a pull request:

- Keep changes focused. Prefer several small PRs over one large one — reviewers can turn around a <300-line diff much faster than a 2,000-line one, and smaller PRs are easier to bisect if something regresses.
- Add tests for new behavior.
- Run `gofmt -w cmd internal pkg` when editing Go files.
- Run `go test ./...`.
- Update docs when changing user-facing behavior.
- Sign off your commits (see DCO below).

### DCO sign-off

Commits must include a `Signed-off-by` trailer certifying you have the right to submit the contribution under this project's license (see "Certificate of Origin" below). Use the `-s` flag:

```bash
git commit -s -m "fix: handle empty tool_choice"
```

This appends `Signed-off-by: Your Name <you@example.com>` using your configured `git config user.name`/`user.email`. Pull requests without sign-off may be asked to amend and force-push before merge.

## Labels

Issues and PRs are triaged with a small label set. See the labels defined in [`.github/labels.yml`](.github/labels.yml):

| Label | Meaning |
| --- | --- |
| `beta-blocker` | Blocks someone from completing the quickstart or trusting the beta; highest triage priority. |
| `good first issue` | Scoped and documented well enough for a first-time contributor. |
| `help wanted` | Maintainers want outside help on this. |
| `docs` | Documentation-only change or gap. |
| `provider` | Provider adapters, model catalog, or pricing. |
| `mcp` | MCP tool gateway, policy engine, or A2A. |
| `security` | Security-relevant; see `SECURITY.md` for vulnerability reports specifically. |

## Docs style

- Write in plain, direct language; avoid marketing superlatives ("blazing fast", "production-grade", "best-in-class"). If you make a capability claim, it must be something a reader can verify against code, tests, or a linked doc.
- Every code block that a reader is meant to run should actually work when copy-pasted; if it depends on state from an earlier block, say so.
- Prefer showing one correct way over listing every option; link to `docs/CONFIGURATION.md` for the full reference instead of duplicating it.
- Call out beta caveats explicitly rather than staying silent (see `docs/KNOWN_LIMITATIONS.md`) — silence reads as a claim that something fully works.

## Project Boundaries

OmniSwitch prefers:

- Local-first operation.
- OpenAI-compatible APIs where practical.
- Declarative config over hidden global state.
- Plain SQLite persistence before distributed infrastructure.
- Clear policy traces and auditable decisions.

## Certificate of Origin

By contributing, you certify that you have the right to submit your contribution under the Apache-2.0 license used by this repository. This is the standard [Developer Certificate of Origin](https://developercertificate.org/); the `git commit -s` sign-off above is how you attest to it on each commit.
