/// <reference types="vitest/config" />
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // Keep fonts and icons as files: data: URIs would need a looser CSP and defeat caching.
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
  },
  test: { include: ['src/**/*.test.ts'] },
  server: {
    proxy: { '/api': 'http://localhost:8080' },
  },
})
