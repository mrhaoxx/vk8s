import { useState } from 'react';
import { errorMessage } from '../../api/client';
import { getBookableImpact, listNodes, setBookable } from '../../api/nodes';
import type { BookableImpactView, NodeView } from '../../api/types';
import { Button } from '../../components/Button';
import { Card } from '../../components/Card';
import { Chips } from '../../components/Chips';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { PageHeader } from '../../components/PageHeader';
import { EmptyState, ErrorState, Loading } from '../../components/State';
import { StatusDot } from '../../components/StatusDot';
import { Table } from '../../components/Table';
import { useToast } from '../../components/Toast';
import { useAsync } from '../../hooks/useAsync';
import { impactMessage } from './bookableImpact';

function Labels({ labels }: { labels: Record<string, string> }) {
  const entries = Object.entries(labels).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) return <span className="muted">—</span>;
  return (
    <details className="labels">
      <summary>{entries.length} 个标签</summary>
      <ul className="label-list">
        {entries.map(([k, v]) => (
          <li key={k}>
            <code>
              {k}={v}
            </code>
          </li>
        ))}
      </ul>
    </details>
  );
}

function taintText(t: NodeView['taints'][number]): string {
  return `${t.key}${t.value ? `=${t.value}` : ''}:${t.effect}`;
}

interface Pending {
  node: NodeView;
  turnOn: boolean;
  impact: BookableImpactView;
}

export function AdminNodesPage() {
  const toast = useToast();
  const { data, error, loading, reload } = useAsync(listNodes, []);
  const [pending, setPending] = useState<Pending | null>(null);
  const [busyNode, setBusyNode] = useState<string | null>(null);
  const ready = (data ?? []).filter((n) => n.ready).length;

  const askToggle = async (node: NodeView) => {
    setBusyNode(node.name);
    try {
      const impact = await getBookableImpact(node.name);
      setPending({ node, turnOn: !node.bookable, impact });
    } catch (err) {
      toast.error(`读取影响失败：${errorMessage(err)}`);
    } finally {
      setBusyNode(null);
    }
  };

  const confirmToggle = async () => {
    if (!pending) return;
    try {
      await setBookable(pending.node.name, pending.turnOn);
      toast.success(pending.turnOn ? `已把 ${pending.node.name} 标记为可预约` : `已取消 ${pending.node.name} 的可预约标记`);
      setPending(null);
      reload();
    } catch (err) {
      toast.error(`操作失败：${errorMessage(err)}`);
    }
  };

  const msg = pending ? impactMessage(pending.impact, pending.turnOn) : null;

  return (
    <>
      <PageHeader
        title="节点"
        subtitle={data ? `共 ${data.length} 个节点，${ready} 个就绪` : undefined}
        actions={
          <Button onClick={reload} disabled={loading}>
            刷新
          </Button>
        }
      />

      {loading && !data ? (
        <Loading />
      ) : error && !data ? (
        <ErrorState message={error} onRetry={reload} />
      ) : (data ?? []).length === 0 ? (
        <EmptyState title="没有节点" hint="宿主集群未返回任何节点。" />
      ) : (
        <Card flush>
          <Table>
            <thead>
              <tr>
                <th>名称</th>
                <th>状态</th>
                <th>可预约</th>
                <th>标签</th>
                <th>Taints</th>
                <th>CPU</th>
                <th>内存</th>
                <th>GPU</th>
              </tr>
            </thead>
            <tbody>
              {(data ?? []).map((n) => (
                <tr key={n.name}>
                  <td className="mono strong">{n.name}</td>
                  <td>
                    <StatusDot value={n.ready ? 'Ready' : 'False'} label={n.ready ? 'Ready' : 'NotReady'} />
                  </td>
                  <td>
                    <div className="actions">
                      <Button size="sm" variant={n.bookable ? 'default' : 'ghost'} onClick={() => askToggle(n)} disabled={busyNode === n.name}>
                        {n.bookable ? '可预约' : '标记可预约'}
                      </Button>
                      {n.bookable && n.leases > 0 && <span className="muted small">{n.leases} 个租约</span>}
                    </div>
                  </td>
                  <td>
                    <Labels labels={n.labels} />
                  </td>
                  <td>{n.taints.length === 0 ? <span className="muted">无</span> : <Chips items={n.taints.map(taintText)} />}</td>
                  <td>{n.allocatable['cpu'] ?? <span className="muted">—</span>}</td>
                  <td>{n.allocatable['memory'] ?? <span className="muted">—</span>}</td>
                  <td>{n.allocatable['nvidia.com/gpu'] ?? <span className="muted">—</span>}</td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Card>
      )}

      {pending && msg && (
        <ConfirmDialog
          title={pending.turnOn ? `标记 ${pending.node.name} 为可预约？` : `取消 ${pending.node.name} 的可预约标记？`}
          danger={msg.danger}
          confirmText={pending.turnOn ? '确认标记' : '确认取消'}
          onClose={() => setPending(null)}
          onConfirm={confirmToggle}
          message={
            <>
              {msg.lines.map((l, i) => (
                <p key={i} className={i > 0 && msg.danger ? 'field-note warn' : undefined}>
                  {l}
                </p>
              ))}
            </>
          }
        />
      )}
    </>
  );
}
