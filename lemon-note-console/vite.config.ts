import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

const target = process.env.LEMON_NOTE_API || 'http://127.0.0.1:18686'

export default defineConfig({
  plugins: [vue()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: {
    port: 5273,
    proxy: {
      '/api/ws': { target, ws: true, changeOrigin: false },
      '/api': { target, changeOrigin: false },
      '/mcp': { target, changeOrigin: false },
      '/.well-known': { target, changeOrigin: false },
      '/oauth/register': { target, changeOrigin: false },
      '/oauth/token': { target, changeOrigin: false },
      '/oauth/revoke': { target, changeOrigin: false },
    },
  },
  build: {
    outDir: 'dist',
    assetsDir: 'assets',
    chunkSizeWarningLimit: 3000,
    sourcemap: false,
  },
})
