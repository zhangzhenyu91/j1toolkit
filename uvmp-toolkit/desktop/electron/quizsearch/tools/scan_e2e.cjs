// 开发工具：题库搜题端到端联调（真实截屏 + OCR 子进程 + 匹配 + 结果窗推送，全链路）。
// 用法（desktop/ 目录下）：node_modules/.bin/electron electron/quizsearch/tools/scan_e2e.cjs
// 注意：会真实弹出两个扫描框与结果窗数秒钟。
'use strict';

const { app } = require('electron');
const path = require('path');
const os = require('os');
const fs = require('fs');

app.on('window-all-closed', () => {});

app.whenReady().then(async () => {
  const { QuizSearchService } = require('../index.cjs');
  const { BankStore } = require('../bank-store.cjs');

  const fail = (msg) => {
    console.error('[e2e] FAIL：' + msg);
    setTimeout(() => process.exit(1), 200);
    throw new Error('e2e-fail');   // 阻断后续断言（Electron 下 process.exit 不保证同步终止）
  };
  const ok = (msg) => console.log('[e2e] ' + msg);

  // 独立数据目录，不污染真实用户数据（构造后替换）
  const dataDir = path.join(os.tmpdir(), 'qs-e2e-' + Date.now());
  const service = new QuizSearchService({ getMainWindow: () => null });
  service.settingsPath = path.join(dataDir, 'settings.json');
  fs.mkdirSync(dataDir, { recursive: true });
  service.store = new BankStore(dataDir);
  service.registerIpc();

  // 导入测试题库（内容随意——先跑通链路，匹配命中用 OCR 实际文本回填验证）
  service.store.importRows('e2e 测试库', 'e2e.xlsx', [
    ['题干', '答案', '选项A', '选项B'],
    ['安全生产责任制是企业安全管理的核心制度', 'A', '正确', '错误'],
  ]);
  ok('题库导入完成');

  service.start();
  if (!service.scanning) fail('start 后 scanning=false');
  if (!service.boxWins.A || !service.boxWins.B) fail('扫描框未创建');
  if (!service.resultWin) fail('结果窗未创建');
  ok('扫描框 A/B + 结果窗已弹出');

  const t0 = Date.now();
  const ready = await new Promise((resolve) => {
    const timer = setInterval(() => {
      if (service.workerReady) { clearInterval(timer); resolve(true); }
      if (Date.now() - t0 > 15000) { clearInterval(timer); resolve(false); }
    }, 200);
  });
  if (!ready) fail('OCR 子进程 15s 未就绪：' + (service.workerError || '无错误信息'));
  ok(`OCR 引擎就绪（${Date.now() - t0}ms）`);

  // 成功标准：OCR 回路在 45s 内有响应（boxState 被写入）。屏幕内容不可控，文本可空。
  const got = await new Promise((resolve) => {
    const timer = setInterval(() => {
      if (service.boxState.A || service.boxState.B) { clearInterval(timer); resolve(true); }
      if (Date.now() - t0 > 45000) { clearInterval(timer); resolve(false); }
    }, 300);
  });
  if (!got) fail('45s 内 OCR 回路无响应');
  ok(`OCR 回路响应（最近一帧 ${service.lastOcrMs}ms）`);

  // 停掉屏幕轮询，避免真实屏幕帧结果覆盖注入帧的匹配状态（轮询/截屏路径前面已验证）
  clearInterval(service.timer);
  service.timer = null;
  const settle0 = Date.now();
  while ((service.inFlight.A || service.inFlight.B) && Date.now() - settle0 < 5000) {
    await new Promise((r) => setTimeout(r, 200));
  }

  // 确定性命中验证：离屏渲染题目图片，直接喂给 OCR 子进程（避开 desktopCapturer 的
  // 独占全屏游戏等环境干扰；截屏裁剪路径由 cap_debug.cjs 单独验证）
  const { BrowserWindow } = require('electron');
  const testWin = new BrowserWindow({ show: false, width: 720, height: 160, webPreferences: { offscreen: true } });
  await testWin.loadURL('data:text/html;charset=utf-8,' + encodeURIComponent(
    '<!doctype html><html><body style="margin:0;background:#fff;color:#1D2129;'
    + 'font:22px/1.9 sans-serif;padding:14px 20px">'
    + '<div>安全生产责任制是企业安全管理的核心制度</div>'
    + '<div style="color:#4E5969">A. 正确　B. 错误</div>'
    + '</body></html>'));
  await new Promise((r) => setTimeout(r, 800));
  const img = await testWin.webContents.capturePage();
  testWin.destroy();
  const size = img.getSize();
  ok(`合成题目帧 ${size.width}x${size.height}，注入 OCR 回路，等待命中…`);

  const t1 = Date.now();
  const hit = await new Promise((resolve) => {
    const timer = setInterval(() => {
      const st = service.boxState.A;
      if (st && st.matched) { clearInterval(timer); resolve(st); return; }
      if (Date.now() - t1 > 30000) { clearInterval(timer); resolve(null); return; }
      // 每 2s 重注一帧（与轮询节奏无关，直接驱动）
      if (!service.inFlight.A) service._queueOcr('A', img.getBitmap(), size.width, size.height);
    }, 2000);
  });
  service._queueOcr('A', img.getBitmap(), size.width, size.height);
  if (!hit) fail('30s 内未命中测试题，框A 最后识别：' + JSON.stringify((service.boxState.A && service.boxState.A.ocrText || '').slice(0, 60)));
  ok(`命中：答案 ${hit.q.answer} · 匹配度 ${Math.round(hit.score * 100)}% · 来源库「${hit.q.bankName}」`);

  service.stop();
  if (service.scanning) fail('stop 后仍 scanning');
  if (service.resultWin || service.boxWins.A || service.boxWins.B) fail('停止后窗口未关闭');
  fs.rmSync(dataDir, { recursive: true, force: true });
  console.log('[e2e] PASS：截屏→OCR→匹配→结果推送→停止清理 全链路通过');
  setTimeout(() => process.exit(0), 300);
}).catch((e) => {
  if (e && e.message === 'e2e-fail') return;   // fail() 已打印并安排退出
  console.error('[e2e] 异常：', e && (e.stack || e));
  process.exit(1);
});
