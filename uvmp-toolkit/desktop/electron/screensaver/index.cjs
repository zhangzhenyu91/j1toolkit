// 机巡审核实时大屏：全屏屏保，按实例展示日志/进度；点击卡片切到真实浏览器。
// 不再做 CDP 画面转播；仅保留 Page.bringToFront 聚焦浏览器 + 返回浮窗。
const path = require('path');

// 依赖注入（deps）：默认取 electron / node 原生模块；测试注入假实现以脱离 GUI。
// 不在模块顶层 require('electron')，保证纯 node 下可加载（回归测试）。
// 也只在真正需要某个依赖时才 require，避免测试环境缺原生模块时整模块加载失败。
function electronDep(name) {
  return require('electron')[name];
}

// 大屏只允许聚焦本机回环 CDP：与核心 toolkit/browser_automation.validate_cdp_url 同口径。
const LOOPBACK_HOSTS = new Set(['127.0.0.1', 'localhost', '::1']);
const CDP_PATH_ALLOWED = new Set(['', '/']);
// CDP /json 列表响应上限与请求超时（CDP 端点异常/被占位时不能挂住主进程）
const DEFAULT_CDP_TIMEOUT_MS = 4000;
const DEFAULT_CDP_MAX_BYTES = 2 * 1024 * 1024;
const DEFAULT_CDP_WS_TIMEOUT_MS = 3000;
const DEFAULT_WS_CLOSE_DELAY_MS = 800;

function hostKey(hostname) {
  return String(hostname || '')
    .replace(/^\[/, '')
    .replace(/\]$/, '');
}

function isLoopbackHost(hostname) {
  return LOOPBACK_HOSTS.has(hostKey(hostname).toLowerCase());
}

/** CDP 基地址校验：仅 http + 回环 + 显式端口 + 无凭据/路径/查询。 */
function isLoopbackCdpUrl(raw) {
  let url;
  try {
    url = new URL(String(raw == null ? '' : raw).trim());
  } catch {
    return false;
  }
  if (url.protocol !== 'http:') return false;
  if (url.username || url.password) return false;
  if (!isLoopbackHost(url.hostname)) return false;
  if (!CDP_PATH_ALLOWED.has(url.pathname)) return false;
  if (url.search || url.hash) return false;
  return !!url.port;
}

/**
 * 从 /json 列表里挑目标页：只要 page 类型、且 WebSocket 调试地址与基地址同源回环。
 * 防止实例参数把聚焦请求导向其它主机（SSRF / 反向代理注入）。
 */
function pickPageTarget(pages, base) {
  if (!Array.isArray(pages)) return null;
  let baseUrl;
  try {
    baseUrl = base instanceof URL ? base : new URL(String(base || ''));
  } catch {
    return null;
  }
  for (const item of pages) {
    if (!item || item.type !== 'page') continue;
    if (/^(devtools|edge|chrome|about|chrome-extension):/i.test(String(item.url || ''))) continue;
    let ws;
    try {
      ws = new URL(String(item.webSocketDebuggerUrl || ''));
    } catch {
      continue;
    }
    if (ws.protocol !== 'ws:' && ws.protocol !== 'wss:') continue;
    if (!isLoopbackHost(ws.hostname)) continue;
    if (hostKey(ws.hostname).toLowerCase() !== hostKey(baseUrl.hostname).toLowerCase()) continue;
    if ((ws.port || '') !== (baseUrl.port || '')) continue;
    return item;
  }
  return null;
}

class ScreensaverService {
  constructor({
    getMainWindow,
    deps = {},
    cdpTimeoutMs,
    cdpMaxBytes,
    cdpWsTimeoutMs,
    closeWsDelayMs,
  } = {}) {
    this.getMainWindow = getMainWindow || (() => null);
    this.deps = deps;
    this.win = null;
    this.returnBar = null;
    this.hiddenForBrowser = false;
    this.focusedId = null;
    this.instances = [];
    this.ipcRegistered = false;
    this.cdpTimeoutMs = cdpTimeoutMs || DEFAULT_CDP_TIMEOUT_MS;
    this.cdpMaxBytes = cdpMaxBytes || DEFAULT_CDP_MAX_BYTES;
    this.cdpWsTimeoutMs = cdpWsTimeoutMs || DEFAULT_CDP_WS_TIMEOUT_MS;
    this.closeWsDelayMs = closeWsDelayMs === undefined ? DEFAULT_WS_CLOSE_DELAY_MS : closeWsDelayMs;
  }

