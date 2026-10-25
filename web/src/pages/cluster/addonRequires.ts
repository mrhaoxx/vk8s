import type { AddonTemplateView } from '../../api/types';

const STRICTNESS: Record<string, number> = { privileged: 0, baseline: 1, restricted: 2 };

// 与后端 addons.RequiresMet 同义：requires 是「至少这么宽松」。
export function requiresMet(t: AddonTemplateView, level: string): { ok: boolean; reason: string } {
  const req = t.requires?.podSecurity;
  if (!req) return { ok: true, reason: '' };
  if ((STRICTNESS[level] ?? 2) <= (STRICTNESS[req] ?? 2)) return { ok: true, reason: '' };
  return { ok: false, reason: `需要 ${req} 档位` };
}
