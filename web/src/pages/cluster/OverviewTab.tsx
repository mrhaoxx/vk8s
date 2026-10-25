import type { BookingView, VClusterView } from '../../api/types';
import { Card } from '../../components/Card';
import { Chips } from '../../components/Chips';
import { CopyButton } from '../../components/CopyButton';
import { DescriptionList } from '../../components/DescriptionList';
import { StatusDot } from '../../components/StatusDot';
import { Table } from '../../components/Table';
import { StatusDot as Dot } from '../../components/StatusDot';
import { TimeAgo } from '../../components/TimeAgo';
import { formatToleration, isNodePlacementEmpty } from '../../utils/format';

const hm = (iso: string) => {
  const d = new Date(iso);
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
};
const md = (iso: string) => {
  const d = new Date(iso);
  return `${d.getMonth() + 1}/${d.getDate()}`;
};

// 概览 Tab：只读信息与健康状态。bookings 为本集群的预约（用于显示待开始的租约）。
export function OverviewTab({ vc, bookings = [] }: { vc: VClusterView; bookings?: BookingView[] }) {
  const { status, quota, nodePlacement: np } = vc;
  const leased = status.leasedNodes ?? [];
  const pending = bookings.filter((b) => b.phase === 'Pending').sort((a, b) => a.start.localeCompare(b.start));
  const placementChips = [
    ...Object.entries(np.nodeSelector).map(([k, v]) => `${k}=${v}`),
    ...np.nodeNames,
    ...np.tolerations.map(formatToleration),
  ];

  return (
    <>
      <div className="grid-2">
        <Card title="基本信息">
          <DescriptionList
            items={[
              {
                term: 'API 地址',
                value: status.apiEndpoint ? (
                  <>
                    <code>{status.apiEndpoint}</code>
                    <CopyButton text={status.apiEndpoint} className="btn btn-xs" />
                  </>
                ) : (
                  <span className="muted">尚未分配</span>
                ),
              },
              { term: '应用域名', value: status.appsDomain ? <code>*.{status.appsDomain}</code> : <span className="muted">尚未分配</span> },
              { term: '宿主命名空间', value: status.hostNamespace ? <code>{status.hostNamespace}</code> : '' },
              { term: '所有者', value: <span className="mono">{vc.owner}</span> },
              { term: '创建时间', value: <TimeAgo value={vc.createdAt} /> },
              { term: '安全档位', value: <span className="mono">{vc.podSecurity}</span> },
              {
                term: '控制平面存储',
                value: (
                  <>
                    {vc.controlPlane.storageSize || '—'}
                    <span className="muted">（{vc.controlPlane.storageClassName || '默认 StorageClass'}）</span>
                  </>
                ),
              },
            ]}
          />
        </Card>
        <Card title="配额与节点">
          <DescriptionList
            items={[
              { term: 'CPU', value: quota.cpu },
              { term: '内存', value: quota.memory },
              { term: 'Pod / Service', value: `${quota.pods || '—'} · ${quota.services || '—'}` },
              { term: '存储', value: quota.storage },
              ...Object.entries(quota.extended).map(([k, v]) => ({ term: <code>{k}</code>, value: v })),
              {
                term: '节点池',
                value: isNodePlacementEmpty(np) ? <span className="muted">平台默认节点池</span> : <Chips items={placementChips} />,
              },
              ...(leased.length > 0 || pending.length > 0
                ? [
                    {
                      term: '预约节点',
                      value: (
                        <div className="chips">
                          {leased.map((l) => (
                            <code key={l.booking} className="chip" title={`预约 ${l.booking}`}>
                              <Dot value="Running" label={`${l.node} · 至 ${hm(l.until)}`} />
                            </code>
                          ))}
                          {pending.map((b) => (
                            <code key={b.name} className="chip" title={`预约 ${b.name}`}>
                              <Dot value="Pending" label={`${b.node} · ${md(b.start)} ${hm(b.start)} 起`} />
                            </code>
                          ))}
                        </div>
                      ),
                    },
                  ]
                : []),
            ]}
          />
        </Card>
      </div>

      <Card title="Conditions">
        {status.conditions.length === 0 ? (
          <p className="muted">暂无</p>
        ) : (
          <Table compact>
            <thead>
              <tr>
                <th>类型</th>
                <th>状态</th>
                <th>原因</th>
                <th>信息</th>
                <th>变更时间</th>
              </tr>
            </thead>
            <tbody>
              {status.conditions.map((c) => (
                <tr key={c.type}>
                  <td className="mono">{c.type}</td>
                  <td>
                    <StatusDot value={c.status} />
                  </td>
                  <td>{c.reason || <span className="muted">—</span>}</td>
                  <td className="wrap">{c.message || <span className="muted">—</span>}</td>
                  <td>
                    <TimeAgo value={c.lastTransitionTime} />
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>
    </>
  );
}
