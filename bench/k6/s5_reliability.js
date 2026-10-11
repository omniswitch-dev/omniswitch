// S5 — reliability under injected upstream failure (bench/README.md, plan
// section 3.8). This script only generates the constant-rate load; the
// scheduled fault windows (30s 429, 30s 50% 500, 30s reset, 30s stall) are
// opened/closed by bench/scripts/run-cell.sh calling the mock's
// POST /admin/fault directly, so the same load profile runs identically
// across "before fault", "during fault", and "recovery" phases.
import http from 'k6/http';
import { check } from 'k6';
import { baseUrl, headers, payload, chatCompletionsPath } from './lib/gateways.js';

export const options = {
  scenarios: {
    s5: {
      executor: 'constant-arrival-rate',
      rate: parseInt(__ENV.RATE || '200', 10),
      timeUnit: '1s',
      duration: __ENV.DURATION || '150s', // covers all four fault windows + recovery
      preAllocatedVUs: 100,
      maxVUs: 400,
    },
  },
};

export default function () {
  const res = http.post(`${baseUrl()}${chatCompletionsPath()}`, payload('P1', false), { headers: headers() });
  check(res, { 'status is 200 or documented error': (r) => r.status === 200 || r.status === 429 || r.status >= 500 });
}
