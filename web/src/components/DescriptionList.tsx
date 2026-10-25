import { Fragment, type ReactNode } from 'react';

export interface DescriptionItem {
  term: ReactNode;
  value: ReactNode;
}

const isEmpty = (v: ReactNode) => v === null || v === undefined || v === '';

// 两列定义列表；值为空显示「—」。
export function DescriptionList({ items, compact }: { items: DescriptionItem[]; compact?: boolean }) {
  return (
    <dl className={compact ? 'dl dl-compact' : 'dl'}>
      {items.map((it, i) => (
        <Fragment key={i}>
          <dt>{it.term}</dt>
          <dd>{isEmpty(it.value) ? <span className="muted">—</span> : it.value}</dd>
        </Fragment>
      ))}
    </dl>
  );
}
