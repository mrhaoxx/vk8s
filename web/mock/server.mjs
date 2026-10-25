// 开发用 mock API：不依赖集群即可运行控制台。只用 Node 内建模块。
//
//   PORT=8080 node mock/server.mjs      # 与 vite.config.ts 的代理目标一致
//
// 行为按 internal/manager/api 的真实实现：会话 cookie、非 GET 必须带 X-Requested-With: vk8s、
// admin 专属端点对非 admin 返回 403、不可见的集群返回 404。变更在内存中生效，重启还原。
import http from 'node:http';
import { randomBytes } from 'node:crypto';
import { API_DOMAIN, APPS_DOMAIN, PASSWORD, addonInstalls, addonTemplates, auditEvents, bookings, config, nodes, reservedNames, tokens, users, vclusters } from './fixtures.mjs';

const PORT = Number(process.env.PORT) || 8080;
const COOKIE = 'vk8s_session';
const DNS_LABEL = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;

// ---- 工具 ----

function json(res, status, body, headers = {}) {
  const data = body === undefined ? '' : JSON.stringify(body);
  res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store', ...headers });
  res.end(data);
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      const raw = Buffer.concat(chunks).toString('utf8');
      if (!raw) return resolve({});
      try {
        resolve(JSON.parse(raw));
      } catch {
        reject(new Error('invalid JSON'));
      }
    });
    req.on('error', reject);
  });
}

function cookies(req) {
  const out = {};
  for (const part of (req.headers.cookie ?? '').split(';')) {
    const [k, ...v] = part.trim().split('=');
    if (k) out[k] = decodeURIComponent(v.join('='));
  }
  return out;
}

function currentUser(req) {
  const name = cookies(req)[COOKIE];
  const u = users.find((x) => x.name === name);
  return u && !u.disabled ? u : null;
}

const isAdmin = (u) => u.role === 'admin';
const canView = (vc, u) => isAdmin(u) || vc.owner === u.name || vc.members.includes(u.name);
const myRole = (vc, u) => (vc.owner === u.name ? 'owner' : vc.members.includes(u.name) ? 'member' : isAdmin(u) ? 'admin' : '');
const HOUR_MS = 3_600_000;
const isBookable = (n) => n.labels?.['vk8s.io/bookable'] === 'true';
// 预约 phase 按请求时刻计算（Rejected 固定不变），与真实控制器的规则一致。
function phaseOf(b, nowMs = Date.now()) {
  if (b.fixedPhase) return b.fixedPhase;
  // 真实后端：控制器写 status 之前 phase 为空，这里模拟创建后 2 秒的空档
  if (b.createdMs && nowMs - b.createdMs < 2000) return '';
  const s = Date.parse(b.start), e = Date.parse(b.end);
  if (nowMs < s) return 'Pending';
  if (nowMs < e) return 'Active';
  return 'Completed';
}
const canDeleteBooking = (b, u) => isAdmin(u) || b.user === u.name || vclusters.some((vc) => vc.name === b.vcluster && vc.owner === u.name);
const bookingView = (b, u) => ({ name: b.name, node: b.node, vcluster: b.vcluster, user: b.user, purpose: b.purpose, start: b.start, end: b.end, phase: phaseOf(b), reason: b.reason ?? '', createdAt: b.createdAt, canDelete: canDeleteBooking(b, u) });
const leasedNodesOf = (vcName) => bookings.filter((b) => b.vcluster === vcName && phaseOf(b) === 'Active').map((b) => ({ node: b.node, booking: b.name, until: b.end })).sort((a, b) => a.node.localeCompare(b.node));
const view = (vc, u) => ({ ...vc, status: { ...vc.status, leasedNodes: leasedNodesOf(vc.name) }, myRole: myRole(vc, u) });
const tokenView = ({ id, user, description, createdAt, expiresAt }) => ({ id, user, description, createdAt, expiresAt });

function parseHours(ttl) {
  if (!ttl) return 720;
  const m = /^(\d+(?:\.\d+)?)(h|m|s)$/.exec(String(ttl).trim());
  if (!m) return null;
  const n = Number(m[1]);
  return m[2] === 'h' ? n : m[2] === 'm' ? n / 60 : n / 3600;
}

