// 独立浏览器运行时（browser-runtime）：极小 Electron 应用，供 uvmp-toolkit/core-go 的
// internal/browser（课程与 GUI）与 internal/render（导出渲染）驱动。两种模式，由
// --uvmp-browser-mode 选择（默认 export）：
//   1. export 不可见导出渲染——提炼自 desktop/electron/cdp-worker.cjs，协议与之完全兼容
//      （cdp-worker-bridge.json + printToPDF/新窗口回环桥 + DevTools 通告 + 地图域名证书
//      豁免/SPKI pin），render 以本目录为宿主即可照旧工作；窗口对用户不可见；
//   2. course 可见课程模式——持久 profile（--user-data-dir 指向固定目录）、可见窗口、
//      --uvmp-start-url 自动访问、调试端口默认 9222、同 profile 单实例复用（二次启动把
//      已有窗口带到前台后退出）、无地图域名豁免（SPKI pin 仍由 Chromium 原生处理）。
// 两种进入方式（与 cdp-worker.cjs 相同）：
//   1. 应用目录：`electron [开关…] <本目录>`（package.json main 指向本脚本，Go 侧标准口径）；
//   2. 直接脚本（开发/集成测试）：`electron [开关…] <本脚本路径> [about:blank]`。
// 职责：保住远程调试端口 + 独立 user-data-dir；export 开对用户不可见的窗口作为 CDP 首个
// page target（chromedp 根上下文附加其上），course 开可见窗口并自动访问 start_url；
// 窗口全部关闭即退出进程；另提供只听 127.0.0.1 的本地回环桥（printToPDF / 新窗口 /
// 打开网址 / 退出）。
// 安全：远程页面拿不到 Node（sandbox + contextIsolation + nodeIntegration 全关、无 preload）。
//
// 实测（Electron 33.4.11）三个坑及对策（export 渲染链路踩坑记录，与 cdp-worker.cjs 一致）：
// 1. 完全隐藏（show:false / offscreen）的窗口合成器不出帧，Page.captureScreenshot
//    会一直等到超时——export 改为 opacity 0 + 不抢焦点 + 不进任务栏的「不可见窗口」，截图正常；
// 2. Page.printToPDF 在 Electron CDP 里是 dispatcher 级 wasn't found（-32601），
//    连 --headless=new 也不注册——本脚本起只听 127.0.0.1 的回环桥兜底，用
//    webContents.printToPDF 出 PDF，Go 侧遇方法缺失时自动切换，普通 Chromium 不受影响；
// 3. Target.createTarget 不支持（-32000 Not supported）——chromedp 开新 tab 走
//    桥接 POST /new-window 开新窗口，Go 侧等 Target 事件后附加。
const { app, BrowserWindow, webContents, session } = require('electron');
const path = require('path');
const fs = require('fs');
const http = require('http');
const crypto = require('crypto');

// ---------------------------------------------------------------- 模式与参数
// --uvmp-browser-mode=export|course（默认 export）；course 另收 --uvmp-start-url=<url>
// （空=about:blank）。调试端口走 Chromium 原生 --remote-debugging-port：export 缺省 0
// （随机端口，DevToolsActivePort 通告），course 缺省 9222（固定端口，供课程引擎/Playwright
// 经 CDP 复用连接）。
const rawMode = String(app.commandLine.getSwitchValue('uvmp-browser-mode') || '').toLowerCase();
const mode = rawMode === 'course' ? 'course' : 'export';
if (rawMode && rawMode !== 'course' && rawMode !== 'export') {
  console.error('[browser-runtime] 未知模式 "' + rawMode + '"，按 export 处理');
}
// start_url 仅课程模式生效（export 首屏固定 about:blank，页面由 render/Go 侧导航）。
const courseStartURL =
  mode === 'course' ? String(app.commandLine.getSwitchValue('uvmp-start-url') || '') : '';