  _dep(name) {
    if (this.deps && this.deps[name]) return this.deps[name];
    if (name === 'http' || name === 'https') return require(name);
    if (name === 'WebSocket') return require('ws');
    return electronDep(name);
  }

  // IPC 来源校验：只接受本服务窗口或主窗口发来的消息（渲染层不可跨窗口伪造）。
  _isTrustedSender(event) {
    const sender = event && event.sender;
    if (!sender) return false;
    const allowed = [];
    for (const target of [this.win, this.returnBar]) {
      if (target && !target.isDestroyed()) allowed.push(target.webContents);
    }
    const main = this.getMainWindow && this.getMainWindow();
    if (main && !main.isDestroyed()) allowed.push(main.webContents);
    return allowed.includes(sender);
  }

  // 包成 async：来源非法时以 reject 形式回给渲染层（ipcMain.handle 语义），不抛到事件循环。
  _guard(fn) {
    return async (event, ...args) => {
      if (!this._isTrustedSender(event)) throw new Error('非法 IPC 来源');
      return fn(...args);
    };
  }

  registerIpc() {
    if (this.ipcRegistered) return;
    this.ipcRegistered = true;
    const ipcMain = this._dep('ipcMain');
    ipcMain.handle(
      'screensaver:open',
      this._guard((instances) => this.open(instances))
    );
    ipcMain.handle(
      'screensaver:update',
      this._guard((instances) => this.update(instances))
    );
    ipcMain.handle(
      'screensaver:close',
      this._guard(() => this.close())
    );
    ipcMain.handle(
      'screensaver:getState',
      this._guard(() => this.state())
    );
    ipcMain.handle(
      'screensaver:focus',
      this._guard((id) => this.focusInstance(id))
    );
    ipcMain.handle(
      'screensaver:return',
      this._guard(() => this.returnToScreensaver())
    );
    ipcMain.handle(
      'screensaver:stopAllNow',
      this._guard(() => this.stopAllNow())
    );
    ipcMain.handle(
      'screensaver:closeNow',
      this._guard(() => this.close())
    );
    ipcMain.handle(
      'screensaver:ready',
      this._guard(() => {
        this._sendUpdate();
        return { instances: this.instances };
      })
    );
  }

  _target() {
    const app = this._dep('app');
    if (!app.isPackaged && process.env.ELECTRON_DEV_URL) {
      return {
        url: process.env.ELECTRON_DEV_URL.replace(/\/$/, '') + '/screensaver.html',
      };
    }
    return { file: path.join(__dirname, '..', '..', 'dist', 'screensaver.html') };
  }

  _createWindow() {
    const BrowserWindow = this._dep('BrowserWindow');
    const screen = this._dep('screen');
    const display = screen.getPrimaryDisplay();
    const bounds = display.bounds;
    const win = new BrowserWindow({
      x: bounds.x,
      y: bounds.y,
      width: bounds.width,
      height: bounds.height,
      frame: false,
      fullscreen: true,
      show: false,
      backgroundColor: '#0A3592',
      skipTaskbar: true,
      alwaysOnTop: true,
      resizable: false,
      minimizable: false,
      maximizable: false,
      webPreferences: {
        preload: path.join(__dirname, 'preload.cjs'),
        contextIsolation: true,
        nodeIntegration: false,
        backgroundThrottling: false,
      },
    });
    win.setAlwaysOnTop(true, 'screen-saver');
    try {
      win.setVisibleOnAllWorkspaces(true, { visibleOnFullScreen: true });
    } catch (_e) {}
    win.setMenuBarVisibility(false);
    const target = this._target();
    if (target.url) win.loadURL(target.url);
    else win.loadFile(target.file);
    win.once('ready-to-show', () => {
      win.show();
      win.focus();
    });
    // 旧窗被替换后其 closed 才到达：此时 this.win 已指向新窗，不得误报「大屏已关闭」
    win.on('closed', () => {
      if (this.win !== win) return;
      this.win = null;
      this.hiddenForBrowser = false;
      this.focusedId = null;
      this._emitState(false);
    });
    this.win = win;
    this._emitState(true);
    win.webContents.on('did-finish-load', () => this._sendUpdate());
  }

