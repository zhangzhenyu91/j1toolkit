// 一次性复现驱动：用打包产物的真实文件布局跑搜题服务（worker 线程环境与安装版一致），
// 打印 worker 就绪/报错原文。用法（desktop/ 下）：node_modules/.bin/electron <此文件>
'use strict';
const { app } = require('electron');
app.on('window-all-closed', () => {});

app.whenReady().then(async () => {
  const svcPath = 'E:/j1toolkit/private/uvmp-toolkit/dist-electron/win-unpacked/resources/app.asar.unpacked/electron/quizsearch/index.cjs';
  const { QuizSearchService } = require(svcPath);
  const service = new QuizSearchService({ getMainWindow: () => null });
  service.store.importRows('t', 't.xlsx', [['题干', '答案', '选项A', '选项B'], ['安全生产责任制是企业安全管理的核心制度', 'A', '正确', '错误']]);
  service.registerIpc();
  try {
    service.start();
  } catch (e) {
    console.log('[repro] start 抛错: ' + (e && (e.stack || e)));
  }
  for (let i = 0; i < 30; i++) {
    await new Promise((r) => setTimeout(r, 500));
    if (service.workerReady) { console.log('[repro] worker READY'); break; }
    if (service.workerError) { console.log('[repro] worker ERROR: ' + service.workerError); break; }
  }
  await new Promise((r) => setTimeout(r, 3000));
  console.log('[repro] final: ready=' + service.workerReady
    + ' err=' + (service.workerError || '(无)')
    + ' boxState.A=' + JSON.stringify(service.boxState.A && service.boxState.A.ocrText && service.boxState.A.ocrText.slice(0, 30)));
  try { service.stop(); } catch (_e) { /* 忽略 */ }
  setTimeout(() => process.exit(0), 300);
}).catch((e) => { console.error('[repro] 异常：', e && (e.stack || e)); process.exit(1); });
