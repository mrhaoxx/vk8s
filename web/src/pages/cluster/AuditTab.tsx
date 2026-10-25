import type { VClusterView } from '../../api/types';
import { AuditView } from '../audit/AuditView';

// 集群详情的「审计」Tab：集群固定，只看该集群内的操作与针对它的平台操作。
export function AuditTab({ vc }: { vc: VClusterView }) {
  return <AuditView fixedVCluster={vc.name} />;
}
