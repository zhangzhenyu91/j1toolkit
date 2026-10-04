// 开发工具：OCR 管线端到端自测（不经扫描框/截屏，直接渲染已知文本→OCR→比对）。
// 用法（desktop/ 目录下）：
//   node_modules/.bin/electron electron/quizsearch/tools/ocr_smoke.cjs            # 渲染已知文本自测
//   node_modules/.bin/electron electron/quizsearch/tools/ocr_smoke.cjs screen     # 截主屏 OCR 打印（人工观察）
'use strict';

const { app, BrowserWindow, desktopCapturer } = require('electron');
const path = require('path');
const { OcrPipeline } = require('../ocr-pipeline.cjs');

const MODELS_DIR = path.join(__dirname, '..', 'models');
const EXPECT = '安全生产责任制'; // 自测页固定文本片段

// 拦住默认的「窗口全关即退出」：截屏/渲染窗销毁后 OCR 仍在跑，进程须活到打印结果
app.on('window-all-closed', () => {});

async function renderTestImage() {
  const win = new BrowserWindow({
    show: false, width: 640, height: 240,
    webPreferences: { offscreen: true },
  });
  const html = '<!doctype html><html><body style="margin:0;background:#fff;color:#1D2129;'
    + 'font:20px/1.9 sans-serif;padding:16px 24px">'
    + '<div>1、企业应当建立健全安全生产责任制，明确各岗位的责任人员。</div>'
    + '<div style="color:#4E5969">A. 正确&nbsp;&nbsp;&nbsp;&nbsp;B. 错误</div>'
    + '</body></html>';
  await win.loadURL('data:text/html;charset=utf-8,' + encodeURIComponent(html));
  await new Promise((r) => setTimeout(r, 800)); // offscreen 首帧渲染等待
  const img = await win.webContents.capturePage();
  win.destroy();
  return img;
}

async function captureScreen() {
  const sources = await desktopCapturer.getSources({
    types: ['screen'],
    thumbnailSize: { width: 1920, height: 1080 },
  });
  return sources[0] ? sources[0].thumbnail : null;
}

app.whenReady().then(async () => {
  const mode = process.argv[2] || 'self';
  try {
    const img = mode === 'screen' ? await captureScreen() : await renderTestImage();
    if (!img || img.isEmpty()) { console.error('[smoke] 未取到图像'); process.exit(1); }
    const { width, height } = img.getSize();
    const bitmap = img.getBitmap(); // BGRA
    console.log(`[smoke] 图像 ${width}x${height}，加载模型…`);
    const t0 = Date.now();
    const pipeline = await OcrPipeline.create(MODELS_DIR);
    console.log(`[smoke] 模型加载 ${Date.now() - t0}ms，开始 OCR…`);
    const res = await pipeline.run(bitmap, width, height);
    console.log(`[smoke] OCR 完成，耗时 ${res.elapsedMs}ms，${res.lines.length} 行：`);
    for (const l of res.lines) {
      console.log(`  [${l.score.toFixed(2)}] (${l.box.x},${l.box.y} ${l.box.w}x${l.box.h}) ${l.text}`);
    }
    if (mode === 'self') {
      const pass = res.text.includes(EXPECT);
      console.log('[smoke] ' + (pass ? 'PASS：检出期望文本「' + EXPECT + '」' : 'FAIL：未检出期望文本「' + EXPECT + '」'));
      process.exitCode = pass ? 0 : 1;
      setTimeout(() => process.exit(process.exitCode), 200); // 给 stdout 留冲刷时间
      return;
    }
    process.exit(0);
  } catch (e) {
    console.error('[smoke] 异常：', e && (e.stack || e.message || e));
    process.exit(1);
  }
});
