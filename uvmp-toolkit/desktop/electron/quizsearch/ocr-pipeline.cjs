// 题库搜题：PP-OCRv4 推理管线（onnxruntime-node，纯 JS 实现，无 OpenCV 依赖）。
// 流程与嗖嗖搜题所用 Sdcb.PaddleOCR 同族（PP-OCRv4 mobile）：
//   det（DBNet 文本检测）→ 行裁剪 → rec（CTC 识别）。
// cls（0/180 度方向分类）省略：截屏文字不存在倒置（原软件亦关闭 Enable180Classification）。
// 检测后处理为轴对齐简化版：位图膨胀 → 连通域 → 外接矩形 → unclip 外扩，
// 对屏幕水平排布文字与原多边形方案等效，且免去轮廓提取/多边形裁剪的复杂实现。
'use strict';

const fs = require('fs');
const path = require('path');
const ort = require('onnxruntime-node');

const DET_MAX_SIDE = 960;    // 长边上限（mobile det 标准）
const DET_MIN_TARGET = 736;  // 短边不足时上采样至此（保留小字召回，同 RapidOCR 默认）
const DET_ABSOLUTE_MAX = 2000;
const DET_THRESH = 0.3;      // 二值化阈值（config.yaml thresh）
const DET_BOX_THRESH = 0.5;  // 框平均得分阈值（config.yaml box_thresh）
const UNCLIP_RATIO = 1.6;    // 框外扩系数（config.yaml unclip_ratio）
const REC_H = 48;            // 识别输入高（config.yaml rec_img_shape）
const REC_MAX_W = 960;       // 识别批量内单条最大宽（过长行截断，防显存/内存膨胀）
const TEXT_SCORE = 0.5;      // 行置信度下限（config.yaml text_score）

// ---------------------------------------------------------------- 图像原语

// BGRA（Electron nativeImage.getBitmap）→ RGB 三分量平面数组
function bgraToRgbPlanes(buf, w, h) {
  const n = w * h;
  const r = new Float32Array(n);
  const g = new Float32Array(n);
  const b = new Float32Array(n);
  for (let i = 0, j = 0; i < n; i++, j += 4) {
    b[i] = buf[j];
    g[i] = buf[j + 1];
    r[i] = buf[j + 2];
  }
  return [r, g, b];
}

// 双线性缩放（平面数组，三通道同比例）
function resizePlanes(planes, sw, sh, dw, dh) {
  const out = planes.map((src) => {
    const dst = new Float32Array(dw * dh);
    const xRatio = sw / dw;
    const yRatio = sh / dh;
    for (let y = 0; y < dh; y++) {
      const fy = (y + 0.5) * yRatio - 0.5;
      let y0 = Math.floor(fy);
      const wy = fy - y0;
      if (y0 < 0) y0 = 0;
      const y1 = Math.min(y0 + 1, sh - 1);
      for (let x = 0; x < dw; x++) {
        const fx = (x + 0.5) * xRatio - 0.5;
        let x0 = Math.floor(fx);
        const wx = fx - x0;
        if (x0 < 0) x0 = 0;
        const x1 = Math.min(x0 + 1, sw - 1);
        const p00 = src[y0 * sw + x0];
        const p01 = src[y0 * sw + x1];
        const p10 = src[y1 * sw + x0];
        const p11 = src[y1 * sw + x1];
        dst[y * dw + x] = p00 * (1 - wx) * (1 - wy) + p01 * wx * (1 - wy) + p10 * (1 - wx) * wy + p11 * wx * wy;
      }
    }
    return dst;
  });
  return out;
}

// 平面 RGB → NCHW Float32，(v/255-0.5)/0.5
function planesToNchw(planes, w, h) {
  const n = w * h;
  const data = new Float32Array(3 * n);
  for (let c = 0; c < 3; c++) {
    const src = planes[c];
    const off = c * n;
    for (let i = 0; i < n; i++) data[off + i] = src[i] / 127.5 - 1;
  }
  return data;
}

// ---------------------------------------------------------------- det 后处理

// 3x3 膨胀（合并一行内的字符碎片，config.yaml use_dilation）
function dilate(bitmap, w, h) {
  const out = new Uint8Array(bitmap);
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      if (!bitmap[y * w + x]) continue;
      for (let dy = -1; dy <= 1; dy++) {
        for (let dx = -1; dx <= 1; dx++) {
          const nx = x + dx;
          const ny = y + dy;
          if (nx >= 0 && nx < w && ny >= 0 && ny < h) out[ny * w + nx] = 1;
        }
      }
    }
  }
  return out;
}