function kubeconfig(name, user) {
  return [
    'apiVersion: v1',
    'kind: Config',
    'clusters:',
    `- name: ${name}`,
    '  cluster:',
    `    server: https://${name}.${API_DOMAIN}`,
    '    certificate-authority-data: LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1PQ0sKLS0tLS1FTkQgQ0VSVElGSUNBVEUtLS0tLQo=',
    'users:',
    `- name: ${user}@${name}`,
    '  user:',
    `    token: vk8s_${randomBytes(24).toString('base64url')}`,
    'contexts:',
    `- name: ${name}`,
    '  context:',
    `    cluster: ${name}`,
    `    user: ${user}@${name}`,
    '    namespace: default',
    `current-context: ${name}`,
    '',
  ].join('\n');
}

// ---- 路由 ----

const routes = [];
const route = (method, pattern, handler, { auth = true, admin = false } = {}) => routes.push({ method, pattern, handler, auth, admin });

route('GET', /^\/auth\/me$/, (req, res, { me }) => {
  if (!me) return json(res, 401, { error: 'unauthenticated', oidcEnabled: false });
  const { name, displayName, email, role } = me;
  json(res, 200, { user: { name, displayName, email, role }, oidcEnabled: false });
}, { auth: false });

route('POST', /^\/auth\/login$/, async (req, res) => {
  const { username, password } = await readBody(req);
  const u = users.find((x) => x.name === String(username ?? '').trim());
  if (!u || u.disabled || password !== PASSWORD) return json(res, 401, { error: 'invalid username or password' });
  u.lastLoginTime = new Date().toISOString();
  json(res, 204, undefined, { 'Set-Cookie': `${COOKIE}=${u.name}; Path=/; HttpOnly; SameSite=Lax` });
}, { auth: false });

route('POST', /^\/auth\/logout$/, (req, res) => {
  json(res, 204, undefined, { 'Set-Cookie': `${COOKIE}=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax` });
}, { auth: false });

route('GET', /^\/auth\/oidc\/start$/, (req, res) => json(res, 404, { error: 'oidc is not enabled' }), { auth: false });

route('GET', /^\/api\/v1\/config$/, (req, res) => json(res, 200, config));

route('GET', /^\/api\/v1\/vclusters$/, (req, res, { me }) => {
  const items = vclusters.filter((vc) => canView(vc, me)).map((vc) => view(vc, me)).sort((a, b) => a.name.localeCompare(b.name));
  json(res, 200, { items });
});

route('POST', /^\/api\/v1\/vclusters$/, async (req, res, { me }) => {
  const body = await readBody(req);
  const name = String(body.name ?? '').trim();
  if (!DNS_LABEL.test(name) || name.length > 30) return json(res, 400, { error: `name "${name}": must be a lowercase DNS label of at most 30 characters` });
  if (name.includes('--')) return json(res, 400, { error: `name "${name}" must not contain "--"` });
  if (reservedNames.includes(name)) return json(res, 400, { error: `name "${name}" is reserved` });
  if (vclusters.some((vc) => vc.name === name)) return json(res, 409, { error: `virtual cluster "${name}" already exists` });
  const owned = vclusters.filter((vc) => vc.owner === me.name && vc.status.phase !== 'Deleting').length;
  if (!isAdmin(me) && owned >= config.limits.maxVClustersPerUser) {
    return json(res, 403, { error: `you already own ${owned} virtual clusters (limit ${config.limits.maxVClustersPerUser})` });
  }
  const vc = {
    name,
    owner: me.name,
    members: [],
    quota: (isAdmin(me) && body.quota) || structuredClone(config.defaults.quota),
    nodePlacement: (isAdmin(me) && body.nodePlacement) || { nodeSelector: {}, nodeNames: [], tolerations: [] },
    podSecurity: (isAdmin(me) && body.podSecurity) || config.defaults.podSecurity,
    controlPlane: { storageSize: '5Gi', storageClassName: '' },
    status: { phase: 'Provisioning', reason: 'ControlPlaneNotReady', hostNamespace: `vc-${name}`, apiEndpoint: '', appsDomain: '', conditions: [] },
    createdAt: new Date().toISOString(),
  };
  const wanted = Array.isArray(body.addons) ? body.addons : [];
  for (const a of wanted) {
    const t = addonTemplates.find((x) => x.name === a.template);
    if (!t) return json(res, 400, { error: `应用 "${a.template}" 不在目录中` });
    if (!requiresOK(t, vc.podSecurity)) return json(res, 400, { error: `应用 ${t.displayName}：需要 ${t.requires.podSecurity} 档位` });
  }
  vclusters.push(vc);
  for (const a of wanted) addonInstalls.push(newInstall(vc.name, a.template, a.inputs ?? {}, 18_000));
  setTimeout(() => {
    if (!vclusters.includes(vc)) return;
    const t = new Date().toISOString();
    vc.status = {
      phase: 'Running',
      reason: '',
      hostNamespace: `vc-${name}`,
      apiEndpoint: `https://${name}.${API_DOMAIN}`,
      appsDomain: `${name}.${APPS_DOMAIN}`,
      conditions: ['CertificatesReady', 'ControlPlaneReady', 'SyncerReady'].map((type) => ({ type, status: 'True', reason: 'Ready', message: '', lastTransitionTime: t })),
    };
  }, 10_000).unref();
  json(res, 201, view(vc, me));
});

