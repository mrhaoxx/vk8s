// 开发用 mock API 的固定数据。形状与 internal/manager/api/views.go 的 View 类型一致（字段名见 src/api/types.ts）。

const HOUR = 3600 * 1000;
const DAY = 24 * HOUR;
const now = Date.now();
const ago = (ms) => new Date(now - ms).toISOString();
const later = (ms) => new Date(now + ms).toISOString();

export const API_DOMAIN = 'vk8s.corp.com';
export const APPS_DOMAIN = 'apps.vk8s.corp.com';

// 任意用户以密码 password 登录。
export const PASSWORD = 'password';

export const users = [
  { name: 'admin', displayName: '平台管理员', email: 'admin@corp.com', role: 'admin', disabled: false, hasStatic: true, hasOIDC: false, lastLoginTime: ago(2 * HOUR) },
  { name: 'alice', displayName: 'Alice', email: 'alice@corp.com', role: 'user', disabled: false, hasStatic: true, hasOIDC: true, lastLoginTime: ago(1 * DAY) },
  { name: 'bob', displayName: 'Bob', email: 'bob@corp.com', role: 'user', disabled: false, hasStatic: true, hasOIDC: false, lastLoginTime: ago(3 * DAY) },
  { name: 'carol', displayName: 'Carol', email: 'carol@corp.com', role: 'user', disabled: true, hasStatic: true, hasOIDC: false, lastLoginTime: null },
];

const condition = (type, status, reason, message, when) => ({ type, status, reason, message, lastTransitionTime: when });

const defaultQuota = { cpu: '16', memory: '64Gi', pods: 50, storage: '200Gi', services: 20, extended: {} };
const emptyPlacement = { nodeSelector: {}, nodeNames: [], tolerations: [] };
const controlPlane = { storageSize: '5Gi', storageClassName: '' };

const endpoints = (name) => ({
  hostNamespace: `vc-${name}`,
  apiEndpoint: `https://${name}.${API_DOMAIN}`,
  appsDomain: `${name}.${APPS_DOMAIN}`,
});

export const vclusters = [
  {
    name: 'alice-dev',
    owner: 'alice',
    members: ['bob'],
    quota: { ...defaultQuota, extended: { 'nvidia.com/gpu': '8' } },
    nodePlacement: { nodeSelector: { 'vk8s.io/pool': 'dev-a' }, nodeNames: [], tolerations: [{ key: 'vk8s.io/pool', operator: 'Equal', value: 'dev-a', effect: 'NoSchedule' }] },
    podSecurity: 'privileged',
    controlPlane,
    status: {
      phase: 'Running',
      reason: '',
      ...endpoints('alice-dev'),
      conditions: [
        condition('CertificatesReady', 'True', 'Ready', '', ago(3 * DAY)),
        condition('ControlPlaneReady', 'True', 'Ready', '', ago(3 * DAY)),
        condition('SyncerReady', 'True', 'Ready', '', ago(3 * DAY)),
      ],
    },
    createdAt: ago(3 * DAY),
  },
  {
    name: 'team-x',
    owner: 'bob',
    members: ['alice'],
    quota: defaultQuota,
    nodePlacement: emptyPlacement,
    podSecurity: 'baseline',
    controlPlane,
    status: {
      phase: 'Provisioning',
      reason: 'ControlPlaneNotReady',
      hostNamespace: 'vc-team-x',
      apiEndpoint: '',
      appsDomain: '',
      conditions: [
        condition('CertificatesReady', 'True', 'Ready', '', ago(40 * 1000)),
        condition('ControlPlaneReady', 'True', 'Ready', '', ago(10 * 1000)),
        condition('SyncerReady', 'False', 'NotReady', 'running, not ready', ago(10 * 1000)),
      ],
    },
    createdAt: ago(60 * 1000),
  },
  {
    name: 'ml-exp',
    owner: 'carol',
    members: [],
    quota: { ...defaultQuota, cpu: '300' },
    nodePlacement: emptyPlacement,
    podSecurity: 'privileged',
    controlPlane,
    status: { phase: 'Failed', reason: 'QuotaExceedsMaximum: quota.cpu 300 exceeds maximum 256', hostNamespace: '', apiEndpoint: '', appsDomain: '', conditions: [] },
    createdAt: ago(2 * HOUR),
  },
  {
    name: 'old-lab',
    owner: 'bob',
    members: [],
    quota: defaultQuota,
    nodePlacement: emptyPlacement,
    podSecurity: 'restricted',
    controlPlane,
    status: { phase: 'Deleting', reason: 'WaitingForNamespaceDeletion', ...endpoints('old-lab'), conditions: [] },
    createdAt: ago(20 * DAY),
  },
];

