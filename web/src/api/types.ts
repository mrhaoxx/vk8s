// 与后端管理 API 的契约类型。字段名与后端 JSON 保持一致，不要自行增减。

export type Role = 'user' | 'admin';
export type PodSecurity = 'privileged' | 'baseline' | 'restricted';
export type Phase = 'Pending' | 'Provisioning' | 'Running' | 'Failed' | 'Deleting';
export type MyRole = 'owner' | 'member' | 'admin';

export interface SessionUser {
  name: string;
  displayName: string;
  email: string;
  role: Role;
}

export interface MeResponse {
  user: SessionUser;
  oidcEnabled: boolean;
}

export interface Toleration {
  key?: string;
  operator?: string;
  value?: string;
  effect?: string;
  tolerationSeconds?: number;
}

// cpu/memory/storage/extended 是 k8s Quantity 字符串；pods/services 是整数。
export interface Quota {
  cpu: string;
  memory: string;
  pods: number;
  storage: string;
  services: number;
  extended: Record<string, string>;
}

export interface NodePlacement {
  nodeSelector: Record<string, string>;
  nodeNames: string[];
  tolerations: Toleration[];
}

export interface Condition {
  type: string;
  status: string;
  reason: string;
  message: string;
  lastTransitionTime: string;
}

export interface VClusterStatus {
  phase: Phase | '';
  reason: string;
  hostNamespace: string;
  apiEndpoint: string;
  appsDomain: string;
  conditions: Condition[];
  // 通过节点预约当前归本集群使用的节点。
  leasedNodes: LeasedNodeView[];
}

export interface LeasedNodeView {
  node: string;
  booking: string;
  until: string;
}

export interface ControlPlane {
  storageSize: string;
  storageClassName: string;
}

export interface VClusterView {
  name: string;
  owner: string;
  members: string[];
  quota: Quota;
  nodePlacement: NodePlacement;
  podSecurity: PodSecurity;
  controlPlane: ControlPlane;
  status: VClusterStatus;
  createdAt: string;
  myRole: MyRole;
}

export interface CreateVClusterRequest {
  name: string;
  quota?: Quota;
  nodePlacement?: NodePlacement;
  podSecurity?: PodSecurity;
  addons?: AddonRequest[];
}

export interface PatchVClusterRequest {
  quota?: Quota;
  nodePlacement?: NodePlacement;
  podSecurity?: PodSecurity;
}

export interface TokenView {
  id: string;
  user: string;
  description: string;
  expiresAt: string;
  createdAt: string;
}

export interface CreateTokenRequest {
  description: string;
  ttl: string; // Go duration，如 "720h"
}

export interface CreateTokenResponse {
  id: string;
  description: string;
  expiresAt: string;
  kubeconfig: string;
}

export interface UserView {
  name: string;
  displayName: string;
  email: string;
  role: Role;
  disabled: boolean;
  hasStatic: boolean;
  hasOIDC: boolean;
  lastLoginTime: string | null;
}

export interface CreateUserRequest {
  name: string;
  displayName: string;
  email: string;
  role: Role;
  password: string;
}

export interface PatchUserRequest {
  displayName?: string;
  email?: string;
  role?: Role;
  disabled?: boolean;
}

export interface Taint {
  key: string;
  value?: string;
  effect: string;
}

export interface NodeView {
  name: string;
  labels: Record<string, string>;
  taints: Taint[];
  allocatable: Record<string, string>;
  ready: boolean;
  // 带 vk8s.io/bookable=true 标签；leases 为当前进行中的预约数。
  bookable: boolean;
  leases: number;
}

// ---- 节点预约 ----

export type BookingPhase = '' | 'Pending' | 'Active' | 'Completed' | 'Rejected' | 'Revoked';

export interface BookingView {
  name: string;
  node: string;
  vcluster: string;
  user: string;
  purpose: string;
  start: string; // RFC3339
  end: string; // RFC3339，不含
  phase: BookingPhase;
  reason: string;
  createdAt: string;
  canDelete: boolean;
}

