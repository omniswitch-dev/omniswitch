"use strict";

/**
 * OmniSwitch Node.js SDK.
 *
 * A small wrapper around the official OpenAI Node.js client that routes
 * requests through a self-hosted OmniSwitch gateway and injects OmniSwitch
 * routing, config, tracing, and session headers.
 */

const OpenAI = require("openai").default || require("openai");

const DEFAULT_GATEWAY_URL = "http://localhost:8080/v1";

// Environment variables read by the SDK, keyed by header. OMNISWITCH_CONFIG
// and OMNISWITCH_SHADOW_PROVIDER are gateway settings, so the SDK uses the
// OMNISWITCH_CLIENT_* names for those to avoid picking up gateway values.
const HEADER_ENV = {
  "x-omniswitch-config": "OMNISWITCH_CLIENT_CONFIG",
  "x-omniswitch-provider": "OMNISWITCH_PROVIDER",
  "x-omniswitch-trace-id": "OMNISWITCH_TRACE_ID",
  "x-omniswitch-session-id": "OMNISWITCH_SESSION_ID",
  "x-omniswitch-shadow-provider": "OMNISWITCH_CLIENT_SHADOW_PROVIDER",
};

function normalizeGatewayUrl(gatewayUrl) {
  const value = String(gatewayUrl || DEFAULT_GATEWAY_URL).trim() || DEFAULT_GATEWAY_URL;
  const trimmed = value.replace(/\/+$/, "");
  return trimmed.endsWith("/v1") ? trimmed : `${trimmed}/v1`;
}

function firstValue(...values) {
  for (const value of values) {
    if (value !== undefined && value !== null && String(value).trim() !== "") {
      return value;
    }
  }
  return undefined;
}

// mergeHeaders returns base updated with overrides, replacing header names
// case-insensitively so "X-OmniSwitch-Config" and "x-omniswitch-config" never
// both end up on a request.
function mergeHeaders(base = {}, overrides = {}) {
  const headers = { ...base };
  for (const [name, value] of Object.entries(overrides)) {
    for (const existing of Object.keys(headers)) {
      if (existing.toLowerCase() === name.toLowerCase()) delete headers[existing];
    }
    headers[name] = value;
  }
  return headers;
}

function hasHeader(headers, name) {
  return Object.keys(headers).some((key) => key.toLowerCase() === name);
}

function buildHeaders(defaultHeaders = {}, options = {}) {
  const headers = { ...defaultHeaders };
  if (options.config) headers["x-omniswitch-config"] = options.config;
  if (options.provider) headers["x-omniswitch-provider"] = options.provider;
  if (options.traceId) headers["x-omniswitch-trace-id"] = options.traceId;
  if (options.sessionId) headers["x-omniswitch-session-id"] = options.sessionId;
  if (options.shadowProvider) {
    headers["x-omniswitch-shadow-provider"] = options.shadowProvider;
  }
  return headers;
}

class OmniSwitch extends OpenAI {
  /**
   * @param {OmniSwitchOptions & import("openai").ClientOptions} [options]
   */
  constructor(options = {}) {
    const {
      gatewayUrl,
      config,
      provider,
      traceId,
      sessionId,
      shadowProvider,
      defaultHeaders,
      ...openaiOptions
    } = options;

    const baseURL = normalizeGatewayUrl(
      firstValue(
        gatewayUrl,
        process.env.OMNISWITCH_GATEWAY_URL,
        process.env.SENTINEL_GATEWAY_URL,
        DEFAULT_GATEWAY_URL,
      ),
    );
    const apiKey = firstValue(
      openaiOptions.apiKey,
      process.env.OMNISWITCH_API_KEY,
      process.env.SENTINEL_API_KEY,
      "omniswitch-no-auth",
    );
    // Precedence: explicit option > caller-supplied defaultHeaders (which
    // includes headers set by withConfig/withTrace/withProvider) > env vars.
    const explicit = buildHeaders({}, {
      config: firstValue(config),
      provider: firstValue(provider),
      traceId: firstValue(traceId),
      sessionId: firstValue(sessionId),
      shadowProvider: firstValue(shadowProvider),
    });
    let headers = { ...(defaultHeaders || {}) };
    for (const [name, envName] of Object.entries(HEADER_ENV)) {
      const value = firstValue(process.env[envName]);
      if (value !== undefined && !hasHeader(headers, name)) headers[name] = value;
    }
    headers = mergeHeaders(headers, explicit);

    super({
      ...openaiOptions,
      apiKey,
      baseURL,
      defaultHeaders: Object.keys(headers).length > 0 ? headers : undefined,
    });

    this._omniswitch = {
      gatewayUrl: baseURL,
      apiKey,
      headers,
      clientOptions: { ...openaiOptions },
    };
  }

  /**
   * Return a new client that sends x-omniswitch-config.
   * @param {string} config
   * @returns {OmniSwitch}
   */
  withConfig(config) {
    return this._clone(
      mergeHeaders(this._omniswitch.headers, { "x-omniswitch-config": config }),
    );
  }

  /**
   * Return a new client with trace and optional session headers.
   * @param {string} traceId
   * @param {string} [sessionId]
   * @returns {OmniSwitch}
   */
  withTrace(traceId, sessionId) {
    const overrides = { "x-omniswitch-trace-id": traceId };
    if (sessionId !== undefined) {
      overrides["x-omniswitch-session-id"] = sessionId;
    }
    return this._clone(mergeHeaders(this._omniswitch.headers, overrides));
  }

  /**
   * Return a new client that forces a specific provider or alias.
   * @param {string} provider
   * @returns {OmniSwitch}
   */
  withProvider(provider) {
    return this._clone(
      mergeHeaders(this._omniswitch.headers, { "x-omniswitch-provider": provider }),
    );
  }

  _clone(headers) {
    return new OmniSwitch({
      ...this._omniswitch.clientOptions,
      gatewayUrl: this._omniswitch.gatewayUrl,
      apiKey: this._omniswitch.apiKey,
      defaultHeaders: headers,
    });
  }
}

// Backwards-compatible alias for early SDK examples.
const Sentinel = OmniSwitch;

/**
 * One-shot convenience function for chat completions.
 *
 * @param {string} model
 * @param {Array<{role: string, content: unknown, [key: string]: unknown}>} messages
 * @param {OmniSwitchOptions} [options]
 * @param {Record<string, unknown>} [requestOptions]
 * @returns {Promise<import("openai").ChatCompletion>}
 */
async function chat(model, messages, options = {}, requestOptions = {}) {
  const client = new OmniSwitch(options);
  return client.chat.completions.create({ model, messages, ...requestOptions });
}

/**
 * List all models available on the gateway.
 * @param {string | OmniSwitchOptions} [options]
 * @returns {Promise<Array<{id: string, owned_by: string}>>}
 */
async function listModels(options) {
  const clientOptions =
    typeof options === "string" ? { gatewayUrl: options } : options || {};
  const client = new OmniSwitch(clientOptions);
  const models = await client.models.list();
  return models.data.map((model) => ({ id: model.id, owned_by: model.owned_by }));
}

module.exports = {
  OmniSwitch,
  Sentinel,
  chat,
  listModels,
  normalizeGatewayUrl,
  buildHeaders,
};
module.exports.default = OmniSwitch;
