// 真实 OCR 运行冒烟：用打包产物里的 Electron（ELECTRON_RUN_AS_NODE=1）执行
// resources/app.asar.unpacked/electron/ocr/captcha_ocr.cjs，喂最小合法灰度 JSON，
// 由脚本自身 require 原生模块、InferenceSession.create、session.run。
//
// 只做「文件存在 / require 成功」的检查挡不住 v2.3.47 现场故障：安装包里
// onnxruntime_binding.node 与模型都在，但 binding 依赖的 msvcp140*.dll / vcruntime140*.dll
// 在目标机缺失，require 才报 "The specified module could not be found"。因此这里必须真跑推理，
// 并在此之前逐个断言原生目录里随包的 VC 运行库齐全（Windows）。
//
// 用法（CI 用默认解包目录，本地可指定）：
//   node test/ocr-smoke.cjs --app-out-dir ../dist-electron/win-unpacked
//   node test/ocr-smoke.cjs --electron <Electron 可执行文件> --helper <captcha_ocr.cjs>
// 退出码：0 通过；1 冒烟失败；2 参数/环境不满足（应视为失败，不做静默跳过）。
// 诊断只输出模块路径与错误文本，绝不记录验证码像素或识别结果。
const fs = require('fs');
const path = require('path');
const { spawnSync } = require('child_process');

const {
  MSVC_RUNTIME_DLLS,
  listOrtNativeDirs,
  missingRuntimeDlls,
} = require('../electron/ocr/runtime-deps.cjs');

const DESKTOP = path.resolve(__dirname, '..');

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (!arg.startsWith('--')) continue;
    const key = arg.slice(2);
    out[key] = argv[i + 1] && !argv[i + 1].startsWith('--') ? argv[++i] : true;
  }
  return out;
}

function detectPlatform(appOutDir) {
  if (appOutDir && fs.existsSync(appOutDir)) {
    // Windows 解包目录根上有 *.exe；Linux 是可执行文件 uvmp-toolkit
    const names = fs.readdirSync(appOutDir);
    if (names.some((name) => name.toLowerCase().endsWith('.exe'))) return 'win32';
    if (names.includes('uvmp-toolkit')) return 'linux';
  }
  return process.platform;
}

// 解包目录（appOutDir）语义：Electron 可执行文件在根，资源在 resources/
// 注意 electron-builder 的 appOutDir 是 win-unpacked / linux-unpacked 这一层，
// 不是 desktop/ 也不是 dist-electron/。asar 解包内容一律在 resources/app.asar.unpacked 下。
function fromAppOutDir(appOutDir, platform) {
  const unpacked = path.join(appOutDir, 'resources', 'app.asar.unpacked');
  const ocrDir = path.join(unpacked, 'electron', 'ocr');
  let electron = '';
  if (platform === 'win32') {
    // 只取应用本体：避免匹配到 resources 下的辅助 exe（如 elevate.exe）
    const names = fs.readdirSync(appOutDir).filter((name) => name.toLowerCase().endsWith('.exe'));
    const preferred = names.find((name) => name === 'uvmp-toolkit.exe');
    if (preferred || names.length) electron = path.join(appOutDir, preferred || names[0]);
  } else if (platform === 'linux') {
    const candidate = path.join(appOutDir, 'uvmp-toolkit');
    if (fs.existsSync(candidate)) electron = candidate;
  }
  return {
    electron,
    helper: path.join(ocrDir, 'captcha_ocr.cjs'),
    model: path.join(ocrDir, 'common.onnx'),
    charsets: path.join(ocrDir, 'charsets.json'),
    nodeModules: path.join(unpacked, 'node_modules'),
  };
}

