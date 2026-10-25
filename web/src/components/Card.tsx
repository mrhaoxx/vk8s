import type { ReactNode } from 'react';

interface Props {
  title?: ReactNode;
  actions?: ReactNode;
  // flush：无内边距，用于整卡一张表格。
  flush?: boolean;
  // danger：红色边框，用于危险操作区。
  danger?: boolean;
  className?: string;
  children: ReactNode;
}

export function Card({ title, actions, flush, danger, className, children }: Props) {
  const cls = ['card', flush && 'card-flush', danger && 'card-danger', className].filter(Boolean).join(' ');
  return (
    <section className={cls}>
      {(title || actions) && (
        <div className="card-header">
          {title && <h2 className="card-title">{title}</h2>}
          {actions && <div className="card-actions">{actions}</div>}
        </div>
      )}
      {children}
    </section>
  );
}
