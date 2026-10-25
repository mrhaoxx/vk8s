import { ApiError, http } from './client';
import type { MeResponse, SessionUser } from './types';

export interface SessionInfo {
  user: SessionUser | null;
  oidcEnabled: boolean;
}

// 直接用 fetch：401 是「未登录」的正常状态，不能触发全局 401 处理；
// 若后端在 401 正文里也给了 oidcEnabled，则顺带读出来供登录页使用。
export async function fetchSession(): Promise<SessionInfo> {
  const res = await fetch('/auth/me', {
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
  });
  let body: Partial<MeResponse> & { error?: string } = {};
  try {
    body = (await res.json()) as typeof body;
  } catch {
    // 无正文
  }
  if (res.ok && body.user) {
    return { user: body.user, oidcEnabled: body.oidcEnabled === true };
  }
  if (res.status === 401) {
    return { user: null, oidcEnabled: body.oidcEnabled === true };
  }
  throw new ApiError(res.status, body.error ?? `请求失败（HTTP ${res.status}）`);
}

export function login(username: string, password: string): Promise<void> {
  return http.post<void>('/auth/login', { username, password }, { ignore401: true });
}

export function logout(): Promise<void> {
  return http.post<void>('/auth/logout');
}

export const OIDC_START_URL = '/auth/oidc/start';