const findVC = (res, me, name) => {
  const vc = vclusters.find((x) => x.name === name);
  if (!vc || !canView(vc, me)) {
    json(res, 404, { error: 'virtual cluster not found' });
    return null;
  }
  return vc;
};

route('GET', /^\/api\/v1\/vclusters\/([^/]+)$/, (req, res, { me, m }) => {
  const vc = findVC(res, me, m[1]);
  if (vc) json(res, 200, view(vc, me));
});

route('PATCH', /^\/api\/v1\/vclusters\/([^/]+)$/, async (req, res, { me, m }) => {
  const vc = vclusters.find((x) => x.name === m[1]);
  if (!vc) return json(res, 404, { error: 'virtual cluster not found' });
  const body = await readBody(req);
  if (!body.quota && !body.nodePlacement && !body.podSecurity) return json(res, 400, { error: 'nothing to update' });
  if (body.podSecurity && !['privileged', 'baseline', 'restricted'].includes(body.podSecurity)) {
    return json(res, 400, { error: `podSecurity "${body.podSecurity}" must be privileged|baseline|restricted` });
  }
  if (body.quota) vc.quota = { extended: {}, ...body.quota };
  if (body.nodePlacement) vc.nodePlacement = { nodeSelector: {}, nodeNames: [], tolerations: [], ...body.nodePlacement };
  if (body.podSecurity) vc.podSecurity = body.podSecurity;
  json(res, 200, view(vc, me));
}, { admin: true });

route('DELETE', /^\/api\/v1\/vclusters\/([^/]+)$/, (req, res, { me, m }) => {
  const vc = findVC(res, me, m[1]);
  if (!vc) return;
  if (!isAdmin(me) && vc.owner !== me.name) return json(res, 403, { error: 'only the owner or an admin can delete a virtual cluster' });
  vc.status = { ...vc.status, phase: 'Deleting', reason: 'WaitingForNamespaceDeletion' };
  setTimeout(() => {
    const i = vclusters.indexOf(vc);
    if (i >= 0) vclusters.splice(i, 1);
    for (let j = tokens.length - 1; j >= 0; j--) if (tokens[j].vcluster === vc.name) tokens.splice(j, 1);
  }, 5_000).unref();
  json(res, 204);
});

route('PUT', /^\/api\/v1\/vclusters\/([^/]+)\/members\/([^/]+)$/, (req, res, { me, m }) => {
  const vc = findVC(res, me, m[1]);
  if (!vc) return;
  const user = m[2];
  if (!users.some((u) => u.name === user)) return json(res, 404, { error: `user "${user}" not found` });
  if (vc.owner !== user && !vc.members.includes(user)) vc.members.push(user);
  json(res, 204);
});

route('DELETE', /^\/api\/v1\/vclusters\/([^/]+)\/members\/([^/]+)$/, (req, res, { me, m }) => {
  const vc = findVC(res, me, m[1]);
  if (!vc) return;
  if (vc.owner === m[2]) return json(res, 400, { error: 'the owner cannot be removed from members' });
  vc.members = vc.members.filter((x) => x !== m[2]);
  json(res, 204);
});

route('GET', /^\/api\/v1\/vclusters\/([^/]+)\/tokens$/, (req, res, { me, m }) => {
  const vc = findVC(res, me, m[1]);
  if (!vc) return;
  const items = tokens
    .filter((t) => t.vcluster === vc.name && (isAdmin(me) || t.user === me.name))
    .map(tokenView)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  json(res, 200, { items });
});

