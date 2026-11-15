// Windows 安装器（NSIS 钩子）回归测试。
//
// 覆盖三件事：
//   1. 时机：进程回收必须挂在 installSection.nsh 的 CHECK_APP_RUNNING（customCheckAppRunning）上，
//      即「删旧版 + 覆盖文件」之前；不能挪到 customInstall（那时文件已写完，回收已晚）。
//   2. 进程回收：只按本客户端两个完整映像名匹配，误伤同名子串/其他程序即失败；
//      回收失败必须明确中止并给出处理办法。
//   3. 明细与交互：ShowInstDetails 必须是 show（辅助安装器，不回退一键安装），
//      控制台子命令一律经 nsExec 隐藏执行。
//
// 测试用假 tasklist/taskkill/schtasks 执行 installer.nsh 的宏（见 nsis-hook-harness.mjs），
// 是纯静态 + 逻辑替身验证；真正的 makensis 编译与 Windows 真机安装需在 Windows 上完成。
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { loadInstallerScript, runMacro, createFakeWindows } from './nsis-hook-harness.mjs';
import { registrySnapshot } from './nsis-hook-harness.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const DESKTOP = path.resolve(HERE, '..');
const INSTALLER_NSH = path.join(DESKTOP, 'build', 'installer.nsh');
const EB_NSIS = path.join(DESKTOP, 'node_modules', 'app-builder-lib', 'templates', 'nsis');

const CLIENT = 'uvmp-toolkit.exe';
const CORE = 'uvmp-core.exe';
const VERSION_VALUES = { APP_EXECUTABLE_FILENAME: CLIENT, VERSION: '2.1.1' };

const readIfExists = (file) => (fs.existsSync(file) ? fs.readFileSync(file, 'utf8') : null);

function nsis(file) {
  const text = readIfExists(file);
  assert.ok(text != null, `缺少 electron-builder NSIS 模板：${file}`);
  return text;
}

// 在模板里按出现顺序取若干锚点，用来断言 hook 的相对位置。
function indexOfInOrder(text, anchors, file) {
  let cursor = 0;
  const found = {};
  for (const anchor of anchors) {
    const at = text.indexOf(anchor, cursor);
    assert.ok(at >= 0, `${file} 中找不到「${anchor}」（或顺序不符）`);
    found[anchor] = at;
    cursor = at;
  }
  return found;
}

test('hook 时机：customCheckAppRunning 展开在删旧版与写文件之前，customInstall 之后', () => {
  const installSection = nsis(path.join(EB_NSIS, 'installSection.nsh'));
  const assets = indexOfInOrder(
    installSection,
    [
      '!insertmacro CHECK_APP_RUNNING',
      '!insertmacro uninstallOldVersion',
      '!insertmacro installApplicationFiles',
      '!insertmacro customInstall',
    ],
    'installSection.nsh'
  );
  assert.ok(
    assets['!insertmacro CHECK_APP_RUNNING'] < assets['!insertmacro uninstallOldVersion'],
    '进程检查必须在卸载旧版之前'
  );
  assert.ok(
    assets['!insertmacro installApplicationFiles'] < assets['!insertmacro customInstall'],
    'customInstall 在文件写完之后，不能用于回收进程'
  );

  // CHECK_APP_RUNNING 优先展开 customCheckAppRunning，而不是自带的 _CHECK_APP_RUNNING。
  const guard = nsis(path.join(EB_NSIS, 'include', 'allowOnlyOneInstallerInstance.nsh'));
  const guardOrder = indexOfInOrder(
    guard,
    [
      '!macro CHECK_APP_RUNNING',
      '!ifmacrodef customCheckAppRunning',
      '!insertmacro customCheckAppRunning',
      '!else',
    ],
    'allowOnlyOneInstallerInstance.nsh'
  );
  assert.ok(
    guardOrder['!insertmacro customCheckAppRunning'] < guardOrder['!else'],
    'customCheckAppRunning 必须是 CHECK_APP_RUNNING 的首选分支'
  );
});

test('hook 时机：卸载在 onInit（静默）与卸载安装段都走同一个进程检查', () => {
  const uninstaller = nsis(path.join(EB_NSIS, 'uninstaller.nsh'));
  assert.match(uninstaller, /Function un\.onInit[\s\S]*?call un\.checkAppRunning/);
  assert.match(uninstaller, /Section "un\.\$\{UNINSTALL_SECTION_NAME\}"/);
  const section = uninstaller.slice(uninstaller.indexOf('Section "un.${UNINSTALL_SECTION_NAME}"'));
  const checkAt = section.indexOf('call un.checkAppRunning');
  const removeAt = section.indexOf('!ifmacrodef customRemoveFiles');
  assert.ok(checkAt >= 0 && removeAt > checkAt, '卸载必须先关进程再删文件');
});