  open(instances) {
    this.instances = Array.isArray(instances) ? instances : [];
    if (!this.win || this.win.isDestroyed()) this._createWindow();
    else if (this.hiddenForBrowser) return this.returnToScreensaver();
    else {
      this.win.show();
      this.win.focus();
      this._emitState(true);
    }
    this._sendUpdate();
    return this.state();
  }

  update(instances) {
    this.instances = Array.isArray(instances) ? instances : [];
    this._sendUpdate();
    this._sendReturnBar();
    return this.state();
  }

  close() {
    const win = this.win;
    this.win = null;
    this.hiddenForBrowser = false;
    this.focusedId = null;
    this._closeReturnBar();
    if (win && !win.isDestroyed()) win.close();
    this._emitState(false);
    return { open: false };
  }

  _createReturnBar() {
    if (this.returnBar && !this.returnBar.isDestroyed()) {
      this.returnBar.show();
      this._sendReturnBar();
      return;
    }
    const BrowserWindow = this._dep('BrowserWindow');
    const screen = this._dep('screen');
    const wa = screen.getPrimaryDisplay().workArea;
    const width = 460;
    const height = 300;
    const bar = new BrowserWindow({
      x: wa.x + wa.width - width - 18,
      y: wa.y + wa.height - height - 18,
      width,
      height,
      frame: false,
      show: false,
      alwaysOnTop: true,
      skipTaskbar: true,
      resizable: false,
      minimizable: false,
      maximizable: false,
      backgroundColor: '#0A3592',
      webPreferences: {
        preload: path.join(__dirname, 'returnbar-preload.cjs'),
        contextIsolation: true,
        nodeIntegration: false,
        backgroundThrottling: false,
      },
    });
    bar.setAlwaysOnTop(true, 'screen-saver');
    bar.setMenuBarVisibility(false);
    bar.loadFile(path.join(__dirname, 'returnbar.html'));
    bar.webContents.on('did-finish-load', () => this._sendReturnBar());
    bar.once('ready-to-show', () => bar.show());
    bar.on('closed', () => {
      if (this.returnBar === bar) this.returnBar = null;
    });
    this.returnBar = bar;
  }

  _closeReturnBar() {
    const bar = this.returnBar;
    this.returnBar = null;
    if (bar && !bar.isDestroyed()) bar.close();
  }

  returnToScreensaver() {
    this.hiddenForBrowser = false;
    this.focusedId = null;
    this._closeReturnBar();
    if (!this.win || this.win.isDestroyed()) return { open: false };
    this.win.show();
    this.win.focus();
    this._sendUpdate();
    this._emitState(true);
    return { open: true };
  }

  focusInstance(id) {
    const item = (this.instances || []).find((entry) => entry && entry.id === id);
    if (!item || !this.win || this.win.isDestroyed()) {
      return { open: false, focused: false };
    }
    // 没有可用浏览器（或地址非本机回环）时不隐藏大屏：否则用户只能对着返回浮窗，
    // 却看不到任何可切换的浏览器窗口。
    if (!isLoopbackCdpUrl(item.cdp_url)) {
      return { open: true, hidden: !!this.hiddenForBrowser, focused: false };
    }
    // 隐藏屏保，直接看真实浏览器；右下角保留返回按钮和当前实例日志。
    this.hiddenForBrowser = true;
    this.focusedId = id;
    this.win.hide();
    this._createReturnBar();
    // 聚焦是尽力而为：失败只记日志，不能让大屏陷入不可返回状态
    Promise.resolve(this._focusBrowser(item.cdp_url)).catch(() => {});
    return { open: true, hidden: true, focused: true };
  }

