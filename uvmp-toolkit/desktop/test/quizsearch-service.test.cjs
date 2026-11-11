// 搜题服务回归测试：帧哈希只在真正入队后记录、inFlight 复位、窗口代际清理、
// 首帧重放、拖拽定时器清理、IPC 异常统一回传、诊断图写受控临时目录。
// 全部用假 Electron 模块面 + 假 worker，不加载真实 Electron、不截屏、不连任何外部服务。
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { EventEmitter } = require('events');

const {
  QuizSearchService,
  encodeOcrId,
  decodeOcrId,
  frameHash,
} = require('../electron/quizsearch/index.cjs');

// ---- 假 Electron ----
class FakeWebContents extends EventEmitter {
  constructor() {
    super();
    this.sent = [];
    this.loaded = null;
    this.destroyed = false;
    this.ready = false;
  }
  send(channel, payload) {
    this.sent.push({ channel, payload });
  }
  isDestroyed() {
    return this.destroyed;
  }
  once(event, fn) {
    if (event !== 'did-finish-load') return super.once(event, fn);
    this._onLoad = fn;
    return this;
  }
  fireLoaded() {
    this.ready = true;
    if (this._onLoad) this._onLoad();
  }
}

class FakeWindow extends EventEmitter {
  constructor(opts) {
    super();
    this.opts = opts || {};
    this.webContents = new FakeWebContents();
    this.destroyed = false;
    this.visible = false;
    this.bounds = {
      x: opts.x || 0,
      y: opts.y || 0,
      width: opts.width || 720,
      height: opts.height || 180,
    };
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
  showInactive() {
    this.visible = true;
    this.calls.push('showInactive');
  }
  setAlwaysOnTop() {}
  setVisibleOnAllWorkspaces() {}
  setOpacity(v) {
    this.calls.push('opacity:' + v);
  }
  setPosition(x, y) {
    this.bounds.x = x;
    this.bounds.y = y;
  }
  setBounds(b) {
    this.bounds = { x: b.x, y: b.y, width: b.width, height: b.height };
  }
  getBounds() {
    return Object.assign({}, this.bounds);
  }
  loadURL(url) {
    this.webContents.loaded = { url };
  }
  loadFile(file, opts) {
    this.webContents.loaded = { file, opts };
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
  on(channel, fn) {
    this.onHandlers.set(channel, fn);
  }
  invoke(channel, ...args) {
    const fn = this.handlers.get(channel);
    if (!fn) throw new Error('未注册: ' + channel);
    return fn({}, ...args);
  }
  emitMain(channel, ...args) {
    const fn = this.onHandlers.get(channel);
    if (!fn) throw new Error('未注册: ' + channel);
    return fn({}, ...args);
  }
}

const DISPLAY_1 = {
  id: 1,
  bounds: { x: 0, y: 0, width: 1920, height: 1080 },
  workArea: { x: 0, y: 0, width: 1920, height: 1040 },
  size: { width: 1920, height: 1080 },
  scaleFactor: 1,
};

function fakeThumbnail(width, height) {
  const bitmap = Buffer.alloc(width * height * 4, 128);
  return {
    isEmpty: () => false,
    getSize: () => ({ width, height }),
    getBitmap: () => bitmap,
  };
}

function makeDeps(overrides = {}) {
  const ipcMain = new FakeIpcMain();
  const getSourcesCalls = [];
  const deps = {
    app: {
      isPackaged: false,
      getPath: () => os.tmpdir(),
    },
    BrowserWindow: FakeWindow,
    screen: {
      getPrimaryDisplay: () => DISPLAY_1,
      getDisplayMatching: () => DISPLAY_1,
      getAllDisplays: () => [DISPLAY_1],
      getCursorScreenPoint: () => ({ x: 100, y: 100 }),
    },
    desktopCapturer: {
      getSources: async (opts) => {
        getSourcesCalls.push(opts);
        const t = fakeThumbnail(1920, 1080);
        return [{ display_id: '1', thumbnail: t }];
      },
    },
    nativeImage: {
      createFromBuffer: (buf, size) => ({
        size,
        toPNG: () => Buffer.from('png'),
      }),
    },
    ipcMain,
    Worker: FakeWorker,
  };
  Object.assign(deps, overrides);
  return { deps, ipcMain, getSourcesCalls };
}

const CREATED = [];
const TEMP_DIRS = [];
test.after(() => {
  for (const svc of CREATED) {
    try {
      svc.dispose();
    } catch (_e) {
      /* 已清理 */
    }
  }
  for (const dir of TEMP_DIRS) fs.rmSync(dir, { recursive: true, force: true }); // 别把临时题库留在系统临时目录
});

function makeService(overrides = {}) {
  FakeWindow.instances = [];
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'quiz-svc-'));
  TEMP_DIRS.push(dir);
  const { deps, ipcMain, getSourcesCalls } = makeDeps(overrides.deps);
  const mainContents = new FakeWebContents();
  const mainWindow = { isDestroyed: () => false, webContents: mainContents, minimized: 0 };
  mainWindow.minimize = () => {
    mainWindow.minimized++;
  };
  const service = new QuizSearchService({
    deps,
    dataDir: dir,
    getMainWindow: () => mainWindow,
    workArea: DISPLAY_1.workArea,
    onScanStateChange: overrides.onScanStateChange,
  });
  CREATED.push(service);
  return { service, ipcMain, dir, mainWindow, getSourcesCalls, deps };
}

