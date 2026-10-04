// 题库搜题：OCR 推理线程（worker_threads 入口）。
// 常驻职责：持有 onnxruntime 会话（模型加载有耗时），接收主线程 BGRA 位图帧，回送识别文本。
// 推理在独立线程进行，主线程/界面不被 CPU 密集计算阻塞。
// 选 worker_threads 而非 utilityProcess：不依赖「再拉起一个进程」——个别麒麟环境子进程
// spawn 即死且无迹可查（已踩坑）；线程方案只要主进程活着它就一定能跑。
// 协议：
//   收 {type:'init', modelsDir}            → 回 {type:'ready'} | {type:'error', message}
//   收 {type:'ocr', id, bitmap, width, height}（bitmap 为 ArrayBuffer，BGRA8888）
//     → 回 {type:'ocr-result', id, ok, text, lines, elapsedMs} | {type:'ocr-result', id, ok:false, error}
// 注意：任何阶段的失败都必须先 postMessage 再退出——主线程只凭 exit code 无法定位（已踩坑）。
'use strict';

const { parentPort } = require('worker_threads');

function fatal(msg) {
  try { parentPort.postMessage({ type: 'error', message: String(msg) }); } catch (_e) { /* 通道已断 */ }
  process.exit(1);
}
process.on('uncaughtException', (e) => fatal('未捕获异常: ' + (e && (e.stack || e))));
process.on('unhandledRejection', (e) => fatal('未处理拒绝: ' + (e && (e.stack || e))));

let OcrPipeline = null;
try {
  OcrPipeline = require('./ocr-pipeline.cjs').OcrPipeline;
} catch (e) {
  fatal('OCR 模块加载失败: ' + (e && (e.stack || e)));
}

let pipeline = null;

parentPort.on('message', async (msg) => {
  msg = msg || {};
  if (msg.type === 'init') {
    try {
      pipeline = await OcrPipeline.create(msg.modelsDir);
      parentPort.postMessage({ type: 'ready' });
    } catch (err) {
      parentPort.postMessage({ type: 'error', message: String((err && err.stack) || err) });
    }
    return;
  }
  if (msg.type === 'ocr') {
    if (!pipeline) {
      parentPort.postMessage({ type: 'ocr-result', id: msg.id, ok: false, error: '引擎未初始化' });
      return;
    }
    try {
      const buf = Buffer.from(msg.bitmap);
      const res = await pipeline.run(buf, msg.width, msg.height);
      parentPort.postMessage({
        type: 'ocr-result', id: msg.id, ok: true,
        text: res.text, lines: res.lines, elapsedMs: res.elapsedMs,
      });
    } catch (err) {
      parentPort.postMessage({ type: 'ocr-result', id: msg.id, ok: false, error: String((err && err.message) || err) });
    }
  }
});

