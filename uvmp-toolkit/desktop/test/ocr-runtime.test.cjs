// 离线验证码 OCR 原生模块相关回归：
// 1) onnxruntime-node 的 Windows 预编译库实际导入的 MSVC 运行库必须全部在随包清单里（升级即报警）；
// 2) afterPack 语义：剪枝按目标架构保留 + 注入 VC 运行库 DLL 与许可说明到 win32/<arch> 实际目录；
// 3) 加载失败诊断：带候选路径、平台/架构、缺失 DLL、错误码，且不含验证码内容。
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const {
  MSVC_RUNTIME_DLLS,
  RUNTIME_NOTICE_NAME,
  listOrtNativeDirs,
  missingRuntimeDlls,
} = require('../electron/ocr/runtime-deps.cjs');
const {
  VC_REDIST,
  buildNotice,
  installVcRuntime,
  verifyVcRuntime,
} = require('../build/vc-runtime.cjs');
const { formatLoadFailure } = require('../electron/ocr/captcha_ocr.cjs');

const DESKTOP = path.resolve(__dirname, '..');
const HELPER = path.join(DESKTOP, 'electron', 'ocr', 'captcha_ocr.cjs');
const ORT_BIN = path.join(DESKTOP, 'node_modules', 'onnxruntime-node', 'bin');

// 读取 PE 文件导入表（只看 DLL 名）：用来独立核对「清单是否覆盖真实依赖」，
// 不依赖 onnxruntime 的构建配置或文档。
function peImports(file) {
  const buf = fs.readFileSync(file);
  if (buf.toString('latin1', 0, 2) !== 'MZ') throw new Error('不是 PE 文件');
  const pe = buf.readUInt32LE(0x3c);
  if (buf.toString('latin1', pe, pe + 4) !== 'PE\0\0') throw new Error('缺少 PE 签名');
  const sections = buf.readUInt16LE(pe + 6);
  const optSize = buf.readUInt16LE(pe + 20);
  const opt = pe + 24;
  const magic = buf.readUInt16LE(opt);
  const dataDir = opt + (magic === 0x20b ? 112 : 96);
  const importRva = buf.readUInt32LE(dataDir + 8);
  const sectionOff = opt + optSize;
  const table = [];
  for (let i = 0; i < sections; i += 1) {
    const off = sectionOff + i * 40;
    table.push({
      va: buf.readUInt32LE(off + 12),
      size: Math.max(buf.readUInt32LE(off + 8), buf.readUInt32LE(off + 16)),
      raw: buf.readUInt32LE(off + 20),
    });
  }
  const toOffset = (rva) => {
    for (const s of table) {
      if (rva >= s.va && rva < s.va + s.size) return s.raw + (rva - s.va);
    }
    return -1;
  };
  const names = [];
  let off = toOffset(importRva);
  while (off > 0) {
    const nameRva = buf.readUInt32LE(off + 12);
    if (!nameRva && !buf.readUInt32LE(off) && !buf.readUInt32LE(off + 16)) break;
    const start = toOffset(nameRva);
    names.push(buf.toString('latin1', start, buf.indexOf(0, start)));
    off += 20;
  }
  return names;
}

// 系统自带的（Windows API 集 / 系统 DLL）无需随包
function isSystemDll(name) {
  return /^(api-ms-win-|KERNEL32|USER32|ADVAPI32|WS2_32|CRYPT32|IPHLPAPI|SETUPAPI|WINHTTP|dbghelp|ole32|SHELL32)/i.test(
    name
  );
}

function tempDir(prefix) {
  return fs.mkdtempSync(path.join(os.tmpdir(), prefix));
}

// 假解包目录：resources/app.asar.unpacked/node_modules/onnxruntime-node/bin/...
function makeFakeAppOut(platformDirs) {
  const appOutDir = tempDir('ocr-appout-');
  const resources = path.join(appOutDir, 'resources');
  const nativeRoot = path.join(
    resources,
    'app.asar.unpacked',
    'node_modules',
    'onnxruntime-node',
    'bin'
  );
  for (const [napi, platforms] of Object.entries(platformDirs)) {
    for (const [platform, arches] of Object.entries(platforms)) {
      for (const arch of arches) {
        const dir = path.join(nativeRoot, napi, platform, arch);
        fs.mkdirSync(dir, { recursive: true });
        fs.writeFileSync(path.join(dir, 'onnxruntime_binding.node'), 'fake-binding');
        fs.writeFileSync(path.join(dir, 'onnxruntime.dll'), 'fake-ort');
      }
    }
  }
  fs.mkdirSync(path.join(resources, 'core'), { recursive: true });
  fs.writeFileSync(path.join(resources, 'core', 'uvmp-core.exe'), 'fake-core');
  return { appOutDir, resources, nativeRoot };
}

