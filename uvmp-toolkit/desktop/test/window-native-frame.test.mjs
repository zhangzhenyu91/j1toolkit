// 双窗口统一使用系统原生边界：frame:false + thickFrame:true，由 Windows DWM 负责外框与边缘拖拽。
// 应用内只保留 48px 自绘 TitleBar（不产生双层标题栏），不再用 setShape 裁圆角、不补自绘缩放手柄，
// 最外层 CSS 也不加额外描边/圆角（否则会与系统外框叠成双线、或在高 DPI 下糊成黑角）。
import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const ROOT = new URL('../', import.meta.url);
const read = (rel) => readFileSync(new URL(rel, ROOT), 'utf8');
const exists = (rel) => existsSync(fileURLToPath(new URL(rel, ROOT)));

const mainSrc = read('electron/main.cjs');
const imageSrc = read('electron/imagetools/index.cjs');
const mainPreload = read('electron/preload.cjs');
const imagePreload = read('electron/imagetools/preload.cjs');
const appVue = read('src/App.vue');
const imageApp = read('src/imagetools/ImageToolsApp.vue');
const imageApi = read('src/imagetools/lib/api.mjs');
const guiSmoke = read('test/image-tools-gui-smoke.cjs');
const themeCss = read('src/theme.css');
const imageCss = read('src/imagetools/imagetools.css');

// 取某个 CSS 选择器的声明块（仅顶层简单选择器）
function cssBlock(css, selector) {
  const m = css.match(
    new RegExp('(^|\\n)' + selector.replace(/[.]/g, '\\.') + '\\s*\\{([^}]*)\\}')
  );
  return m ? m[2] : null;
}

test('主窗口：frame:false + thickFrame:true，系统负责外框与边缘拖拽', () => {
  const windowOpts = mainSrc.slice(mainSrc.indexOf('win = new BrowserWindow({'));
  assert.match(windowOpts, /frame:\s*false/, '仍是无系统标题栏窗口（应用内自绘 TitleBar）');
  assert.match(windowOpts, /thickFrame:\s*true/, '由 Windows 提供原生外框/阴影/边缘缩放');
  assert.doesNotMatch(mainSrc, /thickFrame:\s*false/);
  assert.match(windowOpts, /minWidth:\s*960/);
  assert.match(windowOpts, /minHeight:\s*620/);
  // 不产生双层标题栏：无系统标题栏 + 应用内 TitleBar
  assert.match(appVue, /<TitleBar\s*\/>/);
});

test('图片窗口：同样 frame:false + thickFrame:true，且保留共享 TitleBar 与窗口控制', () => {
  const windowOpts = imageSrc.slice(imageSrc.indexOf('new BrowserWindow({'));
  assert.match(windowOpts, /frame:\s*false/);
  assert.match(windowOpts, /thickFrame:\s*true/);
  assert.match(windowOpts, /minWidth:\s*MIN_WIDTH/);
  assert.match(windowOpts, /minHeight:\s*MIN_HEIGHT/);
  assert.match(imageSrc, /const MIN_WIDTH = 900/);
  assert.match(imageSrc, /const MIN_HEIGHT = 600/);
  // 窗口控制与版本/最大化事件保留（TitleBar 依赖）
  for (const channel of [
    'imagetools:winMinimize',
    'imagetools:winIsMax',
    'imagetools:winToggleMax',
    'imagetools:winClose',
    'imagetools:appVersion',
  ]) {
    assert.match(imageSrc, new RegExp("'" + channel + "'"), '缺 ' + channel);
  }
  assert.match(imageSrc, /'imagetools:max'/);
  assert.match(imageApp, /<TitleBar[\s\S]{0,120}:api="api\.windowApi"/);
});

