import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { fetchSession, login as apiLogin, logout as apiLogout } from '../api/auth';
import { onUnauthorized } from '../api/client';
import type { SessionUser } from '../api/types';

export interface AuthValue {
  ready: boolean; // 首次 /auth/me 是否已返回
  user: SessionUser | null;
  oidcEnabled: boolean;
  isAdmin: boolean;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  refresh: () => Promise<void>;
}

const AuthContext = createContext<AuthValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [ready, setReady] = useState(false);
  const [user, setUser] = useState<SessionUser | null>(null);
  const [oidcEnabled, setOidcEnabled] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const s = await fetchSession();
      setUser(s.user);
      setOidcEnabled(s.oidcEnabled);
    } catch {
      setUser(null);
    } finally {
      setReady(true);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // 任一 API 返回 401 → 清空会话，守卫会跳登录页。
  useEffect(() => {
    onUnauthorized(() => setUser(null));
    return () => onUnauthorized(null);
  }, []);

  const login = useCallback(
    async (username: string, password: string) => {
      await apiLogin(username, password);
      await refresh();
    },
    [refresh],
  );

  const logout = useCallback(async () => {
    try {
      await apiLogout();
    } finally {
      setUser(null);
    }
  }, []);

  const value = useMemo<AuthValue>(
    () => ({ ready, user, oidcEnabled, isAdmin: user?.role === 'admin', login, logout, refresh }),
    [ready, user, oidcEnabled, login, logout, refresh],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthValue {
  const v = useContext(AuthContext);
  if (!v) throw new Error('useAuth 必须在 AuthProvider 内使用');
  return v;
}
