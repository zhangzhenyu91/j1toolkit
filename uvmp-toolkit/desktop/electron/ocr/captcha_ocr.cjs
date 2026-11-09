// 离线验证码 OCR：ddddocr common 模型 + onnxruntime-node。
// 输入（stdin JSON）：{"width":130,"height":48,"gray":"<base64 灰度字节>"}
// 输出（stdout 一行）：{"text":"sfaf","normalized":"SFAF"}
// 完全离线；由 Python 核心在自动登录时以 ELECTRON_RUN_AS_NODE 调用。
const fs = require('fs');
const path = require('path');

function loadOrt() {
  const candidates = [];
  if (process.env.UVMP_OCR_ORT_PATH) candidates.push(process.env.UVMP_OCR_ORT_PATH);
  if (process.env.UVMP_NODE_MODULES) {
    candidates.push(path.join(process.env.UVMP_NODE_MODULES, 'onnxruntime-node'));
  }
  candidates.push('onnxruntime-node');
  let lastError = null;
  for (const candidate of candidates) {
    try {
      return require(candidate);
    } catch (err) {
      lastError = err;
    }
  }
  throw lastError || new Error('onnxruntime-node 不可用');
}

function resizeBilinear(data, width, height, newWidth, newHeight) {
  const out = new Uint8ClampedArray(newWidth * newHeight);
  const xRatio = width / newWidth;
  const yRatio = height / newHeight;
  for (let y = 0; y < newHeight; y += 1) {
    const py = y * yRatio;
    const y1 = Math.floor(py);
    const y2 = Math.min(y1 + 1, height - 1);
    const fy = py - y1;
    for (let x = 0; x < newWidth; x += 1) {
      const px = x * xRatio;
      const x1 = Math.floor(px);
      const x2 = Math.min(x1 + 1, width - 1);
      const fx = px - x1;
      const v1 = data[y1 * width + x1];
      const v2 = data[y1 * width + x2];
      const v3 = data[y2 * width + x1];
      const v4 = data[y2 * width + x2];
      out[y * newWidth + x] = Math.round(
        v1 * (1 - fx) * (1 - fy) + v2 * fx * (1 - fy) + v3 * (1 - fx) * fy + v4 * fx * fy
      );
    }
  }
  return out;
}

function decodeOutput(output, charsets) {
  const values = output && output.data ? output.data : [];
  const indices = [];
  for (let i = 0; i < values.length; i += 1) {
    const value = values[i];
    indices.push(typeof value === 'bigint' ? Number(value) : Number(value) || 0);
  }
  const result = [];
  let previous = -1;
  for (const index of indices) {
    if (index === previous) continue;
    previous = index;
    if (index <= 0 || index >= charsets.length) continue;
    const char = charsets[index];
    if (char) result.push(char);
  }
  return result.join('');
}

async function main() {
  const payload = JSON.parse(fs.readFileSync(0, 'utf8'));
  const width = Number(payload.width);
  const height = Number(payload.height);
  const gray = Buffer.from(String(payload.gray || ''), 'base64');
  if (!Number.isInteger(width) || !Number.isInteger(height) || width <= 0 || height <= 0) {
    throw new Error('验证码尺寸无效');
  }
  if (gray.length !== width * height) {
    throw new Error('验证码像素长度不匹配');
  }

  const ort = loadOrt();
  const modelPath = process.env.UVMP_OCR_MODEL || path.join(__dirname, 'common.onnx');
  const charsetsPath = process.env.UVMP_OCR_CHARSETS || path.join(__dirname, 'charsets.json');
  const charsets = JSON.parse(fs.readFileSync(charsetsPath, 'utf8'));

  const targetHeight = 64;
  const targetWidth = Math.max(1, Math.round(width * (targetHeight / height)));
  const resized = resizeBilinear(gray, width, height, targetWidth, targetHeight);
  const normalized = new Float32Array(resized.length);
  for (let i = 0; i < resized.length; i += 1) {
    normalized[i] = resized[i] / 255.0;
  }

  const session = await ort.InferenceSession.create(modelPath, {
    executionProviders: ['cpu'],
    graphOptimizationLevel: 'all',
  });
  const tensor = new ort.Tensor('float32', normalized, [1, 1, targetHeight, targetWidth]);
  const results = await session.run({ input1: tensor });
  const output = results.output;
  const text = decodeOutput(output, charsets);
  await session.release();
  process.stdout.write(
    JSON.stringify({
      text,
      normalized: text.toUpperCase(),
      width,
      height,
      targetWidth,
    }) + '\n'
  );
}

main().catch((err) => {
  process.stdout.write(JSON.stringify({ error: String((err && err.message) || err) }) + '\n');
  process.exitCode = 1;
});
