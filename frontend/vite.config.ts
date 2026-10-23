/// <reference types="vitest/config" />
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

// The browser only ever talks to /api on its own origin. In development
// Vite proxies it to the Go API; in Docker, nginx does the same.
// No secrets are exposed to client code: only VITE_-prefixed variables
// reach the bundle, and none are defined.
export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    // First match wins: /api/v1 is served by the Go API under the same
    // path; other /api paths (health checks) have the prefix stripped,
    // matching nginx.conf.
    proxy: {
      '/api/v1': {
        target: process.env.API_PROXY_TARGET ?? 'http://localhost:8080',
      },
      '/api': {
        target: process.env.API_PROXY_TARGET ?? 'http://localhost:8080',
        rewrite: (path) => path.replace(/^\/api/, ''),
      },
    },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.spec.ts'],
    restoreMocks: true,
    unstubGlobals: true,
  },
})
