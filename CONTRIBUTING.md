# Contributing to OmniSwitch

Thanks for helping make OmniSwitch better. This project aims to be a small, understandable, self-hostable AI gateway and agent policy layer.

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

## Pull Requests

Before opening a pull request:

- Keep changes focused.
- Add tests for new behavior.
- Run `gofmt -w cmd internal pkg` when editing Go files.
- Run `go test ./...`.
- Update docs when changing user-facing behavior.

## Project Boundaries

OmniSwitch prefers:

- Local-first operation.
- OpenAI-compatible APIs where practical.
- Declarative config over hidden global state.
- Plain SQLite persistence before distributed infrastructure.
- Clear policy traces and auditable decisions.

## Certificate of Origin

By contributing, you certify that you have the right to submit your contribution under the Apache-2.0 license used by this repository.
