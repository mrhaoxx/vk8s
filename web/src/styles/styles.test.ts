import { describe, expect, it } from 'vitest';
import base from './base.css?raw';
import components from './components.css?raw';
import tokens from './tokens.css?raw';

// 对令牌与关键样式规则做静态断言：两套主题的文字对比度、焦点环可见性、Tab 条不产生竖向滚动条。
// 用 Vite 的 ?raw 读源文件，不依赖 Node 类型。

// 取某个选择器之后第一个花括号块的内容。
function block(css: string, selectorStart: string): string {
  const i = css.indexOf(selectorStart);
  if (i < 0) throw new Error(`selector not found: ${selectorStart}`);
  const open = css.indexOf('{', i);
  let depth = 0;
  for (let j = open; j < css.length; j++) {
    if (css[j] === '{') depth++;
    else if (css[j] === '}' && --depth === 0) return css.slice(open + 1, j);
  }
  throw new Error(`unterminated block: ${selectorStart}`);
}

function vars(cssBlock: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const m of cssBlock.matchAll(/--([\w-]+):\s*([^;]+);/g)) out[m[1]] = m[2].trim();
  return out;
}

const light = vars(block(tokens, ':root {'));
const dark = { ...light, ...vars(block(tokens, ":root[data-theme='dark']")) };

function luminance(hex: string): number {
  const h = hex.replace('#', '');
  const [r, g, b] = [0, 2, 4]
    .map((i) => parseInt(h.slice(i, i + 2), 16) / 255)
    .map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

// 焦点环：取 :focus-visible 的 box-shadow 里最后一个 var()，即最外圈颜色。
const focusRule = block(base, ':focus-visible');
const ringVars = [...focusRule.matchAll(/var\(--([\w-]+)\)/g)].map((m) => m[1]);
const ring = ringVars[ringVars.length - 1];

describe.each([
  ['light', light],
  ['dark', dark],
])('%s tokens', (_, t) => {
  it('body text meets 4.5:1 on page and surface', () => {
    expect(contrast(t.fg, t.bg)).toBeGreaterThanOrEqual(4.5);
    expect(contrast(t.fg, t.surface)).toBeGreaterThanOrEqual(4.5);
  });

  it('primary button text meets 4.5:1', () => {
    expect(contrast(t['accent-fg'], t.accent)).toBeGreaterThanOrEqual(4.5);
  });

  it('table header text meets 3:1', () => {
    expect(contrast(t['fg-faint'], t['surface-header'])).toBeGreaterThanOrEqual(3);
  });

  it('focus ring is visible against surfaces (>= 3:1)', () => {
    expect(ring).toBeDefined();
    expect(contrast(t[ring], t.surface)).toBeGreaterThanOrEqual(3);
    expect(contrast(t[ring], t.bg)).toBeGreaterThanOrEqual(3);
  });
});

it('tab strip clips vertical overflow so the -1px tab border cannot create a scrollbar', () => {
  expect(block(components, '.tabs {')).toMatch(/overflow-y:\s*hidden/);
});
