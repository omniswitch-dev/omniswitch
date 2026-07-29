# OmniSwitch Node.js SDK

`omniswitch` is a small OpenAI-compatible client for a self-hosted OmniSwitch
gateway. It preserves the official OpenAI Node.js client surface while adding
provider routing, dynamic config, trace, session, and shadow-provider headers.

## Install

```bash
npm install openai omniswitch
```

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

```bash
export OMNISWITCH_GATEWAY_URL=http://localhost:8080
export OMNISWITCH_API_KEY=sk-omniswitch-...
export OMNISWITCH_CONFIG=production-routing
export OMNISWITCH_TRACE_ID=agent-run-001
export OMNISWITCH_SESSION_ID=conversation-abc
```

## Helpers

```js
const client = new OmniSwitch()
  .withConfig("experiment-a")
  .withTrace("trace-123", "session-123")
  .withProvider("anthropic");
```

Early `Sentinel` imports are still exported as an alias for compatibility.
