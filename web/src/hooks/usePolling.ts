import { useEffect, useRef } from 'react';

// active 为 true 时每 intervalMs 调一次 cb。
export function usePolling(active: boolean, intervalMs: number, cb: () => void): void {
  const ref = useRef(cb);
  ref.current = cb;
  useEffect(() => {
    if (!active) return;
    const id = window.setInterval(() => ref.current(), intervalMs);
    return () => window.clearInterval(id);
  }, [active, intervalMs]);
}
