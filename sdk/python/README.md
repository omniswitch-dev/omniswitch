# OmniSwitch Python SDK

`omniswitch` is a thin wrapper around the official OpenAI Python client. It
keeps the OpenAI client surface while routing requests through a self-hosted
OmniSwitch gateway and injecting OmniSwitch headers for configs, traces,
sessions, provider overrides, and shadow traffic.

## Install

```bash
pip install omniswitch
```

For local development from this repository:

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

```bash
export OMNISWITCH_GATEWAY_URL=http://localhost:8080
export OMNISWITCH_API_KEY=sk-omniswitch-...
export OMNISWITCH_CONFIG=production-routing
export OMNISWITCH_TRACE_ID=agent-run-001
export OMNISWITCH_SESSION_ID=conversation-abc
```

## Helpers

```python
client = OmniSwitch().with_config("experiment-a")
client = client.with_trace("trace-123", session_id="session-123")
client = client.with_provider("anthropic")
```

Early `from sentinel import OmniSwitch` imports are still supported by a small
compatibility shim.
