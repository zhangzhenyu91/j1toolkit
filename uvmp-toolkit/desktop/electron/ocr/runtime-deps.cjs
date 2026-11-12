// onnxruntime-node 在 Windows 上的原生运行库依赖：清单与目录约定。
// 构建期注入（build/vc-runtime.cjs）与运行期诊断（captcha_ocr.cjs）共用同一份清单，
// 避免两处漂移——漏一个 DLL 的表现就是目标机 "The specified module could not be found"。
const fs = require('fs');
const path = require('path');

// 由 onnxruntime-node 1.31.1 的 win32/x64 PE 导入表实读确认：
//   onnxruntime_binding.node → MSVCP140 / VCRUNTIME140 / VCRUNTIME140_1
//   onnxruntime.dll          → 上面三个 + MSVCP140_1 / MSVCP140_ATOMIC_WAIT
// 少复制任何一个，原生模块加载即失败（缺 atomic_wait 只有推理时才暴露）。
const MSVC_RUNTIME_DLLS = [
  'msvcp140.dll',
  'msvcp140_1.dll',
  'msvcp140_atomic_wait.dll',
  'vcruntime140.dll',
  'vcruntime140_1.dll',
];

// 随包附带在原生模块目录里的许可说明文件名
const RUNTIME_NOTICE_NAME = 'MSVC-RUNTIME-NOTICE.txt';

// bin/<napi-v*>/<platform>/<arch>：napi 目录名随版本漂移（napi-v3 → 1.31 的 napi-v6），不写死
function listOrtNativeDirs(nodeModulesRoot, platform, arch) {
  const bin = path.join(nodeModulesRoot, 'onnxruntime-node', 'bin');
  if (!fs.existsSync(bin)) return [];
  const dirs = [];
  for (const napiDir of fs.readdirSync(bin)) {
    const target = path.join(bin, napiDir, platform, arch);
    if (fs.existsSync(target)) dirs.push(target);
  }
  return dirs;
}

// 诊断用：列出 bin/ 下实际存在的平台/架构，便于一眼看出「架构不对/包被剪错」
function describeOrtLayout(nodeModulesRoot) {
  const bin = path.join(nodeModulesRoot, 'onnxruntime-node', 'bin');
  if (!fs.existsSync(bin)) return {};
  const out = {};
  for (const napiDir of fs.readdirSync(bin)) {
    const napiPath = path.join(bin, napiDir);
    if (!fs.statSync(napiPath).isDirectory()) continue;
    out[napiDir] = {};
    for (const platform of fs.readdirSync(napiPath)) {
      const platformPath = path.join(napiPath, platform);
      if (!fs.statSync(platformPath).isDirectory()) continue;
      out[napiDir][platform] = fs.readdirSync(platformPath).filter((name) => {
        try {
          return fs.statSync(path.join(platformPath, name)).isDirectory();
        } catch (_e) {
          return false;
        }
      });
    }
  }
  return out;
}

function missingRuntimeDlls(nativeDir) {
  return MSVC_RUNTIME_DLLS.filter((name) => !fs.existsSync(path.join(nativeDir, name)));
}

// PE 头里的 Machine 字段：0x8664 = x64，0xAA64 = arm64。
// 用来防止把 x64 运行库塞进 arm64 目录（bitness 不对的 DLL 加载同样是「找不到模块」）。
const PE_MACHINE = { x64: 0x8664, arm64: 0xaa64 };

function peMachine(file) {
  const fd = fs.openSync(file, 'r');
  try {
    const head = Buffer.alloc(64);
    fs.readSync(fd, head, 0, 64, 0);
    if (head.toString('latin1', 0, 2) !== 'MZ') return null;
    const pe = head.readUInt32LE(0x3c);
    const sig = Buffer.alloc(6);
    fs.readSync(fd, sig, 0, 6, pe);
    if (sig.toString('latin1', 0, 4) !== 'PE\0\0') return null;
    return sig.readUInt16LE(4);
  } finally {
    fs.closeSync(fd);
  }
}

function machineMismatch(nativeDir, arch) {
  const expected = PE_MACHINE[arch];
  if (!expected) return [];
  return MSVC_RUNTIME_DLLS.filter((name) => {
    const file = path.join(nativeDir, name);
    if (!fs.existsSync(file)) return false;
    const machine = peMachine(file);
    return machine !== null && machine !== expected;
  });
}

module.exports = {
  MSVC_RUNTIME_DLLS,
  PE_MACHINE,
  RUNTIME_NOTICE_NAME,
  listOrtNativeDirs,
  describeOrtLayout,
  machineMismatch,
  missingRuntimeDlls,
  peMachine,
};
