import { useEffect, useState } from 'react';
import { Link, Outlet, useLocation } from 'react-router-dom';
import { useAuth } from '../auth/AuthContext';
import { useConfig } from '../auth/ConfigContext';
import { Sidebar } from './Sidebar';

// 已登录页面的骨架：移动顶栏（窄屏）、侧栏（窄屏为抽屉）、内容区。路由变化时自动收起抽屉。
export function AppShell() {
  const { user, isAdmin, logout } = useAuth();
  const config = useConfig();
  const [open, setOpen] = useState(false);
  const location = useLocation();

  useEffect(() => {
    setOpen(false);
  }, [location.pathname]);

  if (!user) return null;

  return (
    <div className="shell">
      <header className="mobile-bar">
        <button type="button" className="icon-btn" aria-label="打开导航" onClick={() => setOpen(true)}>
          ☰
        </button>
        <Link to="/" className="brand">
          vk8s
        </Link>
      </header>
      <Sidebar user={user} isAdmin={isAdmin} open={open} onClose={() => setOpen(false)} onLogout={() => void logout()} auditEnabled={!!config?.auditEnabled} addonsEnabled={!!config?.addonsEnabled} />
      {open && <div className="drawer-backdrop" onClick={() => setOpen(false)} />}
      <main className="page">
        <Outlet />
      </main>
    </div>
  );
}