test('脚本只定义 electron-builder 认识的 hook，且回收逻辑只用一份实现', () => {
  const text = fs.readFileSync(INSTALLER_NSH, 'utf8');
  const macros = [...text.matchAll(/^!macro\s+(\S+)/gm)].map((m) => m[1]);
  for (const required of [
    'customHeader',
    'customCheckAppRunning',
    'customInstall',
    'customUnInstall',
  ]) {
    assert.ok(macros.includes(required), `缺少 hook ${required}`);
  }
  // 自带模板其实并没有 installer.nsh 模板文件，customCheckAppRunning 只在我们自己的 include 里定义一次。
  const definitions = text.match(/^!macro\s+customCheckAppRunning/gm) ?? [];
  assert.equal(definitions.length, 1);
  const releaseInsertions = text.match(/!insertmacro\s+UVMP_RELEASE_PROCESSES/g) ?? [];
  assert.equal(
    releaseInsertions.length,
    1,
    '回收宏只应在 customCheckAppRunning 里插入一次，避免卸载/安装各写一份'
  );

  // 确认跳转标签必须与它的引用一起被条件编译，否则卸载器构建会出现未使用标签错误。
  const script = loadInstallerScript(INSTALLER_NSH);
  const install = runMacro(script, 'customCheckAppRunning', {
    values: VERSION_VALUES,
    fake: createFakeWindows([]),
    runtimeFlags: { Silent: true },
  });
  assert.ok(install.lines.includes('uvmp_confirm_install:'));
  const uninstall = runMacro(script, 'customCheckAppRunning', {
    values: VERSION_VALUES,
    fake: createFakeWindows([]),
    defines: ['BUILD_UNINSTALLER'],
    runtimeFlags: { Silent: true },
  });
  assert.ok(!uninstall.lines.some((line) => line.startsWith('uvmp_confirm_')));
  // 明细只用编译期 ShowInstDetails + 安装段内 SetDetailsView；.onInit 阶段不调用无文档保证的指令。
  assert.doesNotMatch(text, /!macro\s+customInit\b/);
  assert.doesNotMatch(text, /!macro\s+customUnInit\b/);
});

test('明细：ShowInstDetails 覆盖为 show，仍是辅助安装器', () => {
  const text = fs.readFileSync(INSTALLER_NSH, 'utf8');
  assert.match(text, /!macro customHeader[\s\S]*?ShowInstDetails show/);
  assert.match(text, /!macro customHeader[\s\S]*?ShowUninstDetails show/);
  // 不把安装器切回一键安装：脚本里不得出现 ONE_CLICK 编译开关。
  assert.doesNotMatch(text, /^!define\s+ONE_CLICK/m);
  assert.doesNotMatch(text, /INSTALL_MODE_PER_ALL_USERS/);

  const common = nsis(path.join(EB_NSIS, 'common.nsh'));
  assert.match(
    common,
    /^ShowInstDetails nevershow$/m,
    'electron-builder 默认关闭明细，必须被 customHeader 覆盖'
  );
  const installer = nsis(path.join(EB_NSIS, 'installer.nsi'));
  const headerAt = installer.indexOf('!ifmacrodef customHeader');
  const onInitAt = installer.indexOf('Function .onInit');
  assert.ok(headerAt >= 0 && headerAt < onInitAt, 'customHeader 在 .onInit 之前展开，编译期即生效');
});

test('明细：所有控制台子命令都走 nsExec，不闪黑框，且带 ExecToLog 进明细', () => {
  const text = fs.readFileSync(INSTALLER_NSH, 'utf8');
  const code = text
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith(';'));
  assert.ok(
    !code.some((line) => /^Exec(Wait)?\s/.test(line)),
    '不得用 Exec/ExecWait 直接拉起控制台程序（会闪黑框）'
  );
  for (const bin of ['tasklist', 'findstr', 'taskkill', 'schtasks']) {
    const lines = code.filter((line) => line.includes(bin));
    assert.ok(lines.length > 0, `脚本里应出现 ${bin}`);
    for (const line of lines) {
      assert.match(line, /nsExec::(Exec|ExecToLog)/, `${bin} 必须经 nsExec 隐藏执行：${line}`);
      assert.match(line, /\$SYSDIR/, `${bin} 应使用 $SYSDIR 绝对路径，避免 PATH 劫持：${line}`);
    }
    if (bin === 'tasklist' || bin === 'taskkill') {
      for (const line of lines) {
        assert.match(line, /\/FI "USERNAME eq %USERNAME%"/, `${bin} 只处理当前用户：${line}`);
      }
    }
  }
});

