import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { errorMessage } from '../api/client';
import { getConfig } from '../api/config';
import type { PlatformConfig } from '../api/types';
import { useToast } from '../components/Toast';

const ConfigContext = createContext<PlatformConfig | null>(null);

// 登录后加载一次平台配置；null 表示尚未加载。
export function ConfigProvider({ children }: { children: ReactNode }) {
  const [config, setConfig] = useState<PlatformConfig | null>(null);
  const toast = useToast();

  useEffect(() => {
    let cancelled = false;
    getConfig().then(
      (c) => {
        if (!cancelled) setConfig(c);
      },
      (err: unknown) => {
        if (!cancelled) toast.error(`加载平台配置失败：${errorMessage(err)}`);
      },
    );
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return <ConfigContext.Provider value={config}>{children}</ConfigContext.Provider>;
}

export function useConfig(): PlatformConfig | null {
  return useContext(ConfigContext);
}
