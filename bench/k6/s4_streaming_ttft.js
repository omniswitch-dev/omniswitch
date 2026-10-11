// S4 — streaming TTFT and chunk fidelity (bench/README.md, plan section
// 3.8/3.4). k6 cannot see individual SSE chunks, so this script only
// captures aggregate timings (TTFB via res.timings.waiting, total stream
// duration) under concurrent load. Per-chunk inter-arrival timing and
// chunk-count fidelity (must be 1:1 against the mock's configured cadence)
// require bench/cmd/sse-probe instead — run-cell.sh runs both and reports
// them side by side; do not publish an S4 table from this script alone.
import http from 'k6/http';
import { check } from 'k6';
import { baseUrl, headers, payload, chatCompletionsPath } from './lib/gateways.js';

const concurrency = parseInt(__ENV.CONCURRENCY || '50', 10);

export const options = {
  scenarios: {
    s4: {
      executor: 'constant-vus',
      vus: concurrency,
      duration: '60s',
      startTime: '15s',
    },
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(99)', 'max'],
};

export default function () {
  const res = http.post(`${baseUrl()}${chatCompletionsPath()}`, payload('P1', true), {
    headers: headers({ Accept: 'text/event-stream' }),
  });
  check(res, { 'status is 200': (r) => r.status === 200 });
  // res.timings.waiting approximates TTFB; k6 aggregates it automatically
  // into the run summary as http_req_waiting.
}
