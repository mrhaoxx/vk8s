import type { KeyboardEvent, ReactNode } from 'react';

export interface TabItem<K extends string> {
  key: K;
  label: ReactNode;
  // 计数，undefined 时不渲染（例如数据还没加载）。
  count?: number;
  hidden?: boolean;
}

interface Props<K extends string> {
  items: TabItem<K>[];
  active: K;
  onChange: (k: K) => void;
  ariaLabel: string;
}

// 受控 Tab 条，不负责 URL（见 hooks/useTabParam）。
// 键盘：方向键在可见项间循环移动焦点并切换，Home / End 到首尾；tabIndex 漫游，只有当前项可 Tab 键到达。
export function Tabs<K extends string>({ items, active, onChange, ariaLabel }: Props<K>) {
  const visible = items.filter((i) => !i.hidden);

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (visible.length === 0) return;
    const buttons = Array.from(e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]'));
    const focused = (e.target as HTMLElement).closest<HTMLButtonElement>('[role="tab"]');
    let idx = focused ? buttons.indexOf(focused) : visible.findIndex((i) => i.key === active);
    if (idx < 0) idx = 0;
    let next: number;
    switch (e.key) {
      case 'ArrowRight':
        next = (idx + 1) % visible.length;
        break;
      case 'ArrowLeft':
        next = (idx - 1 + visible.length) % visible.length;
        break;
      case 'Home':
        next = 0;
        break;
      case 'End':
        next = visible.length - 1;
        break;
      default:
        return;
    }
    e.preventDefault();
    buttons[next]?.focus();
    onChange(visible[next].key);
  };

  return (
    <div className="tabs" role="tablist" aria-label={ariaLabel} onKeyDown={onKeyDown}>
      {visible.map((i) => {
        const selected = i.key === active;
        return (
          <button
            key={i.key}
            type="button"
            role="tab"
            id={`tab-${i.key}`}
            aria-selected={selected}
            aria-controls={`panel-${i.key}`}
            tabIndex={selected ? 0 : -1}
            className={selected ? 'tab tab-active' : 'tab'}
            onClick={() => onChange(i.key)}
          >
            {i.label}
            {i.count !== undefined && <span className="tab-count">{i.count}</span>}
          </button>
        );
      })}
    </div>
  );
}
