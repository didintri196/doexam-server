// Uji beban DoExam: N peserta login, memulai attempt, lalu mengirim heartbeat dan jawaban
// seperti exam client (sync tiap 15 detik, kira-kira satu jawaban per 30 detik), sementara
// beberapa proktor memantau monitor sesi tiap 5 detik. Lihat loadtest/README.md.
//
//   ADMIN_USERNAME=... ADMIN_PASSWORD=... EXAM_ID=... k6 run loadtest/exam.js
import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import exec from 'k6/execution';
import { Counter, Trend } from 'k6/metrics';

const BASE = (__ENV.BASE_URL || 'http://localhost:8080') + '/api/v1';
const VUS = Number(__ENV.VUS || 500);
const MINUTES = Number(__ENV.MINUTES || 5); // exam time after the login wave
const RAMP = Number(__ENV.RAMP_SECONDS || 60); // participants arrive over this many seconds
const PROCTORS = Number(__ENV.PROCTORS || 2);
// views (default): the paged monitor views of DOCS/PLAN/15; legacy: every participant per poll
const MONITOR = __ENV.MONITOR || 'views';
const VIEWS = ['violations', 'idle', 'all', 'in_progress', 'locked'];
const PREFIX = __ENV.USER_PREFIX || 'ujibeban';
// test accounts on a development server only (created by setup, removed by cleanup.js)
const PASSWORD = __ENV.PARTICIPANT_PASSWORD || 'UjiBeban-2026!';

const tLogin = new Trend('lt_login', true);
const tStart = new Trend('lt_start', true);
const tHeartbeat = new Trend('lt_sync_heartbeat', true);
const tAnswer = new Trend('lt_sync_answer', true);
const tMonitor = new Trend('lt_monitor', true);
const answersSent = new Counter('lt_answers_sent');
const answersAccepted = new Counter('lt_answers_accepted');
const eventsRejected = new Counter('lt_events_rejected');

export const options = {
  setupTimeout: '15m',
  teardownTimeout: '2m',
  scenarios: {
    participants: {
      executor: 'per-vu-iterations', vus: VUS, iterations: 1, exec: 'participant',
      maxDuration: `${RAMP + MINUTES * 60 + 180}s`,
    },
    proctors: { executor: 'constant-vus', vus: PROCTORS, duration: `${RAMP + MINUTES * 60}s`, exec: 'proctor' },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    lt_events_rejected: ['count==0'],
    lt_sync_heartbeat: ['p(95)<300'],
    lt_sync_answer: ['p(95)<500'],
    lt_start: ['p(95)<2000'],
  },
};

function uuid() {
  // RFC 4122 v4 from Math.random: enough for client_event_id / device ids in a test
  const h = '0123456789abcdef';
  let s = '';
  for (let i = 0; i < 36; i++) {
    if (i === 8 || i === 13 || i === 18 || i === 23) s += '-';
    else if (i === 14) s += '4';
    else if (i === 19) s += h[(Math.random() * 4) | 8];
    else s += h[(Math.random() * 16) | 0];
  }
  return s;
}

const username = (n) => `${PREFIX}${String(n).padStart(4, '0')}`;
const json = (token, extra) => ({
  headers: Object.assign({ 'Content-Type': 'application/json', Authorization: `Bearer ${token}` }, extra || {}),
});

function must(res, what, ok = [200, 201]) {
  if (!ok.includes(res.status)) fail(`${what}: HTTP ${res.status} ${res.body && res.body.slice(0, 300)}`);
  return res.json('data');
}

// adminToken signs in the staff account that runs the test (needs user.manage and
// session management), unless ADMIN_TOKEN is given.
function adminToken(slug) {
  if (__ENV.ADMIN_TOKEN) return __ENV.ADMIN_TOKEN;
  if (!__ENV.ADMIN_USERNAME || !__ENV.ADMIN_PASSWORD) fail('set ADMIN_USERNAME and ADMIN_PASSWORD (or ADMIN_TOKEN)');
  const res = http.post(`${BASE}/auth/login`, JSON.stringify({ tenant: slug, username: __ENV.ADMIN_USERNAME, password: __ENV.ADMIN_PASSWORD }),
    { headers: { 'Content-Type': 'application/json' } });
  return must(res, 'admin login').token;
}

