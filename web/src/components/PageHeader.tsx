import type { ReactNode } from 'react';
import { Breadcrumb, type BreadcrumbItem } from './Breadcrumb';

interface Props {
  breadcrumb?: BreadcrumbItem[];
  title: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
}

// 所有页面共用的页头：面包屑、标题行、副标题、右侧操作。
export function PageHeader({ breadcrumb, title, subtitle, actions }: Props) {
  return (
    <div className="page-header">
      <div className="page-header-main">
        {breadcrumb && breadcrumb.length > 0 && <Breadcrumb items={breadcrumb} />}
        <h1 className="page-title">{title}</h1>
        {subtitle && <p className="page-subtitle muted">{subtitle}</p>}
      </div>
      {actions && <div className="page-actions actions">{actions}</div>}
    </div>
  );
}
