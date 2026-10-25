import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { listBookings } from '../api/bookings';
import { listTokens } from '../api/tokens';
import type { CreateTokenResponse } from '../api/types';
import { getVCluster } from '../api/vclusters';
import { useAuth } from '../auth/AuthContext';
import { useConfig } from '../auth/ConfigContext';
import { RoleBadge } from '../components/Badge';
import { Button } from '../components/Button';
import { PageHeader } from '../components/PageHeader';
import { ErrorState, Loading } from '../components/State';
import { StatusDot } from '../components/StatusDot';
import { Tabs } from '../components/Tabs';
import { useAsync } from '../hooks/useAsync';
import { usePolling } from '../hooks/usePolling';
import { useTabParam } from '../hooks/useTabParam';
import { AppsTab } from './cluster/AppsTab';
import { AuditTab } from './cluster/AuditTab';
import { KubeconfigTab } from './cluster/KubeconfigTab';
import { MembersTab } from './cluster/MembersTab';
import { OverviewTab } from './cluster/OverviewTab';
import { SettingsTab } from './cluster/SettingsTab';
import { TAB_KEYS, tabItems, visibleTabs } from './cluster/tabs';
import { GenerateTokenDialog, KubeconfigResultDialog } from './cluster/TokenDialogs';

const TRANSITIONAL = new Set(['', 'Pending', 'Provisioning', 'Deleting']);

export function ClusterDetailPage() {
  const { name = '' } = useParams<{ name: string }>();
  const { user, isAdmin } = useAuth();
  const config = useConfig();
  const cluster = useAsync(() => getVCluster(name), [name]);
  const tokens = useAsync(() => listTokens(name), [name]);
  // 本集群未来 7 天的预约，概览里显示待开始的租约。
  const bookings = useAsync(() => {
    const from = new Date(Math.floor(Date.now() / 3_600_000) * 3_600_000);
    return listBookings(from, new Date(from.getTime() + 7 * 24 * 3_600_000), name);
  }, [name]);
  const vc = cluster.data;

  // 角色未知（还在加载）时先放行全部 Tab，避免把 ?tab=settings 误判为无权限而改写掉。
  const visible = vc ? visibleTabs(vc.myRole, isAdmin, !!config?.addonsEnabled) : [...TAB_KEYS];
  const [tab, setTab] = useTabParam(visible, 'overview');

  const [generating, setGenerating] = useState(false);
  const [result, setResult] = useState<CreateTokenResponse | null>(null);

  // 部署 / 删除过程中轮询状态。
  usePolling(!!vc && TRANSITIONAL.has(vc.status.phase), 5_000, cluster.reload);

  if (cluster.loading && !vc) return <Loading />;
  if (cluster.error && !vc) return <ErrorState message={cluster.error} onRetry={cluster.reload} />;
  if (!vc) return null;

  const canDelete = isAdmin || vc.myRole === 'owner';

  return (
    <>
      <PageHeader
        breadcrumb={[{ label: '我的集群', to: '/' }, { label: vc.name }]}
        title={
          <>
            <span className="mono">{vc.name}</span>
            <StatusDot value={vc.status.phase} title={vc.status.reason || undefined} />
            <RoleBadge role={vc.myRole} />
          </>
        }
        subtitle={vc.status.reason || undefined}
        actions={
          <>
            <Button
              onClick={() => {
                cluster.reload();
                tokens.reload();
                bookings.reload();
              }}
            >
              刷新
            </Button>
            <Button variant="primary" onClick={() => setGenerating(true)} disabled={vc.status.phase === 'Deleting'}>
              生成 kubeconfig
            </Button>
          </>
        }
      />

      <Tabs items={tabItems(visible, { tokens: tokens.data?.length, members: vc.members.length })} active={tab} onChange={setTab} ariaLabel="集群详情" />

      <div role="tabpanel" id={`panel-${tab}`} aria-labelledby={`tab-${tab}`}>
        {tab === 'overview' && <OverviewTab vc={vc} bookings={bookings.data?.bookings} />}
        {tab === 'kubeconfig' && <KubeconfigTab vc={vc} tokens={tokens} onRevoked={tokens.reload} auditEnabled={config?.auditEnabled} />}
        {tab === 'members' && <MembersTab vc={vc} currentUser={user?.name ?? ''} onChanged={cluster.reload} />}
        {tab === 'apps' && <AppsTab vc={vc} isAdmin={isAdmin} />}
        {tab === 'audit' && <AuditTab vc={vc} />}
        {tab === 'settings' && <SettingsTab vc={vc} isAdmin={isAdmin} canDelete={canDelete} onSaved={cluster.reload} />}
      </div>

      {generating && (
        <GenerateTokenDialog
          vc={vc}
          config={config}
          onClose={() => setGenerating(false)}
          onCreated={(r) => {
            setGenerating(false);
            setResult(r);
            tokens.reload();
          }}
        />
      )}
      {result && <KubeconfigResultDialog vc={vc} result={result} onClose={() => setResult(null)} />}
    </>
  );
}