test('不再裁剪窗口形状：无 setShape / 形状调度 / 显示器监听绑定', () => {
  for (const [name, src] of [
    ['main.cjs', mainSrc],
    ['imagetools/index.cjs', imageSrc],
  ]) {
    assert.doesNotMatch(src, /\.setShape\s*\(/, name + ' 不应再调用 setShape');
    assert.doesNotMatch(
      src,
      /createShapeScheduler|createShapeScheduler\(/,
      name + ' 不应再创建形状调度'
    );
    assert.doesNotMatch(src, /attachWindowChrome/, name + ' 不应再挂形状绑定');
    assert.doesNotMatch(src, /display-metrics-changed/, name + ' 不应再监听显示器事件');
    assert.doesNotMatch(src, /clearNativeBoundsCache/, name + ' 不应再维护原生外框缓存');
    assert.doesNotMatch(src, /window-shape|window-chrome/, name + ' 不应再引用形状模块');
  }
  // 形状模块与专属测试随裁剪方案一起删除（不是留下死代码）
  assert.equal(exists('electron/window-shape.cjs'), false, 'window-shape.cjs 应为死代码并删除');
  assert.equal(exists('test/window-shape.test.cjs'), false, '形状专属测试应删除');
  assert.equal(exists('electron/window-chrome.cjs'), false, 'window-chrome.cjs 应删除');
  assert.equal(exists('test/window-chrome.test.mjs'), false);
  assert.equal(exists('test/window-chrome-binding.test.cjs'), false);
});

test('不补自绘缩放手柄：无 WindowResizeHandles / resize IPC / 手柄样式', () => {
  assert.equal(
    exists('src/components/WindowResizeHandles.vue'),
    false,
    'thickFrame=true 时系统提供边缘缩放，自绘手柄必须撤掉'
  );
  for (const [name, src] of [
    ['src/App.vue', appVue],
    ['src/imagetools/ImageToolsApp.vue', imageApp],
  ]) {
    assert.doesNotMatch(src, /WindowResizeHandles/, name + ' 不应再渲染自绘手柄');
  }
  assert.doesNotMatch(imageApi, /resize(Start|Move|End)/);
  for (const [name, src] of [
    ['electron/preload.cjs', mainPreload],
    ['electron/imagetools/preload.cjs', imagePreload],
    ['electron/main.cjs', mainSrc],
    ['electron/imagetools/index.cjs', imageSrc],
  ]) {
    assert.doesNotMatch(src, /resize(Start|Move|End)/, name + ' 不应再暴露/注册缩放 IPC');
  }
  assert.equal(exists('electron/window-resize.cjs'), false);
  assert.equal(exists('electron/resize-bounds.cjs'), false);
  assert.equal(exists('test/window-resize.test.cjs'), false);
  assert.equal(exists('test/resize-bounds.test.cjs'), false);
  assert.doesNotMatch(themeCss, /\.wr-handle|\.wr-layer/, 'theme.css 不应保留手柄样式');
});

test('最外层壳层不加 CSS 圆角/描边（避免与系统外框叠成双线或黑角）', () => {
  for (const [name, css, selector] of [
    ['theme.css', themeCss, '.app-shell'],
    ['imagetools.css', imageCss, '.it-shell'],
  ]) {
    const block = cssBlock(css, selector);
    assert.ok(block, `${name} 缺少 ${selector}`);
    assert.doesNotMatch(block, /border-radius\s*:\s*(?!0)/, `${selector} 不应有圆角`);
    assert.doesNotMatch(
      block,
      /\bborder\s*:\s*1px|border-width\s*:\s*1px/,
      `${selector} 不应加外描边`
    );
    assert.doesNotMatch(block, /\boutline\s*:/, `${selector} 不应加 outline`);
  }
  // 最大化态也不额外描边/圆角：只保留布局类，不残留壳层形状规则
  assert.doesNotMatch(themeCss, /\.app-shell\.is-maximized\s*\{/);
  assert.doesNotMatch(imageCss, /\.it-shell\.is-maximized\s*\{/);
  // 图片窗口直接复用主主题的 TitleBar 样式（只有一份顶栏）
  assert.match(imageCss, /@import\s+['"]\.\.\/theme\.css['"]/);
  assert.match(themeCss, /\.titlebar\s*\{[^}]*height:\s*48px/);
});

test('主窗口既有行为不受影响：托盘/最大化同步/全屏与核心生命周期仍在', () => {
  // 托盘、单实例、核心事件转发、退出清理
  assert.match(mainSrc, /function createTray\(/);
  assert.match(mainSrc, /app\.on\('second-instance'/);
  assert.match(
    mainSrc,
    /win\.on\('maximize', \(\) => win && win\.webContents\.send\('win:max', true\)\)/
  );
  assert.match(
    mainSrc,
    /win\.on\('unmaximize', \(\) => win && win\.webContents\.send\('win:max', false\)\)/
  );
  assert.match(mainSrc, /if \(imageTools\) imageTools\.dispose\(\)/);
  assert.match(mainSrc, /if \(bridge\) bridge\.stop\(\)/);
  // 截屏模块必须保留（冒烟与 OCR 链路在用），只有形状模块可删
  assert.equal(exists('electron/window-capture.cjs'), true, 'window-capture.cjs 仍被冒烟使用');
  const capture = read('electron/window-capture.cjs');
  assert.match(capture, /captureWindowDesktop/);
});

test('冒烟模式使用独立并行实例，避免已有客户端截获测试参数', () => {
  assert.match(
    mainSrc,
    /process\.env\.UVMP_CI_SMOKE\s*\?\s*true\s*:\s*app\.requestSingleInstanceLock\(\)/
  );
  assert.match(guiSmoke, /UVMP_CI_SMOKE:\s*'1'/);
});

test('主窗口自检在图片/批量冒烟下关闭（否则跑完就 process.exit 掐断冒烟）', () => {
  // 主窗口 CI 自检跑完自身断言即 process.exit(0)：图片与批量冒烟要连续驱动窗口数十秒，
  // 必须由 UVMP_CI_IMAGE_SMOKE 关掉它，否则应用会在冒烟中段退出（表现为 CDP 超时）。
  assert.match(
    mainSrc,
    /if\s*\(\s*process\.env\.UVMP_CI_SMOKE\s*&&\s*!process\.env\.UVMP_CI_IMAGE_SMOKE\s*\)/,
    '主窗口自检必须同时要求「非图片冒烟」'
  );
  // 两个 GUI 冒烟都要设置该变量，否则又会退回被掐断的老问题
  assert.match(guiSmoke, /UVMP_CI_IMAGE_SMOKE:\s*'1'/, '图片冒烟未设置 UVMP_CI_IMAGE_SMOKE');
  const batchSmoke = read('test/batch-rename-gui-smoke.cjs');
  assert.match(
    batchSmoke,
    /UVMP_CI_IMAGE_SMOKE:\s*'1'/,
    '批量重命名冒烟未设置 UVMP_CI_IMAGE_SMOKE'
  );
  // 生产路径不变：该变量只在冒烟脚本里出现，不进打包配置
  const builder = read('electron-builder.yml');
  assert.doesNotMatch(builder, /UVMP_CI_IMAGE_SMOKE/, '打包配置不应包含冒烟变量');
});

test('主窗口渲染进程崩溃：图片/批量冒烟只记录诊断并继续，主窗口冒烟与生产路径仍退出', () => {
  // 图片/批量冒烟借 UVMP_CI_SMOKE 起完整应用，再连续驱动独立窗口数十秒；
  // 主窗口一崩就 process.exit(1) 会让冒烟表现为「CDP 超时」，看不出真正原因（与主窗口自检同类残留）。
  const handler = mainSrc.slice(
    mainSrc.indexOf("win.webContents.on('render-process-gone'"),
    mainSrc.indexOf('if (!crashRecovery)')
  );
  assert.ok(handler, '未找到 render-process-gone 处理');
  // 图片/批量冒烟分支：记录 reason 后 return，不与主窗口冒烟共用退出
  const imageBranch = /if\s*\(\s*process\.env\.UVMP_CI_IMAGE_SMOKE\s*\)\s*\{([\s\S]*?)\n      \}/.exec(
    handler
  );
  assert.ok(imageBranch, 'UVMP_CI_IMAGE_SMOKE 下必须先单独分支，不能直接落到 process.exit(1)');
  assert.match(imageBranch[1], /reason/, '诊断里必须带上崩溃原因');
  assert.match(imageBranch[1], /\[smoke\]/, '诊断必须带 [smoke] 前缀，便于冒烟日志定位');
  assert.match(imageBranch[1], /return;/, '图片/批量冒烟下必须 return 让冒烟继续跑');
  assert.doesNotMatch(imageBranch[1], /process\.exit/, '图片/批量冒烟下不得退出进程');
  // 分支必须排在主窗口冒烟的退出之前，否则永远走不到
  const exitAt = handler.indexOf('process.exit(1)');
  assert.ok(exitAt > 0, '主窗口冒烟仍须在渲染进程崩溃时以 exit 1 失败');
  assert.ok(
    handler.indexOf('UVMP_CI_IMAGE_SMOKE') < exitAt,
    '图片/批量冒烟分支必须写在主窗口冒烟退出之前'
  );
  assert.doesNotMatch(handler.slice(0, exitAt), /process\.exit/, '退出前只允许记录诊断');
  // 主窗口冒烟（UVMP_CI_SMOKE）与生产路径不变：仍然退出、仍然走崩溃恢复
  const tail = mainSrc.slice(mainSrc.indexOf('if (!crashRecovery)'));
  assert.match(tail, /createCrashRecovery\(\{/, '生产路径的崩溃恢复必须保留');
  assert.match(tail, /crashRecovery\.handle\(reason\)/, '生产路径仍要处理崩溃计数与提示');
});

test('冒烟不碰开机自启动：Windows 注册表 Run 与 Linux autostart 目录都不写', () => {
  // applyAutostart 会调 app.setLoginItemSettings（Windows 写 HKCU\\...\\Run）或写
  // ~/.config/autostart；冒烟启动完整应用，必须直接跳过，避免改到用户真实配置。
  const fn = mainSrc.slice(mainSrc.indexOf('function applyAutostart()'));
  const body = fn.slice(0, fn.indexOf('\n  }'));
  assert.match(body, /if\s*\(\s*process\.env\.UVMP_CI_SMOKE\s*\)\s*return;/, '缺少冒烟跳过');
  // 守卫必须排在写注册表/写文件之前
  assert.ok(
    body.indexOf('UVMP_CI_SMOKE') < body.indexOf('setLoginItemSettings'),
    '跳过判断必须在 setLoginItemSettings 之前'
  );
  assert.ok(
    body.indexOf('UVMP_CI_SMOKE') < body.indexOf('writeFileSync'),
    '跳过判断必须在写 autostart 文件之前'
  );
  // 生产分支仍在：true 时要真的登记，false 时要撤销
  assert.match(body, /openAtLogin:\s*!!uiCfg\.autostart/);
  assert.match(body, /buildAutostartEntry/);
});
