import { describe, expect, it } from 'vitest';
import html from '../../index.html?raw';

// index.html 的 <head> 里必须有一段内联脚本，在样式生效前把存储的主题偏好写到 <html data-theme>，
// 否则覆盖了主题的用户每次刷新都会先看到系统主题再切换（闪屏）。
const head = /<head>([\s\S]*?)<\/head>/.exec(html)?.[1] ?? '';
const script = /<script>([\s\S]*?)<\/script>/.exec(head)?.[1];

function run(stored: string | null | Error): string | undefined {
  const localStorage = {
    getItem: () => {
      if (stored instanceof Error) throw stored;
      return stored;
    },
  };
  const document = { documentElement: { dataset: {} as Record<string, string> } };
  new Function('localStorage', 'document', script ?? '')(localStorage, document);
  return document.documentElement.dataset.theme;
}

describe('theme bootstrap script', () => {
  it('exists inline in <head>', () => {
    expect(script).toBeDefined();
  });

  it('applies a stored explicit theme before first paint', () => {
    expect(run('dark')).toBe('dark');
    expect(run('light')).toBe('light');
  });

  it('ignores system, unknown values and storage errors', () => {
    expect(run('system')).toBeUndefined();
    expect(run('blue')).toBeUndefined();
    expect(run(null)).toBeUndefined();
    expect(run(new Error('denied'))).toBeUndefined();
  });
});
