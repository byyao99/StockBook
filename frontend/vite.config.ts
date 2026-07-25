/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The dev server proxies /api to the Go backend so the SPA can use
// same-origin relative URLs during development.
export default defineConfig({
  plugins: [vue()],
  test: {
    // Unit tests need no DOM — the setup file stubs localStorage, the only
    // browser API the tested modules touch. Switch to a DOM environment
    // (jsdom/happy-dom) if component tests are added later.
    environment: 'node',
    setupFiles: ['./vitest.setup.ts'],
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
})
