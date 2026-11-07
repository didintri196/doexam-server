// Uji beban monitor saja: PROCTORS pengawas me-refresh monitor sebuah sesi yang sudah ada
// tiap 5 detik. Tiap iterasi bergantian membuka satu halaman tampilan (10 kartu) atau daftar
// pantauan 25 peserta (DOCS/PLAN/15-MONITOR-KARTU.md). Lihat loadtest/README.md.
//
//   k6 run -e BASE_URL=http://localhost:8080 -e ADMIN_TOKEN=... -e SESSION_ID=... -e PROCTORS=50 loadtest/monitor.js
import http from 'k6/http';
import exec from 'k6/execution';
import { check, fail, sleep } from 'k6';
import { Trend } from 'k6/metrics';

const BASE = (__ENV.BASE_URL || 'http://localhost:8080') + '/api/v1';
const PROCTORS = Number(__ENV.PROCTORS || 50);
const DURATION = __ENV.DURATION || '60s';
const VIEWS = ['violations', 'idle', 'all', 'in_progress', 'locked', 'watch'];
const tMonitor = new Trend('lt_monitor', true);

export const options = {
  scenarios: { proctors: { executor: 'constant-vus', vus: PROCTORS, duration: DURATION, exec: 'proctor' } },
  thresholds: { lt_monitor: ['p(95)<200'] },
};

const auth = (token) => ({ headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` } });

export function setup() {
  const token = __ENV.ADMIN_TOKEN;
  if (!token || !__ENV.SESSION_ID) fail('set ADMIN_TOKEN and SESSION_ID');
  const res = http.get(`${BASE}/sessions/${__ENV.SESSION_ID}/monitor/list?view=all&page=1&per_page=25`, auth(token));
  if (res.status !== 200) fail(`list: HTTP ${res.status} ${res.body}`);
  return { token, session: __ENV.SESSION_ID, watch: res.json('data.rows').map((r) => r.user_id) };
}

export function proctor(data) {
  // spread the first polls over the 5 s so the proctors do not all refresh in lockstep
  if (exec.vu.iterationInScenario === 0) sleep(Math.random() * 5);
  const view = VIEWS[(exec.vu.idInTest + exec.vu.iterationInScenario) % VIEWS.length];
  const res = view === 'watch'
    ? http.post(`${BASE}/sessions/${data.session}/monitor/watch`, JSON.stringify({ user_ids: data.watch }), Object.assign({ tags: { name: 'monitor' } }, auth(data.token)))
    : http.get(`${BASE}/sessions/${data.session}/monitor/list?view=${view}&page=1&per_page=10`, Object.assign({ tags: { name: 'monitor' } }, auth(data.token)));
  tMonitor.add(res.timings.duration);
  check(res, { 'monitor 200': (r) => r.status === 200 });
  sleep(5);
}
