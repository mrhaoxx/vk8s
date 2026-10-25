import { useState } from 'react';
import { errorMessage } from '../../api/client';
import {
  getAddonLogs,
  getAddonOutputs,
  installAddon,
  listAddonCatalog,
  listClusterAddons,
  retryAddon,
  uninstallAddon,
  upgradeAddon,
} from '../../api/addons';
import type { AddonInstallView, AddonOutputValue, AddonTemplateView, VClusterView } from '../../api/types';
import { Badge } from '../../components/Badge';
import { Button } from '../../components/Button';
import { Card } from '../../components/Card';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { CopyButton } from '../../components/CopyButton';
import { Modal } from '../../components/Modal';
import { ErrorState, Loading } from '../../components/State';
import { StatusDot } from '../../components/StatusDot';
import { useToast } from '../../components/Toast';
import { useAsync } from '../../hooks/useAsync';
import { usePolling } from '../../hooks/usePolling';
import { AddonInputsForm } from './AddonInputsForm';
import { requiresMet } from './addonRequires';

interface Props {
  vc: VClusterView;
  isAdmin: boolean;
}

const PHASE_LABEL: Record<string, string> = {
  Pending: '等待中',
  Installing: '安装中',
  Ready: '已就绪',
  Failed: '失败',
  Uninstalling: '卸载中',
};

