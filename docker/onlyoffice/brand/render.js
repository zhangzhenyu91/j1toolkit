// ONLYOFFICE 品牌替换素材生成器（Shade 壹匣）
// 用法（仓根执行）：node docker/onlyoffice/brand/render.js
// 依赖来自 server/node_modules（@napi-rs/canvas 画文字、sharp 栅格化 favicon.svg 标），故须在依赖已安装后运行；
// 素材源：server/public/favicon.svg（品牌标，蓝底白字块）+ design/font/t.ttf（品牌字体）
// 产物（本目录，透明底 PNG + favicon.ico）：
//   header-logo.png      172×40   编辑器左上 header 标（深色文字，浅色 header 用）
//   header-logo-dark.png 172×40   同上（白色文字，深色 header 用）
//   loader-logo.png      432×160  文档加载页标（深色文字）
//   about-logo.png       432×70   关于对话框标（深色文字）
//   favicon.png / favicon.ico    64×64 / 32×32  编辑器页签图标
// 挂载口径与路径发现步骤见 docker/onlyoffice/README.md
// 注：文字用系统微软雅黑（Microsoft YaHei）——design/font/t.ttf 为单字分享图专用字形，
//     @napi-rs/canvas 量测返回 0 不可用；与网页端品牌字（系统无衬线栈）同口径
const path = require('path');
const fs = require('fs');

const ROOT = path.join(__dirname, '..', '..', '..');
const SERVER_MODULES = path.join(ROOT, 'server', 'node_modules');
const { createCanvas, loadImage } = require(path.join(SERVER_MODULES, '@napi-rs/canvas'));
const sharp = require(path.join(SERVER_MODULES, 'sharp'));

const MARK_SVG = path.join(ROOT, 'server', 'public', 'favicon.svg');
const OUT = __dirname;

const FONT_FAMILY = 'Microsoft YaHei';
const INK = '#1D2129'; // 正文色（浅色底）
const INK_DARK = '#FFFFFF'; // 深色 header 用文字色

// 组合标：左 mark（蓝底白字块）+ 右文字，整组水平居中、垂直居中
async function renderWordmark(file, w, h, markPx, fontPx, ink) {
  const canvas = createCanvas(w, h);
  const ctx = canvas.getContext('2d');
  ctx.font = `${fontPx}px ${FONT_FAMILY}`;
  const text = 'Shade 壹匣';
  const textW = ctx.measureText(text).width;
  const gap = Math.round(markPx * 0.32);
  const groupW = markPx + gap + textW;
  const x0 = Math.round((w - groupW) / 2);
  const mark = await loadImage(
    await sharp(MARK_SVG, { density: 384 }).resize(markPx, markPx).png().toBuffer()
  );
  ctx.drawImage(mark, x0, Math.round((h - markPx) / 2), markPx, markPx);
  ctx.fillStyle = ink;
  ctx.textBaseline = 'middle';
  ctx.fillText(text, x0 + markPx + gap, Math.round(h / 2) + Math.round(fontPx * 0.06));
  fs.writeFileSync(path.join(OUT, file), canvas.toBuffer('image/png'));
  console.log(`生成 ${file}（${w}×${h}）`);
}

// 纯标（页签图标用，无文字）
async function renderMark(file, px) {
  const buf = await sharp(MARK_SVG, { density: 384 }).resize(px, px).png().toBuffer();
  fs.writeFileSync(path.join(OUT, file), buf);
  console.log(`生成 ${file}（${px}×${px}）`);
  return buf;
}

// PNG 封装为 ICO（Vista+ 支持 ICO 内嵌 PNG 数据；单张 32×32）
function wrapIco(pngBuf, px) {
  const header = Buffer.alloc(6);
  header.writeUInt16LE(0, 0);
  header.writeUInt16LE(1, 2);
  header.writeUInt16LE(1, 4);
  const entry = Buffer.alloc(16);
  entry.writeUInt8(px, 0);
  entry.writeUInt8(px, 1); // 宽/高（256 记 0）
  entry.writeUInt16LE(1, 4);
  entry.writeUInt16LE(32, 6); // 色平面 / 位深
  entry.writeUInt32LE(pngBuf.length, 8);
  entry.writeUInt32LE(22, 12);
  return Buffer.concat([header, entry, pngBuf]);
}

(async () => {
  await renderWordmark('header-logo.png', 172, 40, 30, 19, INK);
  await renderWordmark('header-logo-dark.png', 172, 40, 30, 19, INK_DARK);
  await renderWordmark('loader-logo.png', 432, 160, 72, 36, INK);
  await renderWordmark('about-logo.png', 432, 70, 48, 24, INK);
  const mark64 = await renderMark('favicon.png', 64);
  const mark32 = await sharp(mark64).resize(32, 32).png().toBuffer();
  fs.writeFileSync(path.join(OUT, 'favicon.ico'), wrapIco(mark32, 32));
  console.log('生成 favicon.ico（32×32）');
})().catch((err) => {
  console.error(err.message || err);
  process.exit(1);
});
