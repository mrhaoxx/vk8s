import type { AuditEventView, AuditSource } from '../../api/types';

export type RangePreset = '1h' | '24h' | '7d' | 'custom';

export interface AuditFilterState {
  preset: RangePreset;
  // custom 时的本地 datetime-local 字符串（如 2026-10-20T08:00）
  from?: string;
  to?: string;
  vcluster: string;
  user: string;
  verb: string;
  resource: string;
  namespace: string;
  source: '' | AuditSource;
  outcome: '' | 'success' | 'failure';
  // 访问令牌 ID（annotations['vk8s.io/token-id']），由抽屉或令牌页带入
  token: string;
}

export const DEFAULT_FILTERS: AuditFilterState = { preset: '24h', vcluster: '', user: '', verb: '', resource: '', namespace: '', source: '', outcome: '', token: '' };

// 过滤项在 URL 里用的键；其他键（如 tab、event）不归这里管。
export const FILTER_KEYS = ['preset', 'from', 'to', 'vcluster', 'user', 'verb', 'resource', 'namespace', 'source', 'outcome', 'token'] as const;

const PRESETS: RangePreset[] = ['1h', '24h', '7d', 'custom'];

export function parseFilters(sp: URLSearchParams): AuditFilterState {
  const f: AuditFilterState = { ...DEFAULT_FILTERS };
  const preset = sp.get('preset');
  if (preset && (PRESETS as string[]).includes(preset)) f.preset = preset as RangePreset;
  if (sp.get('from')) f.from = sp.get('from')!;
  if (sp.get('to')) f.to = sp.get('to')!;
  for (const k of ['vcluster', 'user', 'verb', 'resource', 'namespace', 'token'] as const) f[k] = sp.get(k) ?? '';
  const source = sp.get('source');
  if (source === 'kube' || source === 'platform') f.source = source;
  const outcome = sp.get('outcome');
  if (outcome === 'success' || outcome === 'failure') f.outcome = outcome;
  return f;
}

// 等于缺省的项不写进 URL。
export function serializeFilters(f: AuditFilterState): URLSearchParams {
  const sp = new URLSearchParams();
  if (f.preset !== DEFAULT_FILTERS.preset) sp.set('preset', f.preset);
  if (f.preset === 'custom') {
    if (f.from) sp.set('from', f.from);
    if (f.to) sp.set('to', f.to);
  }
  for (const k of ['vcluster', 'user', 'verb', 'resource', 'namespace', 'source', 'outcome', 'token'] as const) {
    if (f[k]) sp.set(k, f[k]);
  }
  return sp;
}

const HOUR = 3_600_000;

// 过滤状态 → 查询用的 ISO 时间窗。
export function rangeFor(f: AuditFilterState, now: number): { from: string; to: string } {
  if (f.preset === 'custom' && f.from && f.to) {
    return { from: new Date(f.from).toISOString(), to: new Date(f.to).toISOString() };
  }
  const span = f.preset === '1h' ? HOUR : f.preset === '7d' ? 7 * 24 * HOUR : 24 * HOUR;
  return { from: new Date(now - span).toISOString(), to: new Date(now).toISOString() };
}

// 追加下一页，按 id 去重，保持顺序。
export function mergePage(prev: AuditEventView[], next: AuditEventView[]): AuditEventView[] {
  const seen = new Set(prev.map((e) => e.id));
  const out = [...prev];
  for (const e of next) {
    if (!seen.has(e.id)) {
      seen.add(e.id);
      out.push(e);
    }
  }
  return out;
}

// 最后一条的游标 `<ms>:<id>`；空列表为空串。
export function cursorOf(items: AuditEventView[]): string {
  const last = items[items.length - 1];
  return last ? `${Date.parse(last.ts)}:${last.id}` : '';
}

const two = (n: number) => String(n).padStart(2, '0');

// 列表里的时间：今天只显示时分秒，否则带 M/D。
export function formatEventTime(iso: string, now: number): string {
  const d = new Date(iso);
  const n = new Date(now);
  const hms = `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`;
  return d.toDateString() === n.toDateString() ? hms : `${d.getMonth() + 1}/${d.getDate()} ${hms}`;
}

export function formatEventTimeFull(iso: string): string {
  const d = new Date(iso);
  return `${d.getFullYear()}-${two(d.getMonth() + 1)}-${two(d.getDate())} ${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, '0')}`;
}
