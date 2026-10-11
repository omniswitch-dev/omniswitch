// S6 — cache behavior (bench/README.md, plan section 3.8). Sends P1 with a
// controlled duplicate ratio: each VU/iteration deterministically reuses
// one of a small pool of prompts so DUPE_RATIO of requests are exact
// repeats (cache hits, if the gateway's cache is enabled for this cell)
// and the rest are unique. Compare mock's /stats request-count delta to
// client-observed request count to verify cache correctness independent
// of client-reported latency.
import http from 'k6/http';
import { check } from 'k6';
import { baseUrl, headers, chatCompletionsPath, model, shape } from './lib/gateways.js';

const dupeRatio = parseFloat(__ENV.DUPE_RATIO || '0.5'); // 0, 0.5, or 0.9 per plan
const poolSize = parseInt(__ENV.POOL_SIZE || '10', 10);

export const options = {
  scenarios: {
    s6: {
      executor: 'constant-arrival-rate',
      rate: parseInt(__ENV.RATE || '100', 10),
      timeUnit: '1s',
      duration: '60s',
      preAllocatedVUs: 50,
      maxVUs: 200,
      startTime: '15s',
    },
  },
};

function dupeOrUniquePayload(iterId) {
  const useDupe = Math.random() < dupeRatio;
  const seed = useDupe ? iterId % poolSize : `unique-${iterId}-${Math.random()}`;
  const body = { model: model(), stream: false, messages: [{ role: 'user', content: `cache probe ${seed}` }] };
  if (shape() === 'anthropic') body.max_tokens = 64;
  return JSON.stringify(body);
}

export default function () {
  const res = http.post(`${baseUrl()}${chatCompletionsPath()}`, dupeOrUniquePayload(__ITER), { headers: headers() });
  check(res, { 'status is 200': (r) => r.status === 200 });
}
