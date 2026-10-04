import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'path'

export default defineConfig({
  plugins: [vue()],
  base: './',           // 生产环境 file:// 加载，必须相对路径
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // 多页：主界面 + 题库搜题的两个悬浮窗（扫描框 / 结果窗）
    rollupOptions: {
      input: {
        index: resolve(__dirname, 'index.html'),
        scanbox: resolve(__dirname, 'scanbox.html'),
        result: resolve(__dirname, 'result.html'),
      },
    },
  },
  server: { host: '127.0.0.1', port: 5173, strictPort: true },
})
