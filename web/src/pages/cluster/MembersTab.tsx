import { useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import type { VClusterView } from '../../api/types';
import { addMember, removeMember } from '../../api/vclusters';
import { useAuth } from '../../auth/AuthContext';
import { Badge, RoleBadge } from '../../components/Badge';
import { Button } from '../../components/Button';
import { Card } from '../../components/Card';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Table } from '../../components/Table';
import { useToast } from '../../components/Toast';
import { validateUserName } from '../../utils/validate';

interface Props {
  vc: VClusterView;
  currentUser: string;
  onChanged: () => void;
}

// 成员 Tab：owner / member / 平台 admin 均可增删成员；owner 本人不可移除。
export function MembersTab({ vc, currentUser, onChanged }: Props) {
  const toast = useToast();
  const navigate = useNavigate();
  const { isAdmin } = useAuth();
  const [newUser, setNewUser] = useState('');
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);

  const add = async (e: FormEvent) => {
    e.preventDefault();
    const u = newUser.trim();
    const err = validateUserName(u);
    if (err) {
      toast.error(err);
      return;
    }
    if (u === vc.owner || vc.members.includes(u)) {
      toast.info(`${u} 已经是该集群的成员`);
      return;
    }
    setAdding(true);
    try {
      await addMember(vc.name, u);
      toast.success(`已添加成员 ${u}`);
      setNewUser('');
      onChanged();
    } catch (e2) {
      toast.error(`添加失败：${errorMessage(e2)}`);
    } finally {
      setAdding(false);
    }
  };

  const remove = async () => {
    if (!removing) return;
    const u = removing;
    try {
      await removeMember(vc.name, u);
      toast.success(`已移除成员 ${u}`);
      setRemoving(null);
      if (u === currentUser && !isAdmin) {
        // 把自己移出后已无权查看该集群。
        navigate('/');
      } else {
        onChanged();
      }
    } catch (err) {
      toast.error(`移除失败：${errorMessage(err)}`);
    }
  };

  const me = (name: string) => name === currentUser && <Badge tone="neutral">我</Badge>;

  return (
    <Card title="成员">
      <p className="muted">所有者与成员在虚拟集群内均为 cluster-admin，可以各自生成自己的 kubeconfig。</p>
      <Table>
        <thead>
          <tr>
            <th>用户名</th>
            <th>角色</th>
            <th className="col-actions">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td className="mono">
              {vc.owner} {me(vc.owner)}
            </td>
            <td>
              <RoleBadge role="owner" />
            </td>
            <td className="col-actions" />
          </tr>
          {vc.members.map((m) => (
            <tr key={m}>
              <td className="mono">
                {m} {me(m)}
              </td>
              <td>
                <RoleBadge role="member" />
              </td>
              <td className="col-actions">
                <Button variant="danger-ghost" size="sm" onClick={() => setRemoving(m)}>
                  移除
                </Button>
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
      {vc.members.length === 0 && <p className="muted section-gap">暂无其他成员。</p>}

      <form className="inline-form" onSubmit={add}>
        <input type="text" value={newUser} onChange={(e) => setNewUser(e.target.value)} placeholder="输入用户名" spellCheck={false} disabled={adding} />
        <Button type="submit" variant="primary" disabled={adding || !newUser.trim()}>
          {adding ? '添加中…' : '添加成员'}
        </Button>
      </form>

      {removing && (
        <ConfirmDialog
          title="移除成员"
          danger
          confirmText="移除"
          onClose={() => setRemoving(null)}
          onConfirm={remove}
          message={
            <p>
              确定把 <span className="mono strong">{removing}</span> 移出集群 <span className="mono">{vc.name}</span>？其 kubeconfig 将在 2 分钟内失效。
              {removing === currentUser && ' 你正在移除自己，之后将无法再访问该集群。'}
            </p>
          }
        />
      )}
    </Card>
  );
}