function defaultElectron() {
  try {
    // Electron 包在纯 Node 下导出可执行文件路径
    const resolved = require('electron');
    if (typeof resolved === 'string' && fs.existsSync(resolved)) return resolved;
  } catch (_e) {
    /* 回退下面的平台约定路径 */
  }
  const dist = path.join(DESKTOP, 'node_modules', 'electron', 'dist');
  const candidates =
    process.platform === 'win32'
      ? [path.join(dist, 'electron.exe')]
      : process.platform === 'darwin'
        ? [path.join(dist, 'Electron.app', 'Contents', 'MacOS', 'Electron')]
        : [path.join(dist, 'electron')];
  return candidates.find((item) => fs.existsSync(item)) || '';
}

// 最小合法灰度图：48×20 纯白 + 一条深色横带。尺寸合法即足够——本冒烟验证的是
// 「原生模块能加载 + 会话能建 + 能跑出张量」，不校验识别准确率。
function minimalPayload() {
  const width = 48;
  const height = 20;
  const gray = Buffer.alloc(width * height, 255);
  for (let y = 8; y < 12; y += 1) {
    for (let x = 8; x < 40; x += 1) gray[y * width + x] = 24;
  }
  return { width, height, gray: gray.toString('base64') };
}

function checkRuntimeDlls(nodeModules, platform, arch = 'x64') {
  if (platform !== 'win32') return [];
  const dirs = listOrtNativeDirs(nodeModules, platform, arch);
  if (!dirs.length) {
    return [
      `未找到 onnxruntime 原生目录：${path.join(nodeModules, 'onnxruntime-node', 'bin', '*/win32/x64')}`,
    ];
  }
  const problems = [];
  for (const dir of dirs) {
    const missing = missingRuntimeDlls(dir);
    if (missing.length) {
      problems.push(
        `${dir} 缺少随包 VC 运行库 ${missing.join('、')}（目标机会报 The specified module could not be found）`
      );
    }
  }
  return problems;
}

function runOnce({ electron, helper, model, charsets, nodeModules, timeoutMs }) {
  const env = Object.assign({}, process.env, {
    ELECTRON_RUN_AS_NODE: '1',
    UVMP_OCR_MODEL: model,
    UVMP_OCR_CHARSETS: charsets,
    UVMP_NODE_MODULES: nodeModules,
  });
  // 显式指向被打包剪枝后的包目录，避免冒烟误用构建机 node_modules
  env.UVMP_OCR_ORT_PATH = path.join(nodeModules, 'onnxruntime-node');
  const payload = JSON.stringify(minimalPayload());
  const started = Date.now();
  const proc = spawnSync(electron, [helper], {
    input: payload,
    env,
    encoding: 'utf8',
    timeout: timeoutMs,
    windowsHide: true,
  });
  return {
    status: proc.status,
    signal: proc.signal,
    stdout: proc.stdout || '',
    stderr: proc.stderr || '',
    elapsedMs: Date.now() - started,
  };
}

function lastJsonLine(stdout) {
  const lines = String(stdout)
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
  if (!lines.length) return { error: '无输出' };
  try {
    return JSON.parse(lines[lines.length - 1]);
  } catch (_e) {
    return { error: `输出不是 JSON：${lines[lines.length - 1].slice(0, 200)}` };
  }
}

function fail(message, detail) {
  console.error(`[ocr-smoke] 失败：${message}`);
  if (detail) console.error(detail);
  process.exitCode = 1;
}

// --app-out-dir 在 CI 里是相对 uvmp-toolkit/ 写的（defaults.run.working-directory），
// 而本脚本位于 desktop/test/。依次按 cwd → desktop/ → desktop/../ 解析，取第一个存在的，
// 避免把 dist-electron/win-unpacked 解析成 desktop/dist-electron/win-unpacked。
function resolveAppOutDir(raw) {
  if (!raw) return '';
  if (path.isAbsolute(raw)) return raw;
  const bases = [process.cwd(), DESKTOP, path.dirname(DESKTOP)];
  for (const base of bases) {
    const candidate = path.resolve(base, raw);
    if (fs.existsSync(candidate)) return candidate;
  }
  return path.resolve(process.cwd(), raw);
}

