import type { ReactNode } from 'react';

// 状态只分四类，颜色由 tokens.css 的 --status-* 承载：ok / busy / err / idle。
export type StatusKind = 'ok' | 'busy' | 'err' | 'idle';

const KIND: Record<string, StatusKind> = {
  Running: 'ok',
  True: 'ok',
  Ready: 'ok',
  Pending: 'busy',
  Provisioning: 'busy',
  Installing: 'busy',
  Uninstalling: 'busy',
  Failed: 'err',
  False: 'err',
};

// phase / condition 字符串到状态类的映射；未知值（含空串、Deleting、Unknown）为 idle。
export function statusKind(value: string): StatusKind {
  return KIND[value] ?? 'idle';
}

interface Props {
  value: string;
  // 显示文字，缺省显示 value 本身；value 为空显示「未知」。
  label?: ReactNode;
  // 通常放 status.reason。
  title?: string;
}

// 色点 + 文字；颜色只是辅助，文字必有。
export function StatusDot({ value, label, title }: Props) {
  return (
    <span className={`status status-${statusKind(value)}`} title={title}>
      <i className="status-dot" aria-hidden="true" />
      <span className="status-text">{label ?? (value || '未知')}</span>
    </span>
  );
}
