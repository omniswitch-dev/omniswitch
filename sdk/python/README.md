# OmniSwitch Python SDK

`omniswitch` is a thin wrapper around the official OpenAI Python client. It
keeps the OpenAI client surface while routing requests through a self-hosted
OmniSwitch gateway and injecting OmniSwitch headers for configs, traces,
sessions, provider overrides, and shadow traffic.

## Install

The SDK is not published to PyPI yet. If a package named `omniswitch`
appears on PyPI before we publish, it is not ours. Install from the
repository instead:

```bash
pip install "git+https://github.com/omniswitch-dev/omniswitch#subdirectory=sdk/python"
```

For reproducible installs, pin a release tag or commit by adding
`@<tag-or-commit>` after the repository URL, for example
`git+https://github.com/omniswitch-dev/omniswitch@<tag>#subdirectory=sdk/python`.

For local development from a checkout of this repository:

```bash
pip install -e sdk/python
```

## Use

```python
from omniswitch import OmniSwitch

client = OmniSwitch(
    gateway_url="http://localhost:8080",
    api_key="sk-omniswitch-...",
    config="production-routing",
    trace_id="agent-run-001",
    session_id="conversation-abc",
)

response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Hello from OmniSwitch"}],
)

print(response.choices[0].message.content)
```

The gateway URL may include `/v1`, but does not have to.

## Environment

| Variable | Used for |
| --- | --- |
| `OMNISWITCH_GATEWAY_URL` | Gateway URL (default `http://localhost:8080/v1`) |
| `OMNISWITCH_API_KEY` | OmniSwitch API key |
| `OMNISWITCH_CLIENT_CONFIG` | `x-omniswitch-config` (dynamic config name or ID) |
| `OMNISWITCH_PROVIDER` | `x-omniswitch-provider` |
| `OMNISWITCH_TRACE_ID` | `x-omniswitch-trace-id` |
| `OMNISWITCH_SESSION_ID` | `x-omniswitch-session-id` |
| `OMNISWITCH_CLIENT_SHADOW_PROVIDER` | `x-omniswitch-shadow-provider` |

```bash
export OMNISWITCH_GATEWAY_URL=http://localhost:8080
export OMNISWITCH_API_KEY=sk-omniswitch-...
export OMNISWITCH_CLIENT_CONFIG=production-routing
export OMNISWITCH_TRACE_ID=agent-run-001
export OMNISWITCH_SESSION_ID=conversation-abc
```

The SDK does not read `OMNISWITCH_CONFIG` or `OMNISWITCH_SHADOW_PROVIDER`.
Those are gateway settings (`OMNISWITCH_CONFIG` is the path of the gateway's
YAML file), so a shell or container that has the gateway's environment loaded
no longer sends them as request headers. Earlier SDK versions read those names;
rename them to `OMNISWITCH_CLIENT_CONFIG` and
`OMNISWITCH_CLIENT_SHADOW_PROVIDER`.

Precedence, from highest to lowest: keyword arguments (`config=...`) and the
`with_*` helpers, then OmniSwitch headers passed in `default_headers`, then
environment variables.

## Helpers

Each helper returns a new client. Values set by a helper win over the
environment variables above.

```python
client = OmniSwitch().with_config("experiment-a")
client = client.with_trace("trace-123", session_id="session-123")
client = client.with_provider("anthropic")
```

## Tests

The unit tests stub the `openai` package and need no network access:

```bash
python3 -m unittest discover -s sdk/python/tests
```

The package installs one module, `omniswitch`. The old `sentinel` module is no
longer shipped, because it would clash with the unrelated `sentinel` package on
PyPI. Replace `from sentinel import OmniSwitch` with
`from omniswitch import OmniSwitch`; the `Sentinel` alias is still available
from `omniswitch`.