function main(argv) {
  const args = parseArgs(argv);
  const appOutDir = resolveAppOutDir(args['app-out-dir']);
  const platform = args.platform || detectPlatform(appOutDir);
  const fromPack = appOutDir ? fromAppOutDir(appOutDir, platform) : null;
  const electron = args.electron || (fromPack && fromPack.electron) || defaultElectron();
  const helper =
    args.helper ||
    (fromPack && fromPack.helper) ||
    path.join(DESKTOP, 'electron', 'ocr', 'captcha_ocr.cjs');
  const ocrDir = path.dirname(helper);
  const model = args.model || (fromPack && fromPack.model) || path.join(ocrDir, 'common.onnx');
  const charsets =
    args.charsets || (fromPack && fromPack.charsets) || path.join(ocrDir, 'charsets.json');
  const nodeModules =
    args['node-modules'] ||
    (fromPack && fromPack.nodeModules) ||
    path.join(DESKTOP, 'node_modules');
  const arch = args.arch || 'x64';
  const timeoutMs = Number(args.timeout || 180000);

  const required = { electron, helper, model, charsets, nodeModules };
  for (const [name, file] of Object.entries(required)) {
    if (!file || !fs.existsSync(file)) return fail(`缺少 ${name}：${file || '（空）'}`);
  }
  console.log(
    `[ocr-smoke] 平台=${platform} 打包目录=${appOutDir || '（未指定，用构建机 node_modules）'}\n` +
      `[ocr-smoke] electron=${electron}\n[ocr-smoke] helper=${helper}\n[ocr-smoke] 模块目录=${nodeModules}`
  );

  const dllProblems = checkRuntimeDlls(nodeModules, platform, arch);
  if (dllProblems.length) return fail('随包 VC 运行库不齐', dllProblems.join('\n'));

  const result = runOnce({ electron, helper, model, charsets, nodeModules, timeoutMs });
  console.log(`[ocr-smoke] 退出码=${result.status} 用时=${result.elapsedMs}ms`);
  if (result.signal) {
    return fail(
      `OCR 进程被信号终止（${result.signal}）：常见原因是可执行文件与本机平台/架构不符，` +
        `或缺少系统运行库（本次超时上限 ${timeoutMs}ms）`,
      [result.stdout.trim(), result.stderr.trim()].filter(Boolean).join('\n').slice(0, 800)
    );
  }
  if (result.stderr.trim()) {
    // stderr 只做展示：Electron 在部分平台会打印无害告警；判据是退出码 + stdout
    console.log(`[ocr-smoke] stderr（前 800 字符）：\n${result.stderr.trim().slice(0, 800)}`);
  }
  if (result.status !== 0) return fail(`OCR 退出码非 0（${result.status}）`, result.stdout.trim());

  const parsed = lastJsonLine(result.stdout);
  if (parsed.error)
    return fail(`OCR 输出报错：${parsed.error}`, result.stdout.trim().slice(0, 800));
  if (typeof parsed.text !== 'string')
    return fail('OCR 输出缺少 text 字段', JSON.stringify(parsed));
  if (typeof parsed.targetWidth !== 'number' || parsed.targetWidth <= 0) {
    return fail(
      'OCR 输出缺少推理后的 targetWidth（说明未走到预处理/推理）',
      JSON.stringify(parsed)
    );
  }
  // 只报告长度，不打印识别内容本身（避免把验证码文本留在 CI 日志里）
  console.log(
    `[ocr-smoke] 通过：原生模块加载 + InferenceSession.create + session.run 均成功；` +
      `识别文本长度=${parsed.text.length}，targetWidth=${parsed.targetWidth}`
  );
  return undefined;
}

module.exports = {
  checkRuntimeDlls,
  fromAppOutDir,
  lastJsonLine,
  minimalPayload,
  parseArgs,
  resolveAppOutDir,
  runOnce,
};

if (require.main === module) {
  main(process.argv.slice(2));
}