test('回收：客户端与核心都被结束，其他程序与同名子串进程不受影响', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const fake = createFakeWindows([
    { name: CLIENT, pid: 101 },
    { name: CORE, pid: 202 },
    { name: 'uvmp-core-helper.exe', pid: 303 },
    { name: 'my-uvmp-core.exe', pid: 404 },
    { name: 'explorer.exe', pid: 505 },
    { name: 'python.exe', pid: 606 },
  ]);
  const { state } = runMacro(script, 'customCheckAppRunning', {
    values: VERSION_VALUES,
    fake,
    runtimeFlags: { Silent: true },
  });

  assert.equal(state.quit, false, '正常回收后应继续安装');
  assert.deepEqual(fake.names().sort(), [
    'explorer.exe',
    'my-uvmp-core.exe',
    'python.exe',
    'uvmp-core-helper.exe',
  ]);

  const killed = fake.calls.filter((c) => c.bin === 'taskkill').map((c) => c.image);
  assert.deepEqual(killed, [CLIENT, CORE], '只允许结束本客户端的两个完整映像名');
  for (const call of fake.calls.filter((c) => c.bin === 'taskkill')) {
    assert.doesNotMatch(call.image, /[*?]/, '不得使用通配符杀进程');
  }
});

test('回收：核心优先强制结束（无消息循环），客户端先温和关闭', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const fake = createFakeWindows([
    { name: CLIENT, pid: 1 },
    { name: CORE, pid: 2 },
  ]);
  runMacro(script, 'customCheckAppRunning', {
    values: VERSION_VALUES,
    fake,
    runtimeFlags: { Silent: true },
  });

  const order = fake.calls.filter((c) => c.bin === 'taskkill');
  assert.deepEqual(
    order.map((c) => `${c.image}:${c.force ? 'F' : 'soft'}`),
    [`${CLIENT}:soft`, `${CORE}:F`],
    '先温和关闭 GUI 客户端，再强制结束没有窗口控制台的核心'
  );
});

test('回收：客户端不响应温和关闭时升级为强制结束', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const fake = createFakeWindows([{ name: CLIENT, pid: 1, softStubborn: true }]);
  const { state } = runMacro(script, 'customCheckAppRunning', {
    values: VERSION_VALUES,
    fake,
    runtimeFlags: { Silent: true },
  });

  assert.equal(state.quit, false);
  assert.deepEqual(fake.names(), []);
  assert.deepEqual(
    fake.calls
      .filter((c) => c.bin === 'taskkill')
      .map((c) => `${c.image}:${c.force ? 'F' : 'soft'}`),
    [`${CLIENT}:soft`, `${CLIENT}:F`],
    '温和关闭无效后必须强制结束，否则文件仍被占用'
  );
});

test('回收失败（静默安装）：中止并以非零退出码结束，不继续写文件', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const fake = createFakeWindows([{ name: CORE, pid: 2, stubborn: true }]);
  const { state } = runMacro(script, 'customCheckAppRunning', {
    values: VERSION_VALUES,
    fake,
    runtimeFlags: { Silent: true },
  });

  assert.equal(state.quit, true, '进程没关掉就必须中止，不能静默往下走');
  assert.equal(state.errorLevel, 2, '静默安装用非零退出码告知失败');
  assert.equal(state.messages.length, 0, '静默模式不弹窗');
  assert.ok(
    state.details.some((line) => line.includes('已中止本次操作')),
    '必须打印可理解的中止原因'
  );
  assert.deepEqual(fake.names(), [CORE]);
});

test('回收失败（交互安装）：弹「重试 / 取消」并说明去任务管理器结束哪两个进程', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const fake = createFakeWindows([{ name: CORE, pid: 2, stubborn: true }]);
  const { state } = runMacro(script, 'customCheckAppRunning', {
    values: VERSION_VALUES,
    fake,
    runtimeFlags: {},
    answers: ['IDOK', 'IDCANCEL'],
  });

  const failure = state.messages.find((m) => /MB_RETRYCANCEL/.test(m.flags));
  assert.ok(failure, '交互模式下必须给出重试/取消的选择');
  assert.match(failure.text, /任务管理器/);
  assert.match(failure.text, new RegExp(CLIENT.replace('.', '\\.')));
  assert.match(failure.text, new RegExp(CORE.replace('.', '\\.')));
  assert.match(failure.text, /尚未改动任何文件/);
  assert.equal(state.quit, true, '用户取消后必须退出');
  assert.equal(state.errorLevel, 2);
});

test('回收失败（交互安装）：用户点「重试」后继续回收成功', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const fake = createFakeWindows([{ name: CORE, pid: 2, stubborn: true }]);
  // 第一次失败后模拟用户在任务管理器手杀核心，再点「重试」。
  const { state } = runMacro(script, 'customCheckAppRunning', {
    values: VERSION_VALUES,
    fake,
    runtimeFlags: {},
    answers: ['IDOK', 'IDOK'],
    onMessage: ({ flags }) => {
      if (/MB_RETRYCANCEL/.test(flags)) fake.setStubborn(CORE, false);
    },
  });

  assert.equal(state.quit, false, '重试成功后应继续安装');
  assert.equal(state.messages.filter((m) => /MB_RETRYCANCEL/.test(m.flags)).length, 1);
  assert.deepEqual(fake.names(), []);
});