route('POST', /^\/api\/v1\/vclusters\/([^/]+)\/tokens$/, async (req, res, { me, m }) => {
  const vc = findVC(res, me, m[1]);
  if (!vc) return;
  if (vc.status.phase === 'Deleting') return json(res, 409, { error: 'virtual cluster is being deleted' });
  const body = await readBody(req);
  const hours = parseHours(body.ttl);
  if (hours === null || hours <= 0) return json(res, 400, { error: 'invalid ttl' });
  if (hours > 2160) return json(res, 400, { error: 'invalid ttl: exceeds maximum 2160h0m0s' });
  const t = {
    id: 'tok-' + randomBytes(8).toString('hex'),
    user: me.name,
    vcluster: vc.name,
    description: String(body.description ?? '').trim(),
    createdAt: new Date().toISOString(),
    expiresAt: new Date(Date.now() + hours * 3600 * 1000).toISOString(),
  };
  tokens.push(t);
  json(res, 201, { id: t.id, description: t.description, expiresAt: t.expiresAt, kubeconfig: kubeconfig(vc.name, me.name) });
});

route('DELETE', /^\/api\/v1\/tokens\/([^/]+)$/, (req, res, { me, m }) => {
  const i = tokens.findIndex((t) => t.id === m[1]);
  if (i < 0) return json(res, 404, { error: 'token not found' });
  if (!isAdmin(me) && tokens[i].user !== me.name) return json(res, 403, { error: 'you can only revoke your own tokens' });
  tokens.splice(i, 1);
  json(res, 204);
});

route('GET', /^\/api\/v1\/users$/, (req, res) => json(res, 200, { items: [...users].sort((a, b) => a.name.localeCompare(b.name)) }), { admin: true });

route('POST', /^\/api\/v1\/users$/, async (req, res) => {
  const body = await readBody(req);
  const name = String(body.name ?? '').trim();
  if (!DNS_LABEL.test(name) || name.length > 63) return json(res, 400, { error: 'name: must be a lowercase DNS label' });
  if (users.some((u) => u.name === name)) return json(res, 409, { error: `user "${name}" already exists` });
  if (body.password && body.password.length < 8) return json(res, 400, { error: 'password must be at least 8 characters' });
  const u = {
    name,
    displayName: String(body.displayName ?? '').trim() || name,
    email: String(body.email ?? '').trim(),
    role: body.role === 'admin' ? 'admin' : 'user',
    disabled: false,
    hasStatic: Boolean(body.password),
    hasOIDC: false,
    lastLoginTime: null,
  };
  users.push(u);
  json(res, 201, u);
}, { admin: true });

route('PATCH', /^\/api\/v1\/users\/([^/]+)$/, async (req, res, { me, m }) => {
  const u = users.find((x) => x.name === m[1]);
  if (!u) return json(res, 404, { error: 'user not found' });
  const body = await readBody(req);
  if (u.name === me.name && ((body.role && body.role !== me.role) || body.disabled === true)) {
    return json(res, 400, { error: 'you cannot change your own role or disable yourself' });
  }
  if (body.displayName !== undefined) u.displayName = String(body.displayName).trim();
  if (body.email !== undefined) u.email = String(body.email).trim();
  if (body.role !== undefined) u.role = body.role === 'admin' ? 'admin' : 'user';
  if (body.disabled !== undefined) u.disabled = Boolean(body.disabled);
  json(res, 200, u);
}, { admin: true });

route('POST', /^\/api\/v1\/users\/([^/]+)\/password$/, async (req, res, { m }) => {
  const u = users.find((x) => x.name === m[1]);
  if (!u) return json(res, 404, { error: 'user not found' });
  const body = await readBody(req);
  if (!body.password || body.password.length < 8) return json(res, 400, { error: 'password must be at least 8 characters' });
  u.hasStatic = true;
  json(res, 204);
}, { admin: true });

route('GET', /^\/api\/v1\/nodes$/, (req, res) => {
  const items = nodes.map((n) => ({ ...n, bookable: isBookable(n), leases: bookings.filter((b) => b.node === n.name && phaseOf(b) === 'Active').length }));
  json(res, 200, { items });
}, { admin: true });

