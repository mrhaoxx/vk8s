import type { Quota } from '../../api/types';
import { recordToRows, rowsToRecord, type KV } from '../../components/KeyValueEditor';

// 表单里的配额草稿（全部字符串），提交前转成 Quota。
export interface QuotaDraft {
  cpu: string;
  memory: string;
  pods: string;
  storage: string;
  services: string;
  extended: KV[];
}

export function quotaToDraft(q: Quota): QuotaDraft {
  return {
    cpu: q.cpu,
    memory: q.memory,
    pods: q.pods ? String(q.pods) : '',
    storage: q.storage,
    services: q.services ? String(q.services) : '',
    extended: recordToRows(q.extended),
  };
}

// k8s Quantity：数字 + 可选单位（SI / 二进制 / 指数）。
const QUANTITY = /^\d+(\.\d+)?(n|u|m|k|M|G|T|P|E|Ki|Mi|Gi|Ti|Pi|Ei|[eE][+-]?\d+)?$/;

function parseQuantity(s: string, label: string): string | { error: string } {
  const t = s.trim();
  if (!t) return { error: `${label}不能为空` };
  if (!QUANTITY.test(t)) return { error: `${label}不是合法的资源量，例如 16、500m、64Gi` };
  return t;
}

function parseCount(s: string, label: string): number | { error: string } {
  const t = s.trim();
  if (!t) return { error: `${label}不能为空` };
  if (!/^\d+$/.test(t)) return { error: `${label}必须是非负整数` };
  return Number(t);
}

export function draftToQuota(d: QuotaDraft): { quota: Quota } | { error: string } {
  const cpu = parseQuantity(d.cpu, 'CPU');
  if (typeof cpu !== 'string') return cpu;
  const memory = parseQuantity(d.memory, '内存');
  if (typeof memory !== 'string') return memory;
  const storage = parseQuantity(d.storage, '存储');
  if (typeof storage !== 'string') return storage;
  const pods = parseCount(d.pods, 'Pod 数');
  if (typeof pods !== 'number') return pods;
  const services = parseCount(d.services, 'Service 数');
  if (typeof services !== 'number') return services;

  const extended = rowsToRecord(d.extended);
  for (const [k, v] of Object.entries(extended)) {
    const q = parseQuantity(v, `扩展资源 ${k}`);
    if (typeof q !== 'string') return q;
    extended[k] = q;
  }
  return { quota: { cpu, memory, pods, storage, services, extended } };
}