test('客户端在运行时先征得同意；用户取消则不动任何进程', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const fake = createFakeWindows([
    { name: CLIENT, pid: 1 },
    { name: CORE, pid: 2 },
  ]);
  const { state } = runMacro(script, 'customCheckAppRunning', {
    values: VERSION_VALUES,
    fake,
    runtimeFlags: {},
    answers: ['IDCANCEL'],
  });

  assert.equal(state.quit, true, '用户拒绝后必须退出');
  assert.equal(
    fake.calls.filter((c) => c.bin === 'taskkill').length,
    0,
    '未确认前不得结束任何进程'
  );
  assert.deepEqual(fake.names(), [CLIENT, CORE]);
});

test('自动更新（/updated）与静默安装不弹窗，直接回收', () => {
  const script = loadInstallerScript(INSTALLER_NSH);

  const autoUpdate = createFakeWindows([
    { name: CLIENT, pid: 1 },
    { name: CORE, pid: 2 },
  ]);
  const updated = runMacro(script, 'customCheckAppRunning', {
    values: VERSION_VALUES,
    fake: autoUpdate,
    runtimeFlags: { isUpdated: true },
  });
  assert.equal(updated.state.messages.length, 0, '自动更新不应弹「正在运行」确认框');
  assert.equal(updated.state.quit, false);
  assert.deepEqual(autoUpdate.names(), []);

  const silent = createFakeWindows([{ name: CLIENT, pid: 1 }]);
  const quiet = runMacro(script, 'customCheckAppRunning', {
    values: VERSION_VALUES,
    fake: silent,
    runtimeFlags: { Silent: true },
  });
  assert.equal(quiet.state.messages.length, 0, '静默安装不应弹窗');
  assert.equal(quiet.state.quit, false);
});

test('卸载：不弹安装确认框，同样回收核心与后台 agent', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const fake = createFakeWindows([
    { name: CLIENT, pid: 1 },
    { name: CORE, pid: 2 },
  ]);
  const { state } = runMacro(script, 'customCheckAppRunning', {
    values: VERSION_VALUES,
    fake,
    defines: ['BUILD_UNINSTALLER'],
    runtimeFlags: {},
  });

  assert.equal(state.messages.length, 0, '卸载流程不显示安装用的确认框');
  assert.equal(state.quit, false);
  assert.deepEqual(fake.names(), [], '卸载也必须先关核心，否则文件删不掉');
});

test('明细分步：回收过程在明细里可读，并显式打开明细视图', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const fake = createFakeWindows([
    { name: CLIENT, pid: 1 },
    { name: CORE, pid: 2 },
  ]);
  const { state } = runMacro(script, 'customCheckAppRunning', {
    values: VERSION_VALUES,
    fake,
    runtimeFlags: { Silent: true },
  });

  assert.ok(state.detailsPrint.includes('both'), '回收前打开明细输出');
  assert.ok(state.detailsView.includes('show'), '回收前展开明细视图');
  assert.ok(state.details.some((line) => line.includes(`请求关闭客户端：${CLIENT}`)));
  assert.ok(state.details.some((line) => line.includes(`强制结束后台核心：${CORE}`)));
  assert.ok(state.details.some((line) => line.includes('均已退出')));
});

test('customHeader 编译期生效：安装与卸载都展开明细', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const install = runMacro(script, 'customHeader', { fake: createFakeWindows([]) });
  assert.equal(install.state.showInstDetails, 'show', '安装明细必须展开');
  assert.equal(install.state.showUninstDetails, null, '安装构建里不设卸载明细');

  const uninstall = runMacro(script, 'customHeader', {
    fake: createFakeWindows([]),
    defines: ['BUILD_UNINSTALLER'],
  });
  assert.equal(uninstall.state.showInstDetails, 'show');
  assert.equal(uninstall.state.showUninstDetails, 'show', '卸载明细同样展开');
});

