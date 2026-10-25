import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';

// 外观偏好：system 跟随 prefers-color-scheme（不设 data-theme），light / dark 写到 <html data-theme>。
export type ThemePreference = 'system' | 'light' | 'dark';

// localStorage 键。
export const THEME_STORAGE_KEY = 'vk8s.theme';

const PREFERENCES: readonly ThemePreference[] = ['system', 'light', 'dark'];

// 读取存储的偏好；值不合法或存储不可用（隐私模式、被禁用）一律按 system。
export function readStoredTheme(): ThemePreference {
  try {
    const v = localStorage.getItem(THEME_STORAGE_KEY);
    return PREFERENCES.includes(v as ThemePreference) ? (v as ThemePreference) : 'system';
  } catch {
    return 'system';
  }
}

// 把偏好写到 <html>：system 删除属性，让 tokens.css 的媒体查询生效。
export function applyTheme(t: ThemePreference): void {
  const root = document.documentElement;
  if (t === 'system') {
    delete root.dataset.theme;
  } else {
    root.dataset.theme = t;
  }
}

interface ThemeValue {
  theme: ThemePreference;
  setTheme: (t: ThemePreference) => void;
}

const ThemeContext = createContext<ThemeValue | null>(null);

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<ThemePreference>(readStoredTheme);

  useEffect(() => {
    applyTheme(theme);
    try {
      localStorage.setItem(THEME_STORAGE_KEY, theme);
    } catch {
      // 存储不可用时偏好只在本次会话内生效。
    }
  }, [theme]);

  const setTheme = useCallback((t: ThemePreference) => setThemeState(t), []);
  const value = useMemo<ThemeValue>(() => ({ theme, setTheme }), [theme, setTheme]);
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

// 当前是否为深色：显式选深色，或跟随系统且系统为深色。
export function useIsDark(): boolean {
  const { theme } = useTheme();
  const [sysDark, setSysDark] = useState(() => (typeof window.matchMedia === 'function' ? window.matchMedia('(prefers-color-scheme: dark)').matches : false));
  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return;
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const fn = (e: MediaQueryListEvent) => setSysDark(e.matches);
    mq.addEventListener('change', fn);
    return () => mq.removeEventListener('change', fn);
  }, []);
  return theme === 'dark' || (theme === 'system' && sysDark);
}

export function useTheme(): ThemeValue {
  const v = useContext(ThemeContext);
  if (!v) throw new Error('useTheme 必须在 ThemeProvider 内使用');
  return v;
}
