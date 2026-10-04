// electron-builder afterPack 钩子：onnxruntime-node 单包含全平台预编译库（约 287MB），
// 按本次打包的目标平台/架构剪枝 bin/napi-v*/ 下的非目标目录（保留目标 + 删除其余平台/架构）。
const fs = require('fs');
const path = require('path');

const ARCH_NAME = { 1: 'x64', 3: 'arm64' };   // electron-builder Arch: 0=ia32 1=x64 2=armv7l 3=arm64

exports.default = async function pruneOrt(context) {
  const platform = context.electronPlatformName;            // win32 / linux / darwin
  const arch = ARCH_NAME[context.arch] || 'x64';
  const root = path.join(context.appOutDir, 'resources', 'app.asar.unpacked', 'node_modules',
    'onnxruntime-node', 'bin');
  if (!fs.existsSync(root)) return;
  let removed = 0;
  for (const napiDir of fs.readdirSync(root)) {             // napi-v3 / ...
    const napiPath = path.join(root, napiDir);
    for (const pf of fs.readdirSync(napiPath)) {
      const pfPath = path.join(napiPath, pf);
      if (pf !== platform) {
        removed += duMB(pfPath);
        fs.rmSync(pfPath, { recursive: true, force: true });
        continue;
      }
      for (const a of fs.readdirSync(pfPath)) {
        if (a !== arch) {
          const aPath = path.join(pfPath, a);
          removed += duMB(aPath);
          fs.rmSync(aPath, { recursive: true, force: true });
        }
      }
    }
  }
  console.log(`[prune-ort] ${platform}/${arch}：剪除 onnxruntime 非目标二进制约 ${removed}MB`);
};

function duMB(p) {
  let total = 0;
  try {
    for (const e of fs.readdirSync(p, { withFileTypes: true })) {
      const f = path.join(p, e.name);
      total += e.isDirectory() ? duMB(f) : fs.statSync(f).size;
    }
  } catch (_e) { /* 忽略 */ }
  return Math.round(total / 1048576);
}