test('计划任务：安装重建 UsbAgent 登录任务，卸载删除；沿用隐藏启动脚本', () => {
  const script = loadInstallerScript(INSTALLER_NSH);

  const install = createFakeWindows([]);
  const installed = runMacro(script, 'customInstall', { values: VERSION_VALUES, fake: install });
  const created = install.calls.filter((c) => c.bin === 'schtasks' && /\/create/.test(c.cmd));
  assert.equal(
    created.length,
    1,
    '安装只应重建一个 UsbAgent 登录任务（避免多份 agent 抢同一份指令）'
  );
  assert.match(created[0].cmd, /\/tn "UVMP-Toolkit-UsbAgent"/);
  assert.match(created[0].cmd, /\/sc onlogon/);
  assert.match(created[0].cmd, /wscript\.exe .*run-usb-agent\.vbs/);
  assert.match(
    created[0].cmd,
    /\\resources\\core\\run-usb-agent\.vbs/,
    '任务指向安装目录内的隐藏启动脚本'
  );
  const runs = install.calls.filter((c) => c.bin === 'schtasks' && /\/run/.test(c.cmd));
  assert.equal(runs.length, 1, '升级结束后应立刻拉起一次 agent（否则本次登录无人消费 U 盘指令）');
  assert.match(runs[0].cmd, /\/run \/tn "UVMP-Toolkit-UsbAgent"/);
  assert.equal(installed.state.quit, false);

  const removed = install.calls.filter(
    (c) =>
      c.bin === 'schtasks' &&
      /\/delete/.test(c.cmd) &&
      /DailyExport|TreeSync|CrossingSync/.test(c.cmd)
  );
  assert.equal(removed.length, 5, '旧版按分钟触发的任务应清理');

  const uninstall = createFakeWindows([]);
  runMacro(script, 'customUnInstall', {
    values: VERSION_VALUES,
    fake: uninstall,
    defines: ['BUILD_UNINSTALLER'],
  });
  const deleted = uninstall.calls
    .filter((c) => c.bin === 'schtasks' && /\/delete/.test(c.cmd))
    .map((c) => c.cmd);
  assert.ok(
    deleted.some((cmd) => cmd.includes('UVMP-Toolkit-UsbAgent')),
    '卸载要删除登录任务，否则重启后 agent 复活'
  );
  assert.equal(deleted.length, 6, '卸载应删除全部本客户端的计划任务');
});

test('计划任务与版本文件都在 installApplicationFiles 之后执行（不早于文件落地）', () => {
  const installSection = nsis(path.join(EB_NSIS, 'installSection.nsh'));
  const installAt = installSection.indexOf('!insertmacro installApplicationFiles');
  const customAt = installSection.indexOf('!insertmacro customInstall');
  assert.ok(installAt >= 0 && customAt > installAt);

  const text = fs.readFileSync(INSTALLER_NSH, 'utf8');
  assert.match(text, /FileOpen \$0 "\$INSTDIR\\resources\\core\\version\.txt" w/);
});

test('electron-builder 配置仍是辅助安装器且引入本钩子文件', () => {
  const yml = fs.readFileSync(path.join(DESKTOP, 'electron-builder.yml'), 'utf8');
  const nsisBlock = yml.slice(yml.indexOf('\nnsis:'));
  assert.match(nsisBlock, /oneClick:\s*false/);
  assert.match(nsisBlock, /perMachine:\s*false/);
  assert.match(nsisBlock, /allowToChangeInstallationDirectory:\s*true/);
  assert.match(nsisBlock, /include:\s*installer\.nsh/);
});

test('未指定 makensis 时，include 的依赖都存在（模板目录或 NSIS 内置）', () => {
  // NSIS 自带的 include（LogicLib/FileFunc/StrFunc/x64/WinVer/MUI2…）不在 electron-builder 模板里，
  // 由 makensis 的 NSISDIR 解析；这里只约束「模板自带的 include 必须真的存在」，避免拼错文件名。
  const NBuiltIn = new Set([
    'LogicLib.nsh',
    'FileFunc.nsh',
    'StrFunc.nsh',
    'x64.nsh',
    'WinVer.nsh',
    'MUI2.nsh',
    'nsDialogs.nsh',
    'Sections.nsh',
    'WordFunc.nsh',
  ]);

  const text = fs.readFileSync(INSTALLER_NSH, 'utf8');
  const includes = [...text.matchAll(/^!include\s+"?([^"\s]+)"?/gm)].map((m) => m[1]);
  assert.ok(includes.length > 0);
  for (const inc of includes) {
    if (NBuiltIn.has(inc)) continue;
    const candidates = [path.join(EB_NSIS, 'include', inc), path.join(EB_NSIS, inc)];
    assert.ok(
      candidates.some((file) => fs.existsSync(file)),
      `include 的 ${inc} 既非 NSIS 内置也不在 electron-builder 模板内`
    );
  }

  // 用到的插件必须由 electron-builder 提供；nsExec 属于 NSIS 自带插件，模板里以 nsExec:: 调用。
  assert.ok(fs.existsSync(path.join(EB_NSIS, 'include', 'StdUtils.nsh')));
  assert.match(text, /nsExec::(Exec|ExecToLog)\s+`/);
});

test('宏参数不含空格：NSIS 会按空格切分 !insertmacro 参数', () => {
  const text = fs.readFileSync(INSTALLER_NSH, 'utf8');
  for (const line of text.split('\n')) {
    const m = /^\s*!insertmacro\s+(\S+)(?:\s+(.*))?$/.exec(line);
    if (!m || !m[2]) continue;
    const args = m[2].trim().split(/\s+/);
    // 带引号/反引号的参数会被切成多段，说明这一行会被 NSIS 误解析。
    for (const arg of args) {
      assert.doesNotMatch(
        arg,
        /["'`]/,
        `宏 ${m[1]} 的参数里出现引号，NSIS 会按空格切断：${line.trim()}`
      );
    }
  }
});