route('GET', /^\/api\/v1\/nodes\/([^/]+)\/bookable-impact$/, (req, res, { m }) => {
  const n = nodes.find((x) => x.name === m[1]);
  if (!n) return json(res, 404, { error: 'node not found' });
  const bookable = isBookable(n);
  json(res, 200, {
    bookable,
    matchesDefaultPlacement: true,
    affectedClusters: bookable ? [] : [{ name: 'team-x', pods: 5 }, { name: 'demo', pods: 2 }],
    leases: bookable ? bookings.filter((b) => b.node === n.name && phaseOf(b) === 'Active').map((b) => ({ booking: b.name, vcluster: b.vcluster, until: b.end })) : [],
  });
}, { admin: true });

route('PUT', /^\/api\/v1\/nodes\/([^/]+)\/bookable$/, (req, res, { m }) => {
  const n = nodes.find((x) => x.name === m[1]);
  if (!n) return json(res, 404, { error: 'node not found' });
  n.labels['vk8s.io/bookable'] = 'true';
  json(res, 204);
}, { admin: true });

route('DELETE', /^\/api\/v1\/nodes\/([^/]+)\/bookable$/, (req, res, { m }) => {
  const n = nodes.find((x) => x.name === m[1]);
  if (!n) return json(res, 404, { error: 'node not found' });
  delete n.labels['vk8s.io/bookable'];
  for (const b of bookings) {
    if (b.node === n.name && !b.fixedPhase) {
      const ph = phaseOf(b);
      if (ph === 'Active') { b.fixedPhase = 'Revoked'; b.reason = 'NodeNotBookable'; }
      if (ph === 'Pending') { b.fixedPhase = 'Rejected'; b.reason = 'NodeNotBookable'; }
    }
  }
  json(res, 204);
}, { admin: true });

// ---- 节点预约 ----

route('GET', /^\/api\/v1\/bookings$/, (req, res, { me, url }) => {
  const now = Date.now();
  let from = Math.floor(now / HOUR_MS) * HOUR_MS;
  if (url.searchParams.get('from')) from = Date.parse(url.searchParams.get('from'));
  let to = from + 48 * HOUR_MS;
  if (url.searchParams.get('to')) to = Date.parse(url.searchParams.get('to'));
  if (Number.isNaN(from) || Number.isNaN(to)) return json(res, 400, { error: 'from / to 不是 RFC3339 时间' });
  if (to <= from) return json(res, 400, { error: 'to 必须晚于 from' });
  if (to - from > 31 * 24 * HOUR_MS) return json(res, 400, { error: '查询窗口最长 31 天' });
  const onlyVC = url.searchParams.get('vcluster');
  const items = bookings
    .filter((b) => (!onlyVC || b.vcluster === onlyVC) && Date.parse(b.start) < to && Date.parse(b.end) > from)
    .map((b) => bookingView(b, me))
    .sort((a, b) => a.start.localeCompare(b.start) || a.name.localeCompare(b.name));
  const bookable = nodes.filter(isBookable).map((n) => ({ name: n.name, ready: n.ready, gpu: n.allocatable['nvidia.com/gpu'] ?? '' })).sort((a, b) => a.name.localeCompare(b.name));
  json(res, 200, { nodes: bookable, bookings: items, now: new Date(now).toISOString() });
});

route('POST', /^\/api\/v1\/bookings$/, async (req, res, { me }) => {
  const body = await readBody(req);
  const node = nodes.find((x) => x.name === String(body.node ?? '').trim());
  if (!node) return json(res, 404, { error: '节点不存在' });
  if (!isBookable(node)) return json(res, 400, { error: '节点未标记为可预约' });
  const vc = vclusters.find((x) => x.name === String(body.vcluster ?? '').trim());
  if (!vc || !canView(vc, me)) return json(res, 404, { error: 'virtual cluster not found' });
  if (vc.status.phase === 'Deleting') return json(res, 409, { error: 'virtual cluster is being deleted' });
  const start = Date.parse(body.start), end = Date.parse(body.end);
  if (Number.isNaN(start) || Number.isNaN(end)) return json(res, 400, { error: 'start 与 end 必填' });
  if (start % HOUR_MS !== 0 || end % HOUR_MS !== 0) return json(res, 400, { error: 'start 与 end 必须是整点（UTC）' });
  if (end <= start) return json(res, 400, { error: 'end 必须晚于 start' });
  if (start < Math.floor(Date.now() / HOUR_MS) * HOUR_MS) return json(res, 400, { error: 'start 不能早于当前整点' });
  const purpose = String(body.purpose ?? '').trim();
  if (purpose.length > 200) return json(res, 400, { error: '用途说明最长 200 字' });
  const clash = bookings.find((b) => b.node === node.name && !['Completed', 'Rejected', 'Revoked'].includes(phaseOf(b)) && start < Date.parse(b.end) && Date.parse(b.start) < end);
  if (clash) return json(res, 409, { error: `与预约 ${clash.name}（${clash.vcluster}，${clash.start} → ${clash.end}）时间重叠` });
  const b = { name: 'nb-' + randomBytes(5).toString('hex'), node: node.name, vcluster: vc.name, user: me.name, purpose, start: new Date(start).toISOString(), end: new Date(end).toISOString(), createdAt: new Date().toISOString(), createdMs: Date.now(), fixedPhase: null };
  bookings.push(b);
  json(res, 201, bookingView(b, me));
});

