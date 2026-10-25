import { useState } from 'react';
import type { UserView } from '../../api/types';
import { listUsers } from '../../api/users';
import { useAuth } from '../../auth/AuthContext';
import { Badge } from '../../components/Badge';
import { Button } from '../../components/Button';
import { Card } from '../../components/Card';
import { PageHeader } from '../../components/PageHeader';
import { EmptyState, ErrorState, Loading } from '../../components/State';
import { StatusDot } from '../../components/StatusDot';
import { Table } from '../../components/Table';
import { TimeAgo } from '../../components/TimeAgo';
import { useAsync } from '../../hooks/useAsync';
import { CreateUserDialog, EditUserDialog, PasswordDialog } from './UserDialogs';

export function AdminUsersPage() {
  const { user: me } = useAuth();
  const { data, error, loading, reload } = useAsync(listUsers, []);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<UserView | null>(null);
  const [resetting, setResetting] = useState<UserView | null>(null);

  return (
    <>
      <PageHeader
        title="用户"
        subtitle={data ? `共 ${data.length} 个用户` : undefined}
        actions={
          <>
            <Button onClick={reload} disabled={loading}>
              刷新
            </Button>
            <Button variant="primary" onClick={() => setCreating(true)}>
              新建用户
            </Button>
          </>
        }
      />

      {loading && !data ? (
        <Loading />
      ) : error && !data ? (
        <ErrorState message={error} onRetry={reload} />
      ) : (data ?? []).length === 0 ? (
        <EmptyState title="还没有用户" />
      ) : (
        <Card flush>
          <Table>
            <thead>
              <tr>
                <th>用户名</th>
                <th>显示名</th>
                <th>邮箱</th>
                <th>角色</th>
                <th>状态</th>
                <th>登录方式</th>
                <th>最近登录</th>
                <th className="col-actions">操作</th>
              </tr>
            </thead>
            <tbody>
              {(data ?? []).map((u) => (
                <tr key={u.name} className={u.disabled ? 'row-muted' : undefined}>
                  <td className="mono strong">
                    {u.name}
                    {u.name === me?.name && <Badge tone="neutral">我</Badge>}
                  </td>
                  <td>{u.displayName || <span className="muted">—</span>}</td>
                  <td>{u.email || <span className="muted">—</span>}</td>
                  <td>
                    <Badge tone="neutral">{u.role === 'admin' ? '管理员' : '用户'}</Badge>
                  </td>
                  <td>{u.disabled ? <StatusDot value="Failed" label="已禁用" /> : <StatusDot value="Running" label="正常" />}</td>
                  <td>
                    <div className="chips">
                      {u.hasStatic && <Badge tone="neutral">静态密码</Badge>}
                      {u.hasOIDC && <Badge tone="neutral">OIDC</Badge>}
                      {!u.hasStatic && !u.hasOIDC && <span className="muted">—</span>}
                    </div>
                  </td>
                  <td>
                    <TimeAgo value={u.lastLoginTime} />
                  </td>
                  <td className="col-actions">
                    <div className="actions" style={{ justifyContent: 'flex-end' }}>
                      <Button size="sm" onClick={() => setEditing(u)}>
                        编辑
                      </Button>
                      <Button size="sm" onClick={() => setResetting(u)}>
                        {u.hasStatic ? '重置密码' : '设置密码'}
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Card>
      )}

      {creating && (
        <CreateUserDialog
          onClose={() => setCreating(false)}
          onDone={() => {
            setCreating(false);
            reload();
          }}
        />
      )}
      {editing && (
        <EditUserDialog
          user={editing}
          isSelf={editing.name === me?.name}
          onClose={() => setEditing(null)}
          onDone={() => {
            setEditing(null);
            reload();
          }}
        />
      )}
      {resetting && (
        <PasswordDialog
          user={resetting}
          onClose={() => setResetting(null)}
          onDone={() => {
            setResetting(null);
            reload();
          }}
        />
      )}
    </>
  );
}
