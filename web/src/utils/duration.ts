// Go time.Duration 字符串 <-> 小时数。后端用 Duration.String() 输出，形如 "720h0m0s"。

const UNIT_HOURS: Record<string, number> = {
  ns: 1 / 3.6e12,
  us: 1 / 3.6e9,
  'µs': 1 / 3.6e9,
  ms: 1 / 3.6e6,
  s: 1 / 3600,
  m: 1 / 60,
  h: 1,
  d: 24, // 便于用户手输，提交前会换算成 h
};

export function parseDurationHours(input: string): number | null {
  const s = input.trim();
  if (!s) return null;
  if (/^\d+(\.\d+)?$/.test(s)) return Number(s);
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h|d)/g;
  let total = 0;
  let consumed = '';
  let m: RegExpExecArray | null;
  while ((m = re.exec(s)) !== null) {
    total += Number(m[1]) * UNIT_HOURS[m[2]];
    consumed += m[0];
  }
  if (consumed !== s) return null;
  return total;
}

// 输出 Go 可解析的小时表示。
export function hoursToDuration(hours: number): string {
  return `${Math.round(hours)}h`;
}

export function humanizeHours(hours: number): string {
  if (hours >= 24 && hours % 24 === 0) return `${hours / 24} 天`;
  return `${Math.round(hours * 10) / 10} 小时`;
}
