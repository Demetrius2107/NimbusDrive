import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'node:path'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 5173,
    // 开发期将 /api 代理到 APIServer(Gin)，/api/v1/upload 与 /api/v1/download
    // 在生产由反向代理路由到 TransferServer(Hertz)；开发期为简化前端联调，
    // 一并代理到 APIServer，待 TransferServer 接口实现后再分流。
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
})
