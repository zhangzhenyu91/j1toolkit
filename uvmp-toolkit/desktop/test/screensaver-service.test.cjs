// 实时大屏服务回归测试：窗口代际（旧 closed 不误报）、CDP 回环校验、响应上限、真实超时、IPC 面收敛。
const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const { EventEmitter } = require('node:events');

const {
  ScreensaverService,
  isLoopbackCdpUrl,
  pickPageTarget,
} = require('../electron/screensaver/index.cjs');

// ---- 假 electron 依赖 ----
class FakeWebContents extends EventEmitter {
  constructor() {
    super();
    this.sent = [];
    this.loaded = null;
    this.destroyed = false;
  }
  send(channel, payload) {
    this.sent.push({ channel, payload });
  }
  isDestroyed() {
    return this.destroyed;
  }
}

class FakeWindow extends EventEmitter {
  constructor(opts) {
    super();
    this.opts = opts || {};
    this.webContents = new FakeWebContents();
    this.destroyed = false;
    this.visible = false;
    this.calls = [];
    FakeWindow.instances.push(this);
  }
  isDestroyed() {
    return this.destroyed;
  }
  close() {
    if (this.destroyed) return;
    this.destroyed = true;
    this.webContents.destroyed = true;
    this.emit('closed');
  }
  destroy() {
    this.close();
  }
  show() {
    this.visible = true;
    this.calls.push('show');
  }
  hide() {
    this.visible = false;
    this.calls.push('hide');
  }
  focus() {
    this.calls.push('focus');
  }
  setAlwaysOnTop() {}
  setVisibleOnAllWorkspaces() {}
  setMenuBarVisibility() {}
  loadURL(url) {
    this.webContents.loaded = { url };
  }
  loadFile(file) {
    this.webContents.loaded = { file };
  }
}
FakeWindow.instances = [];

class FakeIpcMain {
  constructor() {
    this.handlers = new Map();
    this.onHandlers = new Map();
  }
  handle(channel, fn) {
    if (this.handlers.has(channel)) throw new Error('重复注册: ' + channel);
    this.handlers.set(channel, fn);
  }
  removeHandler(channel) {
    this.handlers.delete(channel);
  }
  on(channel, fn) {
    this.onHandlers.set(channel, fn);
  }
}

const fakeScreen = {
  getPrimaryDisplay: () => ({
    bounds: { x: 0, y: 0, width: 1920, height: 1080 },
    workArea: { x: 0, y: 0, width: 1920, height: 1040 },
  }),
};

function makeService(options = {}) {
  FakeWindow.instances = [];
  const mainContents = new FakeWebContents();
  const mainWindow = {
    isDestroyed: () => false,
    webContents: mainContents,
  };
  const ipcMain = new FakeIpcMain();
  const sockets = [];
  class FakeWebSocket extends EventEmitter {
    constructor(url, opts) {
      super();
      this.url = url;
      this.opts = opts;
      this.sent = [];
      this.closed = false;
      this.terminated = false;
      sockets.push(this);
      if (options.autoOpen !== false) setImmediate(() => this.emit('open'));
    }
    send(data) {
      this.sent.push(String(data));
    }
    close() {
      this.closed = true;
    }
    terminate() {
      this.terminated = true;
      this.closed = true;
    }
  }
  const service = new ScreensaverService({
    getMainWindow: () => mainWindow,
    deps: {
      BrowserWindow: FakeWindow,
      screen: fakeScreen,
      ipcMain,
      app: { isPackaged: true },
      http,
      WebSocket: FakeWebSocket,
    },
    cdpTimeoutMs: options.cdpTimeoutMs || 2000,
    cdpMaxBytes: options.cdpMaxBytes || 4096,
    cdpWsTimeoutMs: options.cdpWsTimeoutMs || 500,
    closeWsDelayMs: 0,
  });
  return { service, ipcMain, mainContents, sockets };
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function startCdpServer(handler) {
  return new Promise((resolve) => {
    const server = http.createServer(handler);
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address();
      resolve({ server, port, url: 'http://127.0.0.1:' + port });
    });
  });
}

