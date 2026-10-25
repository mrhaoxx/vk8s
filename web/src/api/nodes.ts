import { enc, http } from './client';
import type { BookableImpactView, ListResponse, NodeView } from './types';

// 仅 admin。
export async function listNodes(): Promise<NodeView[]> {
  const res = await http.get<ListResponse<NodeView>>('/api/v1/nodes');
  return (res.items ?? []).map((n) => ({
    ...n,
    labels: n.labels ?? {},
    taints: n.taints ?? [],
    allocatable: n.allocatable ?? {},
    bookable: n.bookable ?? false,
    leases: n.leases ?? 0,
  }));
}

// 切换可预约标签前的影响（将被驱逐的 Pod / 将被撤销的租约）。仅 admin。
export async function getBookableImpact(name: string): Promise<BookableImpactView> {
  const v = await http.get<BookableImpactView>(`/api/v1/nodes/${enc(name)}/bookable-impact`);
  return { ...v, affectedClusters: v.affectedClusters ?? [], leases: v.leases ?? [] };
}

// 打 / 摘 vk8s.io/bookable=true 标签。仅 admin。
export function setBookable(name: string, on: boolean): Promise<void> {
  const path = `/api/v1/nodes/${enc(name)}/bookable`;
  return on ? http.put<void>(path) : http.del<void>(path);
}
