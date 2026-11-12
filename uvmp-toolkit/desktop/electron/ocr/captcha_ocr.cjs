// 离线验证码 OCR：ddddocr common 模型 + onnxruntime-node。
// 输入（stdin JSON）：{"width":130,"height":48,"gray":"<base64 灰度字节>"}
// 输出（stdout 一行）：{"text":"sfaf","normalized":"SFAF"}
// 失败时输出：{"error":"<脱敏诊断>"} 并置退出码 1（Python 核心按此报错，不重试）。
// 完全离线；由 Python 核心在自动登录时以 ELECTRON_RUN_AS_NODE 调用。
//
// 原生模块加载失败（Windows 上最常见：目标机缺 VC++ 运行库，报
// "The specified module could not be found"）时，诊断只输出候选模块路径、平台/架构、
// 原生目录与缺失的依赖 DLL、底层错误码——绝不回显验证码像素或识别结果。
const fs = require('fs');
const path = require('path');

const {
  MSVC_RUNTIME_DLLS,
  describeOrtLayout,
  listOrtNativeDirs,
  missingRuntimeDlls,
} = require('./runtime-deps.cjs');

// 诊断上限：要装得下平台/架构、候选路径、原生目录与逐 DLL 状态；关键信息已排在前部，
// 超长时被截掉的只会是末尾重复的路径清单。
const MAX_ERROR_CHARS = 1000;

function ortCandidates() {
  const candidates = [];
  if (process.env.UVMP_OCR_ORT_PATH) {
    candidates.push({ source: 'UVMP_OCR_ORT_PATH', spec: process.env.UVMP_OCR_ORT_PATH });
  }
  if (process.env.UVMP_NODE_MODULES) {
    candidates.push({
      source: 'UVMP_NODE_MODULES',
      spec: path.join(process.env.UVMP_NODE_MODULES, 'onnxruntime-node'),
    });
  }
  // 兜底走 Node 默认解析：安装包里脚本位于 app.asar.unpacked/electron/ocr，
  // 由 ../../node_modules 命中同级的 onnxruntime-node
  candidates.push({ source: '默认解析', spec: 'onnxruntime-node' });
  return candidates;
}

// 候选模块实际解析到的包目录；解析不到返回 null（不抛错，诊断要能继续收集其它信息）
function resolveOrtDir(spec) {
  try {
    return path.dirname(require.resolve(path.join(spec, 'package.json')));
  } catch (_e) {
    return null;
  }
}

function describeCandidates(candidates) {
  return candidates
    .map(({ source, spec }) => {
      const dir = resolveOrtDir(spec);
      const where = dir ? `${dir}（存在）` : `${spec}（未解析到）`;
      return `${source}=${where}`;
    })
    .join('；');
}

function describeNative(nativeDir) {
  return MSVC_RUNTIME_DLLS.map((name) => {
    const ok = fs.existsSync(path.join(nativeDir, name));
    return `${name}=${ok ? '有' : '缺'}`;
  }).join(',');
}

// nativeDir: <node_modules>/onnxruntime-node/bin/<napi>/<platform>/<arch>
function describeNativeDirs(nodeModulesRoot, platform, arch) {
  const dirs = listOrtNativeDirs(nodeModulesRoot, platform, arch);
  if (!dirs.length) {
    let layout = '（无）';
    try {
      layout = JSON.stringify(describeOrtLayout(nodeModulesRoot));
    } catch (_e) {
      /* 忽略 */
    }
    return `原生目录=未找到 ${platform}/${arch}；bin 布局=${layout}`;
  }
  return dirs.map((dir) => `原生目录=${dir}；依赖 ${describeNative(dir)}`).join(' | ');
}

function errorCode(err) {
  const code = err && (err.code || err.errno || '');
  return code ? String(code) : '';
}

function errorMessage(err) {
  if (!err) return '未知错误';
  const message = String((err && err.message) || err);
  // 只保留底层错误文本，去掉可能随调用栈带出的路径噪声
  return message.length > 240 ? `${message.slice(0, 240)}…` : message;
}

function formatLoadFailure(attempts, options = {}) {
  const platform = options.platform || process.platform;
  const arch = options.arch || process.arch;
  const first = attempts.find((item) => item.err) || {};
  const candidates = attempts.map((item) => item.candidate);
  const nodeModulesRoots = candidates
    .filter((c) => c.source === 'UVMP_NODE_MODULES')
    .map((c) => path.dirname(c.spec));
  const resolved = candidates.map((c) => resolveOrtDir(c.spec)).find(Boolean);
  if (resolved) nodeModulesRoots.push(path.dirname(resolved));
  const nativeRoots = nodeModulesRoots.filter((root, i) => nodeModulesRoots.indexOf(root) === i);

  // 关键信息（底层错误码/文本、缺失 DLL）放前面：截断时只应牺牲后面的长路径清单
  const head = ['onnxruntime-node 加载失败', `平台/架构=${platform}/${arch}`];
  const code = errorCode(first.err);
  if (code) head.push(`错误码=${code}`);
  head.push(`底层错误=${errorMessage(first.err)}`);
  if (platform === 'win32') {
    // 只报「随包目录和系统目录都没有」的 DLL：系统装过 VC Redist 时不算缺失
    const sysRoot = process.env.SystemRoot || process.env.windir || 'C:\\Windows';
    const sys32 = path.join(sysRoot, 'System32');
    const missing = new Set();
    for (const root of nativeRoots) {
      for (const dir of listOrtNativeDirs(root, platform, arch)) {
        for (const name of missingRuntimeDlls(dir)) {
          if (!fs.existsSync(path.join(sys32, name))) missing.add(name);
        }
      }
    }
    if (missing.size) {
      head.push(
        `缺少 MSVC 运行库=${[...missing].join('、')}（随包目录与 ${sys32} 均无，` +
          '即目标机未装 VC++ 运行库且安装包未随包携带）'
      );
    }
  }
  const tail = [
    `Electron=${process.versions.electron || '-'} Node=${process.versions.node || '-'}`,
    `候选=${describeCandidates(candidates)}`,
  ];
  for (const root of nativeRoots) tail.push(describeNativeDirs(root, platform, arch));
  if (!nativeRoots.length) tail.push('原生目录=无法定位 node_modules（UVMP_NODE_MODULES 未设置）');
  const text = head.concat(tail).join('；');
  return text.length > MAX_ERROR_CHARS ? `${text.slice(0, MAX_ERROR_CHARS)}…` : text;
}

function loadOrt(options = {}) {
  const candidates = options.candidates || ortCandidates();
  const attempts = [];
  for (const candidate of candidates) {
    try {
      return require(candidate.spec);
    } catch (err) {
      attempts.push({ candidate, err });
    }
  }
  throw new Error(formatLoadFailure(attempts));
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
  for (const [name, file] of [
    ['模型', modelPath],
    ['字符集', charsetsPath],
  ]) {
    if (!fs.existsSync(file)) {
      throw new Error(`离线 OCR ${name}缺失：${file}`);
    }
  }
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

module.exports = {
  formatLoadFailure,
  loadOrt,
  main,
  ortCandidates,
  resolveOrtDir,
};

if (require.main === module) {
  main().catch((err) => {
    // 只输出错误文本：诊断已脱敏，不含验证码像素与识别结果
    process.stdout.write(JSON.stringify({ error: String((err && err.message) || err) }) + '\n');
    process.exitCode = 1;
  });
}
