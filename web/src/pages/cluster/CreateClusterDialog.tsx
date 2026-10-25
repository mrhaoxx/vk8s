import { useEffect, useState, type FormEvent } from 'react';
import { errorMessage } from '../../api/client';
import type { CreateVClusterRequest, PodSecurity, VClusterView } from '../../api/types';
import { createVCluster } from '../../api/vclusters';
import { useAuth } from '../../auth/AuthContext';
import { useConfig } from '../../auth/ConfigContext';
import { Field } from '../../components/Field';
import { Modal } from '../../components/Modal';
import { useToast } from '../../components/Toast';
import { POD_SECURITY_LABELS, PRIVILEGED_WARNING, summarizeNodePlacement } from '../../utils/format';
import { validateClusterName } from '../../utils/validate';
import { QuotaFields } from './QuotaFields';
import { draftToQuota, quotaToDraft, type QuotaDraft } from './quotaDraft';
import { listAddonCatalog } from '../../api/addons';
import type { AddonTemplateView } from '../../api/types';
import { AddonInputsForm } from './AddonInputsForm';
import { requiresMet } from './addonRequires';

const EMPTY_DRAFT: QuotaDraft = { cpu: '', memory: '', pods: '', storage: '', services: '', extended: [] };

interface Props {
  onClose: () => void;
  onCreated: (vc: VClusterView) => void;
}