// ---------------- 回环校验 ----------------
test('isLoopbackCdpUrl 只接受回环 HTTP', () => {
  assert.equal(isLoopbackCdpUrl('http://127.0.0.1:9222'), true);
  assert.equal(isLoopbackCdpUrl('http://localhost:9222/'), true);
  assert.equal(isLoopbackCdpUrl('http://[::1]:9222'), true);
  // 与核心 validate_cdp_url 同口径：仅 http（wss/https 不属于本地 CDP 场景）
  assert.equal(isLoopbackCdpUrl('https://127.0.0.1:9222'), false);
  // 内网/公网地址一律拒绝（防被实例参数诱导弹到任意主机）
  assert.equal(isLoopbackCdpUrl('http://10.20.30.40:9222'), false);
  assert.equal(isLoopbackCdpUrl('http://example.com:9222'), false);
  assert.equal(isLoopbackCdpUrl('http://169.254.169.254'), false);
  assert.equal(isLoopbackCdpUrl('file:///etc/passwd'), false);
  assert.equal(isLoopbackCdpUrl('ws://127.0.0.1:9222'), false);
  assert.equal(isLoopbackCdpUrl(''), false);
  assert.equal(isLoopbackCdpUrl(null), false);
});

test('pickPageTarget 只选同源回环 WS 目标', () => {
  const base = new URL('http://127.0.0.1:9222');
  const pages = [
    {
      type: 'page',
      url: 'http://10.0.0.2/x',
      webSocketDebuggerUrl: 'ws://10.0.0.2:9222/devtools/page/A',
    },
    {
      type: 'page',
      url: 'http://ok/',
      webSocketDebuggerUrl: 'ws://127.0.0.1:9222/devtools/page/B',
    },
  ];
  assert.equal(
    pickPageTarget(pages, base).webSocketDebuggerUrl,
    'ws://127.0.0.1:9222/devtools/page/B'
  );
  assert.equal(pickPageTarget('not-array', base), null);
  assert.equal(pickPageTarget([], base), null);
  assert.equal(
    pickPageTarget(
      [{ type: 'page', url: 'devtools://x', webSocketDebuggerUrl: 'ws://127.0.0.1:9222/a' }],
      base
    ),
    null,
    'devtools/chrome 内部页不作为聚焦目标'
  );
});

// ---------------- 窗口代际 ----------------
test('open 建窗并复用；旧窗 closed 不误报新窗关闭', async () => {
  const { service, mainContents } = makeService();
  service.open([{ id: 'a' }]);
  assert.equal(FakeWindow.instances.length, 1);
  const oldWin = FakeWindow.instances[0];
  assert.deepEqual(mainContents.sent.at(-1), { channel: 'screensaver:state', payload: true });

  // 新窗接管（模拟旧窗尚未 closed 就被替换）
  service.win = null;
  service.open([{ id: 'b' }]);
  assert.equal(FakeWindow.instances.length, 2);
  const newWin = FakeWindow.instances[1];
  const before = mainContents.sent.filter((m) => m.channel === 'screensaver:state').length;

  oldWin.close();
  await sleep(10);
  assert.equal(service.win, newWin, '旧窗关闭不得清空当前窗口');
  assert.equal(service.state().open, true);
  assert.equal(
    mainContents.sent.filter((m) => m.channel === 'screensaver:state').length,
    before,
    '旧窗迟到 closed 不得广播状态变化'
  );
});

test('当前窗 closed 只广播一次 false；close() 也只广播一次', async () => {
  const { service, mainContents } = makeService();
  service.open([{ id: 'a' }]);
  const win = FakeWindow.instances[0];
  win.close();
  await sleep(10);
  assert.equal(service.state().open, false);
  const states = mainContents.sent.filter((m) => m.channel === 'screensaver:state');
  assert.deepEqual(
    states.map((m) => m.payload),
    [true, false]
  );

  mainContents.sent.length = 0;
  service.open([{ id: 'b' }]);
  const second = FakeWindow.instances.at(-1);
  service.close();
  await sleep(10);
  const states2 = mainContents.sent.filter((m) => m.channel === 'screensaver:state');
  assert.deepEqual(
    states2.map((m) => m.payload),
    [true, false],
    'close 后 closed 事件不得重复广播'
  );
  assert.equal(second.destroyed, true);
});

