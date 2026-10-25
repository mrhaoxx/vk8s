import { useState, type FormEvent } from 'react';
import { errorMessage } from '../../api/client';
import type { PatchUserRequest, Role, UserView } from '../../api/types';
import { createUser, patchUser, setUserPassword } from '../../api/users';
import { Field } from '../../components/Field';
import { Modal } from '../../components/Modal';
import { useToast } from '../../components/Toast';
import { validateUserName } from '../../utils/validate';

const MIN_PASSWORD = 8;

function validatePassword(pw: string, confirm: string): string | null {
  if (pw.length < MIN_PASSWORD) return `密码至少 ${MIN_PASSWORD} 个字符`;
  if (pw !== confirm) return '两次输入的密码不一致';
  return null;
}

interface DialogProps {
  onClose: () => void;
  onDone: () => void;
}

function Footer({ formId, busy, onClose, text }: { formId: string; busy: boolean; onClose: () => void; text: string }) {
  return (
    <>
      <button type="button" className="btn" onClick={onClose} disabled={busy}>
        取消
      </button>
      <button type="submit" form={formId} className="btn btn-primary" disabled={busy}>
        {busy ? '处理中…' : text}
      </button>
    </>
  );
}

// 新建静态账号用户。
export function CreateUserDialog({ onClose, onDone }: DialogProps) {
  const toast = useToast();
  const [name, setName] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [email, setEmail] = useState('');
  const [role, setRole] = useState<Role>('user');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const err = validateUserName(name.trim()) ?? validatePassword(password, confirm);
    if (err) {
      setError(err);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const u = await createUser({ name: name.trim(), displayName: displayName.trim(), email: email.trim(), role, password });
      toast.success(`用户 ${u.name} 已创建`);
      onDone();
    } catch (e2) {
      setError(errorMessage(e2));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title="新建用户" onClose={onClose} footer={<Footer formId="create-user-form" busy={busy} onClose={onClose} text="创建" />}>
      <form id="create-user-form" onSubmit={onSubmit} noValidate>
        {error && (
          <div className="alert alert-error" role="alert">
            {error}
          </div>
        )}
        <Field label="用户名" required hint="登录名，小写字母、数字和连字符；创建后不可改">
          <input type="text" value={name} onChange={(e) => setName(e.target.value.trim())} autoFocus spellCheck={false} autoComplete="off" disabled={busy} />
        </Field>
        <div className="grid-2">
          <Field label="显示名">
            <input type="text" value={displayName} onChange={(e) => setDisplayName(e.target.value)} disabled={busy} />
          </Field>
          <Field label="邮箱">
            <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="off" disabled={busy} />
          </Field>
        </div>
        <Field label="角色">
          <select value={role} onChange={(e) => setRole(e.target.value as Role)} disabled={busy}>
            <option value="user">用户</option>
            <option value="admin">管理员</option>
          </select>
        </Field>
        <div className="grid-2">
          <Field label="初始密码" required hint={`至少 ${MIN_PASSWORD} 个字符`}>
            <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" disabled={busy} />
          </Field>
          <Field label="确认密码" required>
            <input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" disabled={busy} />
          </Field>
        </div>
      </form>
    </Modal>
  );
}

// 编辑显示名 / 邮箱 / 角色 / 禁用；只提交有变化的字段。
export function EditUserDialog({ user, isSelf, onClose, onDone }: DialogProps & { user: UserView; isSelf: boolean }) {
  const toast = useToast();
  const [displayName, setDisplayName] = useState(user.displayName);
  const [email, setEmail] = useState(user.email);
  const [role, setRole] = useState<Role>(user.role);
  const [disabled, setDisabled] = useState(user.disabled);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const patch: PatchUserRequest = {};
  if (displayName.trim() !== user.displayName) patch.displayName = displayName.trim();
  if (email.trim() !== user.email) patch.email = email.trim();
  if (role !== user.role) patch.role = role;
  if (disabled !== user.disabled) patch.disabled = disabled;
  const changed = Object.keys(patch).length > 0;

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!changed) {
      onClose();
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await patchUser(user.name, patch);
      toast.success(`用户 ${user.name} 已更新`);
      onDone();
    } catch (e2) {
      setError(errorMessage(e2));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={`编辑用户 ${user.name}`} onClose={onClose} footer={<Footer formId="edit-user-form" busy={busy} onClose={onClose} text="保存" />}>
      <form id="edit-user-form" onSubmit={onSubmit} noValidate>
        {error && (
          <div className="alert alert-error" role="alert">
            {error}
          </div>
        )}
        <div className="grid-2">
          <Field label="显示名">
            <input type="text" value={displayName} onChange={(e) => setDisplayName(e.target.value)} autoFocus disabled={busy} />
          </Field>
          <Field label="邮箱">
            <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} disabled={busy} />
          </Field>
        </div>
        <Field label="角色" hint={isSelf ? '不能修改自己的角色' : undefined}>
          <select value={role} onChange={(e) => setRole(e.target.value as Role)} disabled={busy || isSelf}>
            <option value="user">用户</option>
            <option value="admin">管理员</option>
          </select>
        </Field>
        <label className="check">
          <input type="checkbox" checked={disabled} onChange={(e) => setDisabled(e.target.checked)} disabled={busy || isSelf} />
          <span>禁用该用户（禁用后立即无法登录，其所有 kubeconfig 在 2 分钟内失效）</span>
        </label>
        {isSelf && <p className="muted small">不能禁用自己。</p>}
      </form>
    </Modal>
  );
}

// 设置 / 重置静态密码。
export function PasswordDialog({ user, onClose, onDone }: DialogProps & { user: UserView }) {
  const toast = useToast();
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const err = validatePassword(password, confirm);
    if (err) {
      setError(err);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await setUserPassword(user.name, password);
      toast.success(`已${user.hasStatic ? '重置' : '设置'} ${user.name} 的密码`);
      onDone();
    } catch (e2) {
      setError(errorMessage(e2));
    } finally {
      setBusy(false);
    }
  };

  const title = `${user.hasStatic ? '重置' : '设置'}密码：${user.name}`;
  return (
    <Modal title={title} onClose={onClose} footer={<Footer formId="password-form" busy={busy} onClose={onClose} text="确定" />}>
      <form id="password-form" onSubmit={onSubmit} noValidate>
        {error && (
          <div className="alert alert-error" role="alert">
            {error}
          </div>
        )}
        {!user.hasStatic && <p className="muted">该用户目前只有 OIDC 登录方式，设置密码后也可以用用户名密码登录。</p>}
        <Field label="新密码" required hint={`至少 ${MIN_PASSWORD} 个字符`}>
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoFocus autoComplete="new-password" disabled={busy} />
        </Field>
        <Field label="确认密码" required>
          <input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" disabled={busy} />
        </Field>
      </form>
    </Modal>
  );
}
