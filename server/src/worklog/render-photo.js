// 出工日志：服务端照片加水印（选项「选择照片并添加水印」）
// 渲染核心 watermark.js 从水印 demo 原样并入（无 DOM 依赖），此处用 @napi-rs/canvas 提供 Canvas 2D 实现。
// 字体在进程内注册一次；族名与 watermark.js 默认 fontFamily 引用一致。
const path = require('path');
const { createCanvas, GlobalFonts, loadImage } = require('@napi-rs/canvas');
const Watermark = require('./watermark');

const ASSETS = path.join(__dirname, '..', '..', 'assets');

let fontsReady = false;
function ensureFonts() {
  if (fontsReady) return;
  GlobalFonts.registerFromPath(path.join(ASSETS, 'fonts', 'HYQiHeiX2-65J.ttf'), 'HYQiHei');
  GlobalFonts.registerFromPath(path.join(ASSETS, 'fonts', 'FZRuiZhengHei.ttf'), 'FZRuiZhengHei');
  fontsReady = true;
}

let brandImage = null;
async function ensureBrand() {
  if (brandImage) return brandImage;
  brandImage = await loadImage(path.join(ASSETS, 'brand-logo.png'));
  return brandImage;
}

/**
 * 给原图加水印，返回 JPEG Buffer。
 * @param {Buffer} photoBuf 原图（jpeg/png）
 * @param {object} wm 水印字段：content/time/weather/location/longitude/latitude/antiCode
 * @param {string} orientation 小程序 wx.getImageInfo 返回的 EXIF 方向（up/down/left/right/up-mirrored 等），空按 up 处理
 */
async function renderWatermarkedPhoto(photoBuf, wm, orientation) {
  ensureFonts();
  const photo = await loadImage(photoBuf);
  const brand = await ensureBrand();

  // EXIF 方向矫正：left/right 类需交换宽高并旋转画布，使水印按正常视角绘制
  const o = String(orientation || 'up').toLowerCase();
  const swapped = o.startsWith('left') || o.startsWith('right');
  const W = swapped ? photo.height : photo.width;
  const H = swapped ? photo.width : photo.height;

  const canvas = createCanvas(W, H);
  const ctx = canvas.getContext('2d');
  ctx.save();
  if (o.startsWith('right')) {
    ctx.translate(W, 0);
    ctx.rotate(Math.PI / 2);
  } else if (o.startsWith('left')) {
    ctx.translate(0, H);
    ctx.rotate(-Math.PI / 2);
  } else if (o.startsWith('down')) {
    ctx.translate(W, H);
    ctx.rotate(Math.PI);
  }
  ctx.drawImage(photo, 0, 0);
  ctx.restore();

  Watermark.draw(ctx, W, H, {
    content: wm.content || '',
    time: wm.time || '',
    weather: wm.weather || '',
    location: wm.location || '',
    longitude: wm.longitude || '',
    latitude: wm.latitude || '',
    antiCode: wm.antiCode || '',
    brandImage: brand,
  });

  // 原图分辨率导出；quality 刻度为 0-100（0.9 会被压成渣，95 接近无损观感）
  return canvas.toBuffer('image/jpeg', 95);
}

module.exports = { renderWatermarkedPhoto };
