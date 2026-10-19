import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
export default defineConfig({ plugins: [vue()], server: { proxy: { '/api': { target: 'http://127.0.0.1:9999', ws: true } } }, test: { setupFiles: ['./src/test-setup.ts'], environment: 'jsdom', globals: true } })
