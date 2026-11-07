// Menghapus (soft delete) akun peserta uji beban yang dibuat exam.js.
//
//   ADMIN_USERNAME=... ADMIN_PASSWORD=... k6 run loadtest/cleanup.js
import http from 'k6/http';
import { fail } from 'k6';

const BASE = (__ENV.BASE_URL || 'http://localhost:8080') + '/api/v1';
const VUS = Number(__ENV.VUS || 500);
const PREFIX = __ENV.USER_PREFIX || 'ujibeban';

export const options = { vus: 1, iterations: 1 };

function adminToken() {
  if (__ENV.ADMIN_TOKEN) return __ENV.ADMIN_TOKEN;
  if (!__ENV.ADMIN_USERNAME || !__ENV.ADMIN_PASSWORD) fail('set ADMIN_USERNAME and ADMIN_PASSWORD (or ADMIN_TOKEN)');
  const slug = http.get(`${BASE}/public/tenant`).json('data.slug');
  const res = http.post(`${BASE}/auth/login`, JSON.stringify({ tenant: slug, username: __ENV.ADMIN_USERNAME, password: __ENV.ADMIN_PASSWORD }),
    { headers: { 'Content-Type': 'application/json' } });
  if (res.status !== 200) fail(`admin login: HTTP ${res.status}`);
  return res.json('data.token');
}

export default function () {
  const h = { headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${adminToken()}` } };
  const names = Array.from({ length: VUS }, (_, i) => `${PREFIX}${String(i + 1).padStart(4, '0')}`);
  const res = http.post(`${BASE}/users/resolve`, JSON.stringify({ identifiers: names }), h);
  if (res.status !== 200) fail(`resolve: HTTP ${res.status}`);
  // only exact username matches of the test prefix are deleted
  const ids = res.json('data.found').filter((u) => u.matched_by === 'username' && u.username.startsWith(PREFIX)).map((u) => u.id);
  let deleted = 0;
  for (let i = 0; i < ids.length; i += 25) {
    for (const r of http.batch(ids.slice(i, i + 25).map((id) => ['DELETE', `${BASE}/users/${id}`, null, h]))) {
      if (r.status === 200 || r.status === 204) deleted++;
    }
  }
  console.log(`deleted ${deleted} of ${ids.length} load-test participants`);
}
