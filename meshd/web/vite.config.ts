import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 开发态：vite dev server 跑在 :3000，所有 /api/* 反代到 meshd:7878。
// 生产态：vite build 出来的 dist/ 会被 meshd 内嵌，浏览器从 meshd 自己服务，
// 同源访问无需任何代理。

export default defineConfig({
  plugins: [react()],
  resolve: { alias: { '@': '/src' } },
  server: {
    port: 3000,
    proxy: {
      '/api': { target: 'http://localhost:7878', changeOrigin: true },
    },
  },
  build: {
    // 内嵌进二进制时打包要尽量紧凑
    sourcemap: false,
  },
})
