import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      // proxy API calls to the Go backend during dev
      '/api': 'http://localhost:8080',
    },
  },
})
