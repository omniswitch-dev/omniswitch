// lib/gateways.js — shared k6 helpers for the OmniSwitch competitive
// benchmark harness (bench/README.md). Kept intentionally small: gateway
// URL/headers come from environment variables so the same scripts run
// against every gateway in bench/gateways/ without per-gateway JS.
//
// Env vars (see bench/scripts/run-cell.sh for how these are set per run):
//   GATEWAY_URL        base URL of the gateway under test, e.g. http://gw:8080
//   GATEWAY_API_KEY    bearer key for the production-default cell (empty for forwarding)
//   MOCK_ADMIN_URL      base URL of the mock upstream's /admin endpoints (S5, S6)
//   BENCH_MODEL         logical model name routed to the mock (default: bench-model)
//   BENCH_SHAPE          "openai" or "anthropic" (default: openai)
//   BENCH_PORTKEY_CONFIG optional path to a Portkey x-portkey-config JSON document

export function baseUrl() {
  return __ENV.GATEWAY_URL || 'http://localhost:8080';
}

export function model() {
  return __ENV.BENCH_MODEL || 'bench-model';
}

export function shape() {
  return __ENV.BENCH_SHAPE || 'openai';
}

export function headers(extra) {
  const h = Object.assign({ 'Content-Type': 'application/json' }, extra || {});
  if (__ENV.GATEWAY_API_KEY) {
    h['Authorization'] = 'Bearer ' + __ENV.GATEWAY_API_KEY;
    h['x-api-key'] = __ENV.GATEWAY_API_KEY;
  }
  if (__ENV.BENCH_PORTKEY_CONFIG) {
    h['x-portkey-config'] = open(__ENV.BENCH_PORTKEY_CONFIG);
  }
  return h;
}

// chatPath/anthropicPath let each gateway's forwarding.yaml route path
// stay the vendor default; override with BENCH_PATH if a gateway's harness
// route differs (e.g. Kong's /bench prefix, see bench/gateways/kong-ai-gateway).
export function chatCompletionsPath() {
  return __ENV.BENCH_PATH || (shape() === 'anthropic' ? '/v1/messages' : '/v1/chat/completions');
}

export function payload(sizeLabel, streamFlag, dupeSeed) {
  const sizes = { P1: 1, P2: 8, P3: 40 };
  const turns = sizes[sizeLabel] || 1;
  const messages = [];
  for (let i = 0; i < turns; i++) {
    messages.push({ role: i % 2 === 0 ? 'user' : 'assistant', content: `bench turn ${i} seed ${dupeSeed || i}` });
  }
  const body = { model: model(), stream: !!streamFlag, messages };
  if (shape() === 'anthropic') {
    body.max_tokens = 64;
  }
  return JSON.stringify(body);
}
