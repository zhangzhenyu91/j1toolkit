const { test, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const Module = require('module');

const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'defect-progress-'));
const windows = [];
const handlers = new Map();
const listeners = new Map();
const mainEvents = [];
let cursor = { x: 0, y: 0 };

class FakeBrowserWindow {
  constructor(options) {
    this.options = options;
    this.bounds = {
      x: options.x,
      y: options.y,
      width: options.width,
      height: options.height,
    };
    this.destroyed = false;
    this.loaded = null;
    this.handlers = {};
    this.shown = false;
    this.webContents = {
      send: (channel, payload) => mainEvents.push([channel, payload]),
    };
    windows.push(this);
  }

  setAlwaysOnTop() {}
  setVisibleOnAllWorkspaces() {}
  setMenuBarVisibility() {}
  loadFile(file) {
    this.loaded = { file };
  }
  loadURL(url) {
    this.loaded = { url };
  }
  once(event, fn) {
    this.handlers[event] = fn;
  }
  on(event, fn) {
    this.handlers[event] = fn;
  }
  show() {
    this.shown = true;
  }
  focus() {}
  getBounds() {
    return { ...this.bounds };
  }
  setBounds(bounds) {
    this.bounds = { ...bounds };
  }
  setPosition(x, y) {
    this.bounds.x = x;
    this.bounds.y = y;
  }
  isDestroyed() {
    return this.destroyed;
  }
  close() {
    if (this.destroyed) return;
    this.destroyed = true;
    if (this.handlers.closed) this.handlers.closed();
  }
  destroy() {
    this.close();
  }
}

const fakeApp = {
  isPackaged: false,
  getPath: () => tempRoot,
};
const fakeIpcMain = {
  handle: (channel, fn) => handlers.set(channel, fn),
  on: (channel, fn) => listeners.set(channel, fn),
};
const fakeScreen = {
  getPrimaryDisplay: () => ({
    workArea: { x: 0, y: 0, width: 1600, height: 900 },
  }),
  getCursorScreenPoint: () => cursor,
};

const originalLoad = Module._load;
Module._load = function (request, parent, isMain) {
  if (request === 'electron') {
    return {
      BrowserWindow: FakeBrowserWindow,
      app: fakeApp,
      ipcMain: fakeIpcMain,
      screen: fakeScreen,
    };
  }
  return originalLoad.apply(this, arguments);
};
process.env.ELECTRON_DEV_URL = '';
const { DefectProgressService } = require('../electron/defectprogress/index.cjs');
Module._load = originalLoad;

const mainWindow = {
  isDestroyed: () => false,
  webContents: {
    send: (channel, payload) => mainEvents.push([channel, payload]),
  },
};
const service = new DefectProgressService({ getMainWindow: () => mainWindow });
service.registerIpc();

after(() => {
  service.dispose();
  fs.rmSync(tempRoot, { recursive: true, force: true });
});

test('打开浮窗加载 progress.html 并通过主窗口同步状态', () => {
  const result = handlers.get('defectProgress:open')();
  assert.deepEqual(result, { open: true });
  assert.equal(windows.length, 1);
  const win = windows[0];
  assert.equal(win.options.transparent, true);
  assert.equal(win.options.frame, false);
  assert.equal(win.options.alwaysOnTop, true);
  assert.match(win.options.webPreferences.preload, /electron[\\/]preload-progress\.cjs$/);
  assert.match(win.loaded.file, /dist[\\/]progress\.html$/);
  assert.deepEqual(mainEvents.at(-1), ['defectProgress:state', true]);
  assert.deepEqual(handlers.get('defectProgress:getState')(), { open: true });
});

test('拖动与缩放按 OS 光标位移更新窗口并落盘', () => {
  const win = windows[0];
  win.setBounds({ x: 100, y: 100, width: 380, height: 300 });

  cursor = { x: 150, y: 140 };
  listeners.get('defectProgress:dragStart')(null, 'move');
  cursor = { x: 170, y: 160 };
  service._pollDrag();
  assert.deepEqual({ x: win.bounds.x, y: win.bounds.y }, { x: 120, y: 120 });
  listeners.get('defectProgress:dragEnd')();
  const saved = JSON.parse(
    fs.readFileSync(path.join(tempRoot, 'defect-progress-window.json'), 'utf8')
  );
  assert.deepEqual(saved, { x: 120, y: 120, w: 380, h: 300 });

  cursor = { x: 200, y: 200 };
  listeners.get('defectProgress:dragStart')(null, 'resize');
  cursor = { x: 240, y: 220 };
  service._pollDrag();
  assert.deepEqual({ w: win.bounds.width, h: win.bounds.height }, { w: 420, h: 320 });
  listeners.get('defectProgress:dragEnd')();
});

test('关闭浮窗销毁窗口并通知主窗口', () => {
  const result = handlers.get('defectProgress:close')();
  assert.deepEqual(result, { open: false });
  assert.equal(windows[0].destroyed, true);
  assert.deepEqual(mainEvents.at(-1), ['defectProgress:state', false]);
  assert.deepEqual(handlers.get('defectProgress:getState')(), { open: false });
});

test('重新打开时复用上次保存的位置和尺寸', () => {
  handlers.get('defectProgress:open')();
  const win = windows.at(-1);
  assert.deepEqual(
    { x: win.bounds.x, y: win.bounds.y, w: win.bounds.width, h: win.bounds.height },
    { x: 120, y: 120, w: 420, h: 320 }
  );
});
