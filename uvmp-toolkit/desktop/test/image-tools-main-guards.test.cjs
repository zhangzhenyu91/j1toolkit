// 壳层接线守卫：main/core/preload/window-state 与图片窗口的契约必须一致，
// 且不能把任意路径读取、任意宿主暴露给渲染层。
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');

const ELECTRON = path.join(__dirname, '..', 'electron');
const read = (rel) => fs.readFileSync(path.join(ELECTRON, rel), 'utf8');

const mainSrc = read('main.cjs');
const coreSrc = read('core.cjs');
const preloadSrc = read('preload.cjs');
const windowStateSrc = read('window-state.cjs');
const imageToolsSrc = read(path.join('imagetools', 'index.cjs'));
const imgPreloadSrc = read(path.join('imagetools', 'preload.cjs'));

test('window-state：dist 页面白名单包含 imagetools.html，其余页面不丢', () => {
  const { ALLOWED_PAGES } = require('../electron/window-state.cjs');
  for (const page of [
    'index.html',
    'result.html',
    'scanbox.html',
    'screensaver.html',
    'imagetools.html',
  ]) {
    assert.equal(ALLOWED_PAGES.has(page), true, page + ' 应在白名单');
  }
  assert.match(windowStateSrc, /ALLOWED_PAGES = new Set\(\[[\s\S]*?'imagetools\.html'[\s\S]*?\]\)/);
});

