// 机巡审核进度浮窗：独立置顶透明窗，显示每个实例的运行状态与进度。
// 拖动/缩放只收「开始/结束」信号，位移由主进程轮询 OS 光标得出（与题库搜题结果窗一致：
// 渲染层坐标在透明无边框窗下不可靠）。
const { BrowserWindow, app, ipcMain, screen } = require('electron');
const fs = require('fs');
const path = require('path');

const WINDOW_STATE_FILE = 'defect-progress-window.json';
const MIN_WIDTH = 320;
const MIN_HEIGHT = 180;
const DEFAULT_WIDTH = 380;
const DEFAULT_HEIGHT = 300;
const MAX_DRAG_MS = 5 * 60 * 1000;

class DefectProgressService {
  constructor({ getMainWindow } = {}) {
    this.getMainWindow = getMainWindow || (() => null);
    this.win = null;
    this.drag = null;
    this.ipcRegistered = false;
  }

  _statePath() {
    return path.join(app.getPath('userData'), WINDOW_STATE_FILE);
  }

  _loadBounds() {
    try {
      const data = JSON.parse(fs.readFileSync(this._statePath(), 'utf8'));
      if (!data || typeof data !== 'object') return null;
      return data;
    } catch (_e) {
      return null;
    }
  }

  _saveBounds() {
    if (!this.win || this.win.isDestroyed()) return;
    const b = this.win.getBounds();
    try {
      fs.writeFileSync(
        this._statePath(),
        JSON.stringify({ x: b.x, y: b.y, w: b.width, h: b.height })
      );
    } catch (e) {
      console.error('[defect-progress] 保存浮窗位置失败', e);
    }
  }

  _overlayUrl(page) {
    if (!app.isPackaged && process.env.ELECTRON_DEV_URL) {
      return {
        url: process.env.ELECTRON_DEV_URL.replace(/\/$/, '') + '/' + page,
      };
    }
    return { file: path.join(__dirname, '..', '..', 'dist', page) };
  }

  _bounds() {
    const wa = screen.getPrimaryDisplay().workArea;
    const saved = this._loadBounds() || {};
    const w = Math.min(wa.width, Math.max(MIN_WIDTH, saved.w | 0 || DEFAULT_WIDTH));
    const h = Math.min(wa.height, Math.max(MIN_HEIGHT, saved.h | 0 || DEFAULT_HEIGHT));
    let x = wa.x + wa.width - w - 16;
    let y = wa.y + wa.height - h - 16;
    if (
      saved.x >= wa.x &&
      saved.x <= wa.x + wa.width - 40 &&
      saved.y >= wa.y &&
      saved.y <= wa.y + wa.height - 40
    ) {
      x = saved.x;
      y = saved.y;
    }
    return { x, y, width: w, height: h };
  }

  _emitState(open) {
    if (this.getMainWindow) {
      const main = this.getMainWindow();
      if (main && !main.isDestroyed()) {
        main.webContents.send('defectProgress:state', !!open);
      }
    }
  }

  open() {
    if (this.win && !this.win.isDestroyed()) {
      this.win.show();
      this.win.focus();
      this._emitState(true);
      return { open: true };
    }
    const bounds = this._bounds();
    const win = new BrowserWindow({
      ...bounds,
      minWidth: MIN_WIDTH,
      minHeight: MIN_HEIGHT,
      transparent: true,
      frame: false,
      alwaysOnTop: true,
      skipTaskbar: true,
      resizable: false,
      focusable: true,
      hasShadow: false,
      minimizable: false,
      maximizable: false,
      show: false,
      webPreferences: {
        preload: path.join(__dirname, '..', 'preload-progress.cjs'),
        contextIsolation: true,
        nodeIntegration: false,
        backgroundThrottling: false,
      },
    });
    win.setAlwaysOnTop(true, 'screen-saver');
    win.setVisibleOnAllWorkspaces(true, { visibleOnFullScreen: true });
    win.setMenuBarVisibility(false);
    const target = this._overlayUrl('progress.html');
    if (target.url) win.loadURL(target.url);
    else win.loadFile(target.file);
    win.once('ready-to-show', () => win.show());
    win.on('closed', () => {
      if (this.drag) this._endDrag();
      this.win = null;
      this._emitState(false);
    });
    this.win = win;
    this._emitState(true);
    return { open: true };
  }

  close() {
    const win = this.win;
    this.win = null;
    if (win && !win.isDestroyed()) win.close();
    this._emitState(false);
    return { open: false };
  }

  state() {
    return { open: !!(this.win && !this.win.isDestroyed()) };
  }

  _pollDrag() {
    const d = this.drag;
    if (!d) return;
    const win = this.win;
    if (!win || win.isDestroyed()) {
      this._endDrag();
      return;
    }
    if (Date.now() - d.startedAt > MAX_DRAG_MS) {
      this._endDrag();
      return;
    }
    const cursor = screen.getCursorScreenPoint();
    const dx = cursor.x - d.cursor0.x;
    const dy = cursor.y - d.cursor0.y;
    if (d.mode === 'move') {
      win.setPosition(Math.round(d.bounds0.x + dx), Math.round(d.bounds0.y + dy));
    } else {
      let w = Math.max(MIN_WIDTH, Math.round(d.bounds0.w + dx));
      let h = Math.max(MIN_HEIGHT, Math.round(d.bounds0.h + dy));
      if (w < d.bounds0.w) w += 3;
      if (h < d.bounds0.h) h += 3;
      // Windows 透明窗 setSize 缩小只生效一次；setBounds 更可靠。
      const cur = win.getBounds();
      win.setBounds({ x: cur.x, y: cur.y, width: w, height: h });
    }
  }

  _startDrag(mode) {
    if (!this.win || this.win.isDestroyed()) return;
    clearInterval(this.drag && this.drag.timer);
    const b = this.win.getBounds();
    this.drag = {
      mode: mode === 'resize' ? 'resize' : 'move',
      cursor0: screen.getCursorScreenPoint(),
      bounds0: { x: b.x, y: b.y, w: b.width, h: b.height },
      startedAt: Date.now(),
      timer: setInterval(() => this._pollDrag(), 16),
    };
  }

  _endDrag() {
    if (!this.drag) return;
    clearInterval(this.drag.timer);
    this.drag = null;
    this._saveBounds();
  }

  registerIpc() {
    if (this.ipcRegistered) return;
    this.ipcRegistered = true;
    ipcMain.handle('defectProgress:open', () => this.open());
    ipcMain.handle('defectProgress:close', () => this.close());
    ipcMain.handle('defectProgress:getState', () => this.state());
    ipcMain.on('defectProgress:closeWindow', () => this.close());
    ipcMain.on('defectProgress:dragStart', (_e, mode) => this._startDrag(mode));
    ipcMain.on('defectProgress:dragEnd', () => this._endDrag());
  }

  dispose() {
    clearInterval(this.drag && this.drag.timer);
    this.drag = null;
    if (this.win && !this.win.isDestroyed()) this.win.destroy();
    this.win = null;
  }
}

module.exports = { DefectProgressService };
