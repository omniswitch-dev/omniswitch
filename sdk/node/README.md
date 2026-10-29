# OmniSwitch Node.js SDK

`omniswitch` is a small OpenAI-compatible client for a self-hosted OmniSwitch
gateway. It preserves the official OpenAI Node.js client surface while adding
provider routing, dynamic config, trace, session, and shadow-provider headers.

## Install

The SDK is not published to npm yet. If a package named `omniswitch`
appears on the npm registry before we publish, it is not ours. Install it
from a checkout of this repository or from git:

```bash
# From a local checkout
git clone https://github.com/omniswitch-dev/omniswitch.git
npm install openai ./omniswitch/sdk/node

# Or pack it and install the tarball
(cd omniswitch/sdk/node && npm pack)
npm install openai ./omniswitch/sdk/node/omniswitch-0.1.0.tgz
```

npm cannot install a subdirectory of a git repository directly, so use one of
the commands above. `openai` (4.x or later) is a peer dependency.

## Use

```js
const { OmniSwitch } = require("omniswitch");

const client = new OmniSwitch({
  gatewayUrl: "http://localhost:8080",
  apiKey: "sk-omniswitch-...",
  config: "production-routing",
  traceId: "agent-run-001",
  sessionId: "conversation-abc",
});

const response = await client.chat.completions.create({
  model: "gpt-4o-mini",
  messages: [{ role: "user", content: "Hello from OmniSwitch" }],
});

console.log(response.choices[0].message.content);
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

Precedence, from highest to lowest: constructor options (`config`, `traceId`,
...) and the `with*` helpers, then OmniSwitch headers passed in
`defaultHeaders`, then environment variables.

## Helpers

Each helper returns a new client. Values set by a helper win over the
environment variables above.

```js
const client = new OmniSwitch()
  .withConfig("experiment-a")
  .withTrace("trace-123", "session-123")
  .withProvider("anthropic");
```

## Tests

The unit tests stub the `openai` package and need no network access or
installed dependencies:

```bash
cd sdk/node && npm test
```

Early `Sentinel` imports are still exported as an alias for compatibility.
