import type { AuditEventView } from '../../api/types';

export const PLATFORM_ACTION_LABELS: Record<string, string> = {
  'auth.login': '登录',
  'auth.logout': '退出登录',
  'auth.oidc.login': 'SSO 登录',
  'vcluster.create': '创建集群',
  'vcluster.patch': '修改集群设置',
  'vcluster.delete': '删除集群',
  'member.add': '添加成员',
  'member.remove': '移除成员',
  'token.create': '签发 kubeconfig',
  'token.delete': '撤销令牌',
  'user.create': '创建用户',
  'user.patch': '修改用户',
  'user.password': '设置密码',
  'node.bookable.set': '标记节点可预约',
  'node.bookable.unset': '取消节点可预约',
  'booking.create': '创建预约',
  'booking.delete': '删除预约',
};

export const SOURCE_LABELS: Record<string, string> = { kube: '集群内', platform: '平台' };

export const VERB_OPTIONS = ['get', 'list', 'watch', 'create', 'update', 'patch', 'delete', 'deletecollection', 'login', 'logout'];

// 动作：kube 为 `<verb> <resource>[/<sub>][.<group>]`；平台为动作名的中文标签。
export function actionLabel(e: AuditEventView): string {
  if (e.source === 'platform') return PLATFORM_ACTION_LABELS[e.platformAction] ?? e.platformAction;
  let res = e.resource ?? '';
  if (e.subresource) res += `/${e.subresource}`;
  if (e.apiGroup) res += `.${e.apiGroup}`;
  return res ? `${e.verb} ${res}` : e.verb;
}

// 对象：kube 为 `ns/name`（集群级对象只有 name）；平台为目标名。
export function objectLabel(e: AuditEventView): string {
  if (e.source === 'platform') return e.platformTarget ?? '';
  if (e.namespace) return `${e.namespace}/${e.name ?? ''}`;
  return e.name ?? '';
}

export function outcomeKind(code: number): 'ok' | 'err' {
  return code >= 400 ? 'err' : 'ok';
}

export function reasonText(e: AuditEventView): string {
  if (e.source === 'platform' && e.platformAction.startsWith('auth.')) {
    return { success: '成功', failure: '失败', ratelimited: '被限速' }[e.responseReason] ?? e.responseReason;
  }
  return e.responseReason;
}
