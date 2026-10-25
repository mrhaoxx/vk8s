import { enc, http } from './client';
import type { AuditEventView, AuditListResponse, AuditQuery } from './types';

// 审计事件列表；管理员可不带 vcluster，租户必须带。
export async function listAuditEvents(q: AuditQuery): Promise<AuditListResponse> {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v === undefined || v === '' || v === null) continue;
    sp.set(k, String(v));
  }
  const res = await http.get<AuditListResponse>(`/api/v1/audit/events?${sp.toString()}`);
  return { items: (res.items ?? []).map(normalize), nextBefore: res.nextBefore ?? '' };
}

// 单条详情（含请求体）；租户必须带 vcluster。
export async function getAuditEvent(id: string, vcluster?: string): Promise<AuditEventView> {
  const q = vcluster ? `?vcluster=${enc(vcluster)}` : '';
  return normalize(await http.get<AuditEventView>(`/api/v1/audit/events/${enc(id)}${q}`));
}

function normalize(e: AuditEventView): AuditEventView {
  return { ...e, groups: e.groups ?? [], sourceIPs: e.sourceIPs ?? [], annotations: e.annotations ?? {} };
}
