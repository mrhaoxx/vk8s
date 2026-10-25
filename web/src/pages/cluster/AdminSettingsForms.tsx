import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { errorMessage } from '../../api/client';
import { listNodes } from '../../api/nodes';
import type { NodePlacement, PodSecurity, Toleration, VClusterView } from '../../api/types';
import { patchVCluster } from '../../api/vclusters';
import { useConfig } from '../../auth/ConfigContext';
import { Badge } from '../../components/Badge';
import { Button } from '../../components/Button';
import { Card } from '../../components/Card';
import { Field } from '../../components/Field';
import { KeyValueEditor, recordToRows, rowsToRecord, type KV } from '../../components/KeyValueEditor';
import { Loading } from '../../components/State';
import { StatusDot } from '../../components/StatusDot';
import { useToast } from '../../components/Toast';
import { useAsync } from '../../hooks/useAsync';
import { POD_SECURITY_LABELS, PRIVILEGED_WARNING } from '../../utils/format';
import { parseTolerations } from '../../utils/validate';
import { QuotaFields } from './QuotaFields';
import { draftToQuota, quotaToDraft } from './quotaDraft';

interface Props {
  vc: VClusterView;
  onSaved: () => void;
}

// 仅平台 admin：配额 / 安全档位 / 节点调度三个表单 → PATCH。
export function AdminSettingsForms({ vc, onSaved }: Props) {
  return (
    <>
      <div className="grid-2">
        <QuotaForm vc={vc} onSaved={onSaved} />
        <PodSecurityForm vc={vc} onSaved={onSaved} />
      </div>
      <NodePlacementForm vc={vc} onSaved={onSaved} />
    </>
  );
}

