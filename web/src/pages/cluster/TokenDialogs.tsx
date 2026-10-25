import { useState, type FormEvent } from 'react';
import { errorMessage } from '../../api/client';
import { createToken } from '../../api/tokens';
import type { CreateTokenResponse, PlatformConfig, VClusterView } from '../../api/types';
import { Button } from '../../components/Button';
import { CopyButton } from '../../components/CopyButton';
import { Field } from '../../components/Field';
import { Modal } from '../../components/Modal';
import { TimeAgo } from '../../components/TimeAgo';
import { downloadText } from '../../utils/clipboard';
import { hoursToDuration, humanizeHours, parseDurationHours } from '../../utils/duration';

type Unit = 'h' | 'd';

interface GenerateProps {
  vc: VClusterView;
  config: PlatformConfig | null;
  onClose: () => void;
  onCreated: (r: CreateTokenResponse) => void;
}

// 生成 kubeconfig：描述 + 有效期。
export function GenerateTokenDialog({ vc, config, onClose, onCreated }: GenerateProps) {
  const defaultHours = parseDurationHours(config?.limits.tokenDefaultTTL ?? '') ?? 720;
  const maxHours = parseDurationHours(config?.limits.tokenMaxTTL ?? '') ?? Number.POSITIVE_INFINITY;
  const defaultUnit: Unit = defaultHours >= 24 && defaultHours % 24 === 0 ? 'd' : 'h';

  const [description, setDescription] = useState('');
  const [unit, setUnit] = useState<Unit>(defaultUnit);
  const [amount, setAmount] = useState(String(defaultUnit === 'd' ? defaultHours / 24 : defaultHours));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const hours = Number(amount) * (unit === 'd' ? 24 : 1);
  const ttlError =
    !amount.trim() || !Number.isFinite(hours) || hours <= 0
      ? '请输入有效期'
      : hours > maxHours
        ? `有效期不能超过 ${humanizeHours(maxHours)}`
        : null;

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!description.trim()) {
      setError('请填写描述，便于区分不同设备上的令牌');
      return;
    }
    if (ttlError) {
      setError(ttlError);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const r = await createToken(vc.name, { description: description.trim(), ttl: hoursToDuration(hours) });
      onCreated(r);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title="生成 kubeconfig"
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button type="submit" form="generate-token-form" variant="primary" disabled={busy}>
            {busy ? '生成中…' : '生成'}
          </Button>
        </>
      }
    >
      <form id="generate-token-form" onSubmit={onSubmit} noValidate>
        {error && (
          <div className="alert alert-error" role="alert">
            {error}
          </div>
        )}
        {vc.status.phase !== 'Running' && <p className="field-note warn">集群尚未就绪，生成的 kubeconfig 要等集群 Running 后才能使用。</p>}
        <Field label="描述" required hint="例如：macbook、ci-runner">
          <input type="text" value={description} onChange={(e) => setDescription(e.target.value)} autoFocus maxLength={100} disabled={busy} />
        </Field>
        <Field
          label="有效期"
          required
          error={amount ? ttlError : null}
          hint={Number.isFinite(maxHours) ? `最长 ${humanizeHours(maxHours)}，默认 ${humanizeHours(defaultHours)}` : `默认 ${humanizeHours(defaultHours)}`}
        >
          <div className="input-group">
            <input type="number" min={1} step={1} value={amount} onChange={(e) => setAmount(e.target.value)} disabled={busy} />
            <select value={unit} onChange={(e) => setUnit(e.target.value as Unit)} disabled={busy}>
              <option value="d">天</option>
              <option value="h">小时</option>
            </select>
          </div>
        </Field>
      </form>
    </Modal>
  );
}

// 一次性展示生成结果；关闭后不可再查看。
export function KubeconfigResultDialog({ vc, result, onClose }: { vc: VClusterView; result: CreateTokenResponse; onClose: () => void }) {
  const filename = `${vc.name}.kubeconfig`;
  return (
    <Modal
      title="kubeconfig 已生成"
      onClose={onClose}
      wide
      closeOnBackdrop={false}
      footer={
        <>
          <CopyButton text={result.kubeconfig} label="复制" className="btn" />
          <Button variant="primary" onClick={() => downloadText(filename, result.kubeconfig)}>
            下载为 {filename}
          </Button>
          <Button onClick={onClose}>关闭</Button>
        </>
      }
    >
      <div className="alert alert-warn" role="alert">
        <strong>此 kubeconfig 只显示这一次。</strong> 关闭后无法再次查看，请立即复制或下载保存；遗失请撤销后重新生成。
      </div>
      <p>
        令牌「{result.description}」将于 <TimeAgo value={result.expiresAt} /> 过期。
      </p>
      <textarea className="code-block" readOnly rows={14} value={result.kubeconfig} onFocus={(e) => e.currentTarget.select()} spellCheck={false} />
      <p className="muted small">
        使用：<code>KUBECONFIG=~/.kube/{filename} kubectl get ns</code>
      </p>
    </Modal>
  );
}
