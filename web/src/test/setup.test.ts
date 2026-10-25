import { afterEach, expect, it, vi } from 'vitest';

afterEach(() => vi.restoreAllMocks());

// 测试环境里的 localStorage 必须是 setup.ts 安装的内存实现，与 Node / jsdom 版本无关；
// 并且对实例打桩要能真正拦截调用（jsdom 的 Storage 实例上 defineProperty 只会存一个键，不会覆盖方法）。
it('localStorage is the deterministic in-memory implementation and instance spies intercept', () => {
  expect(Object.getPrototypeOf(localStorage).constructor.name).toBe('MemoryStorage');
  vi.spyOn(localStorage, 'getItem').mockImplementation(() => {
    throw new Error('denied');
  });
  expect(() => localStorage.getItem('x')).toThrow('denied');
});
