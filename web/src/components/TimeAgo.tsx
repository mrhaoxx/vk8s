import { useNow } from '../hooks/useNow';
import { formatAbsolute, formatRelative } from '../utils/time';

// 相对时间 + title 绝对时间。
export function TimeAgo({ value }: { value?: string | null }) {
  const now = useNow();
  if (!value) return <span className="muted">—</span>;
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return <span>{value}</span>;
  return (
    <time dateTime={value} title={formatAbsolute(d)}>
      {formatRelative(d, now)}
    </time>
  );
}
