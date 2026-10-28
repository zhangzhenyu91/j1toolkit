// CDP 渲染 Worker：独立 Electron 入口，供 uvmp-toolkit/core-go 的 render 包以
// `electron <Chromium 开关…> <本脚本> about:blank` 直接启动，不进入主窗口/单实例/托盘。
// 职责：保住远程调试端口 + 独立 user-data-dir，开一个对用户不可见的 BrowserWindow
// 作为 CDP 首个 page target（chromedp 根上下文附加其上），窗口关闭即退出进程；
// 另提供本地 printToPDF 兜底（Electron 的 CDP 不实现 Page.printToPDF）。
// 安全：远程页面拿不到 Node（sandbox + contextIsolation + nodeIntegration 全关、无 preload）。
//
// 实测（Electron 33.4.11）三个坑及对策：
// 1. 完全隐藏（show:false / offscreen）的窗口合成器不出帧，Page.captureScreenshot
//    会一直等到超时——改为 opacity 0 + 不抢焦点 + 不进任务栏的「不可见窗口」，截图正常；
// 2. Page.printToPDF 在 Electron CDP 里是 dispatcher 级 wasn't found（-32601），
//    连 --headless=new 也不注册——本脚本起只听 127.0.0.1 的 HTTP 兜底，用
//    webContents.printToPDF 出 PDF，Go 侧遇方法缺失时自动切换，普通 Chromium 不受影响；
// 3. Target.createTarget 不支持（-32000 Not supported）——chromedp 开新 tab 走
//    桥接 POST /new-window 开新窗口，Go 侧等 Target 事件后附加。
const { app, BrowserWindow, webContents } = require('electron');
const path = require('path');
const fs = require('fs');
const http = require('http');
const crypto = require('crypto');

// 独立 user-data-dir：Go 侧用 --user-data-dir 传入；Electron 虽认这个开关，这里再
// 显式钉一次 userData，保证 profile/DevToolsActivePort 不落进默认目录。
const userDataDir = app.commandLine.getSwitchValue('user-data-dir');
if (userDataDir) {
  app.setPath('userData', userDataDir);
}

// Electron 适配：render 包的默认开关按无头 Chromium 配（swiftshader 软渲染），在
// Electron 里没意义；隐藏窗口走 --disable-gpu 即可（与 main.cjs --print-to-pdf 同款）。
app.commandLine.removeSwitch('use-angle');
app.commandLine.removeSwitch('enable-unsafe-swiftshader');
app.commandLine.appendSwitch('disable-gpu');

// ---------------------------------------------------------------- printToPDF 兜底
// Go 侧（render 包）遇 Page.printToPDF 方法缺失时读 userData 目录的
// cdp-worker-bridge.json（port + token）走本服务；同目录的 DevToolsActivePort 是
// Chromium 自己的调试端口通告文件，互不干扰。app 退出时删掉桥接文件。
const BRIDGE_FILE = 'cdp-worker-bridge.json';
const bridgeToken = crypto.randomBytes(16).toString('hex');

// findWebContentsByMarker 按 Go 侧写进页面的 window.__cdpWorkerPdfMarker 找 webContents。
async function findWebContentsByMarker(marker) {
  for (const wc of webContents.getAllWebContents()) {
    if (wc.isDestroyed()) continue;
    try {
      const v = await wc.executeJavaScript('window.__cdpWorkerPdfMarker', true);
      if (v === marker) return wc;
    } catch {
      /* 崩溃/无页面的 webContents 忽略 */
    }
  }
  return null;
}

