// 机巡审核实时大屏：全屏屏保，按实例展示日志/进度；点击卡片切到真实浏览器。
// 不再做 CDP 画面转播；仅保留 Page.bringToFront 聚焦浏览器 + 返回浮窗。
const { BrowserWindow, app, dialog, ipcMain, screen } = require('electron');
const path = require('path');
const http = require('http');
const WebSocket = require('ws');

class ScreensaverService {
  constructor({ getMainWindow } = {}) {
    this.getMainWindow = getMainWindow || (() => null);
    this.win = null;
    this.returnBar = null;
    this.hiddenForBrowser = false;
    this.focusedId = null;
    this.instances = [];
    this.ipcRegistered = false;
  }

  registerIpc() {
    if (this.ipcRegistered) return;
    this.ipcRegistered = true;
    ipcMain.handle('screensaver:open', (_e, instances) => this.open(instances));
    ipcMain.handle('screensaver:update', (_e, instances) => this.update(instances));
    ipcMain.handle('screensaver:close', () => this.close());
    ipcMain.handle('screensaver:getState', () => this.state());
    ipcMain.handle('screensaver:requestClose', () => this.confirmClose());
    ipcMain.handle('screensaver:focus', (_e, id) => this.focusInstance(id));
    ipcMain.handle('screensaver:return', () => this.returnToScreensaver());
    ipcMain.handle('screensaver:stopAll', () => this.confirmStopAll());
    ipcMain.handle('screensaver:ready', () => {
      this._sendUpdate();
      return { instances: this.instances };
    });
  }

  _target() {
    if (!app.isPackaged && process.env.ELECTRON_DEV_URL) {
      return {
        url:
          process.env.ELECTRON_DEV_URL.replace(/\/$/, '') +
          '/screensaver.html',
      };
    }
    return { file: path.join(__dirname, '..', '..', 'dist', 'screensaver.html') };
  }

  _createWindow() {
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
    win.on('closed', () => {
      if (this.win === win) this.win = null;
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
      return;
    }
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
    const item = (this.instances || []).find((entry) => entry.id === id);
    if (!item || !this.win || this.win.isDestroyed()) {
      return { open: false, focused: false };
    }
    // 隐藏屏保，直接看真实浏览器；右下角保留返回按钮和当前实例日志。
    this.hiddenForBrowser = true;
    this.focusedId = id;
    this.win.hide();
    this._createReturnBar();
    if (item.cdp_url) this._focusBrowser(item.cdp_url);
    return { open: true, hidden: true, focused: true };
  }

  _focusBrowser(cdpUrl) {
    const base = String(cdpUrl || '').replace(/\/$/, '');
    if (!base) return;
    http
      .get(base + '/json', { timeout: 3000 }, (res) => {
        let data = '';
        res.setEncoding('utf8');
        res.on('data', (chunk) => {
          data += chunk;
        });
        res.on('end', () => {
          let target = null;
          try {
            const pages = (JSON.parse(data) || []).filter(
              (item) =>
                item &&
                item.type === 'page' &&
                item.webSocketDebuggerUrl &&
                !/^(devtools|edge|chrome|about):/.test(String(item.url || ''))
            );
            target = pages[0] || null;
          } catch (_e) {
            target = null;
          }
          if (!target) return;
          const ws = new WebSocket(target.webSocketDebuggerUrl);
          ws.on('open', () => {
            try {
              ws.send(JSON.stringify({ id: 1, method: 'Page.bringToFront' }));
            } catch (_e) {}
            setTimeout(() => {
              try {
                ws.close();
              } catch (_e) {}
            }, 800);
          });
          ws.on('error', () => {
            try {
              ws.close();
            } catch (_e) {}
          });
        });
      })
      .on('error', () => {});
  }

  async confirmStopAll() {
    const win = this.win;
    if (!win || win.isDestroyed()) return { stopped: false };
    const result = await dialog.showMessageBox(win, {
      type: 'warning',
      title: '停止所有任务',
      message: '停止所有实例的审核任务？',
      detail: '会取消所有正在运行的任务，已审核的结果保留。',
      buttons: ['继续审核', '停止全部'],
      defaultId: 0,
      cancelId: 0,
      noLink: true,
    });
    if (result.response === 1) {
      const main = this.getMainWindow && this.getMainWindow();
      if (main && !main.isDestroyed()) {
        main.webContents.send('screensaver:stopAll');
      }
      return { stopped: true };
    }
    return { stopped: false };
  }

  async confirmClose() {
    const win = this.win;
    if (!win || win.isDestroyed()) return { open: false };
    const result = await dialog.showMessageBox(win, {
      type: 'question',
      title: '关闭实时大屏',
      message: '关闭实时大屏？',
      detail: '审核会继续在后台运行，不会停止当前实例。',
      buttons: ['继续审核', '关闭大屏'],
      defaultId: 0,
      cancelId: 0,
      noLink: true,
    });
    if (result.response === 1) {
      this.close();
      return { open: false, closed: true };
    }
    return { open: true, closed: false };
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
    const instance = (this.instances || []).find(
      (entry) => entry.id === this.focusedId
    );
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

module.exports = { ScreensaverService };
