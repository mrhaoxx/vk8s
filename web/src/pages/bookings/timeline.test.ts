import { describe, expect, it } from 'vitest';
import type { BookingPhase, BookingView } from '../../api/types';
import { HOUR_COL, HOUR_MS, HOURS_VISIBLE, NODE_COL, blockGeometry, clampHover, floorHour, occupiedHours, rangeToTimes, selection, windowFor } from './timeline';

const base = Date.UTC(2026, 9, 24, 9); // 09:00Z
const bk = (node: string, s: number, e: number, phase: BookingPhase = 'Pending'): BookingView => ({
  name: `nb-${s}`,
  node,
  vcluster: 'a',
  user: 'u',
  purpose: '',
  start: new Date(base + s * HOUR_MS).toISOString(),
  end: new Date(base + e * HOUR_MS).toISOString(),
  phase,
  reason: '',
  createdAt: '',
  canDelete: true,
});

describe('blockGeometry', () => {
  it('positions and clips at the window edges', () => {
    expect(blockGeometry(base + 2 * HOUR_MS, base + 5 * HOUR_MS, base)).toEqual({ left: NODE_COL + 2 * HOUR_COL, width: 3 * HOUR_COL, clipLeft: false, clipRight: false });
    expect(blockGeometry(base - HOUR_MS, base + HOUR_MS, base)).toEqual({ left: NODE_COL, width: HOUR_COL, clipLeft: true, clipRight: false });
    expect(blockGeometry(base + 47 * HOUR_MS, base + 50 * HOUR_MS, base)?.clipRight).toBe(true);
    expect(blockGeometry(base + 50 * HOUR_MS, base + 51 * HOUR_MS, base)).toBeNull();
  });
});

describe('occupiedHours / clampHover', () => {
  it('stops the drag before an existing block and before the past', () => {
    const occ = occupiedHours([bk('n', 5, 7), bk('n', 20, 21, 'Rejected'), bk('m', 1, 2)], 'n', base);
    expect([...occ].sort((a, b) => a - b)).toEqual([5, 6]); // Rejected 不占位；别的节点不算
    // 刚创建、控制器还没写 phase 的预约（phase 为空）同样占位
    expect([...occupiedHours([bk('n', 8, 9, '')], 'n', base)]).toEqual([8]);
    expect(clampHover(2, 9, occ, 0)).toBe(4); // 向右撞到 5
    expect(clampHover(8, 1, occ, 0)).toBe(7); // 向左撞到 6
    expect(clampHover(3, 0, occ, 2)).toBe(2); // 过去的格不可选
    expect(clampHover(2, 2, occ, 0)).toBe(2);
  });
});

describe('selection / rangeToTimes', () => {
  it('is inclusive on hours and half-open on times', () => {
    expect(selection(5, 2)).toEqual([2, 5]);
    const { start, end } = rangeToTimes(base, 2, 4);
    expect(start.getTime()).toBe(base + 2 * HOUR_MS);
    expect(end.getTime()).toBe(base + 5 * HOUR_MS);
  });
});

describe('floorHour / windowFor', () => {
  it('floors to the hour and spans 48h', () => {
    expect(floorHour(base + 35 * 60_000)).toBe(base);
    const w = windowFor(base);
    expect(w.to.getTime() - w.from.getTime()).toBe(HOURS_VISIBLE * HOUR_MS);
  });
});