// 可预约节点：带 vk8s.io/bookable=true，不在任何集群的静态放置里。gpu-09 NotReady 但仍可预约。
const gpuNode = (name, ready) => ({
  name,
  labels: { 'kubernetes.io/hostname': name, 'vk8s.io/bookable': 'true', 'nvidia.com/gpu.present': 'true' },
  taints: [],
  allocatable: { cpu: '96', memory: '1Ti', 'nvidia.com/gpu': '8', pods: '110' },
  ready,
});

export const nodes = [
  gpuNode('gpu-07', true),
  gpuNode('gpu-08', true),
  gpuNode('gpu-09', false),
  {
    name: 'worker-1',
    labels: { 'kubernetes.io/hostname': 'worker-1', 'vk8s.io/pool': 'dev-a', 'vk8s.io/tenant-nodes': 'true', 'nvidia.com/gpu.present': 'true' },
    taints: [{ key: 'vk8s.io/pool', value: 'dev-a', effect: 'NoSchedule' }],
    allocatable: { cpu: '62', memory: '503Gi', 'nvidia.com/gpu': '8', pods: '110' },
    ready: true,
  },
  {
    name: 'worker-2',
    labels: { 'kubernetes.io/hostname': 'worker-2', 'vk8s.io/pool': 'dev-b', 'vk8s.io/tenant-nodes': 'true' },
    taints: [],
    allocatable: { cpu: '30', memory: '125Gi', pods: '110' },
    ready: true,
  },
  {
    name: 'worker-3',
    labels: { 'kubernetes.io/hostname': 'worker-3', 'vk8s.io/pool': 'dev-b', 'vk8s.io/tenant-nodes': 'true' },
    taints: [{ key: 'node.kubernetes.io/unreachable', effect: 'NoExecute' }],
    allocatable: { cpu: '30', memory: '125Gi', pods: '110' },
    ready: false,
  },
];

export const config = {
  auditEnabled: true,
  addonsEnabled: true,
  apiDomain: API_DOMAIN,
  appsDomain: APPS_DOMAIN,
  nodePlacementPresets: [
    { name: 'dev-pool-a', nodeSelector: { 'vk8s.io/pool': 'dev-a' }, nodeNames: [], tolerations: [{ key: 'vk8s.io/pool', operator: 'Equal', value: 'dev-a', effect: 'NoSchedule' }] },
    { name: 'dev-pool-b', nodeSelector: { 'vk8s.io/pool': 'dev-b' }, nodeNames: [], tolerations: [] },
  ],
  defaults: { quota: defaultQuota, podSecurity: 'privileged' },
  limits: { tokenDefaultTTL: '720h0m0s', tokenMaxTTL: '2160h0m0s', maxVClustersPerUser: 3 },
};

export const tokens = [
  { id: 'tok-3fa9c2e1b07d4a56', user: 'alice', vcluster: 'alice-dev', description: 'macbook', createdAt: ago(3 * DAY), expiresAt: later(27 * DAY) },
  { id: 'tok-91be4d02c7e3f8a1', user: 'alice', vcluster: 'alice-dev', description: 'ci-runner', createdAt: ago(1 * DAY), expiresAt: later(89 * DAY) },
];

export const reservedNames = ['console', 'api', 'www'];

// 节点预约：相对当前整点构造，覆盖 Active / Pending / Completed / Rejected。
const hour = Math.floor(now / HOUR) * HOUR;
const atH = (h) => new Date(hour + h * HOUR).toISOString();
const tomorrow0 = (() => { const d = new Date(hour); d.setUTCDate(d.getUTCDate() + 1); d.setUTCHours(0, 0, 0, 0); return d.getTime(); })();
const tomorrowH = (h) => new Date(tomorrow0 + h * HOUR).toISOString();
export const bookings = [
  { name: 'nb-a1f3c902de', node: 'gpu-07', vcluster: 'alice-dev', user: 'alice', purpose: '微调 v3', start: atH(-2), end: atH(3), createdAt: ago(3 * HOUR), fixedPhase: null },
  { name: 'nb-b7e21c4a08', node: 'gpu-07', vcluster: 'team-x', user: 'bob', purpose: 'sglang smoke', start: atH(5), end: atH(8), createdAt: ago(2 * HOUR), fixedPhase: null },
  { name: 'nb-c09d8e6f12', node: 'gpu-08', vcluster: 'ml-exp', user: 'carol', purpose: '夜间评测', start: tomorrowH(0), end: tomorrowH(4), createdAt: ago(1 * HOUR), fixedPhase: null },
  { name: 'nb-d4458a1b77', node: 'gpu-08', vcluster: 'alice-dev', user: 'alice', purpose: '', start: atH(-26), end: atH(-24), createdAt: ago(30 * HOUR), fixedPhase: null },
  { name: 'nb-e5a0b2c3d4', node: 'gpu-07', vcluster: 'team-x', user: 'bob', purpose: '抢同一台', start: atH(-1), end: atH(1), createdAt: ago(1 * HOUR), fixedPhase: 'Rejected', reason: 'Overlap' },
];

