import { mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';

// Shade 壹匣网页端构建配置（单页入口 index.html，账号与权限管理等由 router 挂载）。
// /api 开发代理指向本机 Go 服务（go/internal/httpapi，默认端口 3000）；
// api.js 适配层默认 live 直连 Go API，演示模式经 localStorage shade.apiMode=demo 或
// 构建环境变量 VITE_API_MODE=demo 切换（见 src/api.js）。

// Windows 踩坑：esbuild 往系统 %TEMP% 释放/清理临时文件会被杀软短暂锁住，
// 构建报「remove ...\Temp\esbuild-...: Access is denied」。把临时目录钉到工程内 .tmp 规避。
const rootDir = dirname(fileURLToPath(import.meta.url));
const tmpDir = join(rootDir, '.tmp');
mkdirSync(tmpDir, { recursive: true });
process.env.TMP = tmpDir;
process.env.TEMP = tmpDir;
process.env.TMPDIR = tmpDir;

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.VITE_API_TARGET || 'http://127.0.0.1:3000',
        changeOrigin: true,
      },
    },
  },
});