export function CreateClusterDialog({ onClose, onCreated }: Props) {
  const { isAdmin } = useAuth();
  const config = useConfig();
  const toast = useToast();

  const [name, setName] = useState('');
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  // 管理员高级选项
  const [advanced, setAdvanced] = useState(false);
  const [quota, setQuota] = useState<QuotaDraft>(() => (config ? quotaToDraft(config.defaults.quota) : EMPTY_DRAFT));
  const [quotaTouched, setQuotaTouched] = useState(false);
  const [preset, setPreset] = useState('');
  const [podSecurity, setPodSecurity] = useState<PodSecurity>(config?.defaults.podSecurity ?? 'baseline');
  // 预装应用：模板名 → inputs
  const [catalog, setCatalog] = useState<AddonTemplateView[]>([]);
  const [picked, setPicked] = useState<Record<string, Record<string, string>>>({});
  useEffect(() => {
    if (!config?.addonsEnabled) return;
    listAddonCatalog()
      .then(setCatalog)
      .catch(() => setCatalog([]));
  }, [config?.addonsEnabled]);
  const effectivePodSecurity: PodSecurity = isAdmin && advanced ? podSecurity : (config?.defaults.podSecurity ?? 'baseline');

  // 配置晚于对话框到达时，用默认值回填一次。
  useEffect(() => {
    if (config && !quotaTouched) {
      setQuota(quotaToDraft(config.defaults.quota));
      setPodSecurity(config.defaults.podSecurity);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [config]);

  const nameError = touched ? validateClusterName(name) : null;

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    setFormError(null);
    const err = validateClusterName(name);
    if (err) return;

    const req: CreateVClusterRequest = { name };
    if (isAdmin && advanced) {
      const q = draftToQuota(quota);
      if ('error' in q) {
        setFormError(q.error);
        return;
      }
      req.quota = q.quota;
      req.podSecurity = podSecurity;
      const p = config?.nodePlacementPresets.find((x) => x.name === preset);
      if (p) req.nodePlacement = { nodeSelector: p.nodeSelector, nodeNames: p.nodeNames, tolerations: p.tolerations };
    }
    const addons = catalog
      .filter((t) => picked[t.name] && requiresMet(t, effectivePodSecurity).ok)
      .map((t) => ({ template: t.name, inputs: picked[t.name] }));
    if (addons.length > 0) req.addons = addons;

    setBusy(true);
    try {
      const vc = await createVCluster(req);
      toast.success(`集群 ${vc.name} 已创建，正在部署`);
      onCreated(vc);
    } catch (e2) {
      setFormError(errorMessage(e2));
    } finally {
      setBusy(false);
    }
  };

  const selectedPreset = config?.nodePlacementPresets.find((x) => x.name === preset);

  return (
    <Modal
      title="新建集群"
      onClose={onClose}
      wide={isAdmin && advanced}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            取消
          </button>
          <button type="submit" form="create-cluster-form" className="btn btn-primary" disabled={busy}>
            {busy ? '创建中…' : '创建'}
          </button>
        </>
      }
    >
      <form id="create-cluster-form" onSubmit={onSubmit} noValidate>
        {formError && (
          <div className="alert alert-error" role="alert">
            {formError}
          </div>
        )}
        <Field
          label="集群名称"
          required
          error={nameError}
          hint={
            config && name && !nameError
              ? `API 地址将是 https://${name}.${config.apiDomain}，应用域名 *.${name}.${config.appsDomain}`
              : '小写字母、数字和连字符，最长 30 个字符，不能包含「--」'
          }
        >
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value.trim())}
            onBlur={() => setTouched(true)}
            placeholder="例如 alice-dev"
            autoFocus
            spellCheck={false}
            disabled={busy}
          />
        </Field>

        {!isAdmin && <p className="muted">配额、节点调度与安全档位取平台默认值；如需调整请联系管理员。</p>}

        {catalog.length > 0 && (
          <div className="addon-picks">
            <h3>预装应用</h3>
            {catalog.map((t) => {
              const req = requiresMet(t, effectivePodSecurity);
              const on = !!picked[t.name] && req.ok;
              return (
                <div key={t.name} className="addon-pick">
                  <label>
                    <input
                      type="checkbox"
                      checked={on}
                      disabled={busy || !req.ok}
                      onChange={(e) =>
                        setPicked((cur) => {
                          const next = { ...cur };
                          if (e.target.checked) next[t.name] = {};
                          else delete next[t.name];
                          return next;
                        })
                      }
                    />
                    <span className="strong">{t.displayName}</span>
                    <span className="muted small">{t.description}</span>
                  </label>
                  {!req.ok && <span className="field-note warn">{req.reason}</span>}
                  {on && <AddonInputsForm inputs={t.inputs} value={picked[t.name]} onChange={(v) => setPicked((cur) => ({ ...cur, [t.name]: v }))} />}
                </div>
              );
            })}
          </div>
        )}

        {isAdmin && (
          <div className="advanced">
            <label className="check">
              <input type="checkbox" checked={advanced} onChange={(e) => setAdvanced(e.target.checked)} disabled={busy} />
              <span>自定义配额、节点调度与安全档位（管理员）</span>
            </label>
            {advanced && (
              <div className="advanced-body">
                <h3>配额</h3>
                <QuotaFields
                  draft={quota}
                  onChange={(d) => {
                    setQuota(d);
                    setQuotaTouched(true);
                  }}
                  disabled={busy}
                />
                <h3>节点调度</h3>
                <Field label="预设" hint={selectedPreset ? summarizeNodePlacement(selectedPreset) : '不选则使用平台默认节点池；创建后可在详情页精细调整'}>
                  <select value={preset} onChange={(e) => setPreset(e.target.value)} disabled={busy}>
                    <option value="">平台默认</option>
                    {(config?.nodePlacementPresets ?? []).map((p) => (
                      <option key={p.name} value={p.name}>
                        {p.name}
                      </option>
                    ))}
                  </select>
                </Field>
                <h3>安全档位</h3>
                <Field label="podSecurity">
                  <select value={podSecurity} onChange={(e) => setPodSecurity(e.target.value as PodSecurity)} disabled={busy}>
                    {(['privileged', 'baseline', 'restricted'] as PodSecurity[]).map((l) => (
                      <option key={l} value={l}>
                        {POD_SECURITY_LABELS[l]}
                      </option>
                    ))}
                  </select>
                </Field>
                {podSecurity === 'privileged' && <p className="field-note warn">{PRIVILEGED_WARNING}</p>}
              </div>
            )}
          </div>
        )}
      </form>
    </Modal>
  );
}
