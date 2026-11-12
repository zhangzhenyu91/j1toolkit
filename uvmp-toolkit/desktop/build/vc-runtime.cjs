// Windows 打包期把微软官方 VC++ 运行库（MSVC v14 / VC143）随包带到 onnxruntime 原生模块目录。
//
// 为什么需要：onnxruntime_binding.node 与 onnxruntime.dll 动态链接 msvcp140*.dll、vcruntime140*.dll。
// electron-builder 只打包 node_modules 内容，不带 VC 运行库；目标机（离线内网机）未装 VC Redist 时
// require 原生模块直接报 "The specified module could not be found"（v2.3.47 现场即此现象）。
//
// 做法：构建机静默安装微软官方可再发行组件（CI 的 client-windows job），此处在 afterPack 里把
// 同架构 DLL 原样复制到 app.asar.unpacked/node_modules/onnxruntime-node/bin/<napi>/win32/<arch>，
// 与 binding.node 同级。Windows 解析原生模块依赖时优先查模块所在目录，用户端无需安装器、
// 不弹命令窗口，也不依赖目标机是否装过 VC Redist。
//
// 合法性：DLL 取自微软官方发布的 Visual C++ 可再发行组件（未做任何修改），属于 Visual Studio
// 许可条款允许的「随程序分发」与「应用本地部署（application-local deployment）」用法。
// 官方来源与许可链接见 VC_REDIST 常量，随包附带 RUNTIME_NOTICE_NAME 说明文件。
const fs = require('fs');
const path = require('path');

const {
  MSVC_RUNTIME_DLLS,
  RUNTIME_NOTICE_NAME,
  listOrtNativeDirs,
  machineMismatch,
  missingRuntimeDlls,
} = require('../electron/ocr/runtime-deps.cjs');

const VC_REDIST = {
  // 微软官方「最新受支持的 Visual C++ 可再发行组件」下载入口（x64）
  url: 'https://aka.ms/vs/17/release/vc_redist.x64.exe',
  // 微软随安全更新刷新该文件；哈希不符时构建必须失败并更新这里（与内置 CPython 的 pin 口径一致）
  sha256: 'cc0ff0eb1dc3f5188ae6300faef32bf5beeba4bdd6e8e445a9184072096b713b',
  // 下载页与许可条款（再分发说明）
  page: 'https://learn.microsoft.com/cpp/windows/latest-supported-vc-redist',
  redistribution: 'https://learn.microsoft.com/cpp/windows/redistributing-visual-cpp-files',
  license: 'https://learn.microsoft.com/visualstudio/subscriptions/vs-license-terms',
};

function resourcesFromAppOutDir(appOutDir, platform) {
  if (platform === 'darwin') {
    const app = fs.readdirSync(appOutDir).find((name) => name.endsWith('.app'));
    if (!app) {
      throw new Error(`[vc-runtime] ${appOutDir} 下找不到 .app 包`);
    }
    return path.join(appOutDir, app, 'Contents', 'Resources');
  }
  return path.join(appOutDir, 'resources');
}

// 已安装运行库的候选目录：优先显式 env（CI/离线构建机可指向解包目录），否则系统 System32。
// 只按路径查找，不调用 Windows API，便于在 macOS/Linux 上跑单元测试。
function runtimeSourceCandidates(env = process.env, platform = process.platform) {
  const dirs = [];
  if (env.UVMP_VC_RUNTIME_DIR) dirs.push(env.UVMP_VC_RUNTIME_DIR);
  if (platform === 'win32') {
    const root = env.SystemRoot || env.windir || env.WINDIR || 'C:\\Windows';
    dirs.push(path.join(root, 'System32'));
    // 32 位宿主进程读 System32 会被重定向到 SysWOW64；x64 目标需要真正的 64 位目录
    dirs.push(path.join(root, 'Sysnative'));
  }
  return dirs.filter((dir, index) => dirs.indexOf(dir) === index);
}

function findRuntimeSource(candidates) {
  for (const dir of candidates) {
    const missing = missingRuntimeDlls(dir);
    if (missing.length === 0) return { dir, missing };
  }
  return {
    dir: null,
    missing: candidates.length ? missingRuntimeDlls(candidates[0]) : MSVC_RUNTIME_DLLS,
  };
}