// 审计事件：约 300 条，覆盖两类来源、六种动词、四个集群、成功与失败；按时间倒序。
const seeded = (() => { let x = 20261024; return () => { x = (x * 1103515245 + 12345) & 0x7fffffff; return x / 0x7fffffff; }; })();
const pick = (arr) => arr[Math.floor(seeded() * arr.length)];
const KUBE_OPS = [
  { verb: 'get', resource: 'pods', group: '' }, { verb: 'list', resource: 'pods', group: '' }, { verb: 'create', resource: 'pods', sub: 'exec', group: '' },
  { verb: 'create', resource: 'deployments', group: 'apps' }, { verb: 'patch', resource: 'configmaps', group: '' }, { verb: 'delete', resource: 'deployments', group: 'apps' },
  { verb: 'get', resource: 'secrets', group: '' }, { verb: 'watch', resource: 'pods', group: '' }, { verb: 'update', resource: 'services', group: '' },
];
const PLATFORM_OPS = ['vcluster.create', 'vcluster.patch', 'member.add', 'member.remove', 'token.create', 'token.delete', 'booking.create', 'booking.delete', 'auth.login', 'user.patch'];
const VC_USERS = { 'alice-dev': ['alice', 'bob'], 'team-x': ['bob', 'alice'], 'ml-exp': ['carol'], 'old-lab': ['bob'] };
export const auditEvents = [];
// kube 事件对应到用户在该集群的某个访问令牌（真实后端由 TokenReview 的 user.extra 带来）
const tokenAnnotation = (user, vc, i) => {
  const mine = tokens.filter((t) => t.user === user && t.vcluster === vc);
  return mine.length ? { 'vk8s.io/token-id': mine[i % mine.length].id } : {};
};
for (let i = 0; i < 300; i++) {
  const ts = new Date(now - i * 7 * 60 * 1000 - Math.floor(seeded() * 60_000)).toISOString();
  const vc = pick(Object.keys(VC_USERS));
  const user = pick(VC_USERS[vc]);
  const id = (100000 + i).toString(16).padStart(32, '0');
  if (seeded() < 0.75) {
    const op = pick(KUBE_OPS);
    const forbidden = op.resource === 'secrets' && seeded() < 0.6;
    const mutating = ['create', 'patch', 'delete', 'update'].includes(op.verb);
    auditEvents.push({
      id, ts, source: 'kube', vcluster: vc, stage: 'ResponseComplete', level: mutating && op.resource !== 'secrets' ? 'Request' : 'Metadata',
      verb: op.verb, user, groups: user === 'alice' ? ['vk8s:admins'] : [], impersonatedUser: '', sourceIPs: ['10.244.1.7', '203.0.113.9'],
      userAgent: 'kubectl/v1.36.2 (darwin/arm64) kubernetes/9a3b1c2', namespace: op.resource === 'secrets' ? 'kube-system' : 'demo',
      apiGroup: op.group, apiVersion: 'v1', resource: op.resource, subresource: op.sub ?? '', name: op.verb === 'list' || op.verb === 'watch' ? '' : `${op.resource.slice(0, -1)}-${(i % 7) + 1}`,
      requestURI: `/api${op.group ? 's/' + op.group : ''}/v1/namespaces/demo/${op.resource}`, responseCode: forbidden ? 403 : op.verb === 'create' ? 201 : op.sub === 'exec' ? 101 : 200,
      responseReason: forbidden ? 'Forbidden' : '', responseMessage: forbidden ? `secrets "registry" is forbidden: User "${user}" cannot get resource "secrets"` : '',
      platformAction: '', platformTarget: '', requestTruncated: false,
      annotations: { 'authorization.k8s.io/decision': forbidden ? 'forbid' : 'allow', 'authorization.k8s.io/reason': forbidden ? '' : 'RBAC: allowed by ClusterRoleBinding "vk8s:admins"', ...tokenAnnotation(user, vc, i) },
      requestObject: mutating && op.resource !== 'secrets' ? JSON.stringify({ kind: op.resource === 'deployments' ? 'Deployment' : 'PodExecOptions', apiVersion: op.group ? `${op.group}/v1` : 'v1', metadata: { name: `${op.resource.slice(0, -1)}-${(i % 7) + 1}`, namespace: 'demo' } }, null, 2) : '',
    });
  } else {
    const action = pick(PLATFORM_OPS);
    const failed = action === 'auth.login' && seeded() < 0.5;
    const actor = action === 'auth.login' && failed ? 'carol' : action.startsWith('user.') || action === 'vcluster.patch' ? 'admin' : user;
    auditEvents.push({
      id, ts, source: 'platform', vcluster: action === 'auth.login' || action.startsWith('user.') ? '' : vc, stage: 'ResponseComplete', level: 'Request',
      verb: action === 'auth.login' ? 'login' : action.endsWith('.delete') || action.endsWith('.remove') ? 'delete' : action.endsWith('.patch') ? 'update' : 'create',
      user: actor, groups: [], impersonatedUser: '', sourceIPs: ['203.0.113.9'], userAgent: 'Mozilla/5.0',
      namespace: '', apiGroup: '', apiVersion: '', resource: '', subresource: '', name: '',
      requestURI: action === 'auth.login' ? '/auth/login' : `/api/v1/${action.split('.')[0]}s`, responseCode: failed ? 401 : action.endsWith('.create') ? 201 : 204,
      responseReason: action === 'auth.login' ? (failed ? 'failure' : 'success') : '', responseMessage: '',
      platformAction: action, platformTarget: action === 'auth.login' ? actor : action.startsWith('member.') ? 'dave' : action.startsWith('booking.') ? 'nb-a1f3c902de' : vc,
      requestTruncated: false, annotations: {},
      requestObject: action === 'auth.login' ? '' : action === 'vcluster.patch' ? JSON.stringify({ quota: { cpu: '32' } }) : action === 'booking.create' ? JSON.stringify({ node: 'gpu-07', vcluster: vc, start: ts, end: ts }) : '',
    });
  }
}

