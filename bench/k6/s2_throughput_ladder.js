// S2 — sustained throughput ladder (bench/README.md, plan section 3.8).
// Steps through fixed arrival rates; run-cell.sh inspects the JSON summary
// per step to find the saturation point (first step with p99 > 10x floor
// or error rate > 0.1%) rather than headlining a single max-QPS number.
import http from 'k6/http';
import { check } from 'k6';
import { baseUrl, headers, payload, chatCompletionsPath } from './lib/gateways.js';

const rates = [500, 1000, 2000, 4000, 8000];
const scenarios = {};
let cursor = 0;
for (const r of rates) {
  scenarios[`step_${r}`] = {
    executor: 'constant-arrival-rate',
    rate: r,
    timeUnit: '1s',
    duration: '75s', // 15s warm-up + 60s measured, discard first 15s in analysis
    preAllocatedVUs: 64,
    maxVUs: Math.max(64, r),
    startTime: `${cursor}s`,
  };
  cursor += 75;
}

export const options = { scenarios, summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(99)', 'max'] };

export default function () {
  const res = http.post(`${baseUrl()}${chatCompletionsPath()}`, payload('P1', false), { headers: headers() });
  check(res, { 'status is 200': (r) => r.status === 200 });
}
