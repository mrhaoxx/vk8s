import { PageHeader } from '../../components/PageHeader';
import { AuditView } from '../audit/AuditView';

export function AdminAuditPage() {
  return (
    <>
      <PageHeader title="审计" subtitle="虚拟集群内的 kubectl 操作与平台上的管理操作，按时间倒序；点一行看详情。" />
      <AuditView />
    </>
  );
}