// 官方可再发行组件文件名随架构不同；CI 当前只产 win x64（client-windows），
// arm64 仅用于把错误提示写准（此时需自备 arm64 运行库）。
function redistFor(arch) {
  const name = arch === 'arm64' ? 'vc_redist.arm64.exe' : 'vc_redist.x64.exe';
  return `https://aka.ms/vs/17/release/${name}`;
}

function buildNotice({ sourceDir, version, arch = 'x64' }) {
  const lines = [
    'onnxruntime-node 随包的 Microsoft Visual C++ 运行库（MSVC v14 / VC143）',
    '',
    `目标架构：${arch}`,
    '',
    '文件（未做任何修改的微软官方二进制）：',
    ...MSVC_RUNTIME_DLLS.map((name) => `  - ${name}`),
    '',
    '来源：微软官方「最新受支持的 Visual C++ 可再发行组件」',
    `  下载入口：${VC_REDIST.url}`,
    `  下载页：${VC_REDIST.page}`,
    `  复制自构建机已安装运行库目录：${sourceDir}`,
  ];
  if (version) lines.push(`  已安装运行库版本（注册表 Version）：${version}`);
  lines.push(
    '',
    '许可：Microsoft Visual Studio 许可条款「可分发代码（Distributable Code）」',
    `  许可条款：${VC_REDIST.license}`,
    `  再分发说明：${VC_REDIST.redistribution}`,
    '',
    '说明：这些 DLL 以应用本地部署（application-local deployment）方式随本程序分发，',
    '仅由同目录的 onnxruntime_binding.node / onnxruntime.dll 通过模块目录解析加载，',
    '不安装到系统目录、不改写注册表，卸载客户端即随之删除。',
    ''
  );
  return lines.join('\n');
}

function installVcRuntime({
  resourcesDir,
  platform,
  arch = 'x64',
  env = process.env,
  log = console.log,
}) {
  if (platform !== 'win32')
    return { skipped: true, reason: `非 Windows（${platform}）无需 MSVC 运行库` };
  const nodeModulesRoot = path.join(resourcesDir, 'app.asar.unpacked', 'node_modules');
  const nativeDirs = listOrtNativeDirs(nodeModulesRoot, platform, arch);
  if (nativeDirs.length === 0) {
    throw new Error(
      `[vc-runtime] 未找到 onnxruntime 原生模块目录：${path.join(
        nodeModulesRoot,
        'onnxruntime-node',
        'bin',
        `*/${platform}/${arch}`
      )}\n` +
        '离线验证码 OCR 与题库搜题都依赖它；打包不应缺少该目录（检查 electron-builder 的 asarUnpack 配置）。'
    );
  }
  const candidates = runtimeSourceCandidates(env, process.platform);
  const source = findRuntimeSource(candidates);
  if (!source.dir) {
    throw new Error(
      '[vc-runtime] 构建机未找到完整的 MSVC 运行库（需要 ' +
        MSVC_RUNTIME_DLLS.join('、') +
        '）\n' +
        `已查找：${candidates.join('、') || '（无候选，可用 UVMP_VC_RUNTIME_DIR 指定）'}\n` +
        `首个候选缺失：${source.missing.join('、') || '（目录不存在）'}\n` +
        `请先在构建机安装微软官方 VC++ 可再发行组件（${redistFor(arch)}，静默：/install /quiet /norestart），\n` +
        '或用 UVMP_VC_RUNTIME_DIR 指向包含这些 DLL 的目录。目标机无 VC Redist 时缺这些 DLL 即报\n' +
        '"The specified module could not be found"。'
    );
  }
  // 复制前先校验源位数：发现不对就整批拒绝，不留半套错误 DLL 在产物里
  const sourceMismatch = machineMismatch(source.dir, arch);
  if (sourceMismatch.length) {
    throw new Error(
      `[vc-runtime] ${source.dir} 中的运行库位数与目标 ${arch} 不符：${sourceMismatch.join('、')}\n` +
        '位数不对的 DLL 加载时同样报 "The specified module could not be found"。\n' +
        `请让 UVMP_VC_RUNTIME_DIR 指向 ${arch} 版运行库（${redistFor(arch)} 的安装目录，\n` +
        `或 ${redistFor(arch)} /install /quiet /norestart /layout 解包目录）。`
    );
  }
  const version = env.UVMP_VC_RUNTIME_VERSION || '';
  const notice = buildNotice({ sourceDir: source.dir, version, arch });
  let bytes = 0;
  for (const dir of nativeDirs) {
    for (const name of MSVC_RUNTIME_DLLS) {
      const from = path.join(source.dir, name);
      const to = path.join(dir, name);
      fs.copyFileSync(from, to);
      bytes += fs.statSync(to).size;
    }
    fs.writeFileSync(path.join(dir, RUNTIME_NOTICE_NAME), notice, 'utf8');
    log(`[vc-runtime] 已注入 ${MSVC_RUNTIME_DLLS.length} 个运行库 DLL → ${dir}`);
  }
  log(
    `[vc-runtime] 来源 ${source.dir}，共 ${Math.round(bytes / 1024)}KB（目标机无需另装 VC Redist）`
  );
  return { skipped: false, sourceDir: source.dir, dirs: nativeDirs, bytes };
}