// 「应用」Tab：已装应用的状态与访问信息、目录里可装的应用。所有者与平台管理员可管理，成员只读。
export function AppsTab({ vc, isAdmin }: Props) {
  const toast = useToast();
  const canManage = isAdmin || vc.myRole === 'owner';
  const catalog = useAsync(() => listAddonCatalog(), []);
  const installed = useAsync(() => listClusterAddons(vc.name), [vc.name]);
  const busy = (installed.data ?? []).some((i) => i.phase === 'Installing' || i.phase === 'Uninstalling' || i.phase === 'Pending' || i.deleting);
  usePolling(true, busy ? 5_000 : 15_000, installed.reload);

  const [installing, setInstalling] = useState<AddonTemplateView | null>(null);
  const [inputs, setInputs] = useState<Record<string, string>>({});
  const [removing, setRemoving] = useState<AddonInstallView | null>(null);
  const [logs, setLogs] = useState<{ title: string; text: string | null } | null>(null);
  const [secrets, setSecrets] = useState<Record<string, AddonOutputValue[]>>({});
  const [pending, setPending] = useState(false);

  const act = async (what: string, fn: () => Promise<unknown>) => {
    setPending(true);
    try {
      await fn();
      toast.success(what);
      installed.reload();
    } catch (err) {
      toast.error(`${what}失败：${errorMessage(err)}`);
    } finally {
      setPending(false);
    }
  };

  const showLogs = async (i: AddonInstallView) => {
    setLogs({ title: `${i.displayName} 安装日志`, text: null });
    try {
      const r = await getAddonLogs(vc.name, i.template);
      setLogs({ title: `${i.displayName} 安装日志`, text: r.log || '（暂无日志）' });
    } catch (err) {
      setLogs({ title: `${i.displayName} 安装日志`, text: `读取失败：${errorMessage(err)}` });
    }
  };

  const reveal = async (i: AddonInstallView) => {
    try {
      const all = await getAddonOutputs(vc.name, i.template);
      const known = new Set(i.outputs.map((o) => o.name));
      setSecrets((s) => ({ ...s, [i.template]: all.filter((o) => !known.has(o.name)) }));
    } catch (err) {
      toast.error(`读取失败：${errorMessage(err)}`);
    }
  };

  if ((catalog.loading && !catalog.data) || (installed.loading && !installed.data)) return <Loading />;
  if (installed.error && !installed.data) return <ErrorState message={installed.error} onRetry={installed.reload} />;

  const have = new Set((installed.data ?? []).map((i) => i.template));
  const available = (catalog.data ?? []).filter((t) => !have.has(t.name));

  return (
    <>
      <Card title="已安装">
        {(installed.data ?? []).length === 0 ? (
          <p className="muted">还没有安装应用。</p>
        ) : (
          <div className="addon-list">
            {(installed.data ?? []).map((i) => {
              const phase = i.deleting ? 'Uninstalling' : i.phase;
              return (
                <div key={i.template} className="addon-item" data-addon={i.template}>
                  <div className="addon-head">
                    <span className="strong">{i.displayName}</span>
                    {i.installedVersion && <span className="muted mono small">{i.installedVersion}</span>}
                    <StatusDot value={phase} label={PHASE_LABEL[phase] ?? phase} />
                    {i.availableVersion && <Badge>有新版本 {i.availableVersion}</Badge>}
                    {!i.availableVersion && i.updateAvailable && <Badge>有更新</Badge>}
                    {i.templateRemoved && <Badge tone="neutral">目录中已移除</Badge>}
                  </div>
                  {i.message && <pre className="addon-message">{i.message}</pre>}
                  {(i.outputs.length > 0 || (secrets[i.template] ?? []).length > 0) && (
                    <dl className="addon-outputs">
                      {i.outputs.map((o) => (
                        <div key={o.name}>
                          <dt>{o.label || o.name}</dt>
                          <dd>
                            <span className="mono">{o.value}</span> <CopyButton text={o.value} className="btn btn-xs" />
                          </dd>
                        </div>
                      ))}
                      {(secrets[i.template] ?? []).map((o) => (
                        <div key={o.name}>
                          <dt>{o.label || o.name}</dt>
                          <dd>
                            <span className="mono">{o.value}</span> <CopyButton text={o.value} className="btn btn-xs" />
                          </dd>
                        </div>
                      ))}
                    </dl>
                  )}
                  <div className="addon-actions">
                    {canManage && i.hasSecretOutputs && !secrets[i.template] && (
                      <Button size="sm" variant="ghost" onClick={() => reveal(i)}>
                        显示
                      </Button>
                    )}
                    {(i.phase === 'Failed' || i.phase === 'Installing') && (
                      <Button size="sm" variant="ghost" onClick={() => showLogs(i)}>
                        查看日志
                      </Button>
                    )}
                    {canManage && !i.deleting && i.phase === 'Failed' && (
                      <Button size="sm" disabled={pending} onClick={() => act(`已重试 ${i.displayName}`, () => retryAddon(vc.name, i.template))}>
                        重试
                      </Button>
                    )}
                    {canManage && !i.deleting && (i.availableVersion || i.updateAvailable) && i.phase === 'Ready' && (
                      <Button size="sm" disabled={pending} onClick={() => act(`已开始升级 ${i.displayName}`, () => upgradeAddon(vc.name, i.template))}>
                        升级
                      </Button>
                    )}
                    {canManage && !i.deleting && (
                      <Button size="sm" variant="danger-ghost" disabled={pending} onClick={() => setRemoving(i)}>
                        卸载
                      </Button>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </Card>

      {available.length > 0 && (
        <Card title="应用目录">
          <div className="addon-list">
            {available.map((t) => {
              const req = requiresMet(t, vc.podSecurity);
              return (
                <div key={t.name} className="addon-item" data-addon={t.name}>
                  <div className="addon-head">
                    <span className="strong">{t.displayName}</span>
                    <span className="muted mono small">{t.version}</span>
                  </div>
                  <p className="muted">{t.description}</p>
                  {!req.ok && <p className="field-note warn">{req.reason}</p>}
                  {canManage && (
                    <div className="addon-actions">
                      <Button
                        size="sm"
                        disabled={!req.ok || pending}
                        onClick={() => {
                          setInputs({});
                          setInstalling(t);
                        }}
                      >
                        安装
                      </Button>
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </Card>
      )}

      {installing && (
        <Modal
          title={`安装 ${installing.displayName}`}
          onClose={() => setInstalling(null)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setInstalling(null)}>
                取消
              </Button>
              <Button
                variant="primary"
                disabled={pending}
                onClick={async () => {
                  const t = installing;
                  setInstalling(null);
                  await act(`已开始安装 ${t.displayName}`, () => installAddon(vc.name, { template: t.name, inputs }));
                }}
              >
                安装
              </Button>
            </>
          }
        >
          <p className="muted">{installing.description}</p>
          <AddonInputsForm inputs={installing.inputs} value={inputs} onChange={setInputs} />
        </Modal>
      )}

      {removing && (
        <ConfirmDialog
          title={`卸载 ${removing.displayName}`}
          message="会执行 helm uninstall，应用创建的资源随之删除（PVC 是否保留取决于 chart）。"
          confirmText="卸载"
          danger
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            const i = removing;
            setRemoving(null);
            await act(`已开始卸载 ${i.displayName}`, () => uninstallAddon(vc.name, i.template));
          }}
        />
      )}

      {logs && (
        <Modal title={logs.title} onClose={() => setLogs(null)} wide>
          {logs.text === null ? <Loading /> : <pre className="json-block">{logs.text}</pre>}
        </Modal>
      )}
    </>
  );
}