function importBank(service) {
  return service.store.importRows('测试题库', 'x.xlsx', [
    ['题干', '答案', '选项A', '选项B'],
    ['我国安全生产工作的方针是？', 'A', '安全第一', '质量第一'],
    ['安全生产人人有责的说法是否正确', 'A', '正确', '错误'],
  ]);
}

// 假 worker：记录投递，不启动线程
class FakeWorker extends EventEmitter {
  constructor() {
    super();
    this.posted = [];
    this.terminated = false;
    FakeWorker.instances.push(this);
  }
  postMessage(msg) {
    this.posted.push(msg);
  }
  terminate() {
    this.terminated = true;
  }
}
FakeWorker.instances = [];

// 通过注入的 Worker 类走真实 _forkWorker 装配，确保 exit/error 处理器确由服务注册
function stubWorker(service) {
  FakeWorker.instances = [];
  service._forkWorker();
  const fake = FakeWorker.instances[0];
  service.workerReady = true;
  return fake;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------------------------------------------------------------- 帧哈希与 id 编码

test('帧哈希对同画面稳定、对改动画敏感', () => {
  const a = Buffer.alloc(8192, 7);
  const b = Buffer.alloc(8192, 7);
  assert.equal(frameHash(a), frameHash(b));
  b[128] = 9; // 抽样步长为 16，必须改到采样点才敏感
  assert.notEqual(frameHash(a), frameHash(b));
});

test('OCR 任务 id 可还原帧纪元与框别', () => {
  const id = encodeOcrId(7, 3, 'B');
  assert.deepEqual(decodeOcrId(id), { epoch: 7, boxId: 'B' });
  const idA = encodeOcrId(7, 3, 'A');
  assert.deepEqual(decodeOcrId(idA), { epoch: 7, boxId: 'A' });
});

// ---------------------------------------------------------------- lastHash / inFlight

test('画面未变时才跳过；引擎未就绪时上一帧不会被哈希吞掉', async () => {
  const { service } = makeService();
  importBank(service);
  service.scanning = true;
  service._syncBoxWindows();
  assert.ok(service.boxWins.A, '扫描框应已创建');

  const cap = await service._captureBox('A', null);
  assert.ok(cap && cap.width > 0 && cap.height > 0);

  // 引擎未就绪：不投递、也不记哈希
  assert.equal(service.workerReady, false);
  service._queueOcr('A', cap.bitmap, cap.width, cap.height);
  assert.equal(service.lastHash.A, 0, '未真正入队不得写 lastHash');

  // 引擎就绪后同一帧仍应入队
  const worker = stubWorker(service);
  const hash = frameHash(cap.bitmap);
  const ocrPosts = () => worker.posted.filter((m) => m.type === 'ocr');
  assert.equal(service._queueOcr('A', cap.bitmap, cap.width, cap.height), true);
  service.lastHash.A = hash;
  assert.equal(ocrPosts().length, 1);

  // 该框上一帧仍在识别：第二次不得投递（inFlight 生效）
  assert.equal(service._queueOcr('A', cap.bitmap, cap.width, cap.height), false);
  assert.equal(ocrPosts().length, 1);
});

test('_tick 在投递失败时不更新哈希，恢复后同画面仍会重试', async () => {
  const { service } = makeService();
  importBank(service);
  service.scanning = true;
  service.settings.boxes.B.visible = false;
  service._syncBoxWindows();

  await service._tick();
  assert.equal(service.lastHash.A, 0, '引擎未就绪：不得写哈希');

  const worker = stubWorker(service);
  await service._tick();
  assert.ok(
    worker.posted.filter((m) => m.type === 'ocr').length >= 1,
    '引擎就绪后同画面必须重试投递'
  );
  assert.notEqual(service.lastHash.A, 0);
});

test('start 复位 inFlight / 哈希 / 框状态，stop 复位 inFlight', () => {
  const { service } = makeService();
  importBank(service);
  let states = [];
  service.opts.onScanStateChange = (v) => states.push(v);

  service.lastHash = { A: 123, B: 456 };
  service.inFlight = { A: true, B: true };
  service.boxState = { A: { matched: true }, B: null };
  service.start();
  assert.equal(service.scanning, true);
  assert.deepEqual(service.inFlight, { A: false, B: false }, 'start 必须复位 inFlight');
  assert.deepEqual(service.lastHash, { A: 0, B: 0 });
  assert.deepEqual(service.boxState, { A: null, B: null });
  assert.deepEqual(states, [true]);

  service.inFlight = { A: true, B: true };
  service.stop();
  assert.equal(service.scanning, false);
  assert.deepEqual(service.inFlight, { A: false, B: false }, 'stop 必须复位 inFlight');
  assert.equal(service.timer, null);
  assert.deepEqual(states, [true, false]);
});

test('OCR 线程退出复位 inFlight，否则该框永久卡在「上一帧未回」', () => {
  const { service } = makeService();
  importBank(service);
  const worker = stubWorker(service);
  service.scanning = true;
  service.inFlight = { A: true, B: true };
  service.worker = worker;
  worker.emit('exit', 1);
  assert.equal(service.worker, null);
  assert.equal(service.workerReady, false);
  assert.deepEqual(service.inFlight, { A: false, B: false });
});

test('停止后迟到的 OCR 结果被纪元丢弃，不改框状态', () => {
  const { service } = makeService();
  importBank(service);
  service.scanning = true;
  service.ocrEpoch = 3;
  service.inFlight = { A: true, B: false };
  service._onWorkerMessage({
    type: 'ocr-result',
    id: encodeOcrId(2, 0, 'A'),
    ok: true,
    text: '我国安全生产工作的方针是',
    elapsedMs: 10,
  });
  assert.deepEqual(service.boxState, { A: null, B: null });
  assert.equal(service.inFlight.A, true, '旧纪元结果不得动当前 inFlight');
});

test('OCR 命中后写入框状态并推送结果窗', () => {
  const { service } = makeService();
  importBank(service);
  service.scanning = true;
  service._createResultWin();
  const win = service.resultWin;
  win.webContents.fireLoaded();
  win.webContents.sent.length = 0;

  service._onWorkerMessage({
    type: 'ocr-result',
    id: encodeOcrId(service.ocrEpoch, 0, 'A'),
    ok: true,
    text: '我国安全生产工作的方针是',
    elapsedMs: 42,
  });
  assert.equal(service.boxState.A.matched, true);
  assert.equal(service.boxState.A.q.content, '我国安全生产工作的方针是？');
  assert.equal(service.inFlight.A, false);
  assert.equal(service.lastOcrMs, 42);
  const pushed = win.webContents.sent.filter((s) => s.channel === 'quizsearch:state');
  assert.equal(pushed.length, 1);
});

// ---------------------------------------------------------------- 窗口代际与首帧

test('扫描框 closed 只清理自身实例：被替换后旧窗关闭不清新窗', () => {
  const { service } = makeService();
  importBank(service);
  service.scanning = true;
  service._syncBoxWindows();
  const first = service.boxWins.A;
  assert.ok(first);

  // 模拟重建（先摘引用再建新窗，与 _syncBoxWindows 的关闭路径一致）
  service.boxWins.A = null;
  first.close();
  assert.equal(service.boxWins.A, null, '旧实例的 closed 不应影响已被替换的槽位');

  service._syncBoxWindows();
  const second = service.boxWins.A;
  assert.ok(second && second !== first);
  second.close();
  assert.equal(service.boxWins.A, null, '当前实例关闭后应置空');
});

test('结果窗 closed 清引用并结束后台拖拽定时器', () => {
  const { service } = makeService();
  service.registerIpc();
  service.scanning = true;
  service._createResultWin();
  const win = service.resultWin;
  service.el.ipcMain.emitMain('quizsearch:resultDragStart', 'move');
  assert.ok(service._resultDrag);
  win.close();
  assert.equal(service.resultWin, null);
  assert.equal(service._resultDrag, null, '窗口关闭必须清掉拖拽定时器');
});

test('首帧 did-finish-load 重放权威状态（结果早于加载完成也不丢）', () => {
  const { service } = makeService();
  importBank(service);
  service.scanning = true;
  service.boxState.A = {
    matched: true,
    score: 1,
    q: { content: '题干', answer: 'A', options: [] },
    ocrText: '题干',
  };
  service._createResultWin();
  const win = service.resultWin;
  assert.equal(win.webContents.sent.length, 0, '加载完成前不推');
  win.webContents.fireLoaded();
  assert.equal(win.visible, true);
  const push = win.webContents.sent.filter((s) => s.channel === 'quizsearch:state');
  assert.equal(push.length, 1);
  assert.equal(push[0].payload.boxes.A.matched, true);
});

test('扫描框首帧加载后才显示（避免露出空白框）', () => {
  const { service } = makeService();
  importBank(service);
  service.scanning = true;
  service._createResultWin();
  service.resultWin.webContents.fireLoaded();
  service._syncBoxWindows();
  const win = service.boxWins.A;
  assert.equal(win.visible, false);
  win.webContents.fireLoaded();
  assert.equal(win.visible, true);
  assert.ok(win.calls.includes('showInactive'));
  // 已被替换的实例即使加载完也不应再显示
  const stale = service.boxWins.B;
  refuse: {
    service.scanning = false;
    service._syncBoxWindows();
    stale.webContents.fireLoaded();
    assert.equal(stale.visible, false);
  }
});

test('停止搜题关闭框窗与结果窗，并清掉拖拽定时器', () => {
  const { service } = makeService();
  importBank(service);
  service.registerIpc();
  service.start();
  service.el.ipcMain.emitMain('quizsearch:boxDragStart', 'A', 'move');
  const timer = service._boxDrag.timer;
  assert.ok(timer);
  service.stop();
  assert.equal(service._boxDrag, null);
  assert.equal(service.boxWins.A, null);
  assert.equal(service.resultWin, null);
});

test('拖拽轮询在窗口消失后自行收尾，不继续读光标', () => {
  const { service } = makeService();
  importBank(service);
  service.registerIpc();
  service.scanning = true;
  service._syncBoxWindows();
  service.el.ipcMain.emitMain('quizsearch:boxDragStart', 'A', 'move');
  assert.ok(service._boxDrag);
  service.boxWins.A.close();
  service._pollBoxDrag();
  assert.equal(service._boxDrag, null);
});

test('dispose 清理所有定时器与窗口', () => {
  const { service } = makeService();
  importBank(service);
  service.start();
  const timer = service.timer;
  service.dispose();
  assert.equal(service.timer, null);
  assert.equal(service.scanning, false);
  assert.equal(service.boxWins.A, null);
  assert.equal(service.resultWin, null);
  assert.ok(timer);
});

// ---------------------------------------------------------------- IPC

test('IPC 异常统一转成中文消息回传', async () => {
  const { service, ipcMain } = makeService();
  service.registerIpc();
  await assert.rejects(() => ipcMain.invoke('quizSearch:start'), /请先导入题库/);
  importBank(service);
  service.store.setEnabled(service.store.list()[0].id, false);
  await assert.rejects(() => ipcMain.invoke('quizSearch:start'), /请先启用至少一个题库/);
  await assert.rejects(() => ipcMain.invoke('quizSearch:bankRemove', '../evil'), /题库 ID 非法/);
});

test('题库 IPC：导入/启停/重命名/自测全程返回权威状态', async () => {
  const { service, ipcMain } = makeService();
  service.registerIpc();
  const imported = await ipcMain.invoke('quizSearch:bankImport', {
    name: '安全规程',
    sourceFile: 'a.xlsx',
    rows: [
      ['题干', '答案'],
      ['我国安全生产工作的方针是？', 'A'],
    ],
  });
  assert.equal(imported.ok, true);
  assert.equal(imported.questionCount, 1);
  assert.equal(imported.banks.length, 1);

  const off = await ipcMain.invoke('quizSearch:bankSetEnabled', imported.id, false);
  assert.equal(off.questionCount, 0);
  const on = await ipcMain.invoke('quizSearch:bankSetEnabled', imported.id, true);
  assert.equal(on.questionCount, 1);

  const renamed = await ipcMain.invoke('quizSearch:bankRename', imported.id, '改名后');
  assert.equal(renamed.banks[0].name, '改名后');

  const matched = await ipcMain.invoke('quizSearch:testMatch', '我国安全生产工作的方针是');
  assert.equal(matched.matched, true);
  assert.equal(matched.question.answer, 'A');

  const removed = await ipcMain.invoke('quizSearch:bankRemove', imported.id);
  assert.deepEqual(removed.banks, []);
});

test('题库导入只接受已解析的 rows：缺数据直接报错', async () => {
  const { service, ipcMain } = makeService();
  service.registerIpc();
  await assert.rejects(
    () => ipcMain.invoke('quizSearch:bankImport', { name: 'x', sourceFile: 'a.xlsx' }),
    /导入数据缺失/
  );
  await assert.rejects(
    () => ipcMain.invoke('quizSearch:bankImport', { path: '/etc/hosts' }),
    /导入数据缺失/
  );
});

test('setOptions 钳制非法值，间隔变更即时重设定时器', async () => {
  const { service, ipcMain } = makeService();
  service.registerIpc();
  importBank(service);
  service.start();
  const s = await ipcMain.invoke('quizSearch:setOptions', {
    interval: 1,
    threshold: 99,
    opacity: 'x',
  });
  assert.equal(s.settings.interval, 500);
  assert.equal(s.settings.threshold, 0.9);
  assert.equal(s.settings.opacity, 0.9);
  service.stop();
});

test('诊断截图写入受控临时目录并告知位置', async () => {
  const { service, ipcMain } = makeService();
  service.registerIpc();
  importBank(service);
  service.scanning = true;
  service._syncBoxWindows();
  const r = await ipcMain.invoke('quizSearch:debugCapture');
  assert.match(r.dir, /shade-quiz-debug$/);
  assert.equal(path.dirname(r.file), r.dir);
  assert.ok(fs.existsSync(r.file));
  assert.match(r.notice, /临时目录/);
  fs.unlinkSync(r.file); // 测试自清理：不往系统临时目录留垃圾
  assert.equal(r.file.startsWith(os.tmpdir()), true);
});

test('扫描框未开时诊断截图给出可读错误', async () => {
  const { service, ipcMain } = makeService();
  service.registerIpc();
  await assert.rejects(() => ipcMain.invoke('quizSearch:debugCapture'), /扫描框未开/);
});

test('未知扫描框 ID 被拒绝（不写默认设置）', async () => {
  const { service, ipcMain } = makeService();
  service.registerIpc();
  await assert.rejects(() => ipcMain.invoke('quizSearch:setBoxVisible', 'C', false), /未知扫描框/);
});

test('resetBoxes 只重置几何，保留用户当前显示/隐藏选择', async () => {
  const { service, ipcMain } = makeService();
  service.registerIpc();
  service.settings.boxes.A.x = 999;
  service.settings.boxes.A.visible = false;
  const s = await ipcMain.invoke('quizSearch:resetBoxes');
  assert.equal(s.settings.boxes.A.x, 80);
  assert.equal(s.settings.boxes.A.visible, false);
});

test('设置落盘为完整 JSON（原子写不留临时文件）', async () => {
  const { service, ipcMain, dir } = makeService();
  service.registerIpc();
  await ipcMain.invoke('quizSearch:setOptions', { interval: 1500 });
  const raw = fs.readFileSync(path.join(dir, 'settings.json'), 'utf8');
  assert.equal(JSON.parse(raw).interval, 1500);
  assert.deepEqual(
    fs.readdirSync(dir).filter((f) => f.endsWith('.tmp')),
    []
  );
});

// ---------------------------------------------------------------- 截屏源

test('多显示器下拿不到对应屏幕源：报错而不盲取 sources[0]', async () => {
  const otherDisplay = Object.assign({}, DISPLAY_1, { id: 2 });
  const { service } = makeService({
    deps: {
      screen: {
        getPrimaryDisplay: () => DISPLAY_1,
        getDisplayMatching: () => DISPLAY_1,
        getAllDisplays: () => [DISPLAY_1, otherDisplay],
        getCursorScreenPoint: () => ({ x: 0, y: 0 }),
      },
      desktopCapturer: {
        getSources: async () => [{ display_id: '2', thumbnail: fakeThumbnail(1920, 1080) }],
      },
    },
  });
  importBank(service);
  service.scanning = true;
  service._syncBoxWindows();
  const cap = await service._captureBox('A', null);
  assert.equal(cap, null);
  assert.match(service.lastTickError, /显示器/);
});

test('截屏裁剪尺寸与框几何一致', async () => {
  const { service } = makeService();
  importBank(service);
  service.scanning = true;
  service.settings.boxes.A = { x: 100, y: 200, w: 400, h: 100, visible: true };
  service._syncBoxWindows();
  const cap = await service._captureBox('A', null);
  assert.equal(cap.width, 400);
  assert.equal(cap.height, 100);
  assert.equal(cap.bitmap.length, 400 * 100 * 4);
});

test('同帧多框只抓一次屏（shotCache 复用）', async () => {
  const { service, getSourcesCalls } = makeService();
  importBank(service);
  service.scanning = true;
  service._syncBoxWindows();
  const cache = new Map();
  const a = await service._captureBox('A', cache);
  const b = await service._captureBox('B', cache);
  assert.ok(a && b);
  assert.equal(getSourcesCalls.length, 1);
});

test('state() 重复轮询不重读题库文件（索引对账结果有缓存）', () => {
  const { service } = makeService();
  importBank(service);
  const bankId = service.store.list()[0].id;
  const bankPath = path.join(service.store.banksDir, bankId + '.json');

  // 首次 state() 允许对账读取；之后计数题库文件读取次数
  service.state();
  const origRead = fs.readFileSync;
  let bankReads = 0;
  fs.readFileSync = function (p, ...rest) {
    if (String(p) === bankPath) bankReads++;
    return origRead.call(this, p, ...rest);
  };
  try {
    for (let i = 0; i < 5; i++) service.state();
  } finally {
    fs.readFileSync = origRead;
  }
  assert.equal(bankReads, 0, '轮询路径不得重复读题库文件');
  assert.equal(service.state().questionCount, 2);
});

test('题库变更后缓存失效，状态立即反映新题数', () => {
  const { service } = makeService();
  const first = importBank(service);
  assert.equal(service.state().questionCount, 2);
  service.store.setEnabled(first.id, false);
  assert.equal(service.state().questionCount, 0);
  service.store.remove(first.id);
  assert.equal(service.state().banks.length, 0);
});

test('把 B 框拖到主屏上方（y 为负）后落盘保留该位置，不当作「贴底」哨兵', () => {
  const { service } = makeService();
  importBank(service);
  service.registerIpc();
  service.scanning = true;
  service._syncBoxWindows();
  const win = service.boxWins.B;
  win.setBounds({ x: 200, y: -300, width: 700, height: 160 });
  service.el.ipcMain.emitMain('quizsearch:boxDragStart', 'B', 'move');
  service.el.ipcMain.emitMain('quizsearch:boxDragEnd');
  assert.equal(service.settings.boxes.B.y, -300);
  assert.equal(service.settings.boxes.B.x, 200);
});

test('B 框负 y 重启后不被判为贴底哨兵（settings.json 往返）', () => {
  const { service, dir } = makeService();
  importBank(service);
  service.settings.boxes.B = { x: 200, y: -300, w: 700, h: 160, visible: true };
  service._saveSettings();

  // 重启：同一 dataDir 重新构造服务
  const { deps } = makeDeps();
  const restarted = new QuizSearchService({
    deps,
    dataDir: dir,
    getMainWindow: () => null,
    workArea: DISPLAY_1.workArea,
  });
  CREATED.push(restarted);
  assert.equal(restarted.settings.boxes.B.y, -300);
  assert.equal(restarted.settings.boxes.B.x, 200);
});

// ---- 多显示器：钳制按所在显示器工作区，而不是主屏 ----
const DISPLAY_2 = {
  id: 2,
  bounds: { x: 1920, y: 0, width: 1280, height: 1024 },
  workArea: { x: 1920, y: 0, width: 1280, height: 984 },
  size: { width: 1280, height: 1024 },
  scaleFactor: 1,
};
const DISPLAY_LEFT = {
  id: 3,
  bounds: { x: -1600, y: -300, width: 1600, height: 900 },
  workArea: { x: -1600, y: -300, width: 1600, height: 860 },
  size: { width: 1600, height: 900 },
  scaleFactor: 1,
};

function multiDisplayScreen(matching) {
  return {
    getPrimaryDisplay: () => DISPLAY_1,
    getDisplayMatching: (bounds) => (matching ? matching(bounds) : DISPLAY_1),
    getAllDisplays: () => [DISPLAY_1, DISPLAY_2, DISPLAY_LEFT],
    getCursorScreenPoint: () => ({ x: 100, y: 100 }),
  };
}

test('副屏上的框宽高按所在显示器 workArea 钳制，不被主屏尺寸截断', () => {
  const { service } = makeService({
    deps: { screen: multiDisplayScreen(() => DISPLAY_2) },
  });
  service.registerIpc();
  service.scanning = true;
  service.settings.boxes.A = { x: 2000, y: 100, w: 900, h: 400, visible: true };
  service._syncBoxWindows();
  const win = service.boxWins.A;
  // 试图拖成比主屏工作区更宽的窗口：应放行到副屏工作区宽度
  win.setBounds({ x: 2000, y: 100, width: 1400, height: 500 });
  service.el.ipcMain.emitMain('quizsearch:boxDragStart', 'A', 'resize');
  service.el.ipcMain.emitMain('quizsearch:boxDragEnd');
  assert.equal(service.settings.boxes.A.w, DISPLAY_2.workArea.width, '按副屏工作区宽度钳制');
  assert.ok(service.settings.boxes.A.h <= DISPLAY_2.workArea.height);
  assert.equal(service.settings.boxes.A.x, 2000);
});

test('副屏上的结果窗几何按所在显示器 workArea 落盘', () => {
  const { service } = makeService({
    deps: { screen: multiDisplayScreen((bounds) => (bounds.x >= 1920 ? DISPLAY_2 : DISPLAY_1)) },
  });
  service.registerIpc();
  service._createResultWin();
  const win = service.resultWin;
  // 结果窗拖到副屏并放大到超过主屏工作区高度
  win.setBounds({ x: 2000, y: 60, width: 900, height: 900 });
  service.el.ipcMain.emitMain('quizsearch:resultDragStart', 'resize');
  service.el.ipcMain.emitMain('quizsearch:resultDragEnd');
  assert.equal(service.settings.result.w, 900, '未超副屏工作区宽度：原样保留');
  assert.equal(service.settings.result.h, 900, '未超副屏工作区高度：原样保留');
  assert.equal(service.settings.result.x, 2000);
});

test('负坐标副屏（主屏左上方的屏）同样按其 workArea 钳制', () => {
  const { service } = makeService({
    deps: { screen: multiDisplayScreen(() => DISPLAY_LEFT) },
  });
  service.registerIpc();
  service.scanning = true;
  service.settings.boxes.B = { x: -1500, y: -250, w: 600, h: 200, visible: true };
  service._syncBoxWindows();
  const win = service.boxWins.B;
  win.setBounds({ x: -1500, y: -250, width: 1700, height: 950 });
  service.el.ipcMain.emitMain('quizsearch:boxDragStart', 'B', 'resize');
  service.el.ipcMain.emitMain('quizsearch:boxDragEnd');
  assert.equal(service.settings.boxes.B.x, -1500, '负坐标原样保留');
  assert.equal(service.settings.boxes.B.y, -250);
  assert.equal(service.settings.boxes.B.w, DISPLAY_LEFT.workArea.width);
  assert.equal(service.settings.boxes.B.h, DISPLAY_LEFT.workArea.height);
});

test('resetBoxes 的默认几何按目标显示器工作区计算（副屏不贴到主屏底部）', () => {
  const { service, ipcMain } = makeService({
    deps: { screen: multiDisplayScreen((bounds) => (bounds.x >= 1920 ? DISPLAY_2 : DISPLAY_1)) },
  });
  service.registerIpc();
  service.scanning = true;
  service.settings.boxes.B = { x: 2000, y: 100, w: 600, h: 200, visible: true };
  service._syncBoxWindows();
  const win = service.boxWins.B;
  assert.ok(win);
  return ipcMain.invoke('quizSearch:resetBoxes').then((s) => {
    // 重置后 B 框落在副屏工作区内（或至少不被拽到主屏底部）
    assert.ok(
      s.settings.boxes.B.y !== DISPLAY_1.workArea.y + DISPLAY_1.workArea.height - 180 - 120
    );
  });
});

test('上次拖到副屏的结果窗重启后仍恢复到副屏，不被主屏边界拽回', () => {
  const { service } = makeService({
    deps: { screen: multiDisplayScreen((bounds) => (bounds.x >= 1920 ? DISPLAY_2 : DISPLAY_1)) },
  });
  service.settings.result = { x: 2100, y: 120, w: 500, h: 300 };
  FakeWindow.instances = [];
  service._createResultWin();
  const win = FakeWindow.instances[0];
  assert.equal(win.bounds.x, 2100, '结果窗应留在其保存的副屏位置');
  assert.equal(win.bounds.y, 120);
});

test('从未拖过的结果窗仍落在主屏右下角', () => {
  const { service } = makeService({
    deps: { screen: multiDisplayScreen(() => DISPLAY_LEFT) },
  });
  assert.deepEqual(service.settings.result, { x: -1, y: -1, w: 380, h: 260 });
  FakeWindow.instances = [];
  service._createResultWin();
  const win = FakeWindow.instances[0];
  assert.equal(win.bounds.x, DISPLAY_1.workArea.x + DISPLAY_1.workArea.width - 380 - 16);
  assert.equal(win.bounds.y, DISPLAY_1.workArea.y + DISPLAY_1.workArea.height - 260 - 16);
});

// 比主屏更宽的副屏：设置加载时不得把副屏上的框裁到主屏尺寸
const DISPLAY_WIDE = {
  id: 4,
  bounds: { x: 1920, y: 0, width: 2560, height: 1440 },
  workArea: { x: 1920, y: 0, width: 2560, height: 1400 },
  size: { width: 2560, height: 1440 },
  scaleFactor: 1,
};

test('设置加载时框宽高按所在显示器 workArea 钳制：副屏上的大框重启不被裁到主屏尺寸', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'quiz-svc-'));
  TEMP_DIRS.push(dir);
  fs.writeFileSync(
    path.join(dir, 'settings.json'),
    JSON.stringify({
      boxes: {
        A: { x: 2100, y: 100, w: 2400, h: 1200, visible: true },
        B: { x: 2100, y: 200, w: 2400, h: 1200, visible: true },
      },
    }),
    'utf8'
  );
  const { deps } = makeDeps({
    screen: {
      getPrimaryDisplay: () => DISPLAY_1,
      getDisplayMatching: () => DISPLAY_WIDE,
      getAllDisplays: () => [DISPLAY_1, DISPLAY_WIDE],
      getCursorScreenPoint: () => ({ x: 100, y: 100 }),
    },
  });
  const service = new QuizSearchService({
    deps,
    dataDir: dir,
    getMainWindow: () => null,
    workArea: DISPLAY_1.workArea,
  });
  CREATED.push(service);
  assert.equal(service.settings.boxes.A.w, 2400, '副屏工作区宽度 2560，2400 应原样保留');
  assert.equal(service.settings.boxes.A.h, 1200);
  assert.equal(service.settings.boxes.B.w, 2400);
});

test('脏坐标（字符串/缺宽高）加载设置不抛错，回退主屏兜底', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'quiz-svc-'));
  TEMP_DIRS.push(dir);
  fs.writeFileSync(
    path.join(dir, 'settings.json'),
    JSON.stringify({ boxes: { A: { x: 'left', y: null, w: 'wide' }, B: { y: -3 } } }),
    'utf8'
  );
  const { deps } = makeDeps({ screen: multiDisplayScreen(() => DISPLAY_1) });
  const service = new QuizSearchService({
    deps,
    dataDir: dir,
    getMainWindow: () => null,
    workArea: DISPLAY_1.workArea,
  });
  CREATED.push(service);
  assert.equal(service.settings.boxes.A.x, 80, '坏坐标回退默认值');
  assert.equal(service.settings.boxes.A.w, 720);
  assert.equal(service.settings.boxes.B.y, -3, '真实负坐标保留');
});
