import { useCallback, useEffect } from 'react';
import { useSearchParams } from 'react-router-dom';

// 查询参数名。缺省 Tab（fallback）不写进 URL。
export const TAB_PARAM = 'tab';

// 把当前 Tab 与 URL 的 ?tab= 同步：
//   - 值不在 allowed 中（缺省、非法、无权限）返回 fallback；URL 里带了这样的值则用 replace 删掉，其他参数保留；
//   - setTab(k)：k 为 fallback 时删除参数，否则写入；均 replace，不产生历史记录。
export function useTabParam<K extends string>(allowed: readonly K[], fallback: K): [K, (k: K) => void] {
  const [params, setParams] = useSearchParams();
  const raw = params.get(TAB_PARAM);
  const valid = raw !== null && (allowed as readonly string[]).includes(raw);
  const tab = valid ? (raw as K) : fallback;

  useEffect(() => {
    if (raw === null || valid) return;
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.delete(TAB_PARAM);
        return next;
      },
      { replace: true },
    );
  }, [raw, valid, setParams]);

  const setTab = useCallback(
    (k: K) => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (k === fallback) {
            next.delete(TAB_PARAM);
          } else {
            next.set(TAB_PARAM, k);
          }
          return next;
        },
        { replace: true },
      );
    },
    [fallback, setParams],
  );

  return [tab, setTab];
}