  /** 读取 CDP /json：真实超时（含 socket 挂死）+ 响应体上限 + 凭据/重定向隔离。 */
  _fetchPageList(baseUrl) {
    const http = this._dep('http');
    const https = this._dep('https');
    const mod = baseUrl.protocol === 'https:' ? https : http;
    const target = new URL('/json', baseUrl.origin);
    return new Promise((resolve, reject) => {
      let settled = false;
      const done = (err, value) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        if (err) reject(err);
        else resolve(value);
      };
      const req = mod.get(
        target,
        { headers: { accept: 'application/json' }, timeout: this.cdpTimeoutMs },
        (res) => {
          if (res.statusCode !== 200) {
            res.resume();
            done(new Error('CDP /json 状态码 ' + res.statusCode));
            return;
          }
          const chunks = [];
          let size = 0;
          res.on('data', (chunk) => {
            size += chunk.length;
            if (size > this.cdpMaxBytes) {
              req.destroy(new Error('CDP /json 响应超过上限'));
              return;
            }
            chunks.push(chunk);
          });
          res.on('end', () => {
            try {
              done(null, JSON.parse(Buffer.concat(chunks).toString('utf8') || '[]'));
            } catch (e) {
              done(new Error('CDP /json 解析失败: ' + (e && e.message ? e.message : e)));
            }
          });
          res.on('error', (e) => done(e));
        }
      );
      const timer = setTimeout(() => {
        try {
          req.destroy(new Error('CDP /json 请求超时'));
        } catch (_e) {}
      }, this.cdpTimeoutMs);
      if (timer.unref) timer.unref();
      req.on('timeout', () => {
        try {
          req.destroy(new Error('CDP /json 请求超时'));
        } catch (_e) {}
      });
      req.on('error', (e) => done(e));
    });
  }

  /** 打开调试 WS 并只发 Page.bringToFront：握手超时即断开，绝不无限等待。 */
  _bringToFront(wsUrl) {
    const WS = (this.deps && this.deps.WebSocket) || require('ws');
    return new Promise((resolve) => {
      let ws;
      let closed = false;
      let timer = null;
      const finish = () => {
        if (closed) return;
        closed = true;
        if (timer) clearTimeout(timer);
        try {
          ws.close();
        } catch (_e) {}
        resolve();
      };
      try {
        ws = new WS(wsUrl, { handshakeTimeout: this.cdpWsTimeoutMs });
      } catch (_e) {
        resolve();
        return;
      }
      timer = setTimeout(() => {
        try {
          ws.terminate();
        } catch (_e) {}
        if (!closed) {
          closed = true;
          resolve();
        }
      }, this.cdpWsTimeoutMs);
      if (timer.unref) timer.unref();
      ws.on('open', () => {
        try {
          ws.send(JSON.stringify({ id: 1, method: 'Page.bringToFront' }));
        } catch (_e) {}
        setTimeout(finish, this.closeWsDelayMs);
      });
      ws.on('error', () => {
        try {
          ws.terminate();
        } catch (_e) {}
        if (!closed) {
          closed = true;
          if (timer) clearTimeout(timer);
          resolve();
        }
      });
    });
  }

  async _focusBrowser(cdpUrl) {
    const raw = String(cdpUrl || '').trim();
    if (!isLoopbackCdpUrl(raw)) {
      console.warn('[screensaver] 忽略非回环 CDP 地址: ' + raw);
      return { focused: false };
    }
    const base = new URL(raw);
    let pages;
    try {
      pages = await this._fetchPageList(base);
    } catch (e) {
      console.warn('[screensaver] 读取 CDP 页面列表失败: ' + (e && e.message ? e.message : e));
      return { focused: false };
    }
    const target = pickPageTarget(pages, base);
    if (!target) return { focused: false };
    await this._bringToFront(target.webSocketDebuggerUrl);
    return { focused: true };
  }

  stopAllNow() {
    const main = this.getMainWindow && this.getMainWindow();
    if (main && !main.isDestroyed()) {
      main.webContents.send('screensaver:stopAll');
    }
    return { stopped: true };
  }

  state() {
    return {
      open: !!(this.win && !this.win.isDestroyed()),
      hidden: !!this.hiddenForBrowser,
    };
  }

  _emitState(open) {
    const main = this.getMainWindow && this.getMainWindow();
    if (main && !main.isDestroyed()) {
      main.webContents.send('screensaver:state', !!open);
    }
  }

  _sendReturnBar() {
    const bar = this.returnBar;
    if (!bar || bar.isDestroyed()) return;
    const instance = (this.instances || []).find((entry) => entry.id === this.focusedId);
    bar.webContents.send('returnbar:update', {
      instance: instance || null,
      focusedId: this.focusedId || '',
    });
  }

  _sendUpdate() {
    const win = this.win;
    if (!win || win.isDestroyed()) return;
    win.webContents.send('screensaver:update', { instances: this.instances });
  }
}

module.exports = { ScreensaverService, isLoopbackCdpUrl, pickPageTarget, isLoopbackHost };