// ---------------------------------------------------------------------------
// 文件关联：安装只登记「打开方式」，绝不改写扩展名的默认 ProgID / UserChoice
// ---------------------------------------------------------------------------
// 关联格式必须与图片工具实际能看的格式一致（JPEG/JPG、PNG、GIF、WebP、BMP、ICO、AVIF、TIFF）。
const IMAGE_EXTS = ['avif', 'bmp', 'gif', 'ico', 'jpeg', 'jpg', 'png', 'tif', 'tiff', 'webp'];
const IMAGE_PROGID = 'uvmp-toolkit.image';
const extKey = (ext) => `hkcu\\software\\classes\\.${ext}`.toLowerCase();
const openWithKey = (ext) => `hkcu\\software\\classes\\.${ext}\\openwithprogids`.toLowerCase();
const progIdKey = (suffix) => `hkcu\\software\\classes\\${IMAGE_PROGID}\\${suffix}`.toLowerCase();

test('安装：为全部查看格式登记打开方式，但不写扩展名的默认 ProgID', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const { state } = runMacro(script, 'customInstall', {
    values: VERSION_VALUES,
    fake: createFakeWindows([]),
  });
  const snap = registrySnapshot(state);

  for (const ext of IMAGE_EXTS) {
    const openWith = snap[openWithKey(ext)];
    assert.ok(openWith, `.${ext} 未登记打开方式`);
    assert.ok(IMAGE_PROGID in openWith, `.${ext} 的 OpenWithProgids 缺少 ${IMAGE_PROGID}`);
    assert.equal(
      snap[extKey(ext)],
      undefined,
      `.${ext} 不得写入默认 ProgID（那等于静默改默认打开方式）`
    );
  }

  const command = snap[progIdKey('shell\\open\\command')];
  assert.ok(command, '缺少打开方式命令');
  const cmd = Object.values(command)[0];
  assert.match(cmd, /%1/, '打开方式命令必须接收文件参数');
  assert.match(cmd, /uvmp-toolkit\.exe/i, '打开方式命令应指向客户端本体');
});

test('安装：不改写 UserChoice（Windows 保护的系统默认应用记录）', () => {
  // 只看代码行（注释里解释「为什么不碰 UserChoice」是允许且必要的）
  const code = fs
    .readFileSync(INSTALLER_NSH, 'utf8')
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith(';'));
  const joined = code.join('\n');
  assert.doesNotMatch(joined, /FileExts/i, '安装器不得触碰 FileExts（UserChoice 所在分支）');
  assert.doesNotMatch(joined, /UserChoice/i, '安装器不得读写 UserChoice');
  // 只要出现「写扩展名默认值」的注册表调用就失败
  assert.doesNotMatch(
    joined,
    /WriteRegStr\s+\S+\s+"Software\\\\Classes\\\\\.\$\{?[A-Za-z_]+\}?"\s+""/
  );
});

test('卸载：默认打开方式已指向别家时保留用户选择，只清自己的项', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const seed = {};
  for (const ext of IMAGE_EXTS) {
    seed[extKey(ext)] = { '': 'Photos.exe' };
    seed[openWithKey(ext)] = { [IMAGE_PROGID]: '', 'other.app': '' };
  }
  seed[progIdKey('shell\\open\\command')] = {
    '': '"C:\\Program Files\\uvmp-toolkit\\uvmp-toolkit.exe" "%1"',
  };

  const { state } = runMacro(script, 'customUnInstall', {
    values: VERSION_VALUES,
    fake: createFakeWindows([]),
    defines: ['BUILD_UNINSTALLER'],
    registry: seed,
  });
  const snap = registrySnapshot(state);

  for (const ext of IMAGE_EXTS) {
    assert.equal(
      snap[extKey(ext)][''],
      'Photos.exe',
      `.${ext} 的默认打开方式属于用户/其他程序，卸载不得清除`
    );
    const openWith = snap[openWithKey(ext)];
    assert.ok(!(IMAGE_PROGID in openWith), `.${ext} 的打开方式登记应被清理`);
    assert.ok('other.app' in openWith, `其他程序在 .${ext} 的打开方式登记不得被误删`);
  }
  assert.equal(snap[progIdKey('shell\\open\\command')], undefined, '卸载应删除自己的 ProgID 键');
});

test('卸载：默认打开方式仍指向本工具时清掉该默认值', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const seed = {};
  for (const ext of IMAGE_EXTS) {
    seed[extKey(ext)] = { '': IMAGE_PROGID };
    seed[openWithKey(ext)] = { [IMAGE_PROGID]: '' };
  }
  const { state } = runMacro(script, 'customUnInstall', {
    values: VERSION_VALUES,
    fake: createFakeWindows([]),
    defines: ['BUILD_UNINSTALLER'],
    registry: seed,
  });
  const snap = registrySnapshot(state);
  for (const ext of IMAGE_EXTS) {
    const key = extKey(ext);
    assert.ok(!snap[key] || !('' in snap[key]), `.${ext} 的默认值仍指向已卸载的程序`);
  }
});