test('update 只推给当前大屏窗口，非数组入参归一为空列表', () => {
  const { service } = makeService();
  service.open([{ id: 'a' }]);
  const win = FakeWindow.instances[0];
  win.webContents.sent.length = 0;
  service.update('oops');
  assert.deepEqual(win.webContents.sent, [
    { channel: 'screensaver:update', payload: { instances: [] } },
  ]);
});

// ---------------- _focusBrowser ----------------
test('_focusBrowser 拒绝非回环 CDP 地址（不发请求）', async () => {
  const { service, sockets } = makeService();
  let hits = 0;
  const { server, url } = await startCdpServer((_req, res) => {
    hits += 1;
    res.end('[]');
  });
  try {
    await service._focusBrowser('http://10.20.30.40:9222');
    await service._focusBrowser('http://127.0.0.1:' + new URL(url).port + '@evil.com/');
    await sleep(50);
    assert.equal(hits, 0);
    assert.equal(sockets.length, 0);
  } finally {
    server.close();
  }
});

test('_focusBrowser 响应超上限即中止，不建立 WS', async () => {
  const { service, sockets } = makeService({ cdpMaxBytes: 256 });
  const { server, url } = await startCdpServer((_req, res) => {
    res.writeHead(200, { 'content-type': 'application/json' });
    // 持续写入超过上限的垃圾，模拟异常/恶意端点
    const chunk = 'x'.repeat(128);
    const timer = setInterval(() => res.write(chunk), 5);
    res.on('close', () => clearInterval(timer));
  });
  try {
    await service._focusBrowser(url);
    await sleep(150);
    assert.equal(sockets.length, 0, '超限响应不得用于建立调试连接');
  } finally {
    server.closeAllConnections();
    server.close();
  }
});

test('_focusBrowser 全局超时：服务器不响应时按时返回', async () => {
  const { service } = makeService({ cdpTimeoutMs: 120 });
  const { server, url } = await startCdpServer(() => {
    /* 永不响应 */
  });
  try {
    const started = Date.now();
    await service._focusBrowser(url);
    const elapsed = Date.now() - started;
    assert.ok(elapsed >= 100 && elapsed < 2000, '耗时应在超时预算附近: ' + elapsed);
  } finally {
    server.closeAllConnections();
    server.close();
  }
});

