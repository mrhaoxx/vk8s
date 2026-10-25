// 极薄的 fetch 封装：同源 cookie、变更请求带 X-Requested-With、统一错误与 401 处理。

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

type UnauthorizedHandler = () => void;
let unauthorizedHandler: UnauthorizedHandler | null = null;

// 由 AuthProvider 注册：收到 401 时清空会话，路由守卫随即跳到登录页。
export function onUnauthorized(handler: UnauthorizedHandler | null): void {
  unauthorizedHandler = handler;
}

export interface RequestOptions {
  // 为 true 时 401 不触发全局处理（用于登录接口本身）。
  ignore401?: boolean;
  signal?: AbortSignal;
}

async function request<T>(method: string, path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (method !== 'GET') headers['X-Requested-With'] = 'vk8s';
  if (body !== undefined) headers['Content-Type'] = 'application/json';

  let res: Response;
  try {
    res = await fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'same-origin',
      signal: opts.signal,
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw new ApiError(0, '网络错误，请检查连接后重试');
  }

  if (res.ok) {
    if (res.status === 204) return undefined as T;
    const text = await res.text();
    return (text ? JSON.parse(text) : undefined) as T;
  }

  let message = `请求失败（HTTP ${res.status}）`;
  try {
    const data: unknown = await res.json();
    if (data && typeof data === 'object' && typeof (data as { error?: unknown }).error === 'string') {
      message = (data as { error: string }).error;
    }
  } catch {
    // 无 JSON 正文
  }
  if (res.status === 401 && !opts.ignore401) unauthorizedHandler?.();
  throw new ApiError(res.status, message);
}

export const http = {
  get: <T>(path: string, opts?: RequestOptions) => request<T>('GET', path, undefined, opts),
  post: <T>(path: string, body?: unknown, opts?: RequestOptions) => request<T>('POST', path, body, opts),
  put: <T>(path: string, body?: unknown, opts?: RequestOptions) => request<T>('PUT', path, body, opts),
  patch: <T>(path: string, body?: unknown, opts?: RequestOptions) => request<T>('PATCH', path, body, opts),
  del: <T>(path: string, opts?: RequestOptions) => request<T>('DELETE', path, undefined, opts),
};

// 把任意异常转成可展示的文字。
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message || '未知错误';
  return String(err);
}

export const enc = encodeURIComponent;
