/**
 * Sentinel AI Gateway - Node.js/TypeScript SDK
 *
 * Drop-in replacement for the OpenAI Node.js client that routes all
 * requests through your self-hosted Sentinel AI Gateway.
 *
 * Installation:
 *   npm install sentinel-ai   (or copy this file into your project)
 *
 * Requires: openai >= 4.0
 *   npm install openai
 *
 * Usage:
 *   import { Sentinel } from 'sentinel-ai';
 *   const client = new Sentinel({ gatewayUrl: 'http://localhost:8080' });
 *   const response = await client.chat.completions.create({
 *     model: 'gpt-4o-mini',
 *     messages: [{ role: 'user', content: 'Hello!' }],
 *   });
 *   console.log(response.choices[0].message.content);
 */

const OpenAI = require("openai").default || require("openai");

const DEFAULT_GATEWAY_URL = "http://localhost:8080/v1";

/**
 * @typedef {Object} SentinelOptions
 * @property {string} [gatewayUrl] - Base URL of the Sentinel gateway.
 * @property {string} [apiKey] - Sentinel API key (or provider key for unauthenticated gateways).
 * @property {string} [provider] - Force a specific provider (x-sentinel-provider header).
 * @property {string} [traceId] - Trace ID for agent observability.
 * @property {string} [sessionId] - Session ID for conversation grouping.
 * @property {string} [shadowProvider] - Provider for async shadow comparison.
 */

class Sentinel extends OpenAI {
  /**
   * Create a Sentinel AI Gateway client.
   *
   * @param {SentinelOptions & import('openai').ClientOptions} [options={}]
   *
   * @example
   * // Basic usage
   * const client = new Sentinel();
   * const resp = await client.chat.completions.create({
   *   model: 'gpt-4o-mini',
   *   messages: [{ role: 'user', content: 'Hello!' }],
   * });
   *
   * @example
   * // Force Anthropic provider
   * const client = new Sentinel({ provider: 'anthropic' });
   *
   * @example
   * // With observability
   * const client = new Sentinel({
   *   traceId: 'agent-run-001',
   *   sessionId: 'conv-abc',
   * });
   *
   * @example
   * // Streaming
   * const stream = await client.chat.completions.create({
   *   model: 'gpt-4o-mini',
   *   messages: [{ role: 'user', content: 'Tell me a story' }],
   *   stream: true,
   * });
   * for await (const chunk of stream) {
   *   process.stdout.write(chunk.choices[0]?.delta?.content || '');
   * }
   */
  constructor(options = {}) {
    const {
      gatewayUrl,
      provider,
      traceId,
      sessionId,
      shadowProvider,
      ...openaiOptions
    } = options;

    let baseURL =
      gatewayUrl ||
      process.env.SENTINEL_GATEWAY_URL ||
      DEFAULT_GATEWAY_URL;
    if (!baseURL.endsWith("/v1")) {
      baseURL = baseURL.replace(/\/+$/, "") + "/v1";
    }

    const apiKey =
      openaiOptions.apiKey ||
      process.env.SENTINEL_API_KEY ||
      "sentinel-no-auth";

    // Build Sentinel-specific headers.
    const defaultHeaders = { ...(openaiOptions.defaultHeaders || {}) };
    if (provider) defaultHeaders["x-sentinel-provider"] = provider;
    if (traceId) defaultHeaders["x-sentinel-trace-id"] = traceId;
    if (sessionId) defaultHeaders["x-sentinel-session-id"] = sessionId;
    if (shadowProvider)
      defaultHeaders["x-sentinel-shadow-provider"] = shadowProvider;

    super({
      ...openaiOptions,
      apiKey,
      baseURL,
      defaultHeaders:
        Object.keys(defaultHeaders).length > 0 ? defaultHeaders : undefined,
    });
  }

  /**
   * Return a new client with trace/session headers for per-request observability.
   * @param {string} traceId
   * @param {string} [sessionId]
   * @returns {Sentinel}
   */
  withTrace(traceId, sessionId) {
    const headers = { ...(this._options?.defaultHeaders || {}) };
    headers["x-sentinel-trace-id"] = traceId;
    if (sessionId) headers["x-sentinel-session-id"] = sessionId;
    return new Sentinel({
      gatewayUrl: this.baseURL?.replace(/\/v1$/, ""),
      apiKey: this.apiKey,
      defaultHeaders: headers,
    });
  }
}

/**
 * One-shot convenience function for quick chat completions.
 *
 * @param {string} model
 * @param {Array<{role: string, content: string}>} messages
 * @param {SentinelOptions} [options={}]
 * @returns {Promise<import('openai').ChatCompletion>}
 *
 * @example
 * const { chat } = require('sentinel-ai');
 * const resp = await chat('gpt-4o-mini', [{ role: 'user', content: 'Hi!' }]);
 * console.log(resp.choices[0].message.content);
 */
async function chat(model, messages, options = {}) {
  const client = new Sentinel(options);
  return client.chat.completions.create({ model, messages });
}

/**
 * List all models available on the gateway.
 * @param {string} [gatewayUrl]
 * @returns {Promise<Array<{id: string, owned_by: string}>>}
 */
async function listModels(gatewayUrl) {
  const client = new Sentinel({ gatewayUrl });
  const models = await client.models.list();
  return models.data.map((m) => ({ id: m.id, owned_by: m.owned_by }));
}

module.exports = { Sentinel, chat, listModels };
module.exports.default = Sentinel;
