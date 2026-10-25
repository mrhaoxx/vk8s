import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['src/test/setup.ts'],
    globals: false,
    // vitest 默认把 .css 导入替换为空串；styles.test.ts 用 ?raw 读样式源文件做静态断言，需要让 .css 走 Vite 管线。
    css: { include: [/\.css(\?.*)?$/] },
  },
});
