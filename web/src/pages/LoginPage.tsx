import { useState, type FormEvent } from 'react';
import { Navigate, useLocation, useSearchParams } from 'react-router-dom';
import { OIDC_START_URL } from '../api/auth';
import { ApiError, errorMessage } from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Button } from '../components/Button';
import { Field } from '../components/Field';

export function LoginPage() {
  const { ready, user, oidcEnabled, login } = useAuth();
  const location = useLocation();
  const [params] = useSearchParams();
  const from = (location.state as { from?: string } | null)?.from ?? '/';

  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  // OIDC 回调失败时后端可能带 ?error= 跳回登录页。
  const [error, setError] = useState<string | null>(params.get('error'));
  const [busy, setBusy] = useState(false);

  if (ready && user) return <Navigate to={from} replace />;

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!username.trim() || !password) {
      setError('请输入用户名和密码');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await login(username.trim(), password);
    } catch (err) {
      setError(err instanceof ApiError && err.status === 401 ? '用户名或密码错误' : errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-page">
      <form className="login-card" onSubmit={onSubmit} noValidate>
        <h1 className="brand brand-lg">vk8s</h1>
        <p className="muted login-subtitle">登录虚拟集群控制台</p>
        {error && (
          <div className="alert alert-error" role="alert">
            {error}
          </div>
        )}
        <Field label="用户名">
          <input
            type="text"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
            autoFocus
            spellCheck={false}
            disabled={busy}
          />
        </Field>
        <Field label="密码">
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
            disabled={busy}
          />
        </Field>
        <Button type="submit" variant="primary" block disabled={busy}>
          {busy ? '登录中…' : '登录'}
        </Button>
        {oidcEnabled && (
          <>
            <div className="divider">
              <span>或</span>
            </div>
            <a className="btn btn-block" href={OIDC_START_URL}>
              使用 SSO 登录
            </a>
          </>
        )}
      </form>
    </div>
  );
}
