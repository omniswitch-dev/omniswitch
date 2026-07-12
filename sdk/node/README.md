# Sentinel Node.js SDK

`sentinel-ai` is a small OpenAI-compatible client for a self-hosted Sentinel
gateway. It preserves the official OpenAI Node.js client surface while adding
provider routing and agent observability headers.

## Install

```bash
npm install openai sentinel-ai
```

## Use

```js
const { Sentinel } = require("sentinel-ai");

const client = new Sentinel({
  gatewayUrl: "http://localhost:8080",
  provider: "openai",
  traceId: "agent-run-001",
});

const response = await client.chat.completions.create({
  model: "gpt-4o-mini",
  messages: [{ role: "user", content: "Hello from Sentinel" }],
});
```

Set `SENTINEL_GATEWAY_URL` and `SENTINEL_API_KEY` to configure clients through
the environment. The gateway URL may include `/v1`, but does not have to.