test('_focusBrowser 聚焦合法回环目标：建立 WS 并发送 Page.bringToFront', async () => {
  const { service, sockets } = makeService();
  let port = 0;
  const server = http.createServer((req, res) => {
    assert.equal(req.url, '/json');
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(
      JSON.stringify([
        {
          type: 'page',
          url: 'http://127.0.0.1/page',
          webSocketDebuggerUrl: 'ws://127.0.0.1:' + port + '/devtools/page/P1',
        },
      ])
    );
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  port = server.address().port;
  try {
    await service._focusBrowser('http://127.0.0.1:' + port);
    await sleep(80);
    assert.equal(sockets.length, 1);
    assert.match(sockets[0].url, /\/devtools\/page\/P1$/);
    assert.deepEqual(JSON.parse(sockets[0].sent[0]), { id: 1, method: 'Page.bringToFront' });
  } finally {
    server.close();
  }
});

test('_focusBrowser 跨主机 WS 目标不连接', async () => {
  const { service, sockets } = makeService();
  const server = http.createServer((_req, res) => {
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(
      JSON.stringify([
        {
          type: 'page',
          url: 'http://127.0.0.1/x',
          webSocketDebuggerUrl: 'ws://169.254.169.254:80/devtools/page/X',
        },
      ])
    );
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const { port } = server.address();
  try {
    await service._focusBrowser('http://127.0.0.1:' + port);
    await sleep(80);
    assert.equal(sockets.length, 0);
  } finally {
    server.close();
  }
});

test('_focusBrowser WS 握手超时会 terminate，不悬挂连接', async () => {
  const { service, sockets } = makeService({ cdpWsTimeoutMs: 60, autoOpen: false });
  let port = 0;
  const server = http.createServer((_req, res) => {
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(
      JSON.stringify([
        {
          type: 'page',
          url: 'http://127.0.0.1/x',
          webSocketDebuggerUrl: 'ws://127.0.0.1:' + port + '/devtools/page/X',
        },
      ])
    );
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  port = server.address().port;
  try {
    await service._focusBrowser('http://127.0.0.1:' + port);
    await sleep(200);
    assert.equal(sockets.length, 1);
    assert.equal(sockets[0].terminated, true, '握手超时必须 terminate');
    assert.equal(sockets[0].sent.length, 0, '未连通不得发送命令');
  } finally {
    server.close();
  }
});

// ---------------- IPC 面 ----------------
test('IPC 收敛：不注册无用高危通道，且校验 sender', async () => {
  const { service, ipcMain, mainContents } = makeService();
  service.registerIpc();
  assert.equal(ipcMain.handlers.has('screensaver:requestClose'), false);
  assert.equal(ipcMain.handlers.has('screensaver:requestStopAll'), false);
  assert.equal(typeof ipcMain.handlers.get('screensaver:open'), 'function');

  const untrusted = { sender: new FakeWebContents(), senderFrame: null };
  await assert.rejects(() => ipcMain.handlers.get('screensaver:open')(untrusted, []), /来源/);
  // 主窗口是大屏控制端，允许
  const trusted = { sender: mainContents };
  const state = await ipcMain.handlers.get('screensaver:getState')(trusted);
  assert.equal(state.open, false);
});

test('focusInstance：无可用回环 CDP 时不隐藏大屏', () => {
  const { service } = makeService();
  service.open([{ id: 'a' }, { id: 'b', cdp_url: 'http://10.0.0.9:9222' }]);
  const win = FakeWindow.instances[0];
  const resA = service.focusInstance('a');
  assert.deepEqual(resA, { open: true, hidden: false, focused: false });
  const resB = service.focusInstance('b');
  assert.deepEqual(resB, { open: true, hidden: false, focused: false });
  assert.ok(!win.calls.includes('hide'), '大屏不得被隐藏');
  assert.equal(service.state().hidden, false);
  assert.equal(service.returnBar, null, '无浏览器可看时不应弹返回浮窗');
});

test('focusInstance：合法回环 CDP 时隐藏大屏并弹返回浮窗', async () => {
  const { service } = makeService();
  service.open([{ id: 'a', cdp_url: 'http://127.0.0.1:1' }]);
  const win = FakeWindow.instances[0];
  const res = service.focusInstance('a');
  assert.deepEqual(res, { open: true, hidden: true, focused: true });
  assert.ok(win.calls.includes('hide'), '应隐藏大屏露出浏览器');
  assert.ok(service.returnBar, '应创建返回浮窗');
  assert.equal(service.focusedId, 'a');
  // 返回大屏：浮窗关闭、大屏可见
  const back = service.returnToScreensaver();
  assert.deepEqual(back, { open: true });
  assert.ok(win.calls.includes('show'));
  assert.equal(service.returnBar, null);
});

test('返回浮窗：加载完成后补推首帧（update 早于加载的推送会丢）', () => {
  const { service } = makeService();
  service.open([{ id: 'a', cdp_url: 'http://127.0.0.1:1', userLogs: ['第一步'] }]);
  service.focusInstance('a');
  const bar = service.returnBar;
  assert.ok(bar, '应创建返回浮窗');
  bar.webContents.sent.length = 0;
  // 模拟页面加载完成后回推当前实例
  bar.webContents.emit('did-finish-load');
  const payload = bar.webContents.sent.find((m) => m.channel === 'returnbar:update');
  assert.ok(payload, ' did-finish-load 后必须补推 returnbar:update');
  assert.equal(payload.payload.focusedId, 'a');
  assert.deepEqual(payload.payload.instance.userLogs, ['第一步']);
});

test('stopAllNow 只向可信主窗口转发', () => {
  const { service, mainContents, ipcMain } = makeService();
  service.registerIpc();
  const state = service.stopAllNow();
  assert.deepEqual(state, { stopped: true });
  assert.deepEqual(mainContents.sent.at(-1), {
    channel: 'screensaver:stopAll',
    payload: undefined,
  });
  // 大屏窗口自身不能触发“停止全部”（避免误触/脚本化）
  assert.equal(ipcMain.handlers.has('screensaver:stopAll'), false);
});