export function setup() {
  const examID = __ENV.EXAM_ID;
  if (!examID) fail('set EXAM_ID (a published exam)');
  const slug = must(http.get(`${BASE}/public/tenant`), 'public tenant').slug;
  const admin = adminToken(slug);

  // participants: created once and reused by later runs (an existing username is fine)
  const names = Array.from({ length: VUS }, (_, i) => username(i + 1));
  for (let i = 0; i < names.length; i += 25) {
    const reqs = names.slice(i, i + 25).map((u) => ['POST', `${BASE}/users`, JSON.stringify({
      username: u, full_name: `Peserta Uji Beban ${u.slice(PREFIX.length)}`, password: PASSWORD,
      role_codes: ['participant'], must_change_password: false,
    }), json(admin)]);
    for (const res of http.batch(reqs)) {
      if (res.status !== 201 && res.status !== 200 && !(res.status === 400 && /already in use/.test(res.body))) {
        fail(`create participant: HTTP ${res.status} ${res.body}`);
      }
    }
  }
  const resolved = must(http.post(`${BASE}/users/resolve`, JSON.stringify({ identifiers: names }), json(admin)), 'resolve');
  if (resolved.found.length !== VUS) fail(`resolved ${resolved.found.length} of ${VUS} participants`);

  const now = new Date();
  const session = must(http.post(`${BASE}/sessions`, JSON.stringify({
    exam_id: examID, name: `Uji beban ${now.toISOString()}`, starts_at: now.toISOString(),
    ends_at: new Date(now.getTime() + 3 * 3600e3).toISOString(), token_required: false,
  }), json(admin)), 'create session');
  must(http.post(`${BASE}/sessions/${session.id}/participants`,
    JSON.stringify({ user_ids: resolved.found.map((u) => u.id) }), json(admin)), 'add participants');
  must(http.post(`${BASE}/sessions/${session.id}/status`, JSON.stringify({ status: 'open' }), json(admin)), 'open session');
  sleep(3); // opening warms the exam's plan and item content in the background
  console.log(`session ${session.id} open with ${VUS} participants`);
  return { slug, sessionID: session.id, admin };
}

function answer(item) {
  const opts = item.options || [];
  switch (item.type) {
    case 'mcq_single': return { selected: opts[0].id };
    case 'mcq_multi': return { selected: [opts[0].id] };
    case 'true_false': return { value: true };
    default: return { text: 'jawaban uji beban' }; // short_answer, essay
  }
}

export function participant(data) {
  // __VU numbers VUs across all scenarios (proctors too); the iteration index is 0..VUS-1 here
  const me = username(exec.scenario.iterationInTest + 1);
  sleep(Math.random() * RAMP);
  const device = uuid();

  const login = http.post(`${BASE}/auth/login`, JSON.stringify({ tenant: data.slug, username: me, password: PASSWORD }),
    { headers: { 'Content-Type': 'application/json' }, tags: { name: 'login' } });
  tLogin.add(login.timings.duration);
  if (!check(login, { 'login 200': (r) => r.status === 200 })) return;
  const opts = json(login.json('data.token'), { 'X-Device-ID': device });

  http.get(`${BASE}/my/sessions`, Object.assign({ tags: { name: 'my_sessions' } }, opts));
  const start = http.post(`${BASE}/attempts`, JSON.stringify({ session_id: data.sessionID, token: '' }),
    Object.assign({ tags: { name: 'start' } }, opts));
  tStart.add(start.timings.duration);
  if (!check(start, { 'start 200': (r) => r.status === 200 })) {
    // e.g. 409 "no attempts left": each run uses one attempt per participant (see README)
    console.warn(`${me} start: HTTP ${start.status} ${start.json('message')}`);
    return;
  }
  const view = start.json('data');
  const items = view.sections.flatMap((s) => s.items);

  let seq = 0;
  let next = 0;
  const endAt = Date.now() + MINUTES * 60e3;
  while (Date.now() < endAt) {
    sleep(14 + Math.random() * 2);
    const events = [];
    if (next < items.length && Math.random() < 0.5) {
      const it = items[next++];
      events.push({
        client_event_id: uuid(), client_seq: ++seq, type: 'RESPONSE_SAVED', attempt_item_id: it.id,
        payload: { response: answer(it) }, client_ts: new Date().toISOString(),
      });
    }
    const res = http.post(`${BASE}/attempts/${view.id}/events`, JSON.stringify({ events }),
      Object.assign({ tags: { name: events.length ? 'sync_answer' : 'sync_heartbeat' } }, opts));
    (events.length ? tAnswer : tHeartbeat).add(res.timings.duration);
    if (!check(res, { 'sync 200': (r) => r.status === 200 })) continue;
    const out = res.json('data');
    if (events.length) {
      answersSent.add(1);
      answersAccepted.add(out.accepted);
    }
    if (out.rejected.length) {
      eventsRejected.add(out.rejected.length);
      console.warn(`${me} rejected: ${JSON.stringify(out.rejected)}`);
    }
  }
}

export function proctor(data) {
  const url = MONITOR === 'legacy'
    ? `${BASE}/sessions/${data.sessionID}/monitor`
    : `${BASE}/sessions/${data.sessionID}/monitor/list?view=${VIEWS[exec.scenario.iterationInTest % VIEWS.length]}&page=1&per_page=10`;
  const res = http.get(url, Object.assign({ tags: { name: 'monitor' } }, json(data.admin)));
  tMonitor.add(res.timings.duration);
  check(res, { 'monitor 200': (r) => r.status === 200 });
  sleep(5);
}

export function teardown(data) {
  // closing submits every running attempt, like a proctor ending the session
  const res = http.post(`${BASE}/sessions/${data.sessionID}/status`, JSON.stringify({ status: 'closed' }), json(data.admin));
  console.log(`session ${data.sessionID} closed: HTTP ${res.status}`);
}