function QuotaForm({ vc, onSaved }: Props) {
  const toast = useToast();
  const [draft, setDraft] = useState(() => quotaToDraft(vc.quota));
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);

  // 集群数据刷新且本地无改动时同步表单。
  useEffect(() => {
    if (!dirty) setDraft(quotaToDraft(vc.quota));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [vc.quota]);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const q = draftToQuota(draft);
    if ('error' in q) {
      toast.error(q.error);
      return;
    }
    setBusy(true);
    try {
      await patchVCluster(vc.name, { quota: q.quota });
      toast.success('配额已更新');
      setDirty(false);
      onSaved();
    } catch (err) {
      toast.error(`保存失败：${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="配额">
      <form onSubmit={onSubmit} noValidate>
        <QuotaFields
          draft={draft}
          onChange={(d) => {
            setDraft(d);
            setDirty(true);
          }}
          disabled={busy}
        />
        <div className="actions">
          <Button
            disabled={!dirty || busy}
            onClick={() => {
              setDraft(quotaToDraft(vc.quota));
              setDirty(false);
            }}
          >
            重置
          </Button>
          <Button type="submit" variant="primary" disabled={!dirty || busy}>
            {busy ? '保存中…' : '保存配额'}
          </Button>
        </div>
      </form>
    </Card>
  );
}

const LEVELS: PodSecurity[] = ['privileged', 'baseline', 'restricted'];

function PodSecurityForm({ vc, onSaved }: Props) {
  const toast = useToast();
  const [level, setLevel] = useState<PodSecurity>(vc.podSecurity);
  const [busy, setBusy] = useState(false);

  useEffect(() => setLevel(vc.podSecurity), [vc.podSecurity]);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await patchVCluster(vc.name, { podSecurity: level });
      toast.success(`安全档位已改为 ${level}`);
      onSaved();
    } catch (err) {
      toast.error(`保存失败：${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="安全档位">
      <form onSubmit={onSubmit} noValidate>
        <Field label="podSecurity" hint="写入宿主命名空间的 Pod Security Admission enforce 标签">
          <select value={level} onChange={(e) => setLevel(e.target.value as PodSecurity)} disabled={busy}>
            {LEVELS.map((l) => (
              <option key={l} value={l}>
                {POD_SECURITY_LABELS[l]}
              </option>
            ))}
          </select>
        </Field>
        {level === 'privileged' && <p className="field-note warn">{PRIVILEGED_WARNING}</p>}
        <div className="actions">
          <Button type="submit" variant="primary" disabled={level === vc.podSecurity || busy}>
            {busy ? '保存中…' : '保存档位'}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function tolerationsToText(t: Toleration[]): string {
  return t.length ? JSON.stringify(t, null, 2) : '';
}

function NodePlacementForm({ vc, onSaved }: Props) {
  const toast = useToast();
  const config = useConfig();
  const nodes = useAsync(listNodes, []);

  const [selector, setSelector] = useState<KV[]>(() => recordToRows(vc.nodePlacement.nodeSelector));
  const [nodeNames, setNodeNames] = useState<string[]>(vc.nodePlacement.nodeNames);
  const [tolText, setTolText] = useState(() => tolerationsToText(vc.nodePlacement.tolerations));
  const [preset, setPreset] = useState('');
  const [filter, setFilter] = useState('');
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);

  const resetFrom = (np: NodePlacement) => {
    setSelector(recordToRows(np.nodeSelector));
    setNodeNames(np.nodeNames);
    setTolText(tolerationsToText(np.tolerations));
  };

  useEffect(() => {
    if (!dirty) resetFrom(vc.nodePlacement);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [vc.nodePlacement]);

  const applyPreset = () => {
    const p = config?.nodePlacementPresets.find((x) => x.name === preset);
    if (!p) return;
    resetFrom(p);
    setDirty(true);
  };

  const toggleNode = (name: string, on: boolean) => {
    setNodeNames((xs) => (on ? (xs.includes(name) ? xs : [...xs, name]) : xs.filter((x) => x !== name)));
    setDirty(true);
  };

  const tolResult = useMemo(() => parseTolerations(tolText), [tolText]);
  const tolError = 'error' in tolResult ? tolResult.error : null;

  // 列表里不存在但已选中的节点（可能已下线），仍要显示出来以便取消。
  const knownNames = new Set((nodes.data ?? []).map((n) => n.name));
  const staleNames = nodeNames.filter((n) => !knownNames.has(n));
  const visibleNodes = (nodes.data ?? []).filter((n) => !filter || n.name.includes(filter));

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if ('error' in tolResult) {
      toast.error(tolResult.error);
      return;
    }
    setBusy(true);
    try {
      await patchVCluster(vc.name, {
        nodePlacement: {
          nodeSelector: rowsToRecord(selector),
          nodeNames,
          tolerations: tolResult.value as Toleration[],
        },
      });
      toast.success('节点调度已更新');
      setDirty(false);
      onSaved();
    } catch (err) {
      toast.error(`保存失败：${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="节点调度">
      <form onSubmit={onSubmit} noValidate>
        <p className="muted">三项全部留空即使用平台默认节点池。nodeSelector 与 nodeNames 取交集；专属节点池有 taint 时在 tolerations 中补上。</p>

        {(config?.nodePlacementPresets.length ?? 0) > 0 && (
          <Field label="从预设填入" hint="套用预设会覆盖下面三项，保存前可以继续修改">
            <div className="input-group">
              <select value={preset} onChange={(e) => setPreset(e.target.value)} disabled={busy}>
                <option value="">选择预设…</option>
                {config!.nodePlacementPresets.map((p) => (
                  <option key={p.name} value={p.name}>
                    {p.name}
                  </option>
                ))}
              </select>
              <Button onClick={applyPreset} disabled={!preset || busy}>
                套用
              </Button>
            </div>
          </Field>
        )}

        <div className="grid-2">
          <div>
            <span className="field-label">nodeSelector</span>
            <KeyValueEditor
              rows={selector}
              onChange={(rows) => {
                setSelector(rows);
                setDirty(true);
              }}
              keyPlaceholder="vk8s.io/pool"
              valuePlaceholder="dev-a"
              addText="添加标签"
              disabled={busy}
            />
          </div>
          <div>
            <span className="field-label">nodeNames（{nodeNames.length} 个已选）</span>
            {nodes.loading && !nodes.data ? (
              <Loading text="加载节点…" />
            ) : nodes.error && !nodes.data ? (
              <p className="field-error">节点列表加载失败：{nodes.error}</p>
            ) : (
              <>
                {(nodes.data?.length ?? 0) > 8 && (
                  <input type="search" placeholder="过滤节点名" value={filter} onChange={(e) => setFilter(e.target.value)} className="filter-input" />
                )}
                <div className="check-list">
                  {visibleNodes.map((n) => (
                    <label key={n.name} className="check">
                      <input type="checkbox" checked={nodeNames.includes(n.name)} onChange={(e) => toggleNode(n.name, e.target.checked)} disabled={busy} />
                      <span className="mono">{n.name}</span>
                      {!n.ready && <StatusDot value="False" label="NotReady" />}
                    </label>
                  ))}
                  {staleNames.map((n) => (
                    <label key={n} className="check">
                      <input type="checkbox" checked onChange={() => toggleNode(n, false)} disabled={busy} />
                      <span className="mono">{n}</span>
                      <Badge tone="neutral">不在节点列表</Badge>
                    </label>
                  ))}
                  {visibleNodes.length === 0 && staleNames.length === 0 && <p className="muted">没有匹配的节点</p>}
                </div>
              </>
            )}
          </div>
        </div>

        <Field
          label="tolerations（JSON 数组）"
          error={tolError}
          hint={'例如 [{"key":"vk8s.io/pool","operator":"Equal","value":"dev-a","effect":"NoSchedule"}]'}
        >
          <textarea
            className="code-block"
            rows={6}
            value={tolText}
            onChange={(e) => {
              setTolText(e.target.value);
              setDirty(true);
            }}
            spellCheck={false}
            disabled={busy}
          />
        </Field>

        <div className="actions">
          <Button
            disabled={!dirty || busy}
            onClick={() => {
              resetFrom(vc.nodePlacement);
              setDirty(false);
            }}
          >
            重置
          </Button>
          <Button type="submit" variant="primary" disabled={!dirty || busy || !!tolError}>
            {busy ? '保存中…' : '保存节点调度'}
          </Button>
        </div>
      </form>
    </Card>
  );
}
