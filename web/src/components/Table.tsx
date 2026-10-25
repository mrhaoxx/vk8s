import type { ReactNode } from 'react';

interface Props {
  // 行可点击（hover 高亮、指针）。
  hoverable?: boolean;
  compact?: boolean;
  className?: string;
  // children 仍是 thead / tbody。
  children: ReactNode;
}

// 紧凑表格的样式包装：外层负责横向滚动。
export function Table({ hoverable, compact, className, children }: Props) {
  const cls = ['table', hoverable && 'table-hover', compact && 'table-compact', className].filter(Boolean).join(' ');
  return (
    <div className="table-wrap">
      <table className={cls}>{children}</table>
    </div>
  );
}
