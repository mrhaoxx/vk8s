import { Fragment, type ReactNode } from 'react';
import { Link } from 'react-router-dom';

export interface BreadcrumbItem {
  label: ReactNode;
  to?: string;
}

// 最后一项是当前页，不渲染为链接。
export function Breadcrumb({ items }: { items: BreadcrumbItem[] }) {
  return (
    <nav className="breadcrumb" aria-label="面包屑">
      {items.map((it, i) => {
        const last = i === items.length - 1;
        return (
          <Fragment key={i}>
            {i > 0 && (
              <span className="breadcrumb-sep" aria-hidden="true">
                /
              </span>
            )}
            {!last && it.to ? (
              <Link to={it.to}>{it.label}</Link>
            ) : (
              <span className="breadcrumb-current" aria-current={last ? 'page' : undefined}>
                {it.label}
              </span>
            )}
          </Fragment>
        );
      })}
    </nav>
  );
}