// 独立 user-data-dir：Go 侧用 --user-data-dir 传入（course 为持久 profile、export 为批次
// 临时目录）；Electron 虽认这个开关，这里再显式钉一次 userData，保证 profile /
// DevToolsActivePort / 回环桥文件不落进默认目录。
const userDataDir = app.commandLine.getSwitchValue('user-data-dir');
if (userDataDir) {
  app.setPath('userData', userDataDir);
}

// 调试端口缺省按模式补齐（必须在 app ready 前生效）。
if (!app.commandLine.getSwitchValue('remote-debugging-port')) {
  app.commandLine.appendSwitch('remote-debugging-port', mode === 'course' ? '9222' : '0');
}

// 窗口尺寸：--window-size=W,H 优先，缺省 1500x950（与 render.DefaultWidth/Height 同值）。
const sizeParts = String(app.commandLine.getSwitchValue('window-size') || '').split(',');
const windowWidth = parseInt(sizeParts[0], 10) > 0 ? parseInt(sizeParts[0], 10) : 1500;
const windowHeight = parseInt(sizeParts[1], 10) > 0 ? parseInt(sizeParts[1], 10) : 950;

// 课程模式：同 profile 单实例复用——Go 侧按 CDP 端口探活复用是正路，这里是并发兜底，
// 二次启动把已有窗口带到前台后退出；export 模式多进程并行（渲染 Worker 各自独立），
// 不加锁。以下全部启动逻辑只在拿到实例锁（或非课程模式）时注册。
const hasInstanceLock = mode !== 'course' || app.requestSingleInstanceLock();