route('DELETE', /^\/api\/v1\/bookings\/([^/]+)$/, (req, res, { me, m }) => {
  const i = bookings.findIndex((b) => b.name === m[1]);
  if (i < 0) return json(res, 404, { error: 'booking not found' });
  const b = bookings[i];
  const vc = vclusters.find((x) => x.name === b.vcluster);
  if (!isAdmin(me) && b.user !== me.name && !(vc && canView(vc, me))) return json(res, 404, { error: 'booking not found' });
  if (!canDeleteBooking(b, me)) return json(res, 403, { error: '只有预约创建者、目标集群所有者或管理员可以删除' });
  bookings.splice(i, 1);
  json(res, 204);
});

// ---- 审计 ----

// 非管理员必须带 vcluster（缺 → 400），且是 owner / member（否则 404）；返回 false 表示已响应。
function auditScope(res, me, vcName) {
  if (isAdmin(me)) return true;
  if (!vcName) { json(res, 400, { error: 'vcluster 必填' }); return false; }
  const vc = vclusters.find((x) => x.name === vcName);
  if (!vc || !canView(vc, me)) { json(res, 404, { error: 'virtual cluster not found' }); return false; }
  return true;
}
const stripBody = ({ requestObject, ...rest }) => rest;

route('GET', /^\/api\/v1\/audit\/events$/, (req, res, { me, url }) => {
  const q = url.searchParams;
  const vcName = q.get('vcluster') ?? '';
  if (!auditScope(res, me, vcName)) return;
  const now = Date.now();
  let to = q.get('to') ? Date.parse(q.get('to')) : now;
  let from = q.get('from') ? Date.parse(q.get('from')) : to - 24 * HOUR_MS;
  if (Number.isNaN(from) || Number.isNaN(to) || to <= from) return json(res, 400, { error: 'from / to 不合法' });
  if (to - from > 90 * 24 * HOUR_MS) return json(res, 400, { error: '查询窗口最长 90 天' });
  const limit = Math.min(Math.max(Number(q.get('limit') ?? 100) || 100, 1), 500);
  let before = null;
  if (q.get('before')) {
    const [ms, id] = q.get('before').split(':');
    if (!ms || !id || Number.isNaN(Number(ms))) return json(res, 400, { error: 'before 游标格式应为 <毫秒时间戳>:<id>' });
    before = { ms: Number(ms), id };
  }
  const eq = (key, val) => !val || ((e) => e[key] === val);
  const outcome = q.get('outcome');
  let items = auditEvents.filter((e) => {
    const t = Date.parse(e.ts);
    if (t < from || t >= to) return false;
    if (vcName && e.vcluster !== vcName) return false;
    for (const k of ['user', 'verb', 'resource', 'namespace', 'name', 'source']) if (q.get(k) && e[k] !== q.get(k)) return false;
    if (q.get('token') && e.annotations?.['vk8s.io/token-id'] !== q.get('token')) return false;
    if (outcome === 'success' && e.responseCode >= 400) return false;
    if (outcome === 'failure' && e.responseCode < 400) return false;
    if (before) { const bt = Date.parse(e.ts); if (bt > before.ms || (bt === before.ms && e.id >= before.id)) return false; }
    return true;
  }).sort((a, b) => b.ts.localeCompare(a.ts) || b.id.localeCompare(a.id));
  void eq;
  const page = items.slice(0, limit);
  const last = page[page.length - 1];
  json(res, 200, { items: page.map(stripBody), nextBefore: page.length >= limit && last ? `${Date.parse(last.ts)}:${last.id}` : '' });
});