// ---- 应用目录 ----
export const addonTemplates = [
  {
    name: 'devpods', displayName: 'DevPods', description: '通过 ssh 使用的远程开发环境', version: '0.1.0',
    inputs: [{ name: 'webui', label: '启用 Web 界面', type: 'bool', default: 'false' }],
    requires: { podSecurity: 'privileged' },
    outputs: [{ name: 'ssh', label: 'SSH 网关', value: (vc) => `172.28.0.${10 + vc.length}:22` }],
    secretOutputs: [],
  },
  {
    name: 'grafana', displayName: 'Grafana', description: '指标看板（管理员密码由平台生成）', version: '10.5.15',
    inputs: [{ name: 'persistence', label: '持久化', type: 'bool', default: 'true' }],
    requires: {},
    outputs: [{ name: 'web', label: 'Web', value: (vc) => `http://172.28.0.${20 + vc.length}:80` }],
    secretOutputs: [{ name: 'password', label: '管理员密码', value: 'mock-grafana-pw' }],
  },
  {
    name: 'prometheus', displayName: 'Prometheus', description: '指标采集（只装 server 与 kube-state-metrics）', version: '29.38.0',
    inputs: [{ name: 'retention', label: '保留时长', type: 'enum', options: ['7d', '15d', '30d'], default: '15d' }],
    requires: {},
    outputs: [{ name: 'web', label: 'Web', value: (vc) => `http://172.28.0.${30 + vc.length}:9090` }],
    secretOutputs: [],
  },
];

// alice-dev 已装 grafana（Ready，目录里有新版本可升级的演示用旧版本号）
export const addonInstalls = [
  { vcluster: 'alice-dev', template: 'grafana', phase: 'Ready', installedVersion: '10.4.0', inputs: { persistence: 'true' }, message: '', reason: '', deleting: false, createdAt: ago(2 * DAY), job: 'addon-alice-dev-grafana-k2x9a' },
];
