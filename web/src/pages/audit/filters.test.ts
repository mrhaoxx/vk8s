import { describe, expect, it } from 'vitest';
import type { AuditEventView } from '../../api/types';
import { DEFAULT_FILTERS, FILTER_KEYS, cursorOf, mergePage, parseFilters, rangeFor, serializeFilters } from './filters';

describe('filters ↔ URL', () => {
  it('omits defaults and round-trips', () => {
    const f = { ...DEFAULT_FILTERS, vcluster: 'alice-dev', outcome: 'failure' as const };
    const sp = serializeFilters(f);
    expect(sp.toString()).toBe('vcluster=alice-dev&outcome=failure');
    expect(parseFilters(sp)).toEqual(f);
    expect(parseFilters(new URLSearchParams('outcome=bogus&preset=7d'))).toEqual({ ...DEFAULT_FILTERS, preset: '7d' });
    expect(serializeFilters(DEFAULT_FILTERS).toString()).toBe('');
  });

  it('computes ranges from presets', () => {
    const now = Date.UTC(2026, 9, 24, 12);
    expect(rangeFor({ ...DEFAULT_FILTERS, preset: '1h' }, now)).toEqual({ from: new Date(now - 3_600_000).toISOString(), to: new Date(now).toISOString() });
    expect(rangeFor({ ...DEFAULT_FILTERS, preset: '7d' }, now).from).toBe(new Date(now - 7 * 86_400_000).toISOString());
    const custom = rangeFor({ ...DEFAULT_FILTERS, preset: 'custom', from: '2026-10-20T08:00', to: '2026-10-21T08:00' }, now);
    expect(custom.from).toBe(new Date('2026-10-20T08:00').toISOString());
    expect(custom.to).toBe(new Date('2026-10-21T08:00').toISOString());
  });

  it('merges pages without duplicates and builds the cursor', () => {
    const a = { id: 'a', ts: '2026-10-24T06:00:00.500Z' } as AuditEventView;
    const b = { id: 'b', ts: '2026-10-24T05:59:00.000Z' } as AuditEventView;
    expect(mergePage([a], [a, b]).map((e) => e.id)).toEqual(['a', 'b']);
    expect(cursorOf([a, b])).toBe(`${Date.parse(b.ts)}:b`);
    expect(cursorOf([])).toBe('');
  });

  it('round-trips the token filter and omits it when empty', () => {
    const f = parseFilters(new URLSearchParams('token=tok-1&vcluster=a'));
    expect(f.token).toBe('tok-1');
    expect(serializeFilters(f).get('token')).toBe('tok-1');
    expect(serializeFilters(DEFAULT_FILTERS).has('token')).toBe(false);
    expect(FILTER_KEYS).toContain('token');
  });
});
