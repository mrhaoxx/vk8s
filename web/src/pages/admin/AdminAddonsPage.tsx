import { listAddonCatalog } from '../../api/addons';
import { Card } from '../../components/Card';
import { PageHeader } from '../../components/PageHeader';
import { EmptyState, ErrorState, Loading } from '../../components/State';
import { Table } from '../../components/Table';
import { useAsync } from '../../hooks/useAsync';
import { POD_SECURITY_LABELS } from '../../utils/format';

// 应用目录（只读）：目录由管理员用 kubectl 或 chart values 维护 AddonTemplate。
export function AdminAddonsPage() {
  const catalog = useAsync(() => listAddonCatalog(), []);
  return (
    <>
      <PageHeader title="应用目录" subtitle="用户可在建集群时勾选、或在集群「应用」Tab 安装的应用。" />
      <Card>
        <p className="muted">
          目录项是集群级 CRD <code>AddonTemplate</code>，用 <code>kubectl apply</code> 或 chart values 的 <code>addons.catalog</code> 维护；改版本号后，已装的集群会显示「有新版本」，由集群所有者点升级。
        </p>
        {catalog.loading && !catalog.data ? (
          <Loading />
        ) : catalog.error && !catalog.data ? (
          <ErrorState message={catalog.error} onRetry={catalog.reload} />
        ) : (catalog.data ?? []).length === 0 ? (
          <EmptyState title="目录为空" hint="kubectl apply 一个 AddonTemplate 后这里会出现。" />
        ) : (
          <Table>
            <thead>
              <tr>
                <th>应用</th>
                <th>版本</th>
                <th>安全档位要求</th>
                <th>参数</th>
              </tr>
            </thead>
            <tbody>
              {(catalog.data ?? []).map((t) => (
                <tr key={t.name}>
                  <td>
                    <span className="strong">{t.displayName}</span>
                    <div className="muted mono small">{t.name}</div>
                    <div className="muted small">{t.description}</div>
                  </td>
                  <td className="mono">{t.version}</td>
                  <td>{t.requires.podSecurity ? POD_SECURITY_LABELS[t.requires.podSecurity] : '无'}</td>
                  <td className="muted small">{t.inputs.map((i) => i.label).join('、') || '—'}</td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>
    </>
  );
}
