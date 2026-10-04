// 开发工具：复刻 _tick 的截屏裁剪路径，把框 A 的裁剪结果存为 PNG 供人工查看。
// 用法：node_modules/.bin/electron electron/quizsearch/tools/cap_debug.cjs
'use strict';

const { app, desktopCapturer, screen } = require('electron');
const fs = require('fs');
const path = require('path');
const os = require('os');

app.on('window-all-closed', () => {});

app.whenReady().then(async () => {
  const { BrowserWindow } = require('electron');
  // 复刻 e2e：先在 (80,100) 放一个无边框置顶测试窗
  const testWin = new BrowserWindow({
    x: 80, y: 100, width: 720, height: 180,
    focusable: false, frame: false, skipTaskbar: true, alwaysOnTop: true,
  });
  await testWin.loadURL('data:text/html;charset=utf-8,' + encodeURIComponent(
    '<!doctype html><html><body style="margin:0;background:#fff;color:#1D2129;'
    + 'font:22px/1.9 sans-serif;padding:14px 20px">'
    + '<div>安全生产责任制是企业安全管理的核心制度</div>'
    + '<div style="color:#4E5969">A. 正确　B. 错误</div>'
    + '</body></html>'));
  await new Promise((r) => setTimeout(r, 1000));
  console.log('testWin bounds:', JSON.stringify(testWin.getBounds()), 'visible:', testWin.isVisible());

  // 与 settings 默认值一致：框 A (80,100,720x180)
  const bounds = { x: 80, y: 100, width: 720, height: 180 };
  const display = screen.getDisplayMatching(bounds);
  const scale = display.scaleFactor;
  const physW = Math.round(display.size.width * scale);
  const physH = Math.round(display.size.height * scale);
  console.log('display:', JSON.stringify({ size: display.size, bounds: display.bounds, scaleFactor: scale }), '请求缩略图:', physW + 'x' + physH);

  const sources = await desktopCapturer.getSources({ types: ['screen'], thumbnailSize: { width: physW, height: physH } });
  console.log('sources:', sources.map((s) => `${s.name} display_id=${s.display_id} thumb=${s.thumbnail.getSize().width}x${s.thumbnail.getSize().height}`));
  const src = sources.find((s) => String(s.display_id) === String(display.id)) || sources[0];
  const shot = src.thumbnail;
  const tw = shot.getSize().width;
  const th = shot.getSize().height;
  const kx = tw / physW;
  const ky = th / physH;
  const rx = Math.max(0, Math.round((bounds.x - display.bounds.x) * scale * kx));
  const ry = Math.max(0, Math.round((bounds.y - display.bounds.y) * scale * ky));
  const rw = Math.min(tw - rx, Math.round(bounds.width * scale * kx));
  const rh = Math.min(th - ry, Math.round(bounds.height * scale * ky));
  console.log(`thumb 实际 ${tw}x${th}，裁剪 rect=(${rx},${ry},${rw}x${rh})`);
  const full = path.join(os.tmpdir(), 'qs-cap-full.png');
  const crop = path.join(os.tmpdir(), 'qs-cap-crop.png');
  fs.writeFileSync(full, shot.toPNG());
  fs.writeFileSync(crop, shot.crop({ x: rx, y: ry, width: rw, height: rh }).toPNG());
  console.log('已保存：' + full + ' / ' + crop);
  setTimeout(() => process.exit(0), 300);
}).catch((e) => { console.error(e); process.exit(1); });
