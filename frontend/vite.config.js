import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      // proxy API + uploaded images to the Go backend during dev
      '/api': 'http://localhost:8080',
      '/uploads': 'http://localhost:8080',
    },
  },
})
