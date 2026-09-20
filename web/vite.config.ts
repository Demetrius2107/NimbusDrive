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
    // 开发期代理分流：/api/v1/upload 与 /api/v1/download → TransferServer(Hertz :8081)，
    // 其余 /api → APIServer(Gin :8080)。生产由反向代理按同样规则路由。
    proxy: {
      '/api/v1/upload': {
        target: 'http://localhost:8081',
        changeOrigin: true,
      },
      '/api/v1/download': {
        target: 'http://localhost:8081',
        changeOrigin: true,
      },
      // WebDAV 挂载点 → TransferServer
      '/dav': {
        target: 'http://localhost:8081',
        changeOrigin: true,
      },
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
})
