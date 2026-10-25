const DNS_LABEL = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;

// 集群名：小写 DNS label、≤30、不含 "--"。
export function validateClusterName(name: string): string | null {
  if (!name) return '请输入集群名称';
  if (name.length > 30) return '名称不能超过 30 个字符';
  if (!DNS_LABEL.test(name)) return '只能包含小写字母、数字和连字符，且首尾必须是字母或数字';
  if (name.includes('--')) return '名称不能包含连续的连字符「--」';
  return null;
}

// 用户名：DNS label 规则。
export function validateUserName(name: string): string | null {
  if (!name) return '请输入用户名';
  if (name.length > 63) return '用户名不能超过 63 个字符';
  if (!DNS_LABEL.test(name)) return '只能包含小写字母、数字和连字符，且首尾必须是字母或数字';
  return null;
}

// 校验 tolerations JSON 文本，返回数组或错误文字。
export function parseTolerations(text: string): { value: unknown[] } | { error: string } {
  const s = text.trim();
  if (!s) return { value: [] };
  try {
    const v: unknown = JSON.parse(s);
    if (!Array.isArray(v)) return { error: 'tolerations 必须是 JSON 数组' };
    for (const t of v) {
      if (!t || typeof t !== 'object' || Array.isArray(t)) return { error: '每个 toleration 必须是 JSON 对象' };
    }
    return { value: v };
  } catch {
    return { error: 'tolerations 不是合法的 JSON' };
  }
}