// 连通域标记（两遍法），返回各域外接矩形 [{x,y,w,h,pixels}]
function connectedComponents(bitmap, w, h) {
  const labels = new Int32Array(w * h);
  const parent = [0];
  let next = 1;
  const find = (a) => {
    while (parent[a] !== a) { parent[a] = parent[parent[a]]; a = parent[a]; }
    return a;
  };
  const union = (a, b) => {
    const ra = find(a);
    const rb = find(b);
    if (ra !== rb) parent[rb] = ra;
  };
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const i = y * w + x;
      if (!bitmap[i]) continue;
      const up = y > 0 ? labels[i - w] : 0;
      const left = x > 0 ? labels[i - 1] : 0;
      if (up && left) { labels[i] = Math.min(find(up), find(left)); union(up, left); }
      else if (up) labels[i] = find(up);
      else if (left) labels[i] = find(left);
      else { labels[i] = next; parent[next] = next; next++; }
    }
  }
  const boxes = new Map();
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const i = y * w + x;
      const l = labels[i];
      if (!l) continue;
      const r = find(l);
      let bx = boxes.get(r);
      if (!bx) { bx = { minX: x, maxX: x, minY: y, maxY: y, pixels: 0 }; boxes.set(r, bx); }
      if (x < bx.minX) bx.minX = x;
      if (x > bx.maxX) bx.maxX = x;
      if (y < bx.minY) bx.minY = y;
      if (y > bx.maxY) bx.maxY = y;
      bx.pixels++;
    }
  }
  return [...boxes.values()];
}

// 轴对齐 unclip：每边外扩 d = area*ratio/perimeter（矩形情形下与 Vatti unclip 等效）
function unclipRect(bx, maxW, maxH) {
  const w = bx.maxX - bx.minX + 1;
  const h = bx.maxY - bx.minY + 1;
  const d = (w * h * UNCLIP_RATIO) / (2 * (w + h));
  return {
    x: Math.max(0, Math.round(bx.minX - d)),
    y: Math.max(0, Math.round(bx.minY - d)),
    w: Math.min(maxW, Math.round(bx.maxX + d)) - Math.max(0, Math.round(bx.minX - d)) + 1,
    h: Math.min(maxH, Math.round(bx.maxY + d)) - Math.max(0, Math.round(bx.minY - d)) + 1,
  };
}

// ---------------------------------------------------------------- 管线

class OcrPipeline {
  static async create(modelsDir) {
    const p = new OcrPipeline();
    const opts = { executionProviders: ['cpu'], graphOptimizationLevel: 'all' };
    p.det = await ort.InferenceSession.create(path.join(modelsDir, 'det.onnx'), opts);
    p.rec = await ort.InferenceSession.create(path.join(modelsDir, 'rec.onnx'), opts);
    const keysText = fs.readFileSync(path.join(modelsDir, 'ppocr_keys.txt'), 'utf8');
    const keys = keysText.split('\n').map((s) => s.replace(/\r$/, '')).filter((s) => s.length > 0);
    // CTC 词表：索引 0 为 blank，末尾再追加空格符（同 RapidOCR insert_special_char 语义：blank@0、" "@len）
    p.chars = ['blank', ...keys, ' '];
    p.detInput = p.det.inputNames[0];
    p.detOutput = p.det.outputNames[0];
    p.recInput = p.rec.inputNames[0];
    p.recOutput = p.rec.outputNames[0];
    return p;
  }

