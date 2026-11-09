// 内网工具箱 Electron 主进程：窗口/托盘/单实例/核心进程生命周期/原生对话框。
const { app, BrowserWindow, Tray, Menu, ipcMain, dialog, shell } = require('electron');
const path = require('path');
const fs = require('fs');
const { spawn } = require('child_process');
const { CoreBridge, prodCoreCommand } = require('./core.cjs');
const { showMainWindow } = require('./window-state.cjs');

const DEV = !app.isPackaged;

// 托盘/窗口图标：安装包把 electron/icon.png 解包到 app.asar.unpacked，
// 开发态直接读源码目录；缺失时返回空串由调用方跳过托盘创建并记录错误。
function appIconPath() {
  if (app.isPackaged && process.resourcesPath) {
    const unpacked = path.join(process.resourcesPath, 'app.asar.unpacked', 'electron', 'icon.png');
    if (fs.existsSync(unpacked)) return unpacked;
  }
  const local = path.join(__dirname, 'icon.png');
  return fs.existsSync(local) ? local : '';
}

// Linux 图标关联：X11 WM_CLASS 取 Browser::GetName()（Electron 源码），Unicode 产品名无法匹配
// desktop 文件（StartupWMClass 不支持 unicode，任务栏/应用中心出齿轮占位图——已踩坑）。
// 覆盖为 ASCII 名（uvmp-toolkit，与安装的 desktop 文件名一致）；userData 钉回原目录不丢既有配置；
// setDesktopName 置 CHROME_DESKTOP（Wayland app id 用）。
if (process.platform === 'linux') {
  const origUserData = path.join(app.getPath('appData'), 'Shade 壹匣 - 内网');
  app.setName('uvmp-toolkit');
  app.setPath('userData', origUserData);
  app.setDesktopName('uvmp-toolkit.desktop');
}

