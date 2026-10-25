import type { ReactNode } from 'react';

// 只保留两种：中性（角色、登录方式、「我」）与红色（已过期）。状态一律用 StatusDot。
export type Tone = 'neutral' | 'red';

export function Badge({ tone = 'neutral', children, title }: { tone?: Tone; children: ReactNode; title?: string }) {
  return (
    <span className={`badge badge-${tone}`} title={title}>
      {children}
    </span>
  );
}

const ROLE_LABEL: Record<string, string> = { owner: '所有者', member: '成员', admin: '管理员' };

export function RoleBadge({ role }: { role: string }) {
  return <Badge tone="neutral">{ROLE_LABEL[role] ?? role}</Badge>;
}
