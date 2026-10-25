import { useState } from 'react';
import { copyText } from '../utils/clipboard';

export function CopyButton({ text, label = '复制', className = 'btn btn-sm' }: { text: string; label?: string; className?: string }) {
  const [done, setDone] = useState<'ok' | 'fail' | null>(null);
  const onClick = async () => {
    const ok = await copyText(text);
    setDone(ok ? 'ok' : 'fail');
    window.setTimeout(() => setDone(null), 1500);
  };
  return (
    <button type="button" className={className} onClick={onClick} disabled={!text}>
      {done === 'ok' ? '已复制' : done === 'fail' ? '复制失败' : label}
    </button>
  );
}