export interface BookableNodeView {
  name: string;
  ready: boolean;
  gpu: string;
}

export interface BookingsResponse {
  nodes: BookableNodeView[];
  bookings: BookingView[];
  now: string;
}

export interface CreateBookingRequest {
  node: string;
  vcluster: string;
  start: string;
  end: string;
  purpose?: string;
}

export interface ImpactCluster {
  name: string;
  pods: number;
}

export interface ImpactLease {
  booking: string;
  vcluster: string;
  until: string;
}

export interface BookableImpactView {
  bookable: boolean;
  matchesDefaultPlacement: boolean;
  affectedClusters: ImpactCluster[];
  leases: ImpactLease[];
}

export interface NodePlacementPreset extends NodePlacement {
  name: string;
}

export interface PlatformConfig {
  // 审计是否启用：决定侧栏入口与集群「审计」Tab 的内容。
  auditEnabled: boolean;
  // 应用目录非空时 true：集群「应用」Tab 与建集群的预装勾选。
  addonsEnabled?: boolean;
  apiDomain: string;
  appsDomain: string;
  nodePlacementPresets: NodePlacementPreset[];
  defaults: {
    quota: Quota;
    podSecurity: PodSecurity;
  };
  limits: {
    tokenDefaultTTL: string;
    tokenMaxTTL: string;
    maxVClustersPerUser: number;
  };
}

export interface ListResponse<T> {
  items: T[];
}

// ---- 审计 ----

export type AuditSource = 'kube' | 'platform';

export interface AuditEventView {
  id: string;
  ts: string; // RFC3339
  source: AuditSource;
  vcluster: string;
  stage: string;
  level: string;
  verb: string;
  user: string;
  groups: string[];
  impersonatedUser: string;
  sourceIPs: string[];
  userAgent: string;
  namespace: string;
  apiGroup: string;
  apiVersion: string;
  resource: string;
  subresource: string;
  name: string;
  requestURI: string;
  responseCode: number;
  responseReason: string;
  responseMessage: string;
  platformAction: string;
  platformTarget: string;
  requestTruncated: boolean;
  annotations: Record<string, string>;
  // 只有详情接口带
  requestObject?: string;
}

export interface AuditListResponse {
  items: AuditEventView[];
  nextBefore: string;
}

export interface AuditQuery {
  from?: string;
  to?: string;
  vcluster?: string;
  user?: string;
  verb?: string;
  resource?: string;
  namespace?: string;
  name?: string;
  source?: AuditSource | '';
  outcome?: 'success' | 'failure' | '';
  token?: string;
  limit?: number;
  before?: string;
}

// ---- 应用目录 ----
export type AddonInputType = 'string' | 'bool' | 'enum' | 'password';

export interface AddonInputView {
  name: string;
  label: string;
  type: AddonInputType;
  default?: string;
  options?: string[];
  required?: boolean;
}

export interface AddonTemplateView {
  name: string;
  displayName: string;
  description: string;
  category?: string;
  version: string;
  inputs: AddonInputView[];
  requires: { podSecurity?: PodSecurity };
}

export type AddonPhase = 'Pending' | 'Installing' | 'Ready' | 'Failed' | 'Uninstalling';

export interface AddonOutputValue {
  name: string;
  label?: string;
  value: string;
}

export interface AddonInstallView {
  template: string;
  displayName: string;
  phase: AddonPhase;
  reason?: string;
  message?: string;
  installedVersion?: string;
  availableVersion?: string;
  // 版本号没变、目录改了内容（values 等）
  updateAvailable?: boolean;
  inputs: Record<string, string>;
  outputs: AddonOutputValue[];
  hasSecretOutputs: boolean;
  templateRemoved: boolean;
  deleting: boolean;
  createdAt: string;
}

export interface AddonRequest {
  template: string;
  inputs?: Record<string, string>;
}
