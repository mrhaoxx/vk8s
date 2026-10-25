import { useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { listVClusters } from '../api/vclusters';
import { useAuth } from '../auth/AuthContext';
import { useConfig } from '../auth/ConfigContext';
import { RoleBadge } from '../components/Badge';
import { Button, ButtonLink } from '../components/Button';
import { Card } from '../components/Card';
import { PageHeader } from '../components/PageHeader';
import { EmptyState, ErrorState, Loading } from '../components/State';
import { StatusDot } from '../components/StatusDot';
import { Table } from '../components/Table';
import { TimeAgo } from '../components/TimeAgo';
import { useAsync } from '../hooks/useAsync';
import { usePolling } from '../hooks/usePolling';
import { CreateClusterDialog } from './cluster/CreateClusterDialog';

const TRANSITIONAL = new Set(['', 'Pending', 'Provisioning', 'Deleting']);

export function MyClustersPage() {
  const { user, isAdmin } = useAuth();
  const config = useConfig();
  const navigate = useNavigate();
  const { data, error, loading, reload } = useAsync(listVClusters, []);
  const [creating, setCreating] = useState(false);

  // 后端对 admin 可能返回全部集群；这里只展示我作为 owner / member 的。
  const items = useMemo(() => {
    const all = data ?? [];
    return isAdmin ? all.filter((v) => v.myRole !== 'admin') : all;
  }, [data, isAdmin]);

  usePolling(items.some((v) => TRANSITIONAL.has(v.status.phase)), 10_000, reload);

  const owned = items.filter((v) => v.owner === user?.name).length;
  const max = config?.limits.maxVClustersPerUser ?? 0;
  const atLimit = !isAdmin && max > 0 && owned >= max;

  return (
    <>
      <PageHeader
        title="我的集群"
        subtitle={
          max > 0 ? (
            <>
              已拥有 {owned} / {max} 个集群{isAdmin && '（管理员不受限制）'}
            </>
          ) : undefined
        }
        actions={
          <>
            <Button onClick={reload} disabled={loading}>
              刷新
            </Button>
            <Button variant="primary" onClick={() => setCreating(true)} disabled={atLimit} title={atLimit ? `每人最多拥有 ${max} 个集群` : undefined}>
              新建集群
            </Button>
          </>
        }
      />

      {loading && !data ? (
        <Loading />
      ) : error && !data ? (
        <ErrorState message={error} onRetry={reload} />
      ) : items.length === 0 ? (
        <EmptyState
          title="还没有集群"
          hint={
            isAdmin && (data?.length ?? 0) > 0
              ? '你不是任何集群的所有者或成员；全部集群请到「所有集群」查看。'
              : '创建一个虚拟集群，几分钟后即可下载 kubeconfig 开始使用。'
          }
          action={
            <div className="actions">
              <Button variant="primary" onClick={() => setCreating(true)}>
                新建集群
              </Button>
              {isAdmin && <ButtonLink to="/admin/vclusters">所有集群</ButtonLink>}
            </div>
          }
        />
      ) : (
        <Card flush>
          <Table hoverable>
            <thead>
              <tr>
                <th>名称</th>
                <th>状态</th>
                <th>所有者</th>
                <th>我的角色</th>
                <th>API 地址</th>
                <th>应用域名</th>
                <th>创建时间</th>
              </tr>
            </thead>
            <tbody>
              {items.map((v) => (
                <tr key={v.name} onClick={() => navigate(`/vclusters/${encodeURIComponent(v.name)}`)}>
                  <td>
                    <Link className="mono strong" to={`/vclusters/${encodeURIComponent(v.name)}`} onClick={(e) => e.stopPropagation()}>
                      {v.name}
                    </Link>
                  </td>
                  <td>
                    <StatusDot value={v.status.phase} title={v.status.reason || undefined} />
                  </td>
                  <td className="mono">{v.owner}</td>
                  <td>
                    <RoleBadge role={v.myRole} />
                  </td>
                  <td>{v.status.apiEndpoint ? <code>{v.status.apiEndpoint}</code> : <span className="muted">—</span>}</td>
                  <td>{v.status.appsDomain ? <code>*.{v.status.appsDomain}</code> : <span className="muted">—</span>}</td>
                  <td>
                    <TimeAgo value={v.createdAt} />
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Card>
      )}

      {creating && (
        <CreateClusterDialog
          onClose={() => setCreating(false)}
          onCreated={(vc) => {
            setCreating(false);
            navigate(`/vclusters/${encodeURIComponent(vc.name)}`);
          }}
        />
      )}
    </>
  );
}
