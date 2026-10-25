export function formatAbsolute(d: Date): string {
  return d.toLocaleString('zh-CN', { hour12: false });
}

// 相对时间：过去用「前」，未来用「后」（token 过期时间会是未来）。
export function formatRelative(d: Date, now: number = Date.now()): string {
  const diff = d.getTime() - now;
  const past = diff < 0;
  const sec = Math.round(Math.abs(diff) / 1000);
  if (sec < 45) return past ? '刚刚' : '即将';

  const min = Math.round(sec / 60);
  const hour = Math.floor(min / 60);
  const day = Math.floor(hour / 24);
  let text: string;
  if (min < 60) text = `${min} 分钟`;
  else if (hour < 24) text = `${hour} 小时`;
  else if (day < 30) text = `${day} 天`;
  else if (day < 365) text = `${Math.floor(day / 30)} 个月`;
  else text = `${Math.floor(day / 365)} 年`;
  return past ? `${text}前` : `${text}后`;
}

export function isPast(iso: string | null | undefined, now: number = Date.now()): boolean {
  if (!iso) return false;
  const t = new Date(iso).getTime();
  return !Number.isNaN(t) && t < now;
}
