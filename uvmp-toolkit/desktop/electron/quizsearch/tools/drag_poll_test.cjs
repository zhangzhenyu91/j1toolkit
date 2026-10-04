// 轮询拖拽验证：打桩 OS 光标位置，验证 移动/缩放（含缩小余量）链路
'use strict';
const { app, screen } = require('electron');
app.on('window-all-closed', () => {});
function sleep(ms) { return new Promise((r) => setTimeout(r, ms)); }

app.whenReady().then(async () => {
  let fake = { x: 300, y: 300 };
  screen.getCursorScreenPoint = () => ({ x: fake.x, y: fake.y });   // 打桩 OS 光标

  const { QuizSearchService } = require('../index.cjs');
  const service = new QuizSearchService({ getMainWindow: () => null });
  service.store.importRows('t', 't.xlsx', [['题干', '答案', '选项A', '选项B'], ['x题', 'A', '正确', '错误']]);
  service.registerIpc();
  service.start();
  await sleep(1500);
  const win = service.boxWins.A;
  const b0 = win.getBounds();
  console.log('[poll] 初始：', JSON.stringify(b0));

  // 移动：光标从 (300,300) 拉到 (500,430) → 期望 (x+200, y+130)
  await win.webContents.executeJavaScript("window.scanbox.dragStart('A','move')");
  fake = { x: 500, y: 430 };
  await sleep(400);
  const b1 = win.getBounds();
  console.log('[poll] 移动后：', JSON.stringify(b1), '期望 x=', b0.x + 200, 'y=', b0.y + 130);
  const moveOk = b1.x === b0.x + 200 && b1.y === b0.y + 130;
  await win.webContents.executeJavaScript("window.scanbox.dragEnd('A')");

  // 缩小：从当前光标 (500,430) 拉到 (430,380) → dx=-70,dy=-50，期望 w-70+3, h-50+3
  await win.webContents.executeJavaScript("window.scanbox.dragStart('A','resize')");
  fake = { x: 430, y: 380 };
  await sleep(400);
  const b2 = win.getBounds();
  console.log('[poll] 缩小后：', JSON.stringify(b2), '期望 w=', b1.width - 70 + 3, 'h=', b1.height - 50 + 3);
  const shrinkOk = b2.width === b1.width - 70 + 3 && b2.height === b1.height - 50 + 3;
  await win.webContents.executeJavaScript("window.scanbox.dragEnd('A')");

  // 放大：(430,380)→(530,420) → w+100, h+40（无余量）
  await win.webContents.executeJavaScript("window.scanbox.dragStart('A','resize')");
  fake = { x: 530, y: 420 };
  await sleep(400);
  const b3 = win.getBounds();
  console.log('[poll] 放大后：', JSON.stringify(b3), '期望 w=', b2.width + 100, 'h=', b2.height + 40);
  const growOk = b3.width === b2.width + 100 && b3.height === b2.height + 40;
  await win.webContents.executeJavaScript("window.scanbox.dragEnd('A')");

  console.log('[poll] move=' + (moveOk ? 'OK' : 'FAIL') + ' shrink=' + (shrinkOk ? 'OK' : 'FAIL') + ' grow=' + (growOk ? 'OK' : 'FAIL'));
  service.stop();
  setTimeout(() => process.exit(moveOk && shrinkOk && growOk ? 0 : 1), 300);
}).catch((e) => { console.error('[poll] 异常：', e && (e.stack || e)); process.exit(1); });