test('core.cjs：图片 RPC 不进主窗口白名单，只由独立图片服务放行', () => {
  const { RPC_METHODS, IMAGE_TOOLS_RPC, BRIDGE_RPC_METHODS } = require('../electron/core.cjs');
  const imageMethods = ['imageList', 'imageInspect', 'imagePreview', 'imageRename', 'imageProcess'];
  // 主窗口白名单是 core:call 的唯一闸门：图片方法一进来就等于绕过目录授权与 shade-image 协议
  for (const m of imageMethods) {
    assert.equal(RPC_METHODS.has(m), false, m + ' 不得进入主窗口白名单');
    assert.equal(IMAGE_TOOLS_RPC.has(m), true, m + ' 应由图片服务放行');
    assert.equal(BRIDGE_RPC_METHODS.has(m), true, m + ' 核心桥需允许图片服务调用');
  }
  for (const m of ['imageDelete', 'imageOpen', 'shellExec', 'readFile']) {
    assert.equal(RPC_METHODS.has(m), false, m + ' 不得放行');
    assert.equal(IMAGE_TOOLS_RPC.has(m), false, m + ' 图片服务也不得放行');
  }
  assert.match(coreSrc, /'imageList'/);
  assert.match(coreSrc, /'imageProcess'/);
  // 图片窗口自己的白名单仍完整可用
  assert.match(imageToolsSrc, /const ALLOWED_RPC = new Set\(\[/);
  for (const m of imageMethods) {
    assert.ok(imageToolsSrc.includes("'" + m + "'"), '图片窗口 ALLOWED_RPC 缺少: ' + m);
  }
});

test('主窗口 preload：新增 openImageTools / pickFiles，保留原 pickFile 行为', () => {
  assert.match(
    preloadSrc,
    /openImageTools:\s*\(\)\s*=>\s*ipcRenderer\.invoke\('core:openImageTools'\)/
  );
  assert.match(
    preloadSrc,
    /pickFiles:\s*\(filters\)\s*=>\s*ipcRenderer\.invoke\('core:pickFiles', filters\)/
  );
  assert.match(
    preloadSrc,
    /pickFile:\s*\(filters\)\s*=>\s*ipcRenderer\.invoke\('dialog:pickFile', filters\)/,
    '现有 pickFile 不得被改写'
  );
});

test('图片窗口 preload：暴露约定的 imageTools API，全部通道加 imagetools: 前缀', () => {
  const required = [
    'open',
    'pickFiles',
    'pickDirectory',
    'list',
    'inspect',
    'preview',
    'rename',
    'runProcess',
    'cancelJob',
    'getJob',
    'getConfig',
    'saveQuickPhrases',
    'showItem',
    'openDefaultSettings',
    'setDefaultLinux',
    'getDefaultStatus',
    'onEvent',
  ];
  for (const name of required) {
    assert.match(imgPreloadSrc, new RegExp('\\b' + name + '\\s*:'), '缺少 ' + name);
  }
  const invokeChannels = [...imgPreloadSrc.matchAll(/ipcRenderer\.invoke\('([^']+)'/g)].map(
    (m) => m[1]
  );
  assert.ok(invokeChannels.length >= required.length - 1);
  for (const ch of invokeChannels) {
    assert.match(ch, /^imagetools:[a-zA-Z]+$/, '通道必须收敛到 imagetools: 前缀: ' + ch);
  }
  const onChannels = [...imgPreloadSrc.matchAll(/ipcRenderer\.on\('([^']+)'/g)].map((m) => m[1]);
  assert.deepEqual(onChannels, ['imagetools:open', 'imagetools:event', 'imagetools:max']);
  assert.match(imgPreloadSrc, /contextBridge\.exposeInMainWorld\('imageTools'/);
  // 沙箱 preload 不能 require 业务模块或 node 内置模块
  const requires = [...imgPreloadSrc.matchAll(/require\('([^']+)'\)/g)].map((m) => m[1]);
  assert.deepEqual(requires, ['electron']);
});

test('main.cjs：注册 shade-image 特权协议并在 ready 后挂 handler', () => {
  assert.match(mainSrc, /registerSchemesAsPrivileged\(\s*\[\s*\{[\s\S]*?scheme:\s*'shade-image'/);
  assert.match(mainSrc, /corsEnabled:\s*true/);
  assert.match(mainSrc, /const \{ ImageToolsService \} = require\('\.\/imagetools\/index\.cjs'\)/);
  assert.match(mainSrc, /new ImageToolsService\(/);
  assert.match(mainSrc, /imageTools\.registerIpc\(\)/);
  assert.match(mainSrc, /protocol\.handle\('shade-image'/);
  // 协议注册必须在 app ready 之后、创建窗口之前
  const readyIdx = mainSrc.indexOf('app.whenReady().then(() => {');
  const handleIdx = mainSrc.indexOf("protocol.handle('shade-image'");
  assert.ok(readyIdx !== -1 && handleIdx > readyIdx, 'protocol.handle 必须在 whenReady 内');
  // 图片窗口导航：开发态/dists 都走同一守卫，不放宽 file:// 白名单
  assert.match(mainSrc, /appDir:\s*DEV && process\.env\.ELECTRON_DEV_URL \? '' : APP_DIST_DIR/);
});

test('main.cjs：冷启动与 second-instance 都解析图片参数并交给图片窗口', () => {
  assert.match(mainSrc, /parseImageArgs\(process\.argv/);
  assert.match(
    mainSrc,
    /app\.on\('second-instance',\s*\(_?event,\s*argv,\s*(cwd|workingDirectory)\)/
  );
  assert.match(mainSrc, /parseImageArgs\(argv/);
  assert.match(mainSrc, /imageTools\.openPaths\(/);
  // 第二实例仍要显示主窗口（既有行为不能丢）
  assert.match(mainSrc, /app\.on\('second-instance'[\s\S]{0,400}?showMainWindow\(win\)/);
});

test('main.cjs：图片窗口打开与多选对话框 IPC 都校验主窗口来源', () => {
  assert.match(
    mainSrc,
    /ipcMain\.handle\('core:openImageTools',[\s\S]{0,200}?requireMainWindow\(e\)/
  );
  assert.match(mainSrc, /ipcMain\.handle\('core:pickFiles',[\s\S]{0,400}?requireMainWindow\(e\)/);
  const pickFilesBlock = mainSrc.slice(mainSrc.indexOf("ipcMain.handle('core:pickFiles'"));
  assert.match(pickFilesBlock.slice(0, 800), /multiSelections/);
  assert.match(pickFilesBlock.slice(0, 800), /所有文件/);
  // core:call 仍只认白名单方法
  assert.match(
    mainSrc,
    /if \(!RPC_METHODS\.has\(method\)\) throw new Error\('不支持的 RPC 方法: ' \+ method\)/
  );
});

test('main.cjs：核心事件转发给图片窗口，退出时清理', () => {
  assert.match(mainSrc, /imageTools\.handleCoreEvent\(payload\)/);
  assert.match(mainSrc, /if \(imageTools\) imageTools\.dispose\(\)/);
  // 核心退出事件也要让图片窗口知道，避免页面一直等任务
  assert.match(mainSrc, /core\.exit/);
});

test('main.cjs：图片窗口 IPC 通道不被主窗口误注册（来源校验交给服务层）', () => {
  // imagetools:* 只应由 ImageToolsService.registerIpc 注册，main.cjs 不重复注册
  const duplicated = [...mainSrc.matchAll(/ipcMain\.handle\('(imagetools:[^']+)'/g)].map(
    (m) => m[1]
  );
  assert.deepEqual(duplicated, []);
  assert.match(mainSrc, /imageTools\.registerIpc\(\)/);
});

test('main.cjs：主窗口真正关闭时连带释放图片窗口（保持「关窗即退出」语义）', () => {
  assert.match(
    mainSrc,
    /win\.on\('closed', \(\) => \{\s*win = null;[\s\S]{0,300}?imageTools\.dispose\(\)/
  );
});

test('main.cjs：不提供渲染层任意读盘通道（shade-image 只走路径授权复核）', () => {
  assert.ok(
    !/ipcMain\.handle\('imagetools:readFile'/.test(mainSrc),
    '不得新增 imagetools:readFile'
  );
  assert.ok(!/protocol\.handle\('file'/.test(mainSrc), '不得注册 file 协议 handler');
  const src = read(path.join('imagetools', 'index.cjs'));
  assert.ok(
    !/readFileSync\(\s*[a-zA-Z_$]+\s*\)/.test(src),
    '图片服务不得直接 readFileSync 渲染层入参'
  );
});
