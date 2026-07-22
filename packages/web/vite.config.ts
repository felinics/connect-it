import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

// 开发模式把 API 请求代理到本地 Go 服务（mise run dev）。
const backend = 'http://localhost:8080'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
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
