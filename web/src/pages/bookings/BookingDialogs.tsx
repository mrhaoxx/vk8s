import { useState, type FormEvent } from 'react';
import { createBooking, deleteBooking } from '../../api/bookings';
import { errorMessage } from '../../api/client';
import type { BookingView, VClusterView } from '../../api/types';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { DescriptionList } from '../../components/DescriptionList';
import { Field } from '../../components/Field';
import { Modal } from '../../components/Modal';
import { StatusDot } from '../../components/StatusDot';
import { useToast } from '../../components/Toast';
import type { BookingDraft } from './BookingTimeline';
import { HOUR_MS, formatRange } from './timeline';

const VC_PREF = 'vk8s.booking.vcluster';

const ROLE: Record<string, string> = { owner: '所有者', member: '成员', admin: '管理员' };

export const REASON_TEXT: Record<string, string> = {
  Overlap: '与另一预约时间重叠',
  NodeNotBookable: '节点已不可预约',
  NodeNotFound: '节点已不存在',
  VClusterNotFound: '目标集群已不存在',
};

function hoursOf(startMs: number, endMs: number): number {
  return Math.round((endMs - startMs) / HOUR_MS);
}

export function formatRemaining(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 60_000));
  const h = Math.floor(total / 60);
  const m = total % 60;
  return h > 0 ? `${h} 小时 ${m} 分` : `${m} 分`;
}

interface CreateProps {
  draft: BookingDraft;
  // 我可见、且不在删除中的集群。
  clusters: VClusterView[];
  onClose: () => void;
  onCreated: (b: BookingView) => void;
}

// 拖选结束后的创建对话框：节点与时间只读，选目标集群、填用途。
export function CreateBookingDialog({ draft, clusters, onClose, onCreated }: CreateProps) {
  const [vcluster, setVCluster] = useState(() => {
    let pref = '';
    try {
      pref = localStorage.getItem(VC_PREF) ?? '';
    } catch {
      // 存储不可用时用第一个
    }
    return clusters.some((c) => c.name === pref) ? pref : (clusters[0]?.name ?? '');
  });
  const [purpose, setPurpose] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!vcluster) return;
    setBusy(true);
    setError(null);
    try {
      const b = await createBooking({ node: draft.node, vcluster, start: draft.start.toISOString(), end: draft.end.toISOString(), purpose: purpose.trim() });
      try {
        localStorage.setItem(VC_PREF, vcluster);
      } catch {
        // 忽略
      }
      onCreated(b);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title="新建预约"
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button type="submit" form="create-booking-form" variant="primary" disabled={busy || !vcluster}>
            {busy ? '提交中…' : '确认预约'}
          </Button>
        </>
      }
    >
      <form id="create-booking-form" onSubmit={onSubmit} noValidate>
        {error && (
          <div className="alert alert-error" role="alert">
            {error}
          </div>
        )}
        <DescriptionList
          compact
          items={[
            { term: '节点', value: <span className="mono">{draft.node}</span> },
            { term: '时间', value: `${formatRange(draft.start.getTime(), draft.end.getTime())} · ${hoursOf(draft.start.getTime(), draft.end.getTime())} 小时` },
          ]}
        />
        <div className="section-gap" />
        <Field label="目标集群" required hint="只列出我是所有者或成员、且未在删除中的集群">
          {clusters.length === 0 ? (
            <p className="field-note warn">你还没有可用的集群，先去「我的集群」创建一个。</p>
          ) : (
            <select value={vcluster} onChange={(e) => setVCluster(e.target.value)} disabled={busy} autoFocus>
              {clusters.map((c) => (
                <option key={c.name} value={c.name}>
                  {c.name}（{ROLE[c.myRole] ?? c.myRole}）
                </option>
              ))}
            </select>
          )}
        </Field>
        <Field label="用途" hint="例如：微调 v3、sglang smoke test">
          <input type="text" value={purpose} onChange={(e) => setPurpose(e.target.value)} maxLength={200} disabled={busy} />
        </Field>
      </form>
    </Modal>
  );
}

interface DetailProps {
  booking: BookingView;
  nowMs: number;
  onClose: () => void;
  onDeleted: () => void;
}

// 点色块后的详情：状态、集群、预约人、用途、时间；有权限时可删除。
export function BookingDetailDialog({ booking: b, nowMs, onClose, onDeleted }: DetailProps) {
  const toast = useToast();
  const [confirming, setConfirming] = useState(false);
  const startMs = Date.parse(b.start);
  const endMs = Date.parse(b.end);

  const status = (() => {
    switch (b.phase) {
      case 'Pending':
        return <StatusDot value="Pending" label="待开始" />;
      case 'Active':
        return <StatusDot value="Running" label={`进行中 · 剩余 ${formatRemaining(endMs - nowMs)}`} />;
      case 'Completed':
        return <StatusDot value="Unknown" label="已结束" />;
      case 'Rejected':
        return <StatusDot value="Failed" label={`已拒绝 · ${REASON_TEXT[b.reason] ?? b.reason}`} />;
      case 'Revoked':
        return <StatusDot value="Failed" label={`已撤销 · ${REASON_TEXT[b.reason] ?? b.reason}`} />;
      default:
        // 刚创建、控制器还没写 status
        return <StatusDot value="Pending" label="处理中" />;
    }
  })();

  const doDelete = async () => {
    try {
      await deleteBooking(b.name);
      toast.success(`已删除预约 ${b.name}`);
      setConfirming(false);
      onDeleted();
    } catch (err) {
      toast.error(`删除失败：${errorMessage(err)}`);
    }
  };

  return (
    <Modal
      title="预约详情"
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>关闭</Button>
          {b.canDelete && (
            <Button variant="danger-ghost" onClick={() => setConfirming(true)}>
              删除预约
            </Button>
          )}
        </>
      }
    >
      <DescriptionList
        compact
        items={[
          { term: '状态', value: status },
          { term: '节点', value: <span className="mono">{b.node}</span> },
          { term: '集群', value: <span className="mono">{b.vcluster}</span> },
          { term: '预约人', value: <span className="mono">{b.user}</span> },
          { term: '用途', value: b.purpose },
          { term: '时间', value: `${formatRange(startMs, endMs)} · ${hoursOf(startMs, endMs)} 小时` },
          { term: '编号', value: <span className="mono muted">{b.name}</span> },
        ]}
      />
      {b.phase === 'Active' && b.canDelete && <p className="field-note warn section-gap">现在删除会立刻把节点从集群移除，节点上的 Pod 在 30 秒内被终止。</p>}
      {confirming && (
        <ConfirmDialog
          title="删除预约"
          danger
          confirmText="删除"
          onClose={() => setConfirming(false)}
          onConfirm={doDelete}
          message={
            <p>
              确定删除 <span className="mono">{b.vcluster}</span> 在 <span className="mono">{b.node}</span> 上 {formatRange(startMs, endMs)} 的预约？
              {b.phase === 'Active' && ' 节点会立刻从集群移除，其上的 Pod 在 30 秒内被终止。'}
            </p>
          }
        />
      )}
    </Modal>
  );
}
