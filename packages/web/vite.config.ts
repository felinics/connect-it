import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import path from 'node:path'
import { defineConfig } from 'vite'

// 开发模式把 API 请求代理到本地 Go 服务（mise run dev）。
const backend = 'http://localhost:8080'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    // @felinic/ui is consumed from source in this workspace, so its internal
    // "#" imports must resolve exactly as they do in the UI package itself.
    alias: {
      '#': path.resolve(__dirname, '../ui/src'),
    },
  },
  server: {
    proxy: {
      '/v1': backend,
      '/admin': backend,
      '/healthz': backend,
      '/swagger': backend,
      '/mcp': backend,
    },
  },
})
