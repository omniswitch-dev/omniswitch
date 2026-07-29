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
    const headers = buildHeaders(defaultHeaders, {
      config: firstValue(config, process.env.OMNISWITCH_CONFIG),
      provider: firstValue(provider, process.env.OMNISWITCH_PROVIDER),
      traceId: firstValue(traceId, process.env.OMNISWITCH_TRACE_ID),
      sessionId: firstValue(sessionId, process.env.OMNISWITCH_SESSION_ID),
      shadowProvider: firstValue(
        shadowProvider,
        process.env.OMNISWITCH_SHADOW_PROVIDER,
      ),
    });

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
    return this._clone({
      ...this._omniswitch.headers,
      "x-omniswitch-config": config,
    });
  }

  /**
   * Return a new client with trace and optional session headers.
   * @param {string} traceId
   * @param {string} [sessionId]
   * @returns {OmniSwitch}
   */
  withTrace(traceId, sessionId) {
    const headers = {
      ...this._omniswitch.headers,
      "x-omniswitch-trace-id": traceId,
    };
    if (sessionId !== undefined) {
      headers["x-omniswitch-session-id"] = sessionId;
    }
    return this._clone(headers);
  }

  /**
   * Return a new client that forces a specific provider or alias.
   * @param {string} provider
   * @returns {OmniSwitch}
   */
  withProvider(provider) {
    return this._clone({
      ...this._omniswitch.headers,
      "x-omniswitch-provider": provider,
    });
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
