// S1 — added latency, non-streaming, 0ms mock delay (bench/README.md, plan
// section 3.8). Run once per (gateway, cell, payload, rate) combination and
// once against the mock directly (GATEWAY_URL=mock) as the baseline row.
//
// Usage:
//   k6 run -e GATEWAY_URL=http://localhost:8080 -e RATE=500 -e PAYLOAD=P1 s1_added_latency.js
import http from 'k6/http';
import { check } from 'k6';
import { baseUrl, headers, payload, chatCompletionsPath } from './lib/gateways.js';

const rate = parseInt(__ENV.RATE || '100', 10);
const payloadSize = __ENV.PAYLOAD || 'P1';

export const options = {
  scenarios: {
    s1: {
      executor: 'constant-arrival-rate',
      rate: rate,
      timeUnit: '1s',
      duration: '60s',
      preAllocatedVUs: Math.max(rate, 20),
      maxVUs: rate * 4,
      startTime: '15s', // warm-up discarded per plan section 3.4
    },
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(99)', 'p(99.9)', 'max'],
};

export default function () {
  const res = http.post(`${baseUrl()}${chatCompletionsPath()}`, payload(payloadSize, false), {
    headers: headers(),
  });
  check(res, { 'status is 200': (r) => r.status === 200 });
}
