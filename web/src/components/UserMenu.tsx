import { useEffect, useId, useRef, useState } from 'react';
import { Badge } from './Badge';
import { ThemeToggle } from './ThemeToggle';

// 侧栏用户块需要的最小用户信息（AuthContext 的 SessionUser 结构兼容）。
export interface SidebarUser {
  name: string;
  displayName: string;
  role: 'user' | 'admin';
}

interface Props {
  user: SidebarUser;
  onLogout: () => void;
}

// 侧栏底部的用户块：点击弹出菜单（外观、退出登录）；Esc 或点击外部关闭。
export function UserMenu({ user, onLogout }: Props) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const menuId = useId();

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('keydown', onKey);
    document.addEventListener('mousedown', onDown);
    return () => {
      document.removeEventListener('keydown', onKey);
      document.removeEventListener('mousedown', onDown);
    };
  }, [open]);

  const label = user.displayName || user.name;
  const initial = label.trim().charAt(0).toUpperCase() || '?';

  return (
    <div className="user-menu" ref={ref}>
      <button
        type="button"
        className="user-menu-trigger"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={menuId}
        onClick={() => setOpen((o) => !o)}
      >
        <span className="avatar" aria-hidden="true">
          {initial}
        </span>
        <span className="user-menu-name">{label}</span>
        {user.role === 'admin' && <Badge tone="neutral">管理员</Badge>}
        <span className="user-menu-caret" aria-hidden="true">
          ▾
        </span>
      </button>
      {open && (
        <div className="user-menu-pop" role="menu" id={menuId} aria-label="用户菜单">
          <div className="user-menu-head">
            <strong>{label}</strong>
            <span className="muted small">
              {user.name} · {user.role === 'admin' ? '管理员' : '用户'}
            </span>
          </div>
          <ThemeToggle />
          <button
            type="button"
            role="menuitem"
            className="user-menu-item"
            onClick={() => {
              setOpen(false);
              onLogout();
            }}
          >
            退出登录
          </button>
        </div>
      )}
    </div>
  );
}
