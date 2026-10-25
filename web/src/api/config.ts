import { http } from './client';
import { normalizeNodePlacement, normalizeQuota } from './normalize';
import type { PlatformConfig } from './types';

export async function getConfig(): Promise<PlatformConfig> {
  const c = await http.get<PlatformConfig>('/api/v1/config');
  return {
    ...c,
    nodePlacementPresets: (c.nodePlacementPresets ?? []).map((p) => ({ ...normalizeNodePlacement(p), name: p.name })),
    defaults: {
      quota: normalizeQuota(c.defaults?.quota),
      podSecurity: c.defaults?.podSecurity ?? 'baseline',
    },
    limits: {
      tokenDefaultTTL: c.limits?.tokenDefaultTTL ?? '720h',
      tokenMaxTTL: c.limits?.tokenMaxTTL ?? '2160h',
      maxVClustersPerUser: c.limits?.maxVClustersPerUser ?? 0,
    },
  };
}
