import { useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import type { VClusterView } from '../../api/types';
import { deleteVCluster } from '../../api/vclusters';
import { Button } from '../../components/Button';
import { Card } from '../../components/Card';
import { Field } from '../../components/Field';
import { Modal } from '../../components/Modal';
import { useToast } from '../../components/Toast';

// 危险操作：删除集群。仅 owner / 平台 admin 可见（由 SettingsTab 控制）。
export function DangerZone({ vc }: { vc: VClusterView }) {
  const [open, setOpen] = useState(false);
  const deleting = vc.status.phase === 'Deleting';
  return (
    <Card danger title="危险操作">
      <p>
        删除后虚拟集群内的全部对象、宿主命名空间 <code>{vc.status.hostNamespace || `vc-${vc.name}`}</code> 中的工作负载与持久数据、以及所有访问令牌都会被销毁，<strong>不可恢复</strong>。
      </p>
      <Button variant="danger" onClick={() => setOpen(true)} disabled={deleting}>
        {deleting ? '正在删除…' : '删除集群'}
      </Button>
      {open && <DeleteClusterDialog vc={vc} onClose={() => setOpen(false)} />}
    </Card>
  );
}

function DeleteClusterDialog({ vc, onClose }: { vc: VClusterView; onClose: () => void }) {
  const toast = useToast();
  const navigate = useNavigate();
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const matches = confirm.trim() === vc.name;

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!matches) return;
    setBusy(true);
    try {
      await deleteVCluster(vc.name);
      toast.success(`集群 ${vc.name} 已开始删除`);
      navigate('/');
    } catch (err) {
      toast.error(`删除失败：${errorMessage(err)}`);
      setBusy(false);
    }
  };

  return (
    <Modal
      title="确认删除集群"
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button type="submit" form="delete-cluster-form" variant="danger" disabled={!matches || busy}>
            {busy ? '删除中…' : '永久删除'}
          </Button>
        </>
      }
    >
      <form id="delete-cluster-form" onSubmit={onSubmit} noValidate>
        <p>
          请输入集群名称 <span className="mono strong">{vc.name}</span> 以确认删除。
        </p>
        <Field label="集群名称">
          <input type="text" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoFocus spellCheck={false} disabled={busy} />
        </Field>
      </form>
    </Modal>
  );
}
