import type { MyRole } from '../../api/types';
import type { TabItem } from '../../components/Tabs';

export type TabKey = 'overview' | 'kubeconfig' | 'members' | 'apps' | 'audit' | 'settings';

export const TAB_KEYS: readonly TabKey[] = ['overview', 'kubeconfig', 'members', 'apps', 'audit', 'settings'];

const LABELS: Record<TabKey, string> = {
  overview: '概览',
  kubeconfig: 'kubeconfig 与令牌',
  members: '成员',
  apps: '应用',
  audit: '审计',
  settings: '设置',
};

// 「设置」只对平台管理员与所有者可见；「应用」在目录非空时对所有角色可见。
export function visibleTabs(myRole: MyRole | '', isAdmin: boolean, addonsEnabled = false): TabKey[] {
  const canSettings = isAdmin || myRole === 'owner';
  return TAB_KEYS.filter((k) => (k !== 'settings' || canSettings) && (k !== 'apps' || addonsEnabled));
}

// Tab 条的条目：令牌数在数据加载前不显示；成员数总是显示。
export function tabItems(visible: TabKey[], counts: { tokens: number | undefined; members: number }): TabItem<TabKey>[] {
  return visible.map((key) => {
    const item: TabItem<TabKey> = { key, label: LABELS[key] };
    if (key === 'kubeconfig' && counts.tokens !== undefined) item.count = counts.tokens;
    if (key === 'members') item.count = counts.members;
    return item;
  });
}
