// 开发工具：从 build/icon.png（1024）生成 Linux hicolor 全套尺寸到 build/icons/。
// 用法（desktop/ 目录下）：node_modules/.bin/electron build/make-icons.cjs
// electron-builder 的 linux 图标目录回退源即 build/icons/（单文件 icon 不会自动生成多尺寸——已踩坑）。
'use strict';

const { app, nativeImage } = require('electron');
const fs = require('fs');
const path = require('path');

app.on('window-all-closed', () => {});

app.whenReady().then(() => {
  const src = nativeImage.createFromPath(path.join(__dirname, 'icon.png'));
  if (src.isEmpty()) { console.error('build/icon.png 读取失败'); process.exit(1); }
  const dir = path.join(__dirname, 'icons');
  fs.mkdirSync(dir, { recursive: true });
  for (const s of [16, 24, 32, 48, 64, 96, 128, 256, 512, 1024]) {
    const img = s === 1024 ? src : src.resize({ width: s, height: s, quality: 'best' });
    fs.writeFileSync(path.join(dir, s + 'x' + s + '.png'), img.toPNG());
  }
  console.log('已生成 ' + dir + '（10 个尺寸）');
  setTimeout(() => process.exit(0), 200);
});