test('卸载：清理后通知外壳刷新关联（否则「打开方式」里残留本工具）', () => {
  const text = fs.readFileSync(INSTALLER_NSH, 'utf8');
  assert.match(text, /SHChangeNotify|UPDATEFILEASSOC/, '应刷新 shell 关联缓存');
});

test('electron-builder 配置：Linux 关联声明覆盖全部查看格式并使用 %F', () => {
  const yml = fs.readFileSync(path.join(DESKTOP, 'electron-builder.yml'), 'utf8');
  // 顶层 fileAssociations 会同时喂给 NSIS，生成改写 .ext 默认值的宏；Windows 由 installer.nsh 自理
  assert.doesNotMatch(yml, /^fileAssociations:/m, '不得在顶层声明 fileAssociations');
  assert.match(yml, /executableArgs:[\s\S]*%F/, 'Linux 桌面项必须用 %F（多文件参数）');

  // 取 linux 块里所有 ext 声明（支持 `ext: png` 与 `ext: [jpg, jpeg]` 两种写法）
  const linux = yml.slice(yml.indexOf('\nlinux:'));
  const declared = new Set();
  for (const m of linux.matchAll(/^\s*-\s*ext:\s*(.+)$/gm)) {
    for (const token of m[1].replace(/[[\]]/g, '').split(',')) {
      const ext = token.trim().toLowerCase();
      if (ext) declared.add(ext);
    }
  }
  // Linux 侧每个扩展名单列一项（多扩展名会被 electron-builder 拼成非法的 `*.jpg,jpeg`）；
  // .jpeg / .tiff 别名由 shared-mime-info 自带的映射覆盖，不必重复登记。
  const linuxExts = IMAGE_EXTS.filter((ext) => ext !== 'jpeg' && ext !== 'tiff');
  for (const ext of linuxExts) {
    assert.ok(declared.has(ext), `linux.fileAssociations 缺少 ${ext}（已声明：${[...declared]}）`);
  }

  for (const mime of [
    'image/jpeg',
    'image/png',
    'image/gif',
    'image/webp',
    'image/bmp',
    'image/vnd.microsoft.icon',
    'image/avif',
    'image/tiff',
  ]) {
    assert.ok(linux.includes(mime), `缺少 MIME 声明 ${mime}`);
  }
  assert.doesNotMatch(yml, /ghostscript/i, '不得引入 Ghostscript 依赖');
});

test('升级：卸载旧版时（--updated）保留用户关联登记，只做真实卸载时清理', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const seed = {};
  for (const ext of IMAGE_EXTS) {
    seed[openWithKey(ext)] = { [IMAGE_PROGID]: '', 'other.app': '' };
  }
  seed[progIdKey('shell\\open\\command')] = { '': '"old\\uvmp-toolkit.exe" "%1"' };

  // 覆盖安装会带 --updated 调用旧版卸载器：此时新版本马上写回关联，
  // 若先清掉，用户「打开方式」会在升级窗口内丢失（甚至被别的程序抢走）。
  const { state } = runMacro(script, 'customUnInstall', {
    values: VERSION_VALUES,
    fake: createFakeWindows([]),
    defines: ['BUILD_UNINSTALLER'],
    runtimeFlags: { isUpdated: true },
    registry: seed,
  });
  const snap = registrySnapshot(state);
  for (const ext of IMAGE_EXTS) {
    assert.ok(
      snap[openWithKey(ext)] && IMAGE_PROGID in snap[openWithKey(ext)],
      `升级路径不得清掉 .${ext} 的打开方式（${ext}）`
    );
  }
  assert.ok(
    snap[progIdKey('shell\\open\\command')],
    '升级路径不得删除自己的 ProgID（新版本会复用这条命令）'
  );
  assert.ok(
    state.details.some((line) => line.includes('升级')),
    '升级路径应留下可读的明细说明'
  );

  // 真实卸载仍然清理
  const real = runMacro(script, 'customUnInstall', {
    values: VERSION_VALUES,
    fake: createFakeWindows([]),
    defines: ['BUILD_UNINSTALLER'],
    registry: seed,
  });
  const realSnap = registrySnapshot(real.state);
  for (const ext of IMAGE_EXTS) {
    assert.ok(
      !(IMAGE_PROGID in (realSnap[openWithKey(ext)] ?? {})),
      `真实卸载应清理 .${ext} 的打开方式`
    );
  }
  assert.equal(realSnap[progIdKey('shell\\open\\command')], undefined);
});

