import vue from '@vitejs/plugin-vue'
import path from 'node:path'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    // @felinic/ui is consumed from source in this workspace. Its internal
    // imports use the same "#" alias as the UI package's own Vite config.
    alias: {
      '#': path.resolve(__dirname, '../ui/src'),
    },
  },
  test: {
    environment: 'jsdom',
  },
})