function startBridge() {
  const server = http.createServer(async (req, res) => {
    try {
      if (req.method !== 'POST') {
        res.writeHead(404);
        return res.end();
      }
      if (req.headers['x-cdp-worker-token'] !== bridgeToken) {
        res.writeHead(403);
        return res.end();
      }
      if (req.url === '/new-window') {
        // chromedp newTab 的 Electron 替代（Target.createTarget 不支持，实测）：
        // 开一个对用户不可见的新窗口，Go 侧等 Target.targetCreated 事件后附加。
        createWorkerWindow();
        res.writeHead(200, { 'content-type': 'application/json' });
        return res.end(JSON.stringify({ ok: true }));
      }
      if (req.url !== '/print-to-pdf') {
        res.writeHead(404);
        return res.end();
      }
      let body = '';
      for await (const chunk of req) body += chunk;
      const params = JSON.parse(body || '{}');
      const wc = await findWebContentsByMarker(String(params.marker || ''));
      if (!wc) {
        res.writeHead(404);
        return res.end('marker not found');
      }
      // 参数与 Go tabPrintPDF 的 Page.printToPDF 口径一致（printBackground + preferCSSPageSize）
      const buf = await wc.printToPDF({
        printBackground: !!params.printBackground,
        preferCSSPageSize: !!params.preferCSSPageSize,
        landscape: !!params.landscape,
        scale: Number(params.scale) > 0 ? Number(params.scale) : 1,
        pageRanges: String(params.pageRanges || ''),
      });
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ data: Buffer.from(buf).toString('base64') }));
    } catch (e) {
      res.writeHead(500);
      res.end(String((e && e.message) || e));
    }
  });
  server.listen(0, '127.0.0.1', () => {
    const { port } = server.address();
    fs.writeFileSync(
      path.join(app.getPath('userData'), BRIDGE_FILE),
      JSON.stringify({ port, token: bridgeToken }),
    );
    console.log('[cdp-worker] printToPDF bridge on 127.0.0.1:' + port);
  });
  return server;
}

// ---------------------------------------------------------------- 调试端口通告兜底
// chromedp 靠 stdout 的 `DevTools listening on ws://…` 行发现调试端口。Electron 一般
// 会自己打印，但不保证所有构建/平台都打；这里读 DevToolsActivePort（首行端口、次行
// browser 路径）补打同样一行，两条相同 URL 先到先得、互不影响。
function announceDevToolsLine() {
  const portFile = path.join(app.getPath('userData'), 'DevToolsActivePort');
  const deadline = Date.now() + 20000;
  const timer = setInterval(() => {
    let text;
    try {
      text = fs.readFileSync(portFile, 'utf8');
    } catch {
      if (Date.now() > deadline) clearInterval(timer);
      return;
    }
    const [port, wsPath] = text.split(/\r?\n/);
    clearInterval(timer);
    if (!port || !wsPath) return;
    // 稍等片刻，让 Electron 自带的那行先出（同 URL，重复无害）
    setTimeout(() => {
      console.log(`DevTools listening on ws://127.0.0.1:${port}${wsPath}`);
    }, 300);
  }, 200);
}

// ---------------------------------------------------------------- 窗口
// 所有窗口（初始窗口 + /new-window 桥接开出的）统一对用户不可见。
function hideFromUser(win) {
  win.setOpacity(0); // 透明：屏幕上看不见，但合成器照常出帧（截图依赖出帧）
  win.setSkipTaskbar(true);
  try {
    win.setFocusable(false); // 不抢焦点
  } catch {
    /* 平台不支持时忽略 */
  }
  win.showInactive(); // 出帧需要窗口处于 shown 状态，但不激活
}

app.on('browser-window-created', (_event, win) => {
  hideFromUser(win);
});

// createWorkerWindow 开一个不可见的 CDP page target 窗口（about:blank）。
// 必须持有窗口引用：Electron 的 BrowserWindow 若无 JS 引用会被 GC 回收、窗口销毁，
// 目标会话随之挂死（实测：attach 后不久 eval 永久超时）。
const workerWindows = new Set();

function createWorkerWindow() {
  const win = new BrowserWindow({
    show: false, // 先不出现在任何地方，hideFromUser 里再 showInactive
    focusable: false,
    width: 1500,
    height: 950,
    skipTaskbar: true,
    webPreferences: {
      // 注：webPreferences.offscreen 不用——offscreen/纯隐藏窗口不出帧，截图超时（实测）
      nodeIntegration: false,
      contextIsolation: true,
      sandbox: true,
      webviewTag: false,
      backgroundThrottling: false, // 不可见窗口不节渲染
    },
  });
  workerWindows.add(win);
  win.on('closed', () => workerWindows.delete(win));
  hideFromUser(win);
  win.loadURL('about:blank');
  return win;
}

app.whenReady().then(() => {
  announceDevToolsLine();
  startBridge();

  const win = createWorkerWindow();
  // 窗口关闭即退出（不留在后台）
  win.on('closed', () => app.quit());
});

// 兜底：所有窗口（含 CDP 开出的）都关了也退出
app.on('window-all-closed', () => app.quit());

app.on('will-quit', () => {
  try {
    fs.unlinkSync(path.join(app.getPath('userData'), BRIDGE_FILE));
  } catch {
    /* 已删/不存在忽略 */
  }
});
