import type { NodePlacement, Toleration } from '../api/types';

export function isNodePlacementEmpty(np: NodePlacement): boolean {
  return (
    Object.keys(np.nodeSelector).length === 0 && np.nodeNames.length === 0 && np.tolerations.length === 0
  );
}

export function formatToleration(t: Toleration): string {
  const op = t.operator ?? (t.value !== undefined ? 'Equal' : 'Exists');
  const kv = op === 'Exists' ? `${t.key ?? '*'}` : `${t.key ?? ''}=${t.value ?? ''}`;
  const extra = t.tolerationSeconds !== undefined ? ` (${t.tolerationSeconds}s)` : '';
  return `${kv}:${t.effect ?? 'All'}${extra}`;
}

// 列表页用的一行摘要。
export function summarizeNodePlacement(np: NodePlacement): string {
  if (isNodePlacementEmpty(np)) return '平台默认';
  const parts: string[] = [];
  const sel = Object.entries(np.nodeSelector).map(([k, v]) => `${k}=${v}`);
  if (sel.length) parts.push(sel.join(', '));
  if (np.nodeNames.length) {
    const shown = np.nodeNames.slice(0, 3).join(', ');
    const more = np.nodeNames.length > 3 ? ` +${np.nodeNames.length - 3}` : '';
    parts.push(`节点 ${shown}${more}`);
  }
  if (np.tolerations.length) parts.push(`${np.tolerations.length} 条容忍`);
  return parts.join(' · ');
}

export const POD_SECURITY_LABELS: Record<string, string> = {
  privileged: 'privileged（特权）',
  baseline: 'baseline（基线）',
  restricted: 'restricted（受限）',
};

// 只在管理员选择 privileged 档位时显示的一行短说明（设计文档 2026-10-24 第 5 节）。
export const PRIVILEGED_WARNING = 'privileged 下节点池是唯一隔离边界，请勿与互不信任的租户共享节点。';