test('升级：计划任务在 --updated 路径仍要重建（不能只在第一次安装生效）', () => {
  const script = loadInstallerScript(INSTALLER_NSH);
  const fake = createFakeWindows([]);
  runMacro(script, 'customInstall', {
    values: VERSION_VALUES,
    fake,
    runtimeFlags: { isUpdated: true },
  });
  const created = fake.calls.filter((c) => c.bin === 'schtasks' && /\/create/.test(c.cmd));
  assert.equal(created.length, 1, '升级后也必须重建 UsbAgent 登录任务');
});

// ---------------------------------------------------------------------------
// 门禁正则本身要能识别真实坏写法（曾经写成要求两个反斜杠 → 永远不命中）
// ---------------------------------------------------------------------------
test('门禁：改写扩展名默认 ProgID 的检测正则可识别真实反例', () => {
  // 真实 NSIS 源里只有单个反斜杠；用同一份正则扫真实坏写法必须命中
  const BAD = [
    String.raw`WriteRegStr SHELL_CONTEXT "Software\Classes\.png" "" "uvmp-toolkit.image"`,
    String.raw`WriteRegStr SHELL_CONTEXT "Software\Classes\.jpg" "" "app.jpg"`,
    String.raw`  WriteRegStr HKEY_CURRENT_USER "Software\Classes\.tiff" "" "x"`,
    // 模板里的 ${...} 会被 JS 插值，必须拼接构造
    String.raw`WriteRegStr SHELL_CONTEXT "Software\Classes\.` +
      '${_EXT}' +
      String.raw`" "" "` +
      '${PROGID}' +
      '"',
    String.raw`WriteRegStr SHELL_CONTEXT "Software\Classes\.$R1" "" "app"`,
  ];
  const GOOD = [
    String.raw`WriteRegStr SHELL_CONTEXT "Software\Classes\uvmp-toolkit.image" "" "图片"`,
    String.raw`WriteRegStr SHELL_CONTEXT "Software\Classes\uvmp-toolkit.image\DefaultIcon" "" "$INSTDIR\a.exe,0"`,
    String.raw`WriteRegNone SHELL_CONTEXT "Software\Classes\.png\OpenWithProgids" "uvmp-toolkit.image"`,
    String.raw`DeleteRegValue SHELL_CONTEXT "Software\Classes\.png" ""`,
    String.raw`ReadRegStr $R5 SHELL_CONTEXT "Software\Classes\.png" ""`,
  ];
  for (const line of BAD) {
    assert.ok(findDefaultProgIdWrites(line).length === 1, `未命中坏写法：${line}`);
  }
  for (const line of GOOD) {
    assert.deepEqual(findDefaultProgIdWrites(line), [], `误判合法写法：${line}`);
  }

  // 真实 installer.nsh 必须一条都不命中
  assert.deepEqual(findDefaultProgIdWrites(fs.readFileSync(INSTALLER_NSH, 'utf8')), []);
});

// 与 packaging/test_build_config.py 的 _find_default_progid_writes 保持同一份规则
function findDefaultProgIdWrites(text) {
  // 注意：NSIS 源里反斜杠只有一个，正则里必须写 `\\`（匹配一个反斜杠）。
  // 曾误写成 `\\\\`（要求两个反斜杠）→ 门禁永远不命中，等于没测。
  const pattern =
    /WriteRegStr\s+\S+\s+"Software\\Classes\\\.(?:\$\{?[A-Za-z_][A-Za-z0-9_]*\}?|[A-Za-z0-9._$]+)"\s+""/;
  return text
    .split('\n')
    .map((line, index) => ({ line: line.trim(), index }))
    .filter(({ line }) => !line.startsWith(';'))
    .filter(({ line }) => pattern.test(line));
}

test('升级：打包变体没有 updated 标志时仍能编译，并按真实卸载处理', () => {
  // ${isUpdated} 由 electron-builder scriptGenerator.flags() 生成；个别变体（自定义脚本 /
  // 未来的打包方式）可能不带该标志。钩子用 !ifmacrodef 挡住，不能因此编译失败，
  // 也不能把关联清理整个跳过（那会让卸载残留打开方式）。
  const script = loadInstallerScript(INSTALLER_NSH);
  const text = fs.readFileSync(INSTALLER_NSH, 'utf8');
  assert.match(text, /!ifmacrodef\s+_isUpdated/, '必须按宏是否存在做条件编译');

  const seed = {};
  for (const ext of IMAGE_EXTS) {
    seed[openWithKey(ext)] = { [IMAGE_PROGID]: '' };
  }
  const { state } = runMacro(script, 'customUnInstall', {
    values: VERSION_VALUES,
    fake: createFakeWindows([]),
    defines: ['BUILD_UNINSTALLER'],
    ebMacros: [],
    registry: seed,
  });
  const snap = registrySnapshot(state);
  for (const ext of IMAGE_EXTS) {
    assert.ok(
      !(IMAGE_PROGID in (snap[openWithKey(ext)] ?? {})),
      `无 updated 标志时应按真实卸载清理 .${ext}`
    );
  }
});
