import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  // 相对资源路径 + 运行时 <base> 注入，使同一份构建可部署在任意子路径。
  base: './',
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      // 后端默认部署在 /packrun 子路径，开发代理补上前缀后转发。
      '/api': {
        target: 'http://127.0.0.1:9000',
        changeOrigin: true,
        rewrite: (p) => `/packrun${p}`,
      },
      '/healthz': { target: 'http://127.0.0.1:9000', changeOrigin: true },
    },
  },
})
