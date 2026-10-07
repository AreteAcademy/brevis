// The gateway under load: what a producer sees.
//
// This half measures the CLIENT's view -- latency, throughput, and which
// answers came back. The other half is scraped off the gateway's own
// /metrics when the run ends, and the report puts them side by side. Neither
// is the whole truth on its own:
//
//   k6 says      "I sent 5,000 requests and 4,930 were accepted"
//   /metrics say "I delivered 4,930 and buried none"
//
// A load test that only has the first number cannot tell a fast gateway from
// one that answers 202 and drops what it accepted.
import http from 'k6/http';
import { check } from 'k6';
import { Counter, Trend } from 'k6/metrics';

const URL = __ENV.BREVIS_BENCH_URL || 'http://host.docker.internal:8080/v1/ingestion';
const KEY = __ENV.BREVIS_BENCH_KEY || 'bench';
const PER = parseInt(__ENV.BREVIS_BENCH_PER_REQUEST || '200', 10);
const TABLES = parseInt(__ENV.BREVIS_BENCH_TABLES || '1', 10);

// Counted here rather than derived from the status code, because a 503 is not
// a failure: it is the gateway refusing an event it has nowhere to put, which
// is the honest answer and the one behaviour this service exists to have.
// Folding it into `http_req_failed` would make backpressure look like a bug.
const accepted = new Counter('brevis_events_accepted');
const refused = new Counter('brevis_events_refused_503');
const rejected = new Counter('brevis_events_rejected');
const bodyBytes = new Counter('brevis_body_bytes');
const perEvent = new Trend('brevis_us_per_event', true);

export const options = {
  // p99 is NOT in k6's default summary -- med, p(90) and p(95) are -- and a
  // report asking for a stat that was never computed prints 0.0 and looks
  // like a fast tail. Asked for explicitly here.
  summaryTrendStats: ['min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max', 'avg'],
  scenarios: {
    ramp: {
      executor: 'ramping-vus',
      startVUs: 1,
      stages: [
        { duration: __ENV.BREVIS_BENCH_RAMP || '10s', target: parseInt(__ENV.BREVIS_BENCH_VUS || '16', 10) },
        { duration: __ENV.BREVIS_BENCH_HOLD || '30s', target: parseInt(__ENV.BREVIS_BENCH_VUS || '16', 10) },
        { duration: '5s', target: 0 },
      ],
      gracefulRampDown: '5s',
    },
  },
  // Thresholds make this a GATE and not a number somebody reads. A benchmark
  // whose only output is a chart is a benchmark nobody notices regressing.
  //
  // They are deliberately loose: this runs on whatever machine CI gave us,
  // against a Postgres in a container on the same kernel. They catch an order
  // of magnitude, which is what a regression looks like, and not a percent.
  thresholds: {
    // The accept path must stay off the sink's latency. If this fails, a
    // request is waiting for a COPY somewhere -- the defect the async pipe
    // was built against.
    'http_req_duration{expected_response:true}': ['p(95)<500'],
    // A refusal is fine. A connection error, a timeout or a 5xx that is not a
    // 503 is not.
    'http_req_failed': ['rate<0.01'],
    // And the run has to actually land something, or a threshold suite can
    // pass against a gateway that answered nothing.
    'brevis_events_accepted': ['count>1000'],
  },
};

function body() {
  const t = TABLES > 1 ? Math.floor(Math.random() * TABLES) : 0;
  const seed = Math.floor(Math.random() * 1e9);
  const lines = new Array(PER);
  for (let i = 0; i < PER; i++) {
    lines[i] = JSON.stringify({
      table_name: `app_bench_${t}`,
      operation: 'INSERT',
      data: {
        id: `${seed}-${i}`,
        total: '1500.25',
        status: 'PAID',
        customer: { id: 7741, uf: 'SP', nome: 'Fulano de Tal' },
        items: [{ sku: 'X-1', qtd: 2 }, { sku: 'Y-9', qtd: 1 }],
      },
    });
  }
  return lines.join('\n') + '\n';
}

export default function () {
  const payload = body();
  const started = Date.now();
  const res = http.post(URL, payload, {
    headers: { Authorization: `Bearer ${KEY}`, 'Content-Type': 'application/x-ndjson' },
  });
  bodyBytes.add(payload.length);

  if (res.status === 202) {
    let answer = {};
    try { answer = res.json(); } catch (e) { answer = {}; }
    const ok = answer.accepted || 0;
    accepted.add(ok);
    rejected.add((answer.rejected || []).length);
    if (ok > 0) perEvent.add(((Date.now() - started) * 1000) / ok);
  } else if (res.status === 503) {
    // Backpressure, and it is safe to retry: the ingestion_id is a frozen
    // function of the event, so the same body sent again is the same record.
    refused.add(PER);
  }

  check(res, {
    'answered 202 or 503': (r) => r.status === 202 || r.status === 503,
  });
}

// The machine-readable half. `stdout` keeps the human summary; the JSON is
// what the report is built from, so a report is never somebody retyping
// numbers off a terminal.
export function handleSummary(data) {
  return {
    stdout: textSummary(data),
    '/results/k6.json': JSON.stringify(data, null, 2),
  };
}

// k6's own summary renderer is not importable from a pinned URL without
// network access at run time, so this is the short version of it: enough to
// read in a terminal, with the file next door carrying everything.
function textSummary(data) {
  const m = data.metrics;
  const n = (k, s) => (m[k] && m[k].values && m[k].values[s] !== undefined ? m[k].values[s] : 0);
  const lines = [
    '',
    `  requests          ${n('http_reqs', 'count')}  (${n('http_reqs', 'rate').toFixed(0)}/s)`,
    `  events accepted   ${n('brevis_events_accepted', 'count')}`,
    `  events refused    ${n('brevis_events_refused_503', 'count')}  (503, safe to retry)`,
    `  events rejected   ${n('brevis_events_rejected', 'count')}  (per-event, with a reason)`,
    `  body bytes        ${(n('brevis_body_bytes', 'count') / 1048576).toFixed(1)} MiB`,
    '',
    `  http_req_duration p50 ${n('http_req_duration', 'med').toFixed(1)}ms  ` +
      `p95 ${n('http_req_duration', 'p(95)').toFixed(1)}ms  ` +
      `p99 ${n('http_req_duration', 'p(99)').toFixed(1)}ms  ` +
      `max ${n('http_req_duration', 'max').toFixed(1)}ms`,
    '',
  ];
  return lines.join('\n');
}
