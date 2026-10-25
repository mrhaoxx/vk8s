import type { NodePlacement, Quota } from './types';

// 后端字段带 omitempty，空 map / 空数组可能缺省或为 null，这里统一补齐。

export function normalizeQuota(q: Partial<Quota> | null | undefined): Quota {
  return {
    cpu: q?.cpu ?? '',
    memory: q?.memory ?? '',
    pods: q?.pods ?? 0,
    storage: q?.storage ?? '',
    services: q?.services ?? 0,
    extended: q?.extended ?? {},
  };
}

export function normalizeNodePlacement(np: Partial<NodePlacement> | null | undefined): NodePlacement {
  return {
    nodeSelector: np?.nodeSelector ?? {},
    nodeNames: np?.nodeNames ?? [],
    tolerations: np?.tolerations ?? [],
  };
}