// 最小 PE 头：只有 MZ / e_lfanew / PE 签名 / Machine 字段，够位深校验读取
function fakePe(machine) {
  const buf = Buffer.alloc(0x80);
  buf.write('MZ', 0, 'latin1');
  buf.writeUInt32LE(0x40, 0x3c);
  buf.write('PE\0\0', 0x40, 'latin1');
  buf.writeUInt16LE(machine, 0x44);
  return buf;
}

function minimalPayloadForTest() {
  const width = 48;
  const height = 20;
  const gray = Buffer.alloc(width * height, 255);
  return { width, height, gray: gray.toString('base64') };
}

function makeRuntimeDir({ omit = [] } = {}) {
  const dir = tempDir('ocr-msvc-');
  for (const name of MSVC_RUNTIME_DLLS) {
    if (omit.includes(name)) continue;
    fs.writeFileSync(path.join(dir, name), `fake-${name}`);
  }
  return dir;
}

test('随包 MSVC 运行库清单覆盖 onnxruntime win32 预编译库的真实导入', () => {
  const targets = [
    path.join(ORT_BIN, 'napi-v6', 'win32', 'x64', 'onnxruntime_binding.node'),
    path.join(ORT_BIN, 'napi-v6', 'win32', 'x64', 'onnxruntime.dll'),
    path.join(ORT_BIN, 'napi-v6', 'win32', 'arm64', 'onnxruntime_binding.node'),
    path.join(ORT_BIN, 'napi-v6', 'win32', 'arm64', 'onnxruntime.dll'),
  ].filter((file) => fs.existsSync(file));
  assert.ok(targets.length >= 2, 'onnxruntime-node 应带有 win32 预编译库（缺则说明依赖未安装）');
  const provided = new Set([...MSVC_RUNTIME_DLLS.map((n) => n.toLowerCase()), 'onnxruntime.dll']);
  for (const file of targets) {
    for (const dll of peImports(file)) {
      const lower = dll.toLowerCase();
      if (isSystemDll(lower)) continue;
      assert.ok(
        provided.has(lower),
        `${path.basename(file)} 依赖 ${dll}，但随包清单未覆盖（升级 onnxruntime 后需补 runtime-deps.cjs）`
      );
    }
  }
});

test('位深校验读得懂真实 PE：x64 / arm64 预编译库各自 Machine 正确', () => {
  const { peMachine } = require('../electron/ocr/runtime-deps.cjs');
  const cases = [
    ['x64', 0x8664],
    ['arm64', 0xaa64],
  ];
  for (const [arch, expected] of cases) {
    const file = path.join(ORT_BIN, 'napi-v6', 'win32', arch, 'onnxruntime.dll');
    if (!fs.existsSync(file)) continue;
    assert.equal(peMachine(file), expected, `${arch} 的 onnxruntime.dll Machine 字段不符`);
  }
});

