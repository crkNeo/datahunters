import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      // proxy API + uploaded images to the Go backend during dev.
      // 預設 8080;本機另起後端(例如測試庫跑在別的埠)時用 VITE_API_TARGET 覆蓋,
      // 不必改這個檔:VITE_API_TARGET=http://localhost:8090 npm run dev
      '/api': process.env.VITE_API_TARGET || 'http://localhost:8080',
      '/uploads': process.env.VITE_API_TARGET || 'http://localhost:8080',
    },
  },
})
