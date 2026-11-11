// electron-builder afterPack 钩子：
// 1) onnxruntime-node 单包含全平台预编译库（约 287MB），
//    按本次打包的目标平台/架构剪枝 bin/<napi 版本>/ 下的非目标目录（保留目标 + 删除其余平台/架构）。
//    目录名随版本漂移（napi-v3 → 1.31.x 的 napi-v6），故遍历 bin/ 实际内容，不写死版本号。
// 2) 写 resources/core/version.txt（客户端上报版本用）。NSIS 的 customInstall 只在安装器里跑，
//    绿色 zip 直接解压 win-unpacked 不执行它；tar.gz 也没有 postinst。只有 afterPack 能覆盖
//    zip / tar.gz / 解包目录这三种「非安装器」产物，deb 的 postinst 之后会按 dpkg 版本覆盖同一文件。
// 3) 核心目录缺失即失败。electron-builder 对 extraResources 的缺失源只打一行 warn 就继续，
//    产出的安装包没有 Python 核心、装完应用起不来——必须在这里硬失败（先构建核心再打包）。
const fs = require('fs');
const path = require('path');

const ARCH_NAME = { 1: 'x64', 3: 'arm64' }; // electron-builder Arch: 0=ia32 1=x64 2=armv7l 3=arm64

// resources 的位置随平台不同：macOS 在 <Product>.app/Contents/Resources，
// Windows/Linux 在 appOutDir/resources。钩子按错路径判断会把好包判成坏包。
function resourcesDir(context) {
  if (context.electronPlatformName === 'darwin') {
    const app = fs.readdirSync(context.appOutDir).find((name) => name.endsWith('.app'));
    if (!app) {
      throw new Error(`[resources] ${context.appOutDir} 下找不到 .app 包`);
    }
    return path.join(context.appOutDir, app, 'Contents', 'Resources');
  }
  return path.join(context.appOutDir, 'resources');
}

exports.default = async function pruneOrt(context) {
  const platform = context.electronPlatformName; // win32 / linux / darwin
  const arch = ARCH_NAME[context.arch] || 'x64';
  assertCoreDir(context);
  writeVersionTxt(context);
  const root = path.join(
    resourcesDir(context),
    'app.asar.unpacked',
    'node_modules',
    'onnxruntime-node',
    'bin'
  );
  if (!fs.existsSync(root)) return;
  let removed = 0;
  for (const napiDir of fs.readdirSync(root)) {
    // napi-v3 / napi-v6 / ...
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

function assertCoreDir(context) {
  // 判据用可执行文件而不是目录本身：空目录也能存在（早先一次构建只 `mkdir dist-core/core`），
  // 有目录没有 uvmp-core 同样是坏包。
  const exe = context.electronPlatformName === 'win32' ? 'uvmp-core.exe' : 'uvmp-core';
  const target = path.join(resourcesDir(context), 'core', exe);
  if (fs.existsSync(target)) return;
  throw new Error(
    `[core] 打包产物缺少 Python 核心：${target}\n` +
      'electron-builder 对缺失的 extraResources 只警告不失败，产出的安装包起不来。\n' +
      '请先构建核心并把 onedir 输出放到 uvmp-toolkit/dist-core/core（CI 的 client-* job、\n' +
      'packaging/build_local.ps1、packaging/build_unix.sh 都会做这一步），再重跑打包。'
  );
}

function writeVersionTxt(context) {
  // core/version.py（冻结后）读的是 uvmp-core 可执行文件旁的 version.txt
  const target = path.join(resourcesDir(context), 'core', 'version.txt');
  fs.writeFileSync(target, `${context.packager.appInfo.version}\n`, 'utf8');
  console.log(`[version] 写入 ${target}：${context.packager.appInfo.version}`);
}

function duMB(p) {
  let total = 0;
  try {
    for (const e of fs.readdirSync(p, { withFileTypes: true })) {
      const f = path.join(p, e.name);
      total += e.isDirectory() ? duMB(f) : fs.statSync(f).size;
    }
  } catch (_e) {
    /* 忽略 */
  }
  return Math.round(total / 1048576);
}
