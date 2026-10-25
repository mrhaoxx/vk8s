import type { BookableImpactView } from '../../api/types';

export interface ImpactMessage {
  lines: string[];
  danger: boolean;
}

const hm = (iso: string) => {
  const d = new Date(iso);
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
};

// 切换可预约标签前的确认文案（纯函数，便于测试）。
export function impactMessage(impact: BookableImpactView, turnOn: boolean): ImpactMessage {
  const lines: string[] = [];
  if (turnOn) {
    lines.push('标记后该节点在 vk8s 内只能通过预约使用。');
    const pods = impact.affectedClusters.reduce((n, c) => n + c.pods, 0);
    if (pods > 0) {
      lines.push(`将驱逐 ${impact.affectedClusters.length} 个集群的 ${pods} 个 Pod：${impact.affectedClusters.map((c) => `${c.name}（${c.pods}）`).join('、')}。`);
    }
    if (impact.matchesDefaultPlacement) {
      lines.push('该节点匹配平台默认放置（defaults.nodePlacement），默认池集群今后不再能调度到它。');
    }
    return { lines, danger: pods > 0 || impact.matchesDefaultPlacement };
  }
  lines.push('摘除后该节点回到普通节点。');
  if (impact.leases.length > 0) {
    lines.push(`将撤销 ${impact.leases.length} 个租约：${impact.leases.map((l) => `${l.vcluster}（至 ${hm(l.until)}）`).join('、')}，相关集群会立刻失去该节点并清理 Pod。`);
  }
  return { lines, danger: impact.leases.length > 0 };
}
