import { useState } from 'react';
import { Link } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import { revokeToken } from '../../api/tokens';
import type { TokenView, VClusterView } from '../../api/types';
import { Badge } from '../../components/Badge';
import { Button } from '../../components/Button';
import { Card } from '../../components/Card';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { EmptyState, ErrorState, Loading } from '../../components/State';
import { Table } from '../../components/Table';
import { TimeAgo } from '../../components/TimeAgo';
import { useToast } from '../../components/Toast';
import type { AsyncState } from '../../hooks/useAsync';
import { isPast } from '../../utils/time';

interface Props {
  vc: VClusterView;
  // 令牌列表由页面持有（Tab 条上要显示计数）。
  tokens: AsyncState<TokenView[]>;
  // 审计开启时每个令牌给一个「审计」入口：跳到审计 Tab 并按令牌过滤
  auditEnabled?: boolean;
  onRevoked: () => void;
}

// kubeconfig 与令牌 Tab：令牌表与撤销。生成入口只在页头。
export function KubeconfigTab({ vc, tokens, onRevoked, auditEnabled }: Props) {
  const toast = useToast();
  const [revoking, setRevoking] = useState<TokenView | null>(null);

  const doRevoke = async () => {
    if (!revoking) return;
    try {
      await revokeToken(revoking.id);
      toast.success(`已撤销令牌「${revoking.description || revoking.id}」`);
      setRevoking(null);
      onRevoked();
    } catch (err) {
      toast.error(`撤销失败：${errorMessage(err)}`);
    }
  };

  return (
    <Card title="访问令牌">
      <p className="muted">
        每份 kubeconfig 对应一个访问令牌，令牌明文只在生成时显示一次。撤销令牌、移出成员或禁用用户后，最多 2 分钟内生效。
        {vc.status.phase !== 'Running' && ' 集群尚未就绪时生成的 kubeconfig 要等待集群 Running 后才能使用。'}
      </p>

      {tokens.loading && !tokens.data ? (
        <Loading />
      ) : tokens.error && !tokens.data ? (
        <ErrorState message={tokens.error} onRetry={tokens.reload} />
      ) : (tokens.data ?? []).length === 0 ? (
        <EmptyState title="还没有访问令牌" hint="点击右上角「生成 kubeconfig」创建第一份。" />
      ) : (
        <Table>
          <thead>
            <tr>
              <th>描述</th>
              <th>用户</th>
              <th>创建时间</th>
              <th>过期时间</th>
              <th className="col-actions">操作</th>
            </tr>
          </thead>
          <tbody>
            {(tokens.data ?? []).map((t) => (
              <tr key={t.id}>
                <td>
                  <span className="strong">{t.description || <span className="muted">（无描述）</span>}</span>
                  <div className="muted mono small">{t.id}</div>
                </td>
                <td className="mono">{t.user}</td>
                <td>
                  <TimeAgo value={t.createdAt} />
                </td>
                <td>
                  <TimeAgo value={t.expiresAt} /> {isPast(t.expiresAt) && <Badge tone="red">已过期</Badge>}
                </td>
                <td className="col-actions">
                  {auditEnabled && (
                    <Link className="btn btn-ghost btn-sm" to={`?tab=audit&token=${encodeURIComponent(t.id)}`} title="查看这个令牌的审计记录">
                      审计
                    </Link>
                  )}
                  <Button variant="danger-ghost" size="sm" onClick={() => setRevoking(t)}>
                    撤销
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}

      {revoking && (
        <ConfirmDialog
          title="撤销访问令牌"
          danger
          confirmText="撤销"
          onClose={() => setRevoking(null)}
          onConfirm={doRevoke}
          message={
            <p>
              确定撤销令牌「{revoking.description || revoking.id}」？使用该令牌的 kubeconfig 将在 2 分钟内失效，此操作不可恢复。
            </p>
          }
        />
      )}
    </Card>
  );
}
