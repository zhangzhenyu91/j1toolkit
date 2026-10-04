// 现场诊断工具：在【纯 Node 环境】（= OCR 子进程的运行环境）下验证 onnxruntime 与模型可用。
// 麒麟上「OCR 子进程退出（code=1）」时，在麒麟机终端执行（打包安装路径示例）：
//   ELECTRON_RUN_AS_NODE=1 /opt/uvmp-toolkit/uvmp-toolkit \
//     /opt/uvmp-toolkit/resources/app.asar.unpacked/electron/quizsearch/tools/worker_diag.cjs
// 每步都有输出：在哪一步断/报什么错，直接决定修复方向（缺库/缺指令集/模型损坏）。
'use strict';

const path = require('path');

function step(name) { console.log('[diag] ' + name + '…'); }

(async () => {
  step('加载 onnxruntime-node');
  const ort = require('onnxruntime-node');
  console.log('[diag] onnxruntime 版本：', JSON.stringify(ort.env.versions || {}));

  const modelsDir = path.join(__dirname, '..', 'models');
  step('创建 det 会话（' + modelsDir + '/det.onnx）');
  const det = await ort.InferenceSession.create(path.join(modelsDir, 'det.onnx'));
  console.log('[diag] det 输入/输出：', det.inputNames, det.outputNames);

  step('det 推理（64x64 零张量，验证内核/指令集）');
  const t0 = Date.now();
  const detIn = new ort.Tensor('float32', new Float32Array(3 * 64 * 64), [1, 3, 64, 64]);
  const detOut = await det.run({ [det.inputNames[0]]: detIn });
  console.log('[diag] det 推理 OK（' + (Date.now() - t0) + 'ms），输出维度：', detOut[det.outputNames[0]].dims);

  step('创建 rec 会话');
  const rec = await ort.InferenceSession.create(path.join(modelsDir, 'rec.onnx'));
  step('rec 推理（48x320 零张量）');
  const recIn = new ort.Tensor('float32', new Float32Array(3 * 48 * 320), [1, 3, 48, 320]);
  const recOut = await rec.run({ [rec.inputNames[0]]: recIn });
  console.log('[diag] rec 推理 OK，输出维度：', recOut[rec.outputNames[0]].dims);

  step('读取识别字典 ppocr_keys.txt');
  const fs = require('fs');
  const keys = fs.readFileSync(path.join(modelsDir, 'ppocr_keys.txt'), 'utf8').split('\n').filter(Boolean);
  console.log('[diag] 字典字符数：', keys.length);

  console.log('[diag] 全部 OK —— 纯 Node 环境下 OCR 引擎与模型可用');
})().catch((e) => {
  console.error('[diag] 失败：', e && (e.stack || e));
  process.exit(1);
});
