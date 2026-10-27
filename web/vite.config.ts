import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// In dev, the UI runs on Vite and proxies API calls to the daemon.
export default defineConfig({
  plugins: [vue()],
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    proxy: { '/api': 'http://127.0.0.1:9150' },
  },
})
