# Command Line

The `omniswitch` binary is both the gateway server and its own CLI. Running
it with no arguments is equivalent to `omniswitch serve`.

```
Usage:
  omniswitch [command]

Commands:
  serve             Start the gateway server (default when no command is given)
  validate-config   Validate a gateway config file and exit (defaults to $OMNISWITCH_CONFIG)
  doctor            Check a running gateway's health, readiness, and model access
  version           Print version information
  help              Show this help text

Flags:
  -h, --help        Show this help text
  -v, --version     Print version information
```

None of `--help`, `--version`, or `validate-config` start the HTTP server or
bind a port, so they are safe to run in scripts, health checks, or CI even
while a gateway instance is already listening on the configured address.

## `omniswitch serve`

Starts the gateway. Configuration comes from environment variables and,
optionally, a config file pointed to by `OMNISWITCH_CONFIG` (see
[`docs/CONFIGURATION.md`](CONFIGURATION.md)). At minimum, set one provider
API key:

```bash
OPENAI_API_KEY=sk-... omniswitch serve
```

The process starts listening on `OMNISWITCH_LISTEN` (default `127.0.0.1:8080`
while authentication is off, `:8080` once `OMNISWITCH_AUTH=true`) and runs until it receives `SIGINT` or `SIGTERM`, at which point it stops
accepting new connections, drains in-flight requests (including open SSE
streams) for up to `OMNISWITCH_SHUTDOWN_TIMEOUT` (default `30s`), and exits
`0`.

## `omniswitch validate-config [path]`

Loads and validates a gateway config file without starting the server.
Useful in CI or a pre-deploy step to catch a bad config before a rollout.

```bash
omniswitch validate-config examples/gateway-config.yaml
# OK: examples/gateway-config.yaml

omniswitch validate-config ./broken.yaml
# invalid config ./broken.yaml: <reason>
# exit code 1
```

If no path is given, it falls back to `OMNISWITCH_CONFIG`. If neither is set,
it prints a usage error and exits `2`.

## `omniswitch doctor`

Check a running gateway without making inference requests or changing stored
state:

```bash
omniswitch doctor
OMNISWITCH_API_KEY=your-gateway-key omniswitch doctor --url https://gateway.example.com
omniswitch doctor --api-key-env OMNISWITCH_BOOTSTRAP_API_KEY --json
```

The command requests `GET /healthz`, `GET /readyz`, and `GET /v1/models`.
Authentication is sent only to the models endpoint. It reports common connection,
authentication, readiness, and empty-model failures with a suggested fix. Keys
and server response bodies are never printed; redirects are not followed.

Flags: `--url` (default `http://127.0.0.1:8080`, including an optional proxy path
prefix), `--api-key-env` (default `OMNISWITCH_API_KEY`), `--timeout` (total deadline,
default `10s`, maximum `1m`), and `--json` (machine-readable `ok` and `checks`).
Credentials, queries, and fragments in `--url` are rejected. Exit status is `0`
when all three checks pass, `1` for failed checks, and `2` for invalid arguments.
A passing check confirms setup reachability and model visibility; it does not
verify upstream provider credentials, inference quality, or actual model access.

## `omniswitch version` / `omniswitch --version` / `omniswitch -v`

Prints the build version, commit, and build date, then exits `0`. Useful for
confirming what an installer or Docker image shipped:

```bash
omniswitch version
# omniswitch version 0.2.0-beta (commit abcdef1, built 2026-09-11T00:00:00Z)
```

Release builds report the version without the leading `v` of the git tag.
Builds without release metadata (for example `go build` or `go run`) report
`dev (commit none, built unknown)`. To stamp an image built from source, pass
the `VERSION`, `COMMIT`, and `DATE` build arguments of the `Dockerfile`, for
example `docker build --build-arg VERSION=0.2.0-beta --build-arg COMMIT=$(git rev-parse --short HEAD) .`.
With Docker Compose, set `OMNISWITCH_BUILD_VERSION`, `OMNISWITCH_BUILD_COMMIT`,
and `OMNISWITCH_BUILD_DATE` before `docker compose build`.

## `omniswitch help` / `omniswitch --help` / `omniswitch -h`

Prints the usage text above and exits `0`.

## Unknown commands

Any other first argument prints an error to stderr followed by the usage
text, and exits `1`.

## Health endpoints

These are HTTP endpoints on the running gateway, not CLI subcommands, but
they are the operational counterpart to this CLI and are documented here for
discoverability:

- `GET /healthz` — liveness. Always returns `200 {"status":"ok"}` once the
  process is up; it does not touch the store or provider registry. Point a
  process supervisor's liveness probe here.
- `GET /readyz` — readiness. Returns `200 {"status":"ready"}` when the store
  is reachable and at least one provider is registered (or
  `OMNISWITCH_ALLOW_NO_PROVIDERS=true` is set), otherwise `503
  {"status":"not ready","reasons":[...]}`. Point a load balancer's readiness
  probe here.

Both endpoints are always unauthenticated and bypass `gateway.auth`, rate
limiting, and CORS, so they are safe for infrastructure health checks that
cannot present an API key.

## The policy CLI (`omniswitch-policy`)

A separate binary, `omniswitch-policy`, ships alongside `omniswitch` in
release archives and Homebrew. It manages MCP tool-call policy files (the CEL
rules read by `mcp.policy` / `OMNISWITCH_MCP_POLICY`) independently of the
gateway process. Run `omniswitch-policy --help` for its own usage.