// 打包后自检：每个 win32/<arch> 原生目录都必须有全部 DLL，缺一个即坏包
function verifyVcRuntime({ resourcesDir, platform, arch = 'x64' }) {
  if (platform !== 'win32') return [];
  const nodeModulesRoot = path.join(resourcesDir, 'app.asar.unpacked', 'node_modules');
  return listOrtNativeDirs(nodeModulesRoot, platform, arch).map((dir) => ({
    dir,
    missing: missingRuntimeDlls(dir),
    wrongBitness: machineMismatch(dir, arch),
  }));
}

function printRedist() {
  process.stdout.write(
    JSON.stringify({ ...VC_REDIST, dlls: MSVC_RUNTIME_DLLS, notice: RUNTIME_NOTICE_NAME }) + '\n'
  );
}

function parseArgs(argv) {
  const out = { _: [] };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg.startsWith('--'))
      out[arg.slice(2)] = argv[i + 1] && !argv[i + 1].startsWith('--') ? argv[++i] : true;
    else out._.push(arg);
  }
  return out;
}

function main(argv) {
  const [command] = argv;
  const args = parseArgs(argv.slice(1));
  if (command === 'print-redist') {
    printRedist();
    return 0;
  }
  if (command === 'verify' || command === 'install') {
    const appOutDir =
      args['app-out-dir'] || path.join(__dirname, '..', 'dist-electron', 'win-unpacked');
    const platform = args.platform || 'win32';
    const arch = args.arch || 'x64';
    const resourcesDir = resourcesFromAppOutDir(path.resolve(appOutDir), platform);
    if (command === 'install') {
      installVcRuntime({ resourcesDir, platform, arch });
    }
    const report = verifyVcRuntime({ resourcesDir, platform, arch });
    if (report.length === 0) {
      console.error(`[vc-runtime] 未找到 ${platform}/${arch} 原生模块目录：${resourcesDir}`);
      return 1;
    }
    let bad = 0;
    for (const item of report) {
      if (item.missing.length) {
        bad += 1;
        console.error(`[vc-runtime] 缺少 ${item.missing.join('、')}：${item.dir}`);
      } else if (item.wrongBitness.length) {
        bad += 1;
        console.error(
          `[vc-runtime] 架构不符（应为 ${arch}）：${item.wrongBitness.join('、')}：${item.dir}`
        );
      } else {
        console.log(`[vc-runtime] 运行库齐全：${item.dir}`);
      }
    }
    return bad ? 1 : 0;
  }
  console.error(
    '用法：node build/vc-runtime.cjs print-redist | verify|install --app-out-dir <解包目录> [--arch x64]'
  );
  return 2;
}

module.exports = {
  VC_REDIST,
  buildNotice,
  findRuntimeSource,
  installVcRuntime,
  parseArgs,
  redistFor,
  resourcesFromAppOutDir,
  runtimeSourceCandidates,
  verifyVcRuntime,
};

if (require.main === module) {
  process.exitCode = main(process.argv.slice(2));
}