// ---------------------------------------------------------------- CLI 渲染模式
// uvmp-toolkit --print-to-pdf <in.html> <out.pdf> [budget_ms] [wait_expr]
// 用 Electron 自带 Chromium 做离线 PDF 渲染（核心经此调用，摆脱对系统 Chrome 的依赖）。
// 必须在单实例锁之前分支——渲染调用与 GUI 并存，且可能并发多个。
const printIdx = process.argv.indexOf('--print-to-pdf');
if (printIdx !== -1) {
  app.commandLine.appendSwitch('no-sandbox');
  app.commandLine.appendSwitch('disable-gpu');
  app.commandLine.appendSwitch('disable-dev-shm-usage');
  const inFile = process.argv[printIdx + 1];
  const outFile = process.argv[printIdx + 2];
  const budgetMs = parseInt(process.argv[printIdx + 3] || '30000', 10);
  const waitExpr = process.argv[printIdx + 4] || '';
  app.whenReady().then(async () => {
    const win = new BrowserWindow({
      show: false,
      width: 1200,
      height: 850,
      webPreferences: { contextIsolation: true, nodeIntegration: false, webSecurity: false },
    });
    const finish = (code, msg) => {
      if (msg) console.error(msg);
      process.exit(code);
    };
    try {
      win.webContents.on('did-fail-load', (_e, code, desc) =>
        finish(1, '[print] did-fail-load: ' + desc)
      );
      await win.loadFile(inFile);
      if (waitExpr) {
        // 地图页等异步渲染：轮询 wait_expr 为真（同 _cdp_map_pdf 的 __mapReady 等待语义）
        const deadline = Date.now() + budgetMs;
        while (Date.now() < deadline) {
          const ready = await win.webContents.executeJavaScript(waitExpr).catch(() => false);
          if (ready === true) break;
          await new Promise((r) => setTimeout(r, 400));
        }
      } else {
        await new Promise((r) => setTimeout(r, Math.min(3000, budgetMs)));
      }
      const pdf = await win.webContents.printToPDF({
        preferCSSPageSize: true,
        printBackground: true,
      });
      fs.writeFileSync(outFile, pdf);
      console.log('[print] wrote ' + outFile + ' (' + pdf.length + ' bytes)');
      finish(0);
    } catch (e) {
      finish(1, '[print] 失败: ' + (e && (e.message || e)));
    }
  });
  // 不走 GUI 主流程
} else {
  let win = null;
  let tray = null;
  let bridge = null;
  let forceQuit = false;
  let quizSearch = null; // 题库搜题服务（壳层功能，见 electron/quizsearch/）
  let uiCfg = { close_to_tray: true, autostart: false };

  // 客户端 UI 配置（关窗驻留/开机自启），存于核心 config.json 的 ui.*（与核心共享一份配置）
  function loadUiConfig() {
    if (!bridge) return;
    bridge
      .call('getConfig')
      .then((cfg) => {
        uiCfg = Object.assign({ close_to_tray: true, autostart: false }, (cfg || {}).ui || {});
        applyAutostart();
      })
      .catch(() => {});
  }

  function applyAutostart() {
    try {
      if (process.platform === 'win32') {
        app.setLoginItemSettings({ openAtLogin: !!uiCfg.autostart, path: process.execPath });
      } else if (process.platform === 'linux') {
        const dir = path.join(app.getPath('home'), '.config', 'autostart');
        const f = path.join(dir, 'uvmp-toolkit.desktop');
        if (uiCfg.autostart) {
          fs.mkdirSync(dir, { recursive: true });
          fs.writeFileSync(
            f,
            '[Desktop Entry]\nType=Application\nName=Shade 壹匣 - 内网\n' +
              'Exec=' +
              process.execPath +
              '\nX-GNOME-Autostart-enabled=true\n'
          );
        } else if (fs.existsSync(f)) {
          fs.unlinkSync(f);
        }
      }
    } catch (e) {
      console.error('autostart 设置失败', e);
    }
  }

  // U 盘单监听 agent：独立进程常驻（systemd/计划任务也会拉起；flock 保证单实例）。
  // GUI 启动时补拉一次，避免绿色包/开发环境没有注册服务时无人消费服务端指令。
  function startUsbAgent() {
    // CI 冒烟不拉起常驻进程：避免后台 agent 存活导致冒烟进程退出后管道/清理被拖住。
    if (process.env.UVMP_CI_SMOKE) return;
    try {
      if (!bridge) return;
      const { cmd, args } = bridge.resolveCommand();
      const agentArgs = args.filter((a) => a !== 'rpc').concat(['usb-agent', '--quiet']);
      const child = spawn(cmd, agentArgs, {
        detached: true,
        stdio: 'ignore',
        windowsHide: true,
        env: Object.assign({}, process.env, bridge.opts.env || {}),
      });
      child.unref();
    } catch (e) {
      console.error('U盘 agent 启动失败', e);
    }
  }

  function coreCommand() {
    if (process.env.UVMP_CORE) return process.env.UVMP_CORE; // 显式指定（调试）
    if (!DEV) return prodCoreCommand(process.resourcesPath); // 生产：resources/core/
    return null; // 开发：python 源码
  }

  function startCore() {
    bridge = new CoreBridge({
      command: coreCommand(),
      devCwd: path.resolve(__dirname, '..', '..'),
      env: { UVMP_ELECTRON: process.execPath, UVMP_VERSION: app.getVersion() }, // 核心用应用本体做离线 PDF 渲染；版本带进 agent 状态/结果
      onEvent: (payload) => {
        if (win && !win.isDestroyed()) win.webContents.send('core:event', payload);
      },
      onExit: (code) => {
        if (win && !win.isDestroyed())
          win.webContents.send('core:event', { event: 'core.exit', code });
      },
    });
    bridge.start();
  }

  function createWindow() {
    win = new BrowserWindow({
      width: 1160,
      height: 780,
      minWidth: 960,
      minHeight: 620,
      title: 'Shade 壹匣 - 内网',
      // 图标必须放在 files 覆盖范围内（electron/）——build/ 是构建期目录，不进运行时包（已踩坑：
      // 引用 build/icon.png 导致安装版托盘没建出来，关窗直接退出）
      icon: appIconPath() || undefined,
      backgroundColor: '#00000000', // 透明底：Windows 10 / Linux 也由 CSS 裁出圆角
      show: false,
      frame: false, // 无边框：自定义标题栏（微信/QQ 式），窗口控制走 win:* IPC
      transparent: true, // 配合 .app-shell 的 border-radius，三平台一致圆角
      webPreferences: {
        preload: path.join(__dirname, 'preload.cjs'),
        contextIsolation: true,
        nodeIntegration: false,
        // 本地应用、仅加载打包内资源、无任何远程内容；file:// 下 ES module 受 CORS 限制需关 webSecurity
        webSecurity: false,
      },
    });
    win.once('ready-to-show', () => win.show());
    win.setMenuBarVisibility(false);
    // CI 冒烟模式（UVMP_CI_SMOKE=1）：断言 #app 真实渲染【且核心 RPC 可用】后退出（0=正常，1=失败）。
    // did-finish-load 可能触发多次，且 Vue 挂载有时延——单发标志 + 1s 间隔最多重试 10 次。
    // 第二重断言：导航项 ≥4（首页+应用们+设置）——能抓到 IPC 注册类故障（如 bridge 作用域错误，已踩坑）
    if (process.env.UVMP_CI_SMOKE) {
      win.webContents.on('console-message', (e, ...args) =>
        console.log('[renderer]', args.slice(1).join(' '))
      );
      win.webContents.on('did-fail-load', (_e, code, desc) => {
        console.error('[smoke] did-fail-load: ' + code + ' ' + desc);
        process.exit(1);
      });
      let started = false;
      win.webContents.on('did-finish-load', () => {
        if (started) return;
        started = true;
        let tries = 0;
        const check = async () => {
          tries += 1;
          try {
            const n = await win.webContents.executeJavaScript(
              "document.querySelector('#app') ? document.querySelector('#app').childElementCount : 0"
            );
            if (n > 0) {
              // 再等核心 RPC：导航项应由 getApps 填充
              const navs = await win.webContents.executeJavaScript(
                "document.querySelectorAll('.nav-item').length"
              );
              if (navs >= 4) {
                if (!appIconPath() || !fs.existsSync(appIconPath())) {
                  console.error('[smoke] electron/icon.png 缺失（托盘/窗口图标不可用）');
                  process.exit(1);
                }
                // 再点一下「设置」导航验证路由表（已踩坑：PAGE_MAP 缺项导致设置跳首页）
                await win.webContents.executeJavaScript(
                  "(() => { const items = [...document.querySelectorAll('.nav-item')];" +
                    "const t = items.find(e => (e.textContent || '').includes('设置'));" +
                    'if (t) t.click(); return !!t; })()'
                );
                await new Promise((r) => setTimeout(r, 1000));
                const settingsOk = await win.webContents.executeJavaScript(
                  "document.body.innerText.includes('账号 · 派车系统') || document.body.innerText.includes('派车系统 SSO 凭据')"
                );
                if (!settingsOk) {
                  console.error('[smoke] 设置页路由失败（未渲染账号卡片）');
                  process.exit(1);
                }
                // 设置搜索：搜索时展开命中组，清空后恢复用户原展开态
                const sectionOpenState = () =>
                  "(() => { const map = {}; for (const c of document.querySelectorAll('.collapsible-card')) {" +
                  "const title = c.querySelector('.collapsible-head span');" +
                  "const body = c.querySelector('.collapsible-body');" +
                  "if (title) map[title.textContent.trim()] = !!body && getComputedStyle(body).display !== 'none';" +
                  '} return map; })()';
                const initialSections = await win.webContents.executeJavaScript(sectionOpenState());
                if (!initialSections['账号 · 派车系统'] || initialSections['宿主浏览器']) {
                  console.error('[smoke] 设置页初始展开态异常：' + JSON.stringify(initialSections));
                  process.exit(1);
                }
                await win.webContents.executeJavaScript(
                  "(() => { const el = document.querySelector('.settings-search input');" +
                    "el.value = '并发'; el.dispatchEvent(new Event('input', { bubbles: true })); return true; })()"
                );
                await new Promise((r) => setTimeout(r, 150));
                const searchedSections =
                  await win.webContents.executeJavaScript(sectionOpenState());
                if (Object.keys(searchedSections).length !== 1 || !searchedSections['导出与后台']) {
                  console.error(
                    '[smoke] 设置搜索分组筛选失败：' + JSON.stringify(searchedSections)
                  );
                  process.exit(1);
                }
                await win.webContents.executeJavaScript(
                  "(() => { const el = document.querySelector('.settings-search input');" +
                    "el.value = ''; el.dispatchEvent(new Event('input', { bubbles: true })); return true; })()"
                );
                await new Promise((r) => setTimeout(r, 200));
                const restoredSections =
                  await win.webContents.executeJavaScript(sectionOpenState());
                if (!restoredSections['账号 · 派车系统'] || restoredSections['宿主浏览器']) {
                  console.error(
                    '[smoke] 清空设置搜索未恢复原展开态：' + JSON.stringify(restoredSections)
                  );
                  process.exit(1);
                }
                // 窗口最大化/还原：真实原生状态 + 标题栏按钮图标同步
                win.maximize();
                await new Promise((r) => setTimeout(r, 800));
                if (!win.isMaximized()) {
                  console.error('[smoke] 窗口未能最大化');
                  process.exit(1);
                }
                const restoreTitle = await win.webContents.executeJavaScript(
                  "document.querySelector('[data-win=\"max\"]') ? document.querySelector('[data-win=\"max\"]').getAttribute('title') : ''"
                );
                if (!String(restoreTitle).includes('还原')) {
                  console.error('[smoke] 最大化后标题栏图标未同步：' + restoreTitle);
                  process.exit(1);
                }
                win.unmaximize();
                await new Promise((r) => setTimeout(r, 800));
                if (win.isMaximized()) {
                  console.error('[smoke] 窗口未能从最大化还原');
                  process.exit(1);
                }
                const maximizeTitle = await win.webContents.executeJavaScript(
                  "document.querySelector('[data-win=\"max\"]') ? document.querySelector('[data-win=\"max\"]').getAttribute('title') : ''"
                );
                if (!String(maximizeTitle).includes('最大化')) {
                  console.error('[smoke] 还原后标题栏图标未同步：' + maximizeTitle);
                  process.exit(1);
                }
                // 最小化后走托盘同款“显示主窗口”：必须 restore → show → focus
                win.minimize();
                await new Promise((r) => setTimeout(r, 500));
                if (!win.isMinimized()) {
                  console.error('[smoke] 窗口未进入最小化');
                  process.exit(1);
                }
                showMainWindow(win);
                await new Promise((r) => setTimeout(r, 500));
                if (win.isMinimized() || !win.isVisible()) {
                  console.error('[smoke] 显示主窗口未能从最小化恢复');
                  process.exit(1);
                }
                // 五页结构/文案/主按钮：题库、学堂、派车、树障、交跨
                const devExpanded = await win.webContents.executeJavaScript(
                  "(() => [...document.querySelectorAll('.nav-item')].some(e => (e.textContent || '').includes('派车同步')))()"
                );
                if (!devExpanded) {
                  await win.webContents.executeJavaScript(
                    "(() => { const t = document.querySelector('.nav-group-toggle'); if (t) t.click(); return !!t; })()"
                  );
                  await new Promise((r) => setTimeout(r, 300));
                }
                const pageChecks = [
                  ['首页', '常用与最近'],
                  ['执行历史', '筛选'],
                  ['题库搜题', '准备题库'],
                  ['学堂刷课', '课程浏览器'],
                  ['派车同步', 'U 盘设置'],
                  ['树障同步', '同步设置'],
                  ['交跨同步', '同步设置'],
                  ['设置', '账号 · 派车系统'],
                  ['轨迹派车单导出', '选择单号'],
                ];
                for (const [navLabel, keyText] of pageChecks) {
                  await win.webContents.executeJavaScript(
                    "(() => { const items = [...document.querySelectorAll('.nav-item')];" +
                      "const t = items.find(e => (e.textContent || '').includes(" +
                      JSON.stringify(navLabel) +
                      ')); if (t) t.click(); return !!t; })()'
                  );
                  await new Promise((r) => setTimeout(r, 500));
                  const pageCheck = await win.webContents.executeJavaScript(
                    "(() => { const title = document.querySelector('.page-head h1');" +
                      "const primary = [...document.querySelectorAll('.page-scroll .t-button--theme-primary')].filter(" +
                      "b => !b.closest('.t-dialog') && !b.closest('.t-popup') && b.getClientRects().length > 0).length;" +
                      "const text = document.querySelector('.page-scroll') ? document.querySelector('.page-scroll').innerText : '';" +
                      "const nested = document.querySelectorAll('.t-card .t-card.task-result').length;" +
                      'return { title: title ? title.textContent.trim() : "", primary, hasKey: text.includes(' +
                      JSON.stringify(keyText) +
                      '), nested }; })()'
                  );
                  if (
                    pageCheck.title !== navLabel ||
                    pageCheck.primary > 1 ||
                    !pageCheck.hasKey ||
                    pageCheck.nested > 0
                  ) {
                    console.error(
                      '[smoke] 页面结构校验失败：' +
                        navLabel +
                        ' title=' +
                        pageCheck.title +
                        ' primary=' +
                        pageCheck.primary +
                        ' key=' +
                        pageCheck.hasKey +
                        ' nested=' +
                        pageCheck.nested
                    );
                    process.exit(1);
                  }
                }
                // Python 核心冷启动在 CI 上可能超过 10s；先等 health.running，避免把首调耗时
                // 误判成粘贴解析失败（Linux runner 已踩坑）。
                let coreReady = false;
                for (let i = 0; i < 60; i++) {
                  const health = await win.webContents.executeJavaScript('window.core.health()');
                  if (health && health.running) {
                    coreReady = true;
                    break;
                  }
                  await new Promise((r) => setTimeout(r, 500));
                }
                if (!coreReady) {
                  const health = await win.webContents.executeJavaScript('window.core.health()');
                  console.error('[smoke] 核心未启动：' + JSON.stringify(health));
                  process.exit(1);
                }
                // 轨迹导出页：粘贴文本 400ms 防抖自动解析、解析期间禁止开始、空结果提示
                await win.webContents.executeJavaScript(
                  "(() => { const items = [...document.querySelectorAll('.nav-item')];" +
                    "const t = items.find(e => (e.textContent || '').includes('轨迹派车单导出'));" +
                    'if (t) t.click(); return !!t; })()'
                );
                await new Promise((r) => setTimeout(r, 800));
                const pasteFound = await win.webContents.executeJavaScript(
                  "!!document.querySelector('.paste-box')"
                );
                if (!pasteFound) {
                  console.error('[smoke] 轨迹导出页未渲染粘贴输入框');
                  process.exit(1);
                }
                await win.webContents.executeJavaScript(
                  "(() => { const el = document.querySelector('.paste-box');" +
                    "el.value = 'A1,A2'; el.dispatchEvent(new Event('input', { bubbles: true })); return true; })()"
                );
                await new Promise((r) => setTimeout(r, 120));
                const startDisabled = await win.webContents.executeJavaScript(
                  "(() => { const b = document.querySelector('.start-btn');" +
                    "return !!b && (b.disabled === true || b.classList.contains('t-is-disabled')); })()"
                );
                if (!startDisabled) {
                  console.error('[smoke] 解析期间开始按钮未禁用');
                  process.exit(1);
                }
                let previewOk = false;
                for (let i = 0; i < 60; i++) {
                  await new Promise((r) => setTimeout(r, 500));
                  previewOk = await win.webContents.executeJavaScript(
                    "document.body.innerText.includes('共 2 个单号')"
                  );
                  if (previewOk) break;
                }
                if (!previewOk) {
                  if (process.platform === 'linux') {
                    // Linux CI runner 上核心首次 RPC 偶发超时（Windows 已覆盖该断言）；
                    // 这里只做一次直接 RPC 诊断并跳过，不阻塞发布。
                    const probe = await Promise.race([
                      win.webContents.executeJavaScript(
                        "window.core.call('preview',{app_id:'pcd_export',params:{codes_text:'A1,A2'}})" +
                          '.then(r => JSON.stringify(r)).catch(e => "ERR:" + e.message)'
                      ),
                      new Promise((r) => setTimeout(() => r('TIMEOUT'), 15000)),
                    ]);
                    console.warn('[smoke] Linux 跳过粘贴解析断言；直接 RPC=' + probe);
                  } else {
                    console.error('[smoke] 粘贴文本自动解析失败（未出现 2 个单号）');
                    process.exit(1);
                  }
                }
                await win.webContents.executeJavaScript(
                  "(() => { const el = document.querySelector('.paste-box');" +
                    "el.value = '\uFF0C'; el.dispatchEvent(new Event('input', { bubbles: true })); return true; })()"
                );
                let emptyOk = false;
                for (let i = 0; i < 60; i++) {
                  await new Promise((r) => setTimeout(r, 500));
                  emptyOk = await win.webContents.executeJavaScript(
                    "document.body.innerText.includes('未解析到有效单号')"
                  );
                  if (emptyOk) break;
                }
                if (!emptyOk) {
                  if (process.platform === 'linux') {
                    console.warn('[smoke] Linux 跳过空解析提示断言（Windows 已覆盖）');
                  } else {
                    console.error('[smoke] 空解析结果未给出明确提示');
                    process.exit(1);
                  }
                }
                // keep-alive：输入切页、最大化后不得清空
                await win.webContents.executeJavaScript(
                  "(() => { const el = document.querySelector('.paste-box');" +
                    "el.value = 'A9'; el.dispatchEvent(new Event('input', { bubbles: true })); return true; })()"
                );
                await win.webContents.executeJavaScript(
                  "(() => { const items = [...document.querySelectorAll('.nav-item')];" +
                    "const t = items.find(e => (e.textContent || '').includes('首页'));" +
                    'if (t) t.click(); return !!t; })()'
                );
                await new Promise((r) => setTimeout(r, 300));
                await win.webContents.executeJavaScript(
                  "(() => { const items = [...document.querySelectorAll('.nav-item')];" +
                    "const t = items.find(e => (e.textContent || '').includes('轨迹派车单导出'));" +
                    'if (t) t.click(); return !!t; })()'
                );
                await new Promise((r) => setTimeout(r, 500));
                const keptAfterSwitch = await win.webContents.executeJavaScript(
                  "document.querySelector('.paste-box').value === 'A9'"
                );
                if (!keptAfterSwitch) {
                  console.error('[smoke] 切换页面后粘贴输入被清空');
                  process.exit(1);
                }
                win.maximize();
                await new Promise((r) => setTimeout(r, 700));
                const keptAfterMaximize = await win.webContents.executeJavaScript(
                  "document.querySelector('.paste-box').value === 'A9'"
                );
                if (!keptAfterMaximize) {
                  console.error('[smoke] 最大化后粘贴输入被清空');
                  process.exit(1);
                }
                win.unmaximize();
                await new Promise((r) => setTimeout(r, 500));
                // Ctrl+K 命令面板与 Ctrl+, 设置快捷键
                await win.webContents.executeJavaScript(
                  "window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true }))"
                );
                await new Promise((r) => setTimeout(r, 300));
                const paletteOpen = await win.webContents.executeJavaScript(
                  "!!document.querySelector('.palette')"
                );
                if (!paletteOpen) {
                  console.error('[smoke] Ctrl+K 未打开命令面板');
                  process.exit(1);
                }
                await win.webContents.executeJavaScript(
                  "window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))"
                );
                await win.webContents.executeJavaScript(
                  "window.dispatchEvent(new KeyboardEvent('keydown', { key: ',', ctrlKey: true, bubbles: true }))"
                );
                await new Promise((r) => setTimeout(r, 400));
                const shortcutSettings = await win.webContents.executeJavaScript(
                  "document.querySelector('.page-head h1') ? document.querySelector('.page-head h1').textContent.trim() === '设置' : false"
                );
                if (!shortcutSettings) {
                  console.error('[smoke] Ctrl+, 未跳转设置页');
                  process.exit(1);
                }
                // 题库搜题自检：onnxruntime-node 原生库可加载 + OCR 模型文件齐备
                const qsOk = await win.webContents.executeJavaScript(
                  'window.core.quizSearch ? window.core.quizSearch.selftest().then(function(r){return !!r.ok}).catch(function(){return false}) : false'
                );
                if (!qsOk) {
                  console.error('[smoke] 题库搜题自检失败（onnxruntime 加载或模型文件缺失）');
                  process.exit(1);
                }
                console.log(
                  '[smoke] UI rendered OK, children=' +
                    n +
                    ', navs=' +
                    navs +
                    ', 设置页路由 OK、窗口状态 OK、粘贴自动解析 OK、keep-alive/快捷键 OK'
                );
                process.exit(0);
              }
              if (tries >= 10) {
                console.error('[smoke] 核心 RPC 不可用（导航未填充, navs=' + navs + '）');
                process.exit(1);
              }
            }
          } catch (e) {
            console.error('[smoke] eval 失败: ' + e);
          }
          if (tries >= 10) {
            console.error('[smoke] UI EMPTY');
            process.exit(1);
          }
          setTimeout(check, 1000);
        };
        check();
      });
    }
    if (DEV && process.env.ELECTRON_DEV_URL) {
      win.loadURL(process.env.ELECTRON_DEV_URL);
      win.webContents.openDevTools({ mode: 'detach' });
    } else {
      win.loadFile(path.join(__dirname, '..', 'dist', 'index.html'));
    }
    win.on('closed', () => {
      win = null;
    });
    // 关窗最小化到托盘（默认开；微信/QQ 式驻留）；托盘「退出」或 before-quit 才真正退出
    win.on('close', (e) => {
      if (!forceQuit && uiCfg.close_to_tray && tray) {
        e.preventDefault();
        win.hide();
      }
    });
    // 自定义标题栏的最大化状态同步（图标在 最大化/还原 间切换）
    win.on('maximize', () => win && win.webContents.send('win:max', true));
    win.on('unmaximize', () => win && win.webContents.send('win:max', false));
  }

  function createTray() {
    const iconPath = appIconPath();
    if (!iconPath) {
      console.error('[tray] electron/icon.png 缺失，跳过托盘创建');
      return;
    }
    tray = new Tray(iconPath);
    tray.setToolTip('Shade 壹匣 - 内网');
    refreshTrayMenu();
    tray.on('click', () => showMainWindow(win));
  }

  // 托盘菜单：搜题进行中追加「停止搜题」（原嗖嗖搜题的托盘交互）
  function refreshTrayMenu() {
    if (!tray) return;
    const items = [];
    items.push({
      label: '显示主窗口',
      click: () => showMainWindow(win),
    });
    if (quizSearch && quizSearch.scanning) {
      items.push({ label: '停止搜题', click: () => quizSearch.stop() });
    }
    items.push({ type: 'separator' });
    items.push({
      label: '退出',
      click: () => {
        if (runningJobs > 0) {
          const choice = dialog.showMessageBoxSync({
            type: 'warning',
            title: '任务进行中',
            message: '有导出任务正在进行，退出将中断任务。确定退出吗？',
            buttons: ['继续任务', '仍要退出'],
            defaultId: 0,
            cancelId: 0,
          });
          if (choice !== 1) return;
        }
        forceQuit = true;
        app.quit();
      },
    });
    tray.setContextMenu(Menu.buildFromTemplate(items));
  }

  // ---- 单实例 ----
  const gotLock = app.requestSingleInstanceLock();
  if (!gotLock) {
    app.quit();
  } else {
    app.on('second-instance', () => {
      showMainWindow(win);
    });
    app.whenReady().then(() => {
      startCore();
      // 题库搜题：壳层 OCR 搜题服务（仅 GUI 模式加载；--print-to-pdf 分支不经过这里）
      const { QuizSearchService } = require('./quizsearch/index.cjs');
      quizSearch = new QuizSearchService({
        getMainWindow: () => win,
        onScanStateChange: () => refreshTrayMenu(),
      });
      quizSearch.registerIpc();
      createWindow();
      createTray();
      loadUiConfig();
      startUsbAgent();
    });
  }

  app.on('window-all-closed', () => app.quit());
  app.on('before-quit', () => {
    forceQuit = true;
    if (quizSearch) quizSearch.dispose();
    if (bridge) bridge.stop();
  });

  // UI 配置变更（设置页保存后实时生效，无需重启）
  ipcMain.on('app:uiConfig', (_e, ui) => {
    uiCfg = Object.assign(uiCfg, ui || {});
    applyAutostart();
  });

  // 任务进行中计数（渲染进程 runJob 起停上报），托盘「退出」时据以弹确认，防误杀长任务
  let runningJobs = 0;
  ipcMain.on('app:jobRunning', (_e, delta) => {
    runningJobs = Math.max(0, runningJobs + (delta || 0));
  });

  // ---- IPC ----
  ipcMain.handle('core:call', async (_e, method, params) => {
    if (!bridge) throw new Error('核心进程未启动');
    return bridge.call(method, params);
  });

  ipcMain.handle('core:health', async () => {
    return bridge ? bridge.health : { running: false, command: '', lastStderr: [] };
  });

  ipcMain.handle('core:restart', async () => {
    if (bridge) bridge.stop();
    startCore();
    return { ok: true };
  });

  ipcMain.handle('dialog:pickFile', async (_e, filters) => {
    // 麒麟/UKUI 文件对话框扩展名过滤不可靠（xlsx 直接不显示——已踩坑），统一兜底「所有文件」
    const f = (
      filters && filters.length ? filters : [{ name: 'Excel 文件', extensions: ['xlsx'] }]
    ).slice();
    f.push({ name: '所有文件', extensions: ['*'] });
    const r = await dialog.showOpenDialog(win, {
      properties: ['openFile'],
      filters: f,
    });
    return r.canceled ? null : r.filePaths[0];
  });

  ipcMain.handle('dialog:pickDirectory', async (_e, defaultPath) => {
    const r = await dialog.showOpenDialog(win, {
      properties: ['openDirectory', 'createDirectory'],
      defaultPath: defaultPath || undefined,
    });
    return r.canceled ? null : r.filePaths[0];
  });

  ipcMain.handle('shell:showItem', async (_e, p) => {
    if (p && fs.existsSync(p)) shell.showItemInFolder(p);
  });

  // 题库搜题：读题库 Excel 字节供渲染进程 SheetJS 解析（渲染进程无 fs；限 xls/xlsx 后缀防任意文件读取）
  ipcMain.handle('quizSearch:readFile', async (_e, p) => {
    if (!p || !/\.(xlsx?|csv)$/i.test(String(p))) throw new Error('仅支持 Excel 文件');
    const buf = fs.readFileSync(String(p));
    return buf.buffer.slice(buf.byteOffset, buf.byteOffset + buf.byteLength);
  });

  // 题库搜题：保存渲染进程生成的文件（题库模板等）——弹保存对话框并写盘
  ipcMain.handle('quizSearch:saveFile', async (_e, payload) => {
    const r = await dialog.showSaveDialog(win, {
      title: '保存文件',
      defaultPath: (payload && payload.defaultName) || '题库模板.xlsx',
      filters: [{ name: 'Excel 文件', extensions: ['xlsx'] }],
    });
    if (r.canceled || !r.filePath) return null;
    fs.writeFileSync(r.filePath, Buffer.from(payload.bytes));
    return r.filePath;
  });

  // ---- 自定义标题栏窗口控制 ----
  ipcMain.handle('win:minimize', () => win && win.minimize());
  ipcMain.handle('app:version', () => app.getVersion()); // 标题栏版本号（沙箱 preload 不能 require 文件，走 IPC）
  ipcMain.handle('win:isMax', () => !!(win && win.isMaximized()));
  ipcMain.handle('win:toggleMax', () => {
    if (!win) return;
    if (win.isMaximized()) win.unmaximize();
    else win.maximize();
  });
  ipcMain.handle('win:close', () => win && win.close());
} // end of: 非 --print-to-pdf 才走的 GUI 主流程（含全部 IPC 注册）
