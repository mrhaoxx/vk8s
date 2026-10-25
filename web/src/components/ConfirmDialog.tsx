import { useState, type ReactNode } from 'react';
import { Modal } from './Modal';

interface Props {
  title: string;
  message: ReactNode;
  confirmText?: string;
  danger?: boolean;
  // 出错时由调用方 toast；成功后由调用方 onClose。
  onConfirm: () => Promise<void> | void;
  onClose: () => void;
}

export function ConfirmDialog({ title, message, confirmText = '确定', danger, onConfirm, onClose }: Props) {
  const [busy, setBusy] = useState(false);

  const handle = async () => {
    setBusy(true);
    try {
      await onConfirm();
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            取消
          </button>
          <button type="button" className={danger ? 'btn btn-danger' : 'btn btn-primary'} onClick={handle} disabled={busy}>
            {busy ? '处理中…' : confirmText}
          </button>
        </>
      }
    >
      <div>{message}</div>
    </Modal>
  );
}
