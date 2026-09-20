/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The dev server proxies /api to the Go backend so the SPA can use
// same-origin relative URLs during development.
//
// PORT names the backend, and matching dev.sh's variable is the point: that
// script documents `PORT=8081 ./dev.sh` and passes PORT to the API half, so a
// hardcoded target here left the frontend proxying to whatever was on 8080 —
// nothing, usually, and the SPA showed network errors with no clue why.
const API_PORT = process.env.PORT || '8080'

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
        target: `http://localhost:${API_PORT}`,
        changeOrigin: true,
      },
    },
  },
})