if (!hasInstanceLock) {
  // 已有同 profile 的课程运行时在跑：退出让路，不注册窗口/回环桥，避免双桥双窗口。
  app.quit();
} else {
  if (mode === 'course') {
    app.on('second-instance', () => {
      for (const win of BrowserWindow.getAllWindows()) {
        if (win.isDestroyed()) continue;
        if (win.isMinimized()) win.restore();
        win.show();
        win.focus();
        break;
      }
    });
    // 手工裸跑（无 Go 侧组参）时不弹首次运行/默认浏览器询问；Go 侧同传，重复无害。
    app.commandLine.appendSwitch('no-first-run');
    app.commandLine.appendSwitch('no-default-browser-check');
  } else {
    // Electron 适配：render 包的默认开关按无头 Chromium 配（swiftshader 软渲染），在
    // Electron 里没意义；不可见窗口走 --disable-gpu 即可。course 模式保留默认 GPU 加速。
    app.commandLine.removeSwitch('use-angle');
    app.commandLine.removeSwitch('enable-unsafe-swiftshader');
    app.commandLine.appendSwitch('disable-gpu');
  }

  // 思极地图使用内网私有证书。export 模式未配置 SPKI pin 时，仅这个专用渲染 profile 对
  // 地图域名豁免校验；其他域名仍走 Chromium 默认验证。配置 pin 后由 Chromium 按 pin 校验，
  // 不再使用域名豁免。course 模式无地图豁免（课程域名走默认验证，SPKI pin 仍可配）。
  // 该回调只存在于本运行时进程，不影响桌面壳主窗口或任何其他浏览器。
  function configureCertificatePolicy() {
    if (mode !== 'export') return;
    if (app.commandLine.getSwitchValue('ignore-certificate-errors-spki-list')) return;
    session.defaultSession.setCertificateVerifyProc((request, callback) => {
      const hostname = String(request.hostname || '').toLowerCase();
      callback(hostname === 'map.sgcc.com.cn' ? 0 : -3);
    });
  }

  // ---------------------------------------------------------------- 回环桥
  // Electron CDP 缺失方法与运行时控制走本地回环桥：Go 侧（render 包 / internal/browser）
  // 读 userData 目录的 cdp-worker-bridge.json（port + token）调用本服务；同目录的
  // DevToolsActivePort 是 Chromium 自己的调试端口通告文件，互不干扰。app 退出时删掉桥接文件。
  // 文件名与字段名是 render/bridge.go 的契约，勿改；mode/pid 为附加诊断字段（老解析方忽略）。
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

  // firstPageContents 取第一个可用页面 webContents（/open-url 导航目标）。
  function firstPageContents() {
    for (const wc of webContents.getAllWebContents()) {
      if (!wc.isDestroyed()) return wc;
    }
    return null;
  }

  // openURL 打开网址：已有页面则导航（与受控浏览器「打开启动网址」语义一致），无页面则
  // 按模式开窗。仅允许 http/https/data/about（与 internal/browser 配置校验同口径），
  // javascript: 等一律拒绝。
  function openURL(raw) {
    const url = String(raw || '');
    if (!/^(https?|data|about):/i.test(url)) {
      throw new Error('不支持的网址协议（仅 http/https/data/about）');
    }
    const wc = firstPageContents();
    if (wc) {
      wc.loadURL(url);
      return;
    }
    createPageWindow(url);
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
          // 开一个新窗口（export 不可见 / course 可见），Go 侧等 Target.targetCreated
          // 事件后附加。
          createPageWindow('about:blank');
          res.writeHead(200, { 'content-type': 'application/json' });
          return res.end(JSON.stringify({ ok: true }));
        }
        if (req.url === '/open-url') {
          // internal/browser 课程模式复用已有进程时打开启动网址（内部浏览器导航）。
          let body = '';
          for await (const chunk of req) body += chunk;
          const params = JSON.parse(body || '{}');
          openURL(String(params.url || ''));
          res.writeHead(200, { 'content-type': 'application/json' });
          return res.end(JSON.stringify({ ok: true }));
        }
        if (req.url === '/shutdown') {
          // internal/browser Stop 的优雅退出通道（先应答再退，勿在响应前 quit）。
          res.writeHead(200, { 'content-type': 'application/json' });
          res.end(JSON.stringify({ ok: true }));
          setTimeout(() => app.quit(), 50);
          return;
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
        JSON.stringify({ port, token: bridgeToken, mode, pid: process.pid })
      );
      console.log('[browser-runtime] bridge on 127.0.0.1:' + port + ' (' + mode + ')');
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
  // export：所有窗口（初始窗口 + /new-window 桥接开出的）统一对用户不可见。
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
    if (mode === 'export') hideFromUser(win);
  });

  // createPageWindow 开 CDP page target 窗口：export 不可见（about:blank），course 可见
  // （start_url 或 about:blank）。
  // 必须持有窗口引用：Electron 的 BrowserWindow 若无 JS 引用会被 GC 回收、窗口销毁，
  // 目标会话随之挂死（实测：attach 后不久 eval 永久超时）。
  const pageWindows = new Set();

  function createPageWindow(url) {
    const opts = {
      width: windowWidth,
      height: windowHeight,
      webPreferences: {
        // 注：webPreferences.offscreen 不用——offscreen/纯隐藏窗口不出帧，截图超时（实测）
        nodeIntegration: false,
        contextIsolation: true,
        sandbox: true,
        webviewTag: false,
        backgroundThrottling: false, // 不可见/后台窗口不节渲染（截图与视频等待依赖出帧）
      },
    };
    if (mode === 'export') {
      opts.show = false; // 先不出现在任何地方，hideFromUser 里再 showInactive
      opts.focusable = false;
      opts.skipTaskbar = true;
    }
    const win = new BrowserWindow(opts);
    pageWindows.add(win);
    win.on('closed', () => pageWindows.delete(win));
    if (mode === 'export') hideFromUser(win);
    win.loadURL(url || 'about:blank');
    return win;
  }

  app.whenReady().then(() => {
    configureCertificatePolicy();
    announceDevToolsLine();
    startBridge();

    const win = createPageWindow(courseStartURL || 'about:blank');
    // 窗口关闭即退出（不留在后台）
    win.on('closed', () => app.quit());
  });

  // 兜底：所有窗口（含桥接开出的）都关了也退出
  app.on('window-all-closed', () => app.quit());

  app.on('will-quit', () => {
    try {
      fs.unlinkSync(path.join(app.getPath('userData'), BRIDGE_FILE));
    } catch {
      /* 已删/不存在忽略 */
    }
  });
}
