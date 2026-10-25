import { enc, http } from './client';
import { normalizeNodePlacement, normalizeQuota } from './normalize';
import type { CreateVClusterRequest, ListResponse, PatchVClusterRequest, VClusterView } from './types';

function normalize(v: VClusterView): VClusterView {
  return {
    ...v,
    members: v.members ?? [],
    quota: normalizeQuota(v.quota),
    nodePlacement: normalizeNodePlacement(v.nodePlacement),
    controlPlane: v.controlPlane ?? { storageSize: '', storageClassName: '' },
    status: {
      phase: v.status?.phase ?? '',
      reason: v.status?.reason ?? '',
      hostNamespace: v.status?.hostNamespace ?? '',
      apiEndpoint: v.status?.apiEndpoint ?? '',
      appsDomain: v.status?.appsDomain ?? '',
      conditions: v.status?.conditions ?? [],
      leasedNodes: v.status?.leasedNodes ?? [],
    },
  };
}

export async function listVClusters(): Promise<VClusterView[]> {
  const res = await http.get<ListResponse<VClusterView>>('/api/v1/vclusters');
  return (res.items ?? []).map(normalize);
}

export async function getVCluster(name: string): Promise<VClusterView> {
  return normalize(await http.get<VClusterView>(`/api/v1/vclusters/${enc(name)}`));
}

export async function createVCluster(req: CreateVClusterRequest): Promise<VClusterView> {
  return normalize(await http.post<VClusterView>('/api/v1/vclusters', req));
}

// 仅 admin。
export async function patchVCluster(name: string, req: PatchVClusterRequest): Promise<VClusterView> {
  return normalize(await http.patch<VClusterView>(`/api/v1/vclusters/${enc(name)}`, req));
}

export function deleteVCluster(name: string): Promise<void> {
  return http.del<void>(`/api/v1/vclusters/${enc(name)}`);
}

export function addMember(name: string, user: string): Promise<void> {
  return http.put<void>(`/api/v1/vclusters/${enc(name)}/members/${enc(user)}`);
}

export function removeMember(name: string, user: string): Promise<void> {
  return http.del<void>(`/api/v1/vclusters/${enc(name)}/members/${enc(user)}`);
}
