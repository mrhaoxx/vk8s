import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

// globals 关闭时 Testing Library 不会自动注册清理，这里显式注册，避免用例之间 DOM 互相污染。
afterEach(cleanup);

// 无条件用内存实现替换 localStorage：不同 Node / jsdom 版本下全局 localStorage 可能缺失（Node 的实验性全局遮蔽）
// 或是 jsdom 的 Storage 实例（对它 vi.spyOn 不会覆盖方法，只会存一个键），统一替换后行为在所有版本上确定。
// 测试里模拟存储故障：vi.spyOn(localStorage, 'getItem')。
class MemoryStorage {
  private map = new Map<string, string>();
  get length(): number {
    return this.map.size;
  }
  key(index: number): string | null {
    return Array.from(this.map.keys())[index] ?? null;
  }
  getItem(key: string): string | null {
    return this.map.has(key) ? (this.map.get(key) as string) : null;
  }
  setItem(key: string, value: string): void {
    this.map.set(key, String(value));
  }
  removeItem(key: string): void {
    this.map.delete(key);
  }
  clear(): void {
    this.map.clear();
  }
}

const store = new MemoryStorage();
Object.defineProperty(globalThis, 'localStorage', { value: store, configurable: true, writable: true });
if (typeof window !== 'undefined' && window !== (globalThis as unknown as Window)) {
  Object.defineProperty(window, 'localStorage', { value: store, configurable: true, writable: true });
}
