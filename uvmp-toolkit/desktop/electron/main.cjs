// 内网工具箱 Electron 主进程：窗口/托盘/单实例/核心进程生命周期/原生对话框。
const { app, BrowserWindow, Tray, Menu, ipcMain, dialog, shell } = require('electron');
const path = require('path');
const fs = require('fs');
const { CoreBridge, prodCoreCommand } = require('./core.cjs');

const DEV = !app.isPackaged;

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
      show: false, width: 1200, height: 850,
      webPreferences: { contextIsolation: true, nodeIntegration: false, webSecurity: false },
    });
    const finish = (code, msg) => {
      if (msg) console.error(msg);
      process.exit(code);
    };
    try {
      win.webContents.on('did-fail-load', (_e, code, desc) =>
        finish(1, '[print] did-fail-load: ' + desc));
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
        preferCSSPageSize: true, printBackground: true,
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
let quizSearch = null;   // 题库搜题服务（壳层功能，见 electron/quizsearch/）
let uiCfg = { close_to_tray: true, autostart: false };

// 客户端 UI 配置（关窗驻留/开机自启），存于核心 config.json 的 ui.*（与核心共享一份配置）
function loadUiConfig() {
  if (!bridge) return;
  bridge.call('getConfig').then((cfg) => {
    uiCfg = Object.assign({ close_to_tray: true, autostart: false }, (cfg || {}).ui || {});
    applyAutostart();
  }).catch(() => {});
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
        fs.writeFileSync(f, '[Desktop Entry]\nType=Application\nName=Shade 壹匣 - 内网\n'
          + 'Exec=' + process.execPath + '\nX-GNOME-Autostart-enabled=true\n');
      } else if (fs.existsSync(f)) {
        fs.unlinkSync(f);
      }
    }
  } catch (e) {
    console.error('autostart 设置失败', e);
  }
}

// 客户端驻留调度：每分钟问一次核心「到点了吗」（driver=app 时核心才执行，见 scheduler.due_check 门闸）
function startInAppScheduler() {
  const tick = () => {
    if (!bridge) return;
    bridge.call('maybeRunDaily').then((r) => {
      if (r && r.started) console.log('[驻留调度] 到点执行每日导出，job=' + r.job_id);
    }).catch(() => {});
  };
  setTimeout(tick, 10000);   // 启动 10s 后先查一次（补跑昨夜关机错过的）
  setInterval(tick, 60000);
}

function coreCommand() {
  if (process.env.UVMP_CORE) return process.env.UVMP_CORE;   // 显式指定（调试）
  if (!DEV) return prodCoreCommand(process.resourcesPath);   // 生产：resources/core/
  return null;                                                // 开发：python 源码
}

function startCore() {
  bridge = new CoreBridge({
    command: coreCommand(),
    devCwd: path.resolve(__dirname, '..', '..'),
    env: { UVMP_ELECTRON: process.execPath },   // 核心用应用本体做离线 PDF 渲染
    onEvent: (payload) => {
      if (win && !win.isDestroyed()) win.webContents.send('core:event', payload);
    },
    onExit: (code) => {
      if (win && !win.isDestroyed()) win.webContents.send('core:event', { event: 'core.exit', code });
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
    icon: path.join(__dirname, 'icon.png'),
    backgroundColor: '#F5F7FA',   // 政企蓝白页面底，避免白闪
    show: false,
    frame: false,                 // 无边框：自定义标题栏（微信/QQ 式），窗口控制走 win:* IPC
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
      console.log('[renderer]', args.slice(1).join(' ')));
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
            "document.querySelector('#app') ? document.querySelector('#app').childElementCount : 0");
          if (n > 0) {
            // 再等核心 RPC：导航项应由 getApps 填充
            const navs = await win.webContents.executeJavaScript(
              "document.querySelectorAll('.nav-item').length");
            if (navs >= 4) {
              // 再点一下「设置」导航验证路由表（已踩坑：PAGE_MAP 缺项导致设置跳首页）
              await win.webContents.executeJavaScript(
                "(() => { const items = [...document.querySelectorAll('.nav-item')];"
                + "const t = items.find(e => (e.textContent || '').includes('设置'));"
                + "if (t) t.click(); return !!t; })()");
              await new Promise((r) => setTimeout(r, 1200));
              const settingsOk = await win.webContents.executeJavaScript(
                "document.body.innerText.includes('派车系统 SSO 凭据')");
              if (!settingsOk) {
                console.error('[smoke] 设置页路由失败（未渲染 SSO 凭据卡片）');
                process.exit(1);
              }
              // 题库搜题自检：onnxruntime-node 原生库可加载 + OCR 模型文件齐备
              const qsOk = await win.webContents.executeJavaScript(
                "window.core.quizSearch ? window.core.quizSearch.selftest().then(function(r){return !!r.ok}).catch(function(){return false}) : false");
              if (!qsOk) {
                console.error('[smoke] 题库搜题自检失败（onnxruntime 加载或模型文件缺失）');
                process.exit(1);
              }
              console.log('[smoke] UI rendered OK, children=' + n + ', navs=' + navs
                          + ', 设置页路由 OK');
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
  win.on('closed', () => { win = null; });
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
  const iconPath = path.join(__dirname, 'icon.png');
  if (!fs.existsSync(iconPath)) return;
  tray = new Tray(iconPath);
  tray.setToolTip('Shade 壹匣 - 内网');
  refreshTrayMenu();
  tray.on('click', () => { if (win) { win.show(); win.focus(); } });
}

// 托盘菜单：搜题进行中追加「停止搜题」（原嗖嗖搜题的托盘交互）
function refreshTrayMenu() {
  if (!tray) return;
  const items = [];
  items.push({ label: '显示主窗口', click: () => { if (win) { win.show(); win.focus(); } } });
  if (quizSearch && quizSearch.scanning) {
    items.push({ label: '停止搜题', click: () => quizSearch.stop() });
  }
  items.push({ type: 'separator' });
  items.push({ label: '退出', click: () => {
    if (runningJobs > 0) {
      const choice = dialog.showMessageBoxSync({
        type: 'warning', title: '任务进行中',
        message: '有导出任务正在进行，退出将中断任务。确定退出吗？',
        buttons: ['继续任务', '仍要退出'], defaultId: 0, cancelId: 0,
      });
      if (choice !== 1) return;
    }
    forceQuit = true; app.quit();
  } });
  tray.setContextMenu(Menu.buildFromTemplate(items));
}

// ---- 单实例 ----
const gotLock = app.requestSingleInstanceLock();
if (!gotLock) {
  app.quit();
} else {
  app.on('second-instance', () => {
    if (win) {
      if (!win.isVisible()) win.show();
      win.focus();
    }
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
    startInAppScheduler();
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
  const f = (filters && filters.length ? filters : [{ name: 'Excel 文件', extensions: ['xlsx'] }]).slice();
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
ipcMain.handle('app:version', () => app.getVersion());   // 标题栏版本号（沙箱 preload 不能 require 文件，走 IPC）
ipcMain.handle('win:toggleMax', () => {
  if (!win) return;
  if (win.isMaximized()) win.unmaximize();
  else win.maximize();
});
ipcMain.handle('win:close', () => win && win.close());

}  // end of: 非 --print-to-pdf 才走的 GUI 主流程（含全部 IPC 注册）