route('GET', /^\/api\/v1\/audit\/events\/([^/]+)$/, (req, res, { me, m, url }) => {
  const vcName = url.searchParams.get('vcluster') ?? '';
  if (!auditScope(res, me, vcName)) return;
  const e = auditEvents.find((x) => x.id === m[1] && (!vcName || x.vcluster === vcName));
  if (!e) return json(res, 404, { error: 'audit event not found' });
  json(res, 200, e);
});

// ---- 服务 ----

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host ?? 'localhost'}`);
  const path = url.pathname;
  try {
    if (req.method !== 'GET' && req.method !== 'HEAD' && req.headers['x-requested-with'] !== 'vk8s') {
      return json(res, 403, { error: 'missing or invalid X-Requested-With header' });
    }
    const me = currentUser(req);
    for (const r of routes) {
      if (r.method !== req.method) continue;
      const m = r.pattern.exec(path);
      if (!m) continue;
      if (r.auth && !me) return json(res, 401, { error: 'unauthenticated' });
      if (r.admin && !isAdmin(me)) return json(res, 403, { error: 'admin role required' });
      return await r.handler(req, res, { me, m: m.map((s) => (s === undefined ? s : decodeURIComponent(s))), url });
    }
    json(res, 404, { error: 'not found' });
  } catch (err) {
    json(res, err.message === 'invalid JSON' ? 400 : 500, { error: err.message === 'invalid JSON' ? 'invalid JSON' : 'internal error' });
  }
});

server.listen(PORT, '127.0.0.1', () => {
  console.log(`vk8s mock API listening on http://127.0.0.1:${PORT}  (users: ${users.map((u) => u.name).join(', ')}; password: ${PASSWORD})`);
});

// ---- 应用目录 ----
const STRICT = { privileged: 0, baseline: 1, restricted: 2 };
const requiresOK = (t, level) => !t.requires.podSecurity || (STRICT[level] ?? 2) <= STRICT[t.requires.podSecurity];
const canManage = (vc, u) => isAdmin(u) || vc.owner === u.name;
const templateView = (t) => ({ name: t.name, displayName: t.displayName, description: t.description, version: t.version, inputs: t.inputs, requires: t.requires });
function newInstall(vcName, template, inputs, delayMs = 8_000) {
  const i = { vcluster: vcName, template, phase: 'Installing', installedVersion: '', inputs, message: '', reason: '', deleting: false, createdAt: new Date().toISOString(), job: `addon-${vcName}-${template}-${Math.random().toString(36).slice(2, 7)}` };
  setTimeout(() => {
    if (!addonInstalls.includes(i) || i.deleting) return;
    const t = addonTemplates.find((x) => x.name === template);
    i.phase = 'Ready';
    i.installedVersion = t ? t.version : i.installedVersion;
  }, delayMs).unref();
  return i;
}
const installView = (i) => {
  const t = addonTemplates.find((x) => x.name === i.template);
  const vc = vclusters.find((x) => x.name === i.vcluster);
  const pw = new Set((t?.inputs ?? []).filter((x) => x.type === 'password').map((x) => x.name));
  return {
    template: i.template, displayName: t?.displayName ?? i.template, phase: i.deleting ? 'Uninstalling' : i.phase, reason: i.reason, message: i.message,
    installedVersion: i.installedVersion, availableVersion: t && i.phase === 'Ready' && t.version !== i.installedVersion ? t.version : '',
    inputs: Object.fromEntries(Object.entries(i.inputs).map(([k, v]) => [k, !t || pw.has(k) ? '***' : v])),
    outputs: i.phase === 'Ready' && t && vc ? t.outputs.map((o) => ({ name: o.name, label: o.label, value: o.value(vc.name) })) : [],
    hasSecretOutputs: !!t && t.secretOutputs.length > 0, templateRemoved: !t, deleting: i.deleting, createdAt: i.createdAt,
  };
};
const findInstall = (res, me, m, manage) => {
  const vc = findVC(res, me, m[1]);
  if (!vc) return null;
  if (manage && !canManage(vc, me)) {
    json(res, 403, { error: '只有集群所有者与平台管理员可以管理应用' });
    return null;
  }
  const i = addonInstalls.find((x) => x.vcluster === vc.name && x.template === m[2]);
  if (!i) {
    json(res, 404, { error: '应用未安装' });
    return null;
  }
  return { vc, i };
};

