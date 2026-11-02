import { readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The daemon's API token (see internal/config/token.go), so the dev UI is signed in.
function token(): string {
  const dir = process.env.LARANEST_HOME
    ? join(process.env.LARANEST_HOME, 'config')
    : join(process.env.XDG_CONFIG_HOME ?? join(homedir(), '.config'), 'laranest')
  try {
    return readFileSync(join(dir, 'token'), 'utf8').trim()
  } catch {
    return ''
  }
}

// In dev, the UI runs on Vite and proxies API calls to the daemon.
export default defineConfig({
  plugins: [vue()],
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:9150',
        configure: (proxy) => proxy.on('proxyReq', (req) => req.setHeader('Authorization', `Bearer ${token()}`)),
      },
    },
  },
})