  /**
   * 对一帧 BGRA 位图做 OCR。
   * @returns {text, lines:[{text,score,box}], elapsedMs}
   */
  async run(bgra, width, height) {
    const t0 = Date.now();
    // ---- det 前处理：等比缩放（短边上采样至 736、长边封顶 960/2000），32 对齐
    const minSide = Math.min(width, height);
    const maxSide = Math.max(width, height);
    let ratio = 1;
    if (minSide < DET_MIN_TARGET) ratio = DET_MIN_TARGET / minSide;
    if (maxSide * ratio > DET_ABSOLUTE_MAX) ratio = DET_ABSOLUTE_MAX / maxSide;
    if (maxSide * ratio > DET_MAX_SIDE && ratio <= 1) ratio = DET_MAX_SIDE / maxSide;
    let dw = Math.max(32, Math.round((width * ratio) / 32) * 32);
    let dh = Math.max(32, Math.round((height * ratio) / 32) * 32);
    const usedRatio = dw / width;

    const planes = bgraToRgbPlanes(bgra, width, height);
    const resized = resizePlanes(planes, width, height, dw, dh);
    const nchw = planesToNchw(resized, dw, dh);

    // ---- det 推理
    const detTensor = new ort.Tensor('float32', nchw, [1, 3, dh, dw]);
    const detOut = await this.det.run({ [this.detInput]: detTensor });
    const prob = detOut[this.detOutput].data; // [1,1,dh,dw]

    // ---- det 后处理：阈值 → 膨胀 → 连通域 → unclip → 框得分
    const bitmap = new Uint8Array(dw * dh);
    for (let i = 0; i < dw * dh; i++) bitmap[i] = prob[i] > DET_THRESH ? 1 : 0;
    const dilated = dilate(bitmap, dw, dh);
    const comps = connectedComponents(dilated, dw, dh);
    const boxes = [];
    for (const c of comps) {
      const rect = unclipRect(c, dw, dh);
      if (rect.w < 6 || rect.h < 6) continue;              // 噪点
      // 框平均得分（score_mode=fast 近似：外接矩形内位图像素均值）
      let sum = 0;
      let cnt = 0;
      for (let y = rect.y; y < rect.y + rect.h; y++) {
        for (let x = rect.x; x < rect.x + rect.w; x++) {
          const i = y * dw + x;
          if (bitmap[i]) { sum += prob[i]; cnt++; }
        }
      }
      if (cnt === 0 || sum / cnt < DET_BOX_THRESH) continue;
      // 映射回原图坐标
      boxes.push({
        x: Math.max(0, Math.floor(rect.x / usedRatio)),
        y: Math.max(0, Math.floor(rect.y / usedRatio)),
        w: Math.min(width, Math.ceil((rect.x + rect.w) / usedRatio)) - Math.max(0, Math.floor(rect.x / usedRatio)),
        h: Math.min(height, Math.ceil((rect.y + rect.h) / usedRatio)) - Math.max(0, Math.floor(rect.y / usedRatio)),
      });
    }
    boxes.sort((a, b) => (a.y - b.y) || (a.x - b.x));

    // ---- rec：逐行裁剪（原图）→ 48 高等比 → 批量推理 → CTC 解码
    const lines = [];
    for (const box of boxes) {
      const line = this._cropLine(planes, width, height, box);
      if (!line) continue;
      const recRes = await this._recognize(line.planes, line.w, line.h);
      if (recRes.text && recRes.score >= TEXT_SCORE) {
        lines.push({ text: recRes.text, score: recRes.score, box });
      }
    }
    return { text: lines.map((l) => l.text).join('\n'), lines, elapsedMs: Date.now() - t0 };
  }

  // 从原图平面裁剪并按 rec 输入高 48 等比缩放
  _cropLine(planes, width, height, box) {
    const w = Math.max(1, Math.min(box.w, width - box.x));
    const h = Math.max(1, Math.min(box.h, height - box.y));
    if (w < 3 || h < 3) return null;
    const cropped = planes.map((src) => {
      const dst = new Float32Array(w * h);
      for (let y = 0; y < h; y++) {
        const srcOff = (box.y + y) * width + box.x;
        dst.set(src.subarray(srcOff, srcOff + w), y * w);
      }
      return dst;
    });
    let nw = Math.max(4, Math.round((w * REC_H) / h));
    if (nw > REC_MAX_W) nw = REC_MAX_W;
    return { planes: resizePlanes(cropped, w, h, nw, REC_H), w: nw, h: REC_H };
  }

  async _recognize(planes, w, h) {
    const nchw = planesToNchw(planes, w, h);
    const tensor = new ort.Tensor('float32', nchw, [1, 3, h, w]);
    const out = await this.rec.run({ [this.recInput]: tensor });
    const preds = out[this.recOutput];           // [1, T, C]
    const dims = preds.dims;                      // [1, T, C]
    const T = dims[1];
    const C = dims[2];
    const data = preds.data;
    let text = '';
    let scoreSum = 0;
    let scoreCnt = 0;
    let prev = 0;
    for (let t = 0; t < T; t++) {
      const off = t * C;
      let maxI = 0;
      let maxV = data[off];
      for (let c = 1; c < C; c++) {
        const v = data[off + c];
        if (v > maxV) { maxV = v; maxI = c; }
      }
      if (maxI !== 0 && maxI !== prev) {
        const ch = this.chars[maxI];
        if (ch) { text += ch; scoreSum += maxV; scoreCnt++; }
      }
      prev = maxI;
    }
    return { text, score: scoreCnt ? scoreSum / scoreCnt : 0 };
  }
}

module.exports = { OcrPipeline };
