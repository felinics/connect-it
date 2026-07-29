import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

// In dev mode, proxy API requests to the local Go server started by
// `mise run dev`.
const backend = 'http://localhost:8080'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    tsconfigPaths: true,
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