route('GET', /^\/api\/v1\/addons$/, (req, res) => json(res, 200, addonTemplates.map(templateView)));

route('GET', /^\/api\/v1\/vclusters\/([^/]+)\/addons$/, (req, res, { me, m }) => {
  const vc = findVC(res, me, m[1]);
  if (!vc) return;
  json(res, 200, addonInstalls.filter((i) => i.vcluster === vc.name).map(installView).sort((a, b) => a.template.localeCompare(b.template)));
});

route('POST', /^\/api\/v1\/vclusters\/([^/]+)\/addons$/, async (req, res, { me, m }) => {
  const vc = findVC(res, me, m[1]);
  if (!vc) return;
  if (!canManage(vc, me)) return json(res, 403, { error: '只有集群所有者与平台管理员可以管理应用' });
  const body = await readBody(req);
  const t = addonTemplates.find((x) => x.name === body.template);
  if (!t) return json(res, 400, { error: `应用 "${body.template}" 不在目录中` });
  if (!requiresOK(t, vc.podSecurity)) return json(res, 400, { error: `应用 ${t.displayName}：需要 ${t.requires.podSecurity} 档位` });
  if (addonInstalls.some((x) => x.vcluster === vc.name && x.template === t.name)) return json(res, 409, { error: `应用 ${t.displayName} 已安装` });
  const i = newInstall(vc.name, t.name, body.inputs ?? {});
  addonInstalls.push(i);
  json(res, 201, installView(i));
});

route('DELETE', /^\/api\/v1\/vclusters\/([^/]+)\/addons\/([^/]+)$/, (req, res, { me, m }) => {
  const f = findInstall(res, me, m, true);
  if (!f) return;
  f.i.deleting = true;
  setTimeout(() => {
    const idx = addonInstalls.indexOf(f.i);
    if (idx >= 0) addonInstalls.splice(idx, 1);
  }, 4_000).unref();
  res.writeHead(204).end();
});

route('POST', /^\/api\/v1\/vclusters\/([^/]+)\/addons\/([^/]+)\/upgrade$/, (req, res, { me, m }) => {
  const f = findInstall(res, me, m, true);
  if (!f) return;
  const t = addonTemplates.find((x) => x.name === f.i.template);
  if (!t) return json(res, 409, { error: '应用已从目录中移除，无法升级' });
  if (t.version !== f.i.installedVersion) {
    f.i.phase = 'Installing';
    setTimeout(() => {
      f.i.phase = 'Ready';
      f.i.installedVersion = t.version;
    }, 6_000).unref();
  }
  res.writeHead(204).end();
});

route('POST', /^\/api\/v1\/vclusters\/([^/]+)\/addons\/([^/]+)\/retry$/, (req, res, { me, m }) => {
  const f = findInstall(res, me, m, true);
  if (!f) return;
  if (f.i.phase !== 'Failed') return json(res, 409, { error: '只有失败的应用可以重试' });
  f.i.phase = 'Installing';
  f.i.message = '';
  setTimeout(() => {
    f.i.phase = 'Ready';
    f.i.installedVersion = addonTemplates.find((x) => x.name === f.i.template)?.version ?? '';
  }, 8_000).unref();
  res.writeHead(204).end();
});

route('GET', /^\/api\/v1\/vclusters\/([^/]+)\/addons\/([^/]+)\/outputs$/, (req, res, { me, m }) => {
  const f = findInstall(res, me, m, true);
  if (!f) return;
  const v = installView(f.i);
  const t = addonTemplates.find((x) => x.name === f.i.template);
  json(res, 200, [...v.outputs, ...(t?.secretOutputs ?? []).map((o) => ({ name: o.name, label: o.label, value: o.value }))]);
});

route('GET', /^\/api\/v1\/vclusters\/([^/]+)\/addons\/([^/]+)\/logs$/, (req, res, { me, m }) => {
  const f = findInstall(res, me, m, false);
  if (!f) return;
  json(res, 200, { log: `Release "${f.i.template}" has been upgraded. Happy Helming!\nNAME: ${f.i.template}\nSTATUS: deployed\n` });
});
