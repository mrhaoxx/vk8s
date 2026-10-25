import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// 构建产物由 Go manager 通过 embed.FS 打进二进制。
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/manager/ui/dist',
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: false },
      '/auth': { target: 'http://localhost:8080', changeOrigin: false },
    },
  },
});
