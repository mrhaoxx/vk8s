import { useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { listVClusters } from '../../api/vclusters';
import { Button } from '../../components/Button';
import { Card } from '../../components/Card';
import { PageHeader } from '../../components/PageHeader';
import { EmptyState, ErrorState, Loading } from '../../components/State';
import { StatusDot } from '../../components/StatusDot';
import { Table } from '../../components/Table';
import { TimeAgo } from '../../components/TimeAgo';
import { useAsync } from '../../hooks/useAsync';
import { usePolling } from '../../hooks/usePolling';
import { summarizeNodePlacement } from '../../utils/format';

const TRANSITIONAL = new Set(['', 'Pending', 'Provisioning', 'Deleting']);

export function AdminClustersPage() {
  const navigate = useNavigate();
  const { data, error, loading, reload } = useAsync(listVClusters, []);
  const [filter, setFilter] = useState('');

  usePolling((data ?? []).some((v) => TRANSITIONAL.has(v.status.phase)), 15_000, reload);

  const items = useMemo(() => {
    const q = filter.trim().toLowerCase();
    const all = data ?? [];
    return q ? all.filter((v) => v.name.includes(q) || v.owner.includes(q) || v.members.some((m) => m.includes(q))) : all;
  }, [data, filter]);

  return (
    <>
      <PageHeader
        title="所有集群"
        subtitle={data ? `共 ${data.length} 个集群` : undefined}
        actions={
          <>
            <input type="search" placeholder="按名称 / 所有者 / 成员过滤" value={filter} onChange={(e) => setFilter(e.target.value)} className="filter-input" />
            <Button onClick={reload} disabled={loading}>
              刷新
            </Button>
          </>
        }
      />

      {loading && !data ? (
        <Loading />
      ) : error && !data ? (
        <ErrorState message={error} onRetry={reload} />
      ) : items.length === 0 ? (
        <EmptyState title={filter ? '没有匹配的集群' : '还没有任何集群'} />
      ) : (
        <Card flush>
          <Table hoverable>
            <thead>
              <tr>
                <th>名称</th>
                <th>状态</th>
                <th>所有者</th>
                <th>成员</th>
                <th>安全档位</th>
                <th>节点调度</th>
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
                  <td title={v.members.join(', ') || undefined}>{v.members.length}</td>
                  <td className="mono">{v.podSecurity}</td>
                  <td className="wrap muted">{summarizeNodePlacement(v.nodePlacement)}</td>
                  <td>
                    <TimeAgo value={v.createdAt} />
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Card>
      )}
    </>
  );
}