test('afterPack 语义：剪枝保留目标架构并注入 VC 运行库与许可说明', async () => {
  const { default: pruneOrt } = require('../build/prune-ort.cjs');
  const { appOutDir, nativeRoot } = makeFakeAppOut({
    'napi-v6': {
      win32: ['x64', 'arm64'],
      linux: ['x64'],
      darwin: ['arm64'],
    },
  });
  const runtimeDir = makeRuntimeDir();
  const previous = process.env.UVMP_VC_RUNTIME_DIR;
  process.env.UVMP_VC_RUNTIME_DIR = runtimeDir;
  const logs = [];
  const originalLog = console.log;
  console.log = (line) => logs.push(String(line));
  try {
    await pruneOrt({
      electronPlatformName: 'win32',
      arch: 1, // electron-builder Arch.x64
      appOutDir,
      packager: { appInfo: { version: '9.9.9' } },
    });
  } finally {
    console.log = originalLog;
    if (previous === undefined) delete process.env.UVMP_VC_RUNTIME_DIR;
    else process.env.UVMP_VC_RUNTIME_DIR = previous;
  }

  assert.equal(fs.existsSync(path.join(nativeRoot, 'napi-v6', 'win32', 'x64')), true);
  assert.equal(
    fs.existsSync(path.join(nativeRoot, 'napi-v6', 'win32', 'arm64')),
    false,
    '非目标架构应剪除'
  );
  assert.equal(fs.existsSync(path.join(nativeRoot, 'napi-v6', 'linux')), false, '非目标平台应剪除');

  const nativeDir = path.join(nativeRoot, 'napi-v6', 'win32', 'x64');
  assert.deepEqual(missingRuntimeDlls(nativeDir), [], 'VC 运行库必须与 binding.node 同目录');
  for (const name of MSVC_RUNTIME_DLLS) {
    assert.equal(
      fs.readFileSync(path.join(nativeDir, name), 'utf8'),
      `fake-${name}`,
      `${name} 必须原样复制（不得修改微软二进制）`
    );
  }
  const notice = fs.readFileSync(path.join(nativeDir, RUNTIME_NOTICE_NAME), 'utf8');
  assert.match(notice, /Microsoft Visual C\+\+ 运行库/);
  assert.match(notice, new RegExp(VC_REDIST.page.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
  assert.match(notice, /许可/);
  assert.match(logs.join('\n'), /\[vc-runtime\]/);

  const report = verifyVcRuntime({
    resourcesDir: path.join(appOutDir, 'resources'),
    platform: 'win32',
    arch: 'x64',
  });
  assert.equal(report.length, 1);
  assert.deepEqual(report[0].missing, []);
});

test('安装缺少任一运行库时构建硬失败，并指明官方获取方式', () => {
  const { resources } = makeFakeAppOut({ 'napi-v6': { win32: ['x64'] } });
  const runtimeDir = makeRuntimeDir({ omit: ['msvcp140_atomic_wait.dll'] });
  assert.throws(
    () =>
      installVcRuntime({
        resourcesDir: resources,
        platform: 'win32',
        arch: 'x64',
        env: { UVMP_VC_RUNTIME_DIR: runtimeDir },
      }),
    (err) => {
      assert.match(err.message, /msvcp140_atomic_wait\.dll/);
      assert.match(err.message, /vc_redist\.x64\.exe/);
      return true;
    }
  );
});

test('非 Windows 平台不注入运行库（Linux/macOS 打包不受影响）', () => {
  const { resources } = makeFakeAppOut({ 'napi-v6': { linux: ['x64'] } });
  const result = installVcRuntime({
    resourcesDir: resources,
    platform: 'linux',
    arch: 'x64',
    env: {},
  });
  assert.equal(result.skipped, true);
  assert.deepEqual(
    verifyVcRuntime({ resourcesDir: resources, platform: 'linux', arch: 'x64' }),
    []
  );
});

test('原生目录缺失时构建硬失败（避免发出没有 OCR 的包）', () => {
  const { resources } = makeFakeAppOut({ 'napi-v6': { linux: ['x64'] } });
  assert.throws(
    () =>
      installVcRuntime({
        resourcesDir: resources,
        platform: 'win32',
        arch: 'x64',
        env: { UVMP_VC_RUNTIME_DIR: makeRuntimeDir() },
      }),
    /未找到 onnxruntime 原生模块目录/
  );
});

test('VC Redist 仅使用微软官方来源', () => {
  assert.match(VC_REDIST.url, /^https:\/\/aka\.ms\/vs\/17\/release\/vc_redist\.x64\.exe$/);
  assert.match(VC_REDIST.page, /^https:\/\/learn\.microsoft\.com\//);
  assert.match(VC_REDIST.redistribution, /^https:\/\/learn\.microsoft\.com\//);
  assert.match(VC_REDIST.sha256, /^[0-9a-f]{64}$/);
});

test('加载失败诊断包含候选路径/平台架构/缺失 DLL/错误码，且不含验证码内容', () => {
  const tmp = tempDir('ocr-diag-');
  const nativeDir = path.join(tmp, 'onnxruntime-node', 'bin', 'napi-v6', 'win32', 'x64');
  fs.mkdirSync(nativeDir, { recursive: true });
  fs.writeFileSync(path.join(nativeDir, 'onnxruntime_binding.node'), 'fake');
  // 只放一部分运行库，模拟目标机缺 VC Redist
  fs.writeFileSync(path.join(nativeDir, 'msvcp140.dll'), 'fake');
  const secret = Buffer.from('验证码像素不该出现').toString('base64');
  const err = Object.assign(new Error('The specified module could not be found.'), {
    code: 'ERR_DLOPEN_FAILED',
  });
  const text = formatLoadFailure(
    [
      { candidate: { source: 'UVMP_NODE_MODULES', spec: path.join(tmp, 'onnxruntime-node') }, err },
      { candidate: { source: '默认解析', spec: 'onnxruntime-node' }, err: undefined },
    ],
    { platform: 'win32', arch: 'x64' }
  );
  assert.match(text, /onnxruntime-node 加载失败/);
  assert.match(text, /平台\/架构=win32\/x64/);
  assert.match(text, /Electron=.*Node=/);
  assert.match(text, /UVMP_NODE_MODULES=/);
  assert.match(text, new RegExp(nativeDir.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
  assert.match(text, /vcruntime140\.dll=缺/);
  assert.match(text, /msvcp140\.dll=有/);
  assert.match(text, /错误码=ERR_DLOPEN_FAILED/);
  assert.match(text, /The specified module could not be found/);
  // 关键信息必须排在长路径清单之前：截断时不能先丢掉底层错误
  assert.ok(text.indexOf('底层错误=') < text.indexOf('候选='), '底层错误应排在候选路径之前');
  assert.ok(!text.includes(secret), '诊断不得包含验证码像素');
  assert.ok(text.length <= 1000);
});

test('运行库位数与目标架构不符时构建硬失败', () => {
  const { resources } = makeFakeAppOut({ 'napi-v6': { win32: ['x64'] } });
  const runtimeDir = makeRuntimeDir();
  // 把 arm64 头写进假 DLL：只改 Machine 字段即可让位深校验生效
  for (const name of MSVC_RUNTIME_DLLS) {
    fs.writeFileSync(path.join(runtimeDir, name), fakePe(0xaa64));
  }
  assert.throws(
    () =>
      installVcRuntime({
        resourcesDir: resources,
        platform: 'win32',
        arch: 'x64',
        env: { UVMP_VC_RUNTIME_DIR: runtimeDir },
      }),
    /位数与目标 x64 不符/
  );
  const nativeDir = path.join(
    resources,
    'app.asar.unpacked',
    'node_modules',
    'onnxruntime-node',
    'bin',
    'napi-v6',
    'win32',
    'x64'
  );
  assert.equal(
    fs.existsSync(path.join(nativeDir, 'msvcp140.dll')),
    false,
    '位数不符时不得留下半套 DLL'
  );
});

test('OCR 加载失败时退出码 1 且诊断含候选路径/平台架构，不回显验证码', () => {
  const { spawnSync } = require('node:child_process');
  const tmp = tempDir('ocr-e2e-');
  const modulesRoot = path.join(tmp, 'node_modules');
  const pkgDir = path.join(modulesRoot, 'onnxruntime-node');
  // 原生目录存在但 DLL 全缺：与目标机未装 VC Redist 的形态一致
  const nativeDir = path.join(pkgDir, 'bin', 'napi-v6', process.platform, process.arch);
  fs.mkdirSync(nativeDir, { recursive: true });
  fs.writeFileSync(path.join(nativeDir, 'onnxruntime_binding.node'), 'fake');
  fs.writeFileSync(
    path.join(pkgDir, 'package.json'),
    JSON.stringify({ name: 'onnxruntime-node', version: '1.31.1', main: 'index.js' })
  );
  fs.writeFileSync(
    path.join(pkgDir, 'index.js'),
    "const err = new Error('The specified module could not be found.');\n" +
      "err.code = 'ERR_DLOPEN_FAILED';\nthrow err;\n"
  );

  // 助手要复制到临时树里跑：否则末尾的「默认解析」会从本仓 desktop/node_modules
  // 找到真的 onnxruntime-node，掩盖失败路径（目标机上没有这个兜底）
  const appDir = path.join(tmp, 'app', 'electron', 'ocr');
  fs.mkdirSync(appDir, { recursive: true });
  for (const name of ['captcha_ocr.cjs', 'runtime-deps.cjs']) {
    fs.copyFileSync(path.join(path.dirname(HELPER), name), path.join(appDir, name));
  }
  const payload = JSON.stringify(minimalPayloadForTest());
  const proc = spawnSync(process.execPath, [path.join(appDir, 'captcha_ocr.cjs')], {
    input: payload,
    encoding: 'utf8',
    env: Object.assign({}, process.env, { UVMP_NODE_MODULES: modulesRoot }),
  });
  assert.equal(proc.status, 1, '加载失败必须退出码 1');
  const parsed = JSON.parse(proc.stdout.trim());
  assert.ok(parsed.error, '必须输出 error 字段');
  assert.match(parsed.error, /onnxruntime-node 加载失败/);
  assert.match(parsed.error, new RegExp(`平台/架构=${process.platform}/${process.arch}`));
  assert.match(parsed.error, /UVMP_NODE_MODULES=/);
  assert.match(parsed.error, new RegExp(nativeDir.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
  assert.match(parsed.error, /错误码=ERR_DLOPEN_FAILED/);
  assert.match(parsed.error, /The specified module could not be found/);
  assert.ok(
    !proc.stdout.includes(payload.slice(0, 40).replace(/[.*+?^${}()|[\]\\]/g, '')),
    '不得回显输入'
  );
});

test('--app-out-dir 按 cwd → desktop → 上一级依次解析（CI 从 uvmp-toolkit 调用）', () => {
  const { resolveAppOutDir } = require('./ocr-smoke.cjs');
  const parent = path.dirname(DESKTOP);
  const underDesktop = path.join(DESKTOP, 'dist-electron', 'win-unpacked');
  const underParent = path.join(parent, 'dist-electron', 'win-unpacked');
  const previous = process.cwd();
  try {
    // 只有 desktop/ 下有产物：从 desktop 调用应命中 cwd 的结果
    fs.mkdirSync(underDesktop, { recursive: true });
    process.chdir(DESKTOP);
    assert.equal(resolveAppOutDir('dist-electron/win-unpacked'), underDesktop);

    // CI 的真实形态：产物在 uvmp-toolkit/dist-electron，工作目录也是 uvmp-toolkit
    fs.mkdirSync(underParent, { recursive: true });
    process.chdir(parent);
    assert.equal(resolveAppOutDir('dist-electron/win-unpacked'), underParent);

    // 显式绝对路径不再做任何猜测
    assert.equal(resolveAppOutDir(underDesktop), path.resolve(underDesktop));
  } finally {
    process.chdir(previous);
    fs.rmSync(path.join(DESKTOP, 'dist-electron'), { recursive: true, force: true });
    fs.rmSync(path.join(parent, 'dist-electron'), { recursive: true, force: true });
  }
});

test('OCR 冒烟脚本能按解包目录定位 Electron/助手/模型', () => {
  const { fromAppOutDir, checkRuntimeDlls, minimalPayload } = require('./ocr-smoke.cjs');
  const { appOutDir } = makeFakeAppOut({ 'napi-v6': { win32: ['x64'] } });
  fs.writeFileSync(path.join(appOutDir, 'uvmp-toolkit.exe'), 'fake-exe');
  const paths = fromAppOutDir(appOutDir, 'win32');
  assert.equal(paths.electron, path.join(appOutDir, 'uvmp-toolkit.exe'));
  assert.match(
    paths.helper,
    /resources[/\\]app\.asar\.unpacked[/\\]electron[/\\]ocr[/\\]captcha_ocr\.cjs$/
  );
  assert.match(paths.nodeModules, /resources[/\\]app\.asar\.unpacked[/\\]node_modules$/);

  const problems = checkRuntimeDlls(paths.nodeModules, 'win32');
  assert.equal(problems.length, 1);
  assert.match(problems[0], /缺少随包 VC 运行库/);
  assert.match(problems[0], /msvcp140\.dll/);

  const payload = minimalPayload();
  assert.equal(Buffer.from(payload.gray, 'base64').length, payload.width * payload.height);
});
