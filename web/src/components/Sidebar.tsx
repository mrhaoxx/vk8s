import { Link, NavLink } from 'react-router-dom';
import { UserMenu, type SidebarUser } from './UserMenu';

export type { SidebarUser };

interface Props {
  user: SidebarUser;
  isAdmin: boolean;
  // 窄屏抽屉是否展开；宽屏下无影响。
  open: boolean;
  onClose: () => void;
  onLogout: () => void;
  // 审计启用时「管理」组显示「审计」
  auditEnabled?: boolean;
  // 应用目录非空时「管理」组显示「应用目录」
  addonsEnabled?: boolean;
}

const itemClass = ({ isActive }: { isActive: boolean }) => (isActive ? 'sidebar-item active' : 'sidebar-item');

// 左侧栏：品牌、「我的」组、「管理」组（仅管理员）、底部用户块。展示组件，由 AppShell 提供数据与状态。
export function Sidebar({ user, isAdmin, open, onClose, onLogout, auditEnabled = false, addonsEnabled = false }: Props) {
  return (
    <aside className={open ? 'sidebar sidebar-open' : 'sidebar'} aria-label="主导航">
      <div className="sidebar-brand">
        <Link to="/" className="brand">
          vk8s
        </Link>
        <button type="button" className="icon-btn sidebar-close" aria-label="关闭导航" onClick={onClose}>
          ×
        </button>
      </div>
      <nav className="sidebar-nav">
        <div className="sidebar-group">
          <div className="sidebar-group-title">我的</div>
          <NavLink to="/" end className={itemClass} onClick={onClose}>
            我的集群
          </NavLink>
          <NavLink to="/bookings" className={itemClass} onClick={onClose}>
            节点预约
          </NavLink>
        </div>
        {isAdmin && (
          <div className="sidebar-group">
            <div className="sidebar-group-title">管理</div>
            <NavLink to="/admin/users" className={itemClass} onClick={onClose}>
              用户
            </NavLink>
            <NavLink to="/admin/vclusters" className={itemClass} onClick={onClose}>
              所有集群
            </NavLink>
            <NavLink to="/admin/nodes" className={itemClass} onClick={onClose}>
              节点
            </NavLink>
            {addonsEnabled && (
              <NavLink to="/admin/addons" className={itemClass} onClick={onClose}>
                应用目录
              </NavLink>
            )}
            {auditEnabled && (
              <NavLink to="/admin/audit" className={itemClass} onClick={onClose}>
                审计
              </NavLink>
            )}
          </div>
        )}
      </nav>
      <div className="sidebar-footer">
        <UserMenu user={user} onLogout={onLogout} />
      </div>
    </aside>
  );
}
