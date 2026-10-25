import { describe, expect, it } from 'vitest';
import type { BookableImpactView } from '../../api/types';
import { impactMessage } from './bookableImpact';

describe('impactMessage', () => {
  const base: BookableImpactView = { bookable: false, matchesDefaultPlacement: false, affectedClusters: [], leases: [] };

  it('is calm when nothing is affected', () => {
    const m = impactMessage(base, true);
    expect(m.danger).toBe(false);
    expect(m.lines.join('\n')).not.toMatch(/驱逐/);
  });

  it('warns about evictions and default placement', () => {
    const m = impactMessage({ ...base, matchesDefaultPlacement: true, affectedClusters: [{ name: 'team-x', pods: 5 }, { name: 'demo', pods: 2 }] }, true);
    expect(m.danger).toBe(true);
    expect(m.lines.join('\n')).toMatch(/2 个集群的 7 个 Pod：team-x（5）、demo（2）/);
    expect(m.lines.join('\n')).toMatch(/defaults\.nodePlacement/);
  });

  it('lists leases that will be revoked when turning off', () => {
    const m = impactMessage({ ...base, bookable: true, leases: [{ booking: 'nb-1', vcluster: 'alice-dev', until: '2026-10-24T13:00:00Z' }] }, false);
    expect(m.danger).toBe(true);
    expect(m.lines.join('\n')).toMatch(/撤销 1 个租约：alice-dev/);
  });
});
