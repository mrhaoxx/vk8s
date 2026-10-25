import type { BookingView } from '../../api/types';

// 时间轴的尺寸与几何换算（纯函数，组件只负责渲染与事件）。
export const HOUR_MS = 3_600_000;
export const HOURS_VISIBLE = 48;
export const HOUR_COL = 48;
export const NODE_COL = 160;
export const ROW_H = 34;

// 向下取整到整点。
export function floorHour(ms: number): number {
  return Math.floor(ms / HOUR_MS) * HOUR_MS;
}

// 可见窗口 [base, base + 48h)。
export function windowFor(baseHourMs: number): { from: Date; to: Date } {
  return { from: new Date(baseHourMs), to: new Date(baseHourMs + HOURS_VISIBLE * HOUR_MS) };
}

export interface Geometry {
  left: number; // 含节点列宽
  width: number;
  clipLeft: boolean; // 起点在窗口之前
  clipRight: boolean; // 终点在窗口之后
}

// 色块在窗口内的位置；完全不在窗口内返回 null。
export function blockGeometry(startMs: number, endMs: number, baseHourMs: number): Geometry | null {
  const windowEnd = baseHourMs + HOURS_VISIBLE * HOUR_MS;
  const s = Math.max(startMs, baseHourMs);
  const e = Math.min(endMs, windowEnd);
  if (e <= s) return null;
  return {
    left: NODE_COL + ((s - baseHourMs) / HOUR_MS) * HOUR_COL,
    width: ((e - s) / HOUR_MS) * HOUR_COL,
    clipLeft: startMs < baseHourMs,
    clipRight: endMs > windowEnd,
  };
}

// 占位的 phase：Pending / Active，以及刚创建、控制器还没写 status 的空 phase。
export const OCCUPYING = new Set<string>(['', 'Pending', 'Active']);

// 某节点在窗口内被占用的小时下标（只算 OCCUPYING）。
export function occupiedHours(bookings: BookingView[], node: string, baseHourMs: number): Set<number> {
  const out = new Set<number>();
  for (const b of bookings) {
    if (b.node !== node || !OCCUPYING.has(b.phase)) continue;
    const s = Math.max(0, Math.floor((Date.parse(b.start) - baseHourMs) / HOUR_MS));
    const e = Math.min(HOURS_VISIBLE, Math.ceil((Date.parse(b.end) - baseHourMs) / HOUR_MS));
    for (let h = s; h < e; h++) out.add(h);
  }
  return out;
}

// 从 anchor 向 hover 逐格推进，遇到已占用或早于 firstSelectable 的格就停下；返回能到达的最远小时。
export function clampHover(anchor: number, hover: number, occupied: Set<number>, firstSelectable: number): number {
  const step = hover >= anchor ? 1 : -1;
  let last = anchor;
  for (let h = anchor + step; step > 0 ? h <= hover : h >= hover; h += step) {
    if (occupied.has(h) || h < firstSelectable || h < 0 || h >= HOURS_VISIBLE) break;
    last = h;
  }
  return last;
}

// 选区 [min, max]（含）。
export function selection(anchor: number, hover: number): [number, number] {
  return [Math.min(anchor, hover), Math.max(anchor, hover)];
}

// 小时选区 → 半开时间区间：end 为 hi + 1 小时。
export function rangeToTimes(baseHourMs: number, lo: number, hi: number): { start: Date; end: Date } {
  return { start: new Date(baseHourMs + lo * HOUR_MS), end: new Date(baseHourMs + (hi + 1) * HOUR_MS) };
}

const two = (n: number) => String(n).padStart(2, '0');

// 表头小时标签（本地时间）。
export function hourLabel(ms: number): string {
  return two(new Date(ms).getHours());
}

// 本地日期的 0 点。
export function isLocalDayStart(ms: number): boolean {
  const d = new Date(ms);
  return d.getHours() === 0 && d.getMinutes() === 0;
}

// "10/24 14:00 → 17:00"；跨天时右侧也带日期。
export function formatRange(startMs: number, endMs: number): string {
  const a = new Date(startMs);
  const b = new Date(endMs);
  const hm = (d: Date) => `${two(d.getHours())}:${two(d.getMinutes())}`;
  const full = (d: Date) => `${d.getMonth() + 1}/${d.getDate()} ${hm(d)}`;
  return a.toDateString() === b.toDateString() ? `${full(a)} → ${hm(b)}` : `${full(a)} → ${full(b)}`;
}

// "M/D"。
export function dayLabel(ms: number): string {
  const d = new Date(ms);
  return `${d.getMonth() + 1}/${d.getDate()}`;
}
