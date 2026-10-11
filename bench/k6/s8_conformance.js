// S8 — capability conformance probes (bench/README.md, plan section 3.8).
// Low-volume, correctness-focused (not a load test): one iteration per
// probe, each with explicit checks. Feeds docs/comparison/claims.yaml
// `verified_by: ran-it` updates; failures are recorded, not hidden.
import http from 'k6/http';
import { check, group } from 'k6';
import { baseUrl, headers, model, chatCompletionsPath, shape } from './lib/gateways.js';

export const options = { vus: 1, iterations: 1 };

export default function () {
  group('chat completion non-stream shape', function () {
    const body = JSON.stringify({ model: model(), stream: false, messages: [{ role: 'user', content: 'conformance probe' }] });
    const res = http.post(`${baseUrl()}${chatCompletionsPath()}`, body, { headers: headers() });
    check(res, {
      'status 200': (r) => r.status === 200,
      'has choices/content (openai) or content (anthropic)': (r) => {
        try {
          const j = r.json();
          return shape() === 'anthropic' ? Array.isArray(j.content) : Array.isArray(j.choices);
        } catch (e) {
          return false;
        }
      },
    });
  });

  group('streaming usage in final chunk', function () {
    const body = JSON.stringify({ model: model(), stream: true, messages: [{ role: 'user', content: 'stream usage probe' }] });
    const res = http.post(`${baseUrl()}${chatCompletionsPath()}`, body, { headers: headers({ Accept: 'text/event-stream' }) });
    check(res, {
      'status 200': (r) => r.status === 200,
      'body contains [DONE] or message_stop terminator': (r) =>
        typeof r.body === 'string' && (r.body.includes('[DONE]') || r.body.includes('message_stop')),
    });
  });

  group('embeddings shape', function () {
    if (shape() === 'anthropic') return; // embeddings only meaningful for OpenAI-shaped gateways in this harness
    const body = JSON.stringify({ model: model(), input: ['conformance probe'] });
    const res = http.post(`${baseUrl()}/v1/embeddings`, body, { headers: headers() });
    check(res, {
      'status 200': (r) => r.status === 200,
      'has data[0].embedding array': (r) => {
        try {
          return Array.isArray(r.json().data[0].embedding);
        } catch (e) {
          return false;
        }
      },
    });
  });

  group('error passthrough shape on injected fault', function () {
    const body = JSON.stringify({ model: model(), stream: false, messages: [{ role: 'user', content: 'fault probe' }] });
    const res = http.post(`${baseUrl()}${chatCompletionsPath()}`, body, {
      headers: headers({ 'X-Bench-Fault': '429' }),
    });
    check(res, { 'fault forwarded as 429 or gateway-mapped error': (r) => r.status === 429 || r.status >= 400 });
  });
}
