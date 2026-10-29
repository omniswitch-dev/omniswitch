"use strict";

// Unit tests for the OmniSwitch Node.js SDK. The `openai` package is replaced
// with a stub, so the tests need no network access and no installed
// dependencies. Run from sdk/node with: node --test

const test = require("node:test");
const assert = require("node:assert/strict");
const Module = require("node:module");
const path = require("node:path");

class FakeOpenAI {
  constructor(options = {}) {
    this.options = options;
  }
}

const originalLoad = Module._load;
Module._load = function load(request, parent, isMain) {
  if (request === "openai") return { default: FakeOpenAI };
  return originalLoad.call(this, request, parent, isMain);
};
const sdk = require(path.join(__dirname, "..", "index.js"));
Module._load = originalLoad;

const SDK_ENV = [
  "OMNISWITCH_GATEWAY_URL",
  "SENTINEL_GATEWAY_URL",
  "OMNISWITCH_API_KEY",
  "SENTINEL_API_KEY",
  "OMNISWITCH_CONFIG",
  "OMNISWITCH_CLIENT_CONFIG",
  "OMNISWITCH_PROVIDER",
  "OMNISWITCH_TRACE_ID",
  "OMNISWITCH_SESSION_ID",
  "OMNISWITCH_SHADOW_PROVIDER",
  "OMNISWITCH_CLIENT_SHADOW_PROVIDER",
];

// withEnv runs fn with only the given SDK env vars set, then restores them.
function withEnv(values, fn) {
  const saved = {};
  for (const name of SDK_ENV) {
    saved[name] = process.env[name];
    delete process.env[name];
  }
  Object.assign(process.env, values);
  try {
    return fn();
  } finally {
    for (const name of SDK_ENV) {
      if (saved[name] === undefined) delete process.env[name];
      else process.env[name] = saved[name];
    }
  }
}

function headersOf(client) {
  return client.options.defaultHeaders || {};
}

test("gateway OMNISWITCH_CONFIG and OMNISWITCH_SHADOW_PROVIDER are ignored", () => {
  const client = withEnv(
    {
      OMNISWITCH_CONFIG: "examples/gateway-config.yaml",
      OMNISWITCH_SHADOW_PROVIDER: "anthropic",
    },
    () => new sdk.OmniSwitch({ gatewayUrl: "http://gw:8080" }),
  );
  assert.deepEqual(headersOf(client), {});
});

test("client env vars are used", () => {
  const client = withEnv(
    {
      OMNISWITCH_CLIENT_CONFIG: "production-routing",
      OMNISWITCH_CLIENT_SHADOW_PROVIDER: "anthropic",
      OMNISWITCH_PROVIDER: "openai",
      OMNISWITCH_TRACE_ID: "trace-env",
      OMNISWITCH_SESSION_ID: "session-env",
    },
    () => new sdk.OmniSwitch(),
  );
  assert.deepEqual(headersOf(client), {
    "x-omniswitch-config": "production-routing",
    "x-omniswitch-shadow-provider": "anthropic",
    "x-omniswitch-provider": "openai",
    "x-omniswitch-trace-id": "trace-env",
    "x-omniswitch-session-id": "session-env",
  });
});

test("withConfig/withProvider/withTrace win over env vars", () => {
  const client = withEnv(
    {
      OMNISWITCH_CLIENT_CONFIG: "production-routing",
      OMNISWITCH_PROVIDER: "openai",
      OMNISWITCH_TRACE_ID: "trace-env",
      OMNISWITCH_SESSION_ID: "session-env",
    },
    () =>
      new sdk.OmniSwitch()
        .withConfig("experiment-a")
        .withProvider("anthropic")
        .withTrace("trace-123", "session-123"),
  );
  const headers = headersOf(client);
  assert.equal(headers["x-omniswitch-config"], "experiment-a");
  assert.equal(headers["x-omniswitch-provider"], "anthropic");
  assert.equal(headers["x-omniswitch-trace-id"], "trace-123");
  assert.equal(headers["x-omniswitch-session-id"], "session-123");
});

test("explicit options win over env vars", () => {
  const client = withEnv(
    { OMNISWITCH_CLIENT_CONFIG: "env", OMNISWITCH_TRACE_ID: "env" },
    () => new sdk.OmniSwitch({ config: "arg", traceId: "arg-trace" }),
  );
  assert.equal(headersOf(client)["x-omniswitch-config"], "arg");
  assert.equal(headersOf(client)["x-omniswitch-trace-id"], "arg-trace");
});

test("defaultHeaders win over env vars case-insensitively", () => {
  const client = withEnv(
    { OMNISWITCH_CLIENT_CONFIG: "env" },
    () => new sdk.OmniSwitch({ defaultHeaders: { "X-OmniSwitch-Config": "hdr" } }),
  );
  assert.deepEqual(headersOf(client), { "X-OmniSwitch-Config": "hdr" });
});

test("explicit option replaces a mixed-case default header", () => {
  const client = withEnv({}, () =>
    new sdk.OmniSwitch({
      config: "arg",
      defaultHeaders: { "X-OmniSwitch-Config": "hdr", "x-other": "1" },
    }),
  );
  assert.deepEqual(headersOf(client), { "x-omniswitch-config": "arg", "x-other": "1" });
});

test("clone keeps gateway URL, API key and client options", () => {
  const clone = withEnv({}, () =>
    new sdk.OmniSwitch({ gatewayUrl: "http://gw", apiKey: "k", timeout: 5 }).withConfig("c"),
  );
  assert.equal(clone.options.baseURL, "http://gw/v1");
  assert.equal(clone.options.apiKey, "k");
  assert.equal(clone.options.timeout, 5);
});

test("defaults and env gateway/key", () => {
  const def = withEnv({}, () => new sdk.OmniSwitch());
  assert.equal(def.options.baseURL, "http://localhost:8080/v1");
  assert.equal(def.options.apiKey, "omniswitch-no-auth");
  assert.equal(def.options.defaultHeaders, undefined);

  const env = withEnv(
    { OMNISWITCH_GATEWAY_URL: "http://gw:9000/", OMNISWITCH_API_KEY: "sk-1" },
    () => new sdk.OmniSwitch(),
  );
  assert.equal(env.options.baseURL, "http://gw:9000/v1");
  assert.equal(env.options.apiKey, "sk-1");
});

test("normalizeGatewayUrl", () => {
  const cases = {
    "http://gw": "http://gw/v1",
    "http://gw/": "http://gw/v1",
    "http://gw/v1": "http://gw/v1",
    "http://gw/v1/": "http://gw/v1",
    "  ": "http://localhost:8080/v1",
  };
  for (const [value, want] of Object.entries(cases)) {
    assert.equal(sdk.normalizeGatewayUrl(value), want);
  }
});
