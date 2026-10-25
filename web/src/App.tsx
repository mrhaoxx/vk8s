import { BrowserRouter, Navigate, Outlet, Route, Routes, useLocation } from 'react-router-dom';
import { AuthProvider, useAuth } from './auth/AuthContext';
import { ThemeProvider } from './theme/ThemeContext';
import { ConfigProvider } from './auth/ConfigContext';
import { AppShell } from './components/AppShell';
import { Loading } from './components/State';
import { ToastProvider } from './components/Toast';
import { AdminClustersPage } from './pages/admin/AdminClustersPage';
import { AdminAddonsPage } from './pages/admin/AdminAddonsPage';
import { AdminAuditPage } from './pages/admin/AdminAuditPage';
import { AdminNodesPage } from './pages/admin/AdminNodesPage';
import { AdminUsersPage } from './pages/admin/AdminUsersPage';
import { BookingsPage } from './pages/BookingsPage';
import { ClusterDetailPage } from './pages/ClusterDetailPage';
import { LoginPage } from './pages/LoginPage';
import { MyClustersPage } from './pages/MyClustersPage';
import { NotFoundPage } from './pages/NotFoundPage';

// 未登录 → /login（记住来源路径）；已登录 → 加载平台配置并渲染布局。
function RequireAuth() {
  const { ready, user } = useAuth();
  const location = useLocation();
  if (!ready) {
    return (
      <div className="page-center">
        <Loading />
      </div>
    );
  }
  if (!user) {
    return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />;
  }
  return (
    <ConfigProvider>
      <AppShell />
    </ConfigProvider>
  );
}

function RequireAdmin() {
  const { isAdmin } = useAuth();
  if (!isAdmin) return <Navigate to="/" replace />;
  return <Outlet />;
}

export function App() {
  return (
    <ThemeProvider>
      <BrowserRouter>
        <ToastProvider>
          <AuthProvider>
            <Routes>
              <Route path="/login" element={<LoginPage />} />
              <Route element={<RequireAuth />}>
                <Route path="/" element={<MyClustersPage />} />
                <Route path="/vclusters/:name" element={<ClusterDetailPage />} />
                <Route path="/bookings" element={<BookingsPage />} />
                <Route element={<RequireAdmin />}>
                  <Route path="/admin/users" element={<AdminUsersPage />} />
                  <Route path="/admin/vclusters" element={<AdminClustersPage />} />
                  <Route path="/admin/nodes" element={<AdminNodesPage />} />
                  <Route path="/admin/addons" element={<AdminAddonsPage />} />
                  <Route path="/admin/audit" element={<AdminAuditPage />} />
                </Route>
                <Route path="*" element={<NotFoundPage />} />
              </Route>
            </Routes>
          </AuthProvider>
        </ToastProvider>
      </BrowserRouter>
    </ThemeProvider>
  );
}
