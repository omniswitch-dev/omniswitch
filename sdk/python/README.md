# OmniSwitch: install, connect, build

The release wheel includes the complete gateway. No Go compiler, Docker,
provider SDK, or first-run binary download is required.

> PyPI publication is being prepared in this branch. The bare install command
> below becomes available after the first verified package release is published
> by `omniswitch-dev/omniswitch`. Until then, install a wheel built by this
> repository's Python package workflow, not an unrelated package.

## Install a published release

```bash
pip install omniswitch
omniswitch
```

Your browser opens a short local setup. Choose a provider, paste its key (or
use a detected environment key), and copy your integration. A free simulated
demo is available before connecting a real provider. Subsequent launches reuse
the saved profile.

```bash
omniswitch start --no-browser  # Print a private setup link for headless use
omniswitch start --demo        # Start a simulated demo on a fresh profile
omniswitch start --setup       # Reconnect your provider
omniswitch serve               # Advanced environment/config-driven server
```

The package launches a loopback gateway with authentication enabled, a separate
app key restricted to model `default`, and local SQLite state. See
[onboarding and integrations](https://github.com/omniswitch-dev/omniswitch/blob/main/docs/ONBOARDING.md).

Release wheels support Linux x86-64/ARM64, macOS 12+ Intel/Apple Silicon, and
Windows x86-64. Python 3.9+ is required. Source installs contain the SDK/launcher
only; use a platform wheel for the bundled gateway, or set `OMNISWITCH_BINARY`
to a locally built gateway executable.

## Optional Python SDK

Standard OpenAI-compatible SDKs work with the integration code provided by the
setup page. For the additional OmniSwitch convenience wrapper:

```bash
pip install 'omniswitch[sdk]'
```

With the optional extra, `OmniSwitch()` automatically discovers the saved local
profile and uses its app key. Choose model `default`. Explicit gateway URLs
keep their own credentials and disable local credential discovery.

For SDK development from a checkout:

```bash
pip install -e 'sdk/python[sdk]'
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
