// 独立图片窗口服务回归：窗口参数、IPC 来源、多选/目录授权、RPC 转发、预览 URL 生成、
// 默认应用引导与退出清理。全部用假 electron 依赖，不弹真实窗口。
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { EventEmitter } = require('events');

const { ImageToolsService } = require('../electron/imagetools/index.cjs');
// 真实路径期望值统一走产品口径（Windows native realpath 会把 8.3 短名归一成长名）。
// 用 legacy fs.realpathSync 当期望时，短名 TEMP（CI 的 C:\Users\RUNNER~1\…）会把短名当期望，误报失败。
const { realOrEmpty, resolveRealisticPath } = require('../electron/imagetools/path-guard.cjs');

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
    this.maximized = false;
    this.fullscreen = false;
    this.bounds = { x: 0, y: 0, width: 1280, height: 800 };
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
  minimize() {
    this.calls.push('minimize');
  }
  isMinimized() {
    return false;
  }
  restore() {
    this.calls.push('restore');
  }
  maximize() {
    this.maximized = true;
    this.emit('maximize');
  }
  unmaximize() {
    this.maximized = false;
    this.emit('unmaximize');
  }
  isMaximized() {
    return this.maximized;
  }
  isFullScreen() {
    return this.fullscreen;
  }
  getContentSize() {
    return [this.bounds.width, this.bounds.height];
  }
  getBounds() {
    return { ...this.bounds };
  }
  setBounds(next) {
    this.bounds = { ...next };
    this.calls.push('setBounds');
  }
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
  }
  handle(channel, fn) {
    if (this.handlers.has(channel)) throw new Error('重复注册: ' + channel);
    this.handlers.set(channel, fn);
  }
  removeHandler(channel) {
    this.handlers.delete(channel);
  }
}

const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'uvmp-imgservice-'));
const DIR = path.join(TMP, '相册 一');
const OUT = path.join(TMP, '外部');
for (const d of [DIR, OUT]) fs.mkdirSync(d, { recursive: true });
const w = (p, body = 'x') => {
  fs.writeFileSync(p, body);
  return p;
};
const IMG1 = w(path.join(DIR, '一.jpg'), 'jpg-bytes');
const IMG2 = w(path.join(DIR, '二.png'), 'png-bytes');
const SECRET = w(path.join(OUT, '私密.jpg'), 'secret');
test.after(() => fs.rmSync(TMP, { recursive: true, force: true }));

function makeService(options = {}) {
  FakeWindow.instances = [];
  const ipcMain = new FakeIpcMain();
  const mainContents = new FakeWebContents();
  const mainWindow = {
    isDestroyed: () => false,
    webContents: mainContents,
    isMinimized: () => false,
  };
  const opened = [];
  const closed = [];
  const execCalls = [];
  const calls = [];
  const bridge = {
    call: async (method, params) => {
      calls.push({ method, params });
      if (options.bridge) {
        const custom = options.bridge[method];
        if (custom) return custom(params);
      }
      if (method === 'getConfig') return { image_tools: { quick_phrases: ['_待复核', '_已核'] } };
      if (method === 'saveConfig') return params;
      if (method === 'imageList') {
        return {
          directory: path.dirname(params.path),
          files: [
            { name: '一.jpg', path: IMG1, ext: '.jpg', size: 9, supported: true },
            { name: '二.png', path: IMG2, ext: '.png', size: 9, supported: true },
          ],
        };
      }
      if (method === 'imageInspect') return { path: params.path, width: 10, height: 20 };
      if (method === 'imagePreview')
        return { path: params.path, mime: 'image/jpeg', width: 10, height: 20, animated: false };
      if (method === 'imageRename') return { path: params.path, name: '新名.jpg' };
      if (method === 'imageProcess') return { job_id: 'job-1' };
      if (method === 'getJob') return { id: params.job_id, status: 'running' };
      if (method === 'cancelJob') return { ok: true };
      return {};
    },
  };
  const service = new ImageToolsService({
    bridge,
    platform: options.platform || 'win32',
    desktopFile: options.desktopFile,
    deps: {
      BrowserWindow: FakeWindow,
      ipcMain,
      dialog: {
        showOpenDialog: async (parent, opts) => {
          opened.push(opts);
          return options.dialogResult
            ? options.dialogResult(opts)
            : { canceled: true, filePaths: [] };
        },
      },
      screen: {
        getDisplayMatching: () => ({
          id: 1,
          scaleFactor: 2,
          size: { width: 2560, height: 1440 },
          bounds: { x: 0, y: 0, width: 2560, height: 1440 },
          workArea: { x: 0, y: 0, width: 2560, height: 1392 },
        }),
        on: () => {},
        removeListener: () => {},
      },
      shell: {
        showItemInFolder: (p) => calls.push({ method: 'showItemInFolder', params: p }),
        openExternal: async (url) => {
          calls.push({ method: 'openExternal', params: url });
          return undefined;
        },
      },
      app: {
        isPackaged: options.isPackaged !== false,
        getVersion: () => '2.1.1',
      },
      execFile: (cmd, args, opts, cb) => {
        const handler = options.execFile || ((c, a, cb2) => cb2(null, ''));
        execCalls.push({ cmd, args });
        handler(cmd, args, cb);
      },
    },
  });
  return { service, ipcMain, mainContents, opened, closed, calls, execCalls };
}

const trusted = (service) => ({ sender: service.win.webContents });
const handler = (ipcMain, channel) => {
  const fn = ipcMain.handlers.get(channel);
  assert.equal(typeof fn, 'function', '应注册 ' + channel);
  return fn;
};

test('窗口参数：无边框系统边界 1280×800、最小 900×600、thickFrame=true 且不裁形状', () => {
  const { service } = makeService();
  service.open({});
  assert.equal(FakeWindow.instances.length, 1);
  const win = FakeWindow.instances[0];
  const opts = win.opts;
  assert.equal(opts.width, 1280);
  assert.equal(opts.height, 800);
  assert.equal(opts.minWidth, 900);
  assert.equal(opts.minHeight, 600);
  assert.equal(opts.frame, false, '图片窗口与主窗口一致：无系统顶栏，用共享 TitleBar');
  assert.equal(opts.thickFrame, true, '外框/阴影/边缘缩放交给 Windows，避免自绘圆角黑角');
  assert.equal(opts.resizable, true);
  assert.equal(opts.show, false, '先隐藏，ready-to-show 再显示');
  assert.match(opts.webPreferences.preload.replace(/\\/g, '/'), /imagetools\/preload\.cjs$/);
  assert.equal(opts.webPreferences.contextIsolation, true);
  assert.equal(opts.webPreferences.nodeIntegration, false);
  assert.equal(
    opts.webPreferences.sandbox,
    true,
    '图片窗口 preload 不 require 外部模块，必须开沙箱'
  );
  assert.equal(opts.title, '图片工具');
  // 不再裁窗口形状：测试桩上的 setShape 未被调用，系统外框即最终形状
  assert.equal(service.chrome, undefined, '不应再挂共享形状绑定');
  assert.equal(win.shapeCalls, undefined, '不应再有形状调度记录');
  // 复用同一个窗口，不重复建窗
  service.open({});
  assert.equal(FakeWindow.instances.length, 1);
  assert.ok(FakeWindow.instances[0].calls.includes('focus'));
});

test('窗口目标：开发态走 Vite 的 imagetools.html，打包态走 dist/imagetools.html', () => {
  const dev = makeService({ isPackaged: false });
  const saved = process.env.ELECTRON_DEV_URL;
  process.env.ELECTRON_DEV_URL = 'http://127.0.0.1:5173';
  try {
    dev.service.open({});
    assert.equal(
      FakeWindow.instances[0].webContents.loaded.url,
      'http://127.0.0.1:5173/imagetools.html'
    );
  } finally {
    if (saved === undefined) delete process.env.ELECTRON_DEV_URL;
    else process.env.ELECTRON_DEV_URL = saved;
  }
  const packed = makeService({ isPackaged: true });
  packed.service.open({});
  assert.match(
    FakeWindow.instances[0].webContents.loaded.file.replace(/\\/g, '/'),
    /desktop\/dist\/imagetools\.html$/
  );
});

test('open：授权所选文件的父目录，加载完成后下发首帧；已加载窗口立即下发', () => {
  const { service } = makeService();
  service.open({ files: [IMG1, SECRET] });
  const win = FakeWindow.instances[0];
  assert.deepEqual(win.webContents.sent, [], '加载完成前不推送');
  assert.equal(service.pathGuard.check(IMG1), realOrEmpty(IMG1));
  assert.equal(service.pathGuard.check(SECRET), realOrEmpty(SECRET), '双击的文件同样授权其父目录');

  win.webContents.emit('did-finish-load');
  const first = win.webContents.sent.find((m) => m.channel === 'imagetools:open');
  assert.ok(first, '加载完成必须下发 imagetools:open');
  assert.deepEqual(first.payload.files, [realOrEmpty(IMG1), realOrEmpty(SECRET)]);

  win.webContents.sent.length = 0;
  service.open({ files: [IMG2] });
  const second = win.webContents.sent.find((m) => m.channel === 'imagetools:open');
  assert.deepEqual(second.payload.files, [realOrEmpty(IMG2)]);
  assert.ok(win.calls.includes('show'));
});

test('ready：面补齐冷启动竞态——渲染层晚订阅时也能拿到当前待开文件', async () => {
  const { service, ipcMain } = makeService();
  service.registerIpc();
  service.open({ files: [IMG1] });
  const ready = handler(ipcMain, 'imagetools:ready');
  // 主进程在 did-finish-load 前就记下了待开文件：页面挂载晚于该消息时靠 ready 取回
  assert.deepEqual(await ready(trusted(service)), { files: [realOrEmpty(IMG1)] });
  // 未打开过任何文件时给出空列表，不报错
  const empty = makeService();
  empty.service.registerIpc();
  empty.service.open({});
  assert.deepEqual(await handler(empty.ipcMain, 'imagetools:ready')(trusted(empty.service)), {
    files: [],
  });
  // 来源校验
  await assert.rejects(() => ready({ sender: new FakeWebContents() }), /来源/);
});

test('openPaths 过滤非图片与重复项，空列表也能开窗口', () => {
  const { service } = makeService();
  const txt = w(path.join(DIR, '说明.txt'));
  service.openPaths([IMG1, IMG1, txt, path.join(DIR, '不存在.jpg')]);
  const win = FakeWindow.instances[0];
  win.webContents.emit('did-finish-load');
  const payload = win.webContents.sent.find((m) => m.channel === 'imagetools:open').payload;
  assert.deepEqual(payload.files, [realOrEmpty(IMG1)]);

  service.openPaths([]);
  win.webContents.sent.length = 0;
  service.openPaths([]);
  const empty = win.webContents.sent.find((m) => m.channel === 'imagetools:open');
  assert.deepEqual(empty.payload.files, []);
});

test('IPC 来源：只接受图片窗口；主窗口与陌生 contents 一律拒绝', async () => {
  const { service, ipcMain, mainContents } = makeService();
  service.registerIpc();
  service.open({});
  const getConfig = handler(ipcMain, 'imagetools:getConfig');
  await assert.rejects(() => getConfig({ sender: mainContents }), /来源/);
  await assert.rejects(() => getConfig({ sender: new FakeWebContents() }), /来源/);
  await assert.rejects(() => getConfig({}), /来源/);
  const cfg = await getConfig(trusted(service));
  assert.deepEqual(cfg.quick_phrases, ['_待复核', '_已核']);
});

test('pickFiles：多选 + 兜底所有文件过滤器，授权父目录并返回路径数组', async () => {
  const { service, ipcMain, opened } = makeService({
    dialogResult: () => ({ canceled: false, filePaths: [IMG1, IMG2] }),
  });
  service.registerIpc();
  service.open({});
  const res = await handler(ipcMain, 'imagetools:pickFiles')(trusted(service));
  assert.deepEqual(res, [realOrEmpty(IMG1), realOrEmpty(IMG2)]);
  const opts = opened.at(-1);
  assert.deepEqual(opts.properties, ['openFile', 'multiSelections']);
  assert.equal(opts.filters.at(-1).extensions[0], '*', '麒麟对话框兜底所有文件');
  assert.ok(opts.filters.some((f) => f.extensions.includes('jpg')));
  assert.equal(service.pathGuard.check(IMG1), realOrEmpty(IMG1));
  // 选中后即可用 list 取同目录列表（切图依据）
  const listing = await handler(ipcMain, 'imagetools:list')(trusted(service), res[0]);
  // 产品返回的是真实目录（native realpath，8.3 短名已归一），期望值必须同口径
  assert.equal(listing.directory, realOrEmpty(path.dirname(IMG1)));

  // 取消返回 null，不授权任何路径
  const cancel = makeService();
  cancel.service.registerIpc();
  cancel.service.open({});
  const none = await handler(cancel.ipcMain, 'imagetools:pickFiles')(trusted(cancel.service));
  assert.equal(none, null);
});

test('pickDirectory：授权目录并返回目录内文件列表', async () => {
  const { service, ipcMain, opened } = makeService({
    dialogResult: () => ({ canceled: false, filePaths: [DIR] }),
  });
  service.registerIpc();
  service.open({});
  const res = await handler(ipcMain, 'imagetools:pickDirectory')(trusted(service));
  assert.equal(res.directory, realOrEmpty(DIR));
  assert.deepEqual(opened.at(-1).properties, ['openDirectory']);
  assert.equal(service.pathGuard.check(IMG2), realOrEmpty(IMG2));
  assert.equal(service.pathGuard.check(SECRET), '', '目录授权不得放开其它目录');
});

test('list/inspect：路径必须先授权，授权后转发到核心', async () => {
  const { service, ipcMain, calls } = makeService();
  service.registerIpc();
  service.open({});
  const list = handler(ipcMain, 'imagetools:list');
  const inspect = handler(ipcMain, 'imagetools:inspect');
  await assert.rejects(() => list(trusted(service), SECRET), /未授权/);
  await assert.rejects(() => inspect(trusted(service), SECRET), /未授权/);

  service.pathGuard.authorizeFile(IMG1);
  const res = await list(trusted(service), IMG1);
  assert.equal(res.directory, realOrEmpty(path.dirname(IMG1)));
  assert.equal(res.files.length, 2);
  const info = await inspect(trusted(service), IMG1);
  assert.equal(info.width, 10);
  assert.equal(calls.find((c) => c.method === 'imageList').params.path, realOrEmpty(IMG1));
});

test('list：同目录兄弟图片可列，授权目录的子目录也可列，目录外拒绝', async () => {
  const { service, ipcMain, calls } = makeService();
  service.registerIpc();
  service.open({});
  const list = handler(ipcMain, 'imagetools:list');
  service.pathGuard.authorizeFile(IMG1);
  const sub = path.join(DIR, '子目录');
  fs.mkdirSync(sub, { recursive: true });
  const res = await list(trusted(service), sub);
  assert.equal(res.directory, realOrEmpty(path.dirname(IMG1)));
  // list 转发的是产品解析后的真实目录（native realpath），期望值同口径
  assert.equal(calls.at(-1).params.path, resolveRealisticPath(sub));
  await assert.rejects(() => list(trusted(service), OUT), /未授权/);
});

test('preview：真实路径编码进 shade-image://local URL；未授权、越界与文件被替换都拒绝', async () => {
  const { service, ipcMain } = makeService();
  service.registerIpc();
  service.open({});
  const preview = handler(ipcMain, 'imagetools:preview');
  const proto = service.protocolHandler;
  await assert.rejects(() => preview(trusted(service), SECRET), /未授权/);

  service.pathGuard.authorizeFile(IMG1);
  const real1 = realOrEmpty(IMG1);
  const r1 = await preview(trusted(service), IMG1);
  assert.equal(r1.url, 'shade-image://local?path=' + encodeURIComponent(real1));
  assert.doesNotMatch(r1.url, /^shade-image:\/\/[a-f0-9]{48}/, 'URL 里不得再有随机令牌');
  assert.equal(r1.mime, 'image/jpeg');
  assert.equal(r1.width, 10);
  assert.equal(r1.path, undefined, '不下发真实路径字段，渲染层用协议 URL');
  assert.equal(service.tokens, undefined, '服务不再持有令牌表');

  // 协议处理器按 URL 重新授权校验后流式返回字节
  const streamed = await proto(new Request(r1.url));
  assert.equal(streamed.status, 200);
  assert.equal(await streamed.text(), 'jpg-bytes');

  // URL 由真实路径确定，不再随机：同文件重复预览结果稳定
  const r2 = await preview(trusted(service), IMG1);
  assert.equal(r2.url, r1.url);

  // 文件被替换成外部符号链接：URL 未变，每次请求重新 realpath 后拒绝
  const swap = w(path.join(DIR, 'swap.png'), 'swap-bytes');
  service.pathGuard.authorizeFile(swap);
  const r3 = await preview(trusted(service), swap);
  assert.equal(r3.url, 'shade-image://local?path=' + encodeURIComponent(realOrEmpty(swap)));
  assert.equal((await proto(new Request(r3.url))).status, 200);
  fs.rmSync(swap);
  try {
    fs.symlinkSync(SECRET, swap);
  } catch (_e) {
    return;
  }
  assert.equal((await proto(new Request(r3.url))).status, 404, '外部替换后旧 URL 立即失效');
  await assert.rejects(() => preview(trusted(service), swap), /未授权/);
});

test('preview：核心返回的缓存预览路径精确授权后同样可读；未返回路径时回退入参原图', async () => {
  const cache = w(path.join(OUT, 'preview-cache.png'), 'cache-bytes');
  const { service, ipcMain } = makeService({
    bridge: { imagePreview: () => ({ path: cache, mime: 'image/png', width: 5, height: 6 }) },
  });
  service.registerIpc();
  service.open({});
  const preview = handler(ipcMain, 'imagetools:preview');
  service.pathGuard.authorizeFile(IMG1);

  const res = await preview(trusted(service), IMG1);
  assert.equal(res.url, 'shade-image://local?path=' + encodeURIComponent(realOrEmpty(cache)));
  assert.equal(res.name, path.basename(IMG1));
  assert.equal(res.ext, '.jpg');
  assert.equal(res.path, undefined);
  const streamed = await service.protocolHandler(new Request(res.url));
  assert.equal(await streamed.text(), 'cache-bytes');
  assert.equal(service.pathGuard.check(SECRET), '', '只授权缓存文件本身，不放开所在目录');

  // 核心只回 mime 时按现有流程回退到已授权原图
  const fallback = makeService({ bridge: { imagePreview: () => ({ mime: 'image/jpeg' }) } });
  fallback.service.registerIpc();
  fallback.service.open({});
  fallback.service.pathGuard.authorizeFile(IMG1);
  const plain = await handler(fallback.ipcMain, 'imagetools:preview')(
    trusted(fallback.service),
    IMG1
  );
  assert.equal(plain.url, 'shade-image://local?path=' + encodeURIComponent(realOrEmpty(IMG1)));
});

test('rename：授权后转发 imageRename，新文件仍在授权目录内可继续查看', async () => {
  const { service, ipcMain, calls } = makeService();
  service.registerIpc();
  service.open({});
  const rename = handler(ipcMain, 'imagetools:rename');
  await assert.rejects(() => rename(trusted(service), SECRET, '新名'), /未授权/);
  service.pathGuard.authorizeFile(IMG1);
  const res = await rename(trusted(service), IMG1, '新名');
  assert.equal(res.name, '新名.jpg');
  const call = calls.find((c) => c.method === 'imageRename');
  assert.equal(call.params.name, '新名');
  assert.equal(call.params.path, realOrEmpty(IMG1));
  await assert.rejects(() => rename(trusted(service), IMG1, ''), /名称/);
});

test('runProcess：逐个复核文件授权，任一未授权即整体拒绝且不下发任务', async () => {
  const { service, ipcMain, calls } = makeService();
  service.registerIpc();
  service.open({});
  const run = handler(ipcMain, 'imagetools:runProcess');
  service.pathGuard.authorizeFile(IMG1);
  await assert.rejects(
    () =>
      run(trusted(service), {
        operation: 'convert',
        files: [IMG1, SECRET],
        options: { format: 'png' },
      }),
    /未授权/
  );
  assert.equal(
    calls.some((c) => c.method === 'imageProcess'),
    false
  );

  const res = await run(trusted(service), {
    operation: 'compress',
    files: [IMG1],
    options: {},
    output_dir: path.join(DIR, '导出'),
  });
  assert.equal(res.job_id, 'job-1');
  const call = calls.find((c) => c.method === 'imageProcess');
  assert.equal(call.params.operation, 'compress');
  assert.deepEqual(call.params.files, [realOrEmpty(IMG1)]);
  assert.equal(service.isJobTracked('job-1'), true);
  // 非法 operation 不接受
  await assert.rejects(() => run(trusted(service), { operation: 'rm', files: [IMG1] }), /操作/);
});

test('runProcess：输出目录必须在已授权目录内（含尚不存在子目录），符号链接逃逸拒绝', async () => {
  const { service, ipcMain, calls } = makeService();
  service.registerIpc();
  service.open({});
  const run = handler(ipcMain, 'imagetools:runProcess');
  service.pathGuard.authorizeFile(IMG1);

  // 尚不存在的子目录允许（核心负责创建）
  const outSub = path.join(DIR, '导出', '批次 1');
  await run(trusted(service), {
    operation: 'convert',
    files: [IMG1],
    options: {},
    output_dir: outSub,
  });
  // 输出目录可能是尚不存在的子目录：产品按「最深已存在祖先 realpath + 剩余段」解析后转发
  assert.equal(
    calls.filter((c) => c.method === 'imageProcess').at(-1).params.output_dir,
    resolveRealisticPath(outSub)
  );

  // 目录外一律拒绝
  await assert.rejects(
    () => run(trusted(service), { operation: 'convert', files: [IMG1], output_dir: OUT }),
    /未授权的输出目录/
  );
  // 授权目录内的符号链接指向外部：按真实位置判越界
  const escape = path.join(DIR, '外链');
  try {
    fs.symlinkSync(OUT, escape);
  } catch (_e) {
    return;
  }
  await assert.rejects(
    () => run(trusted(service), { operation: 'convert', files: [IMG1], output_dir: escape }),
    /未授权的输出目录/
  );
});

test('job 事件：只把本窗口发起的 image_tools 任务事件转给图片窗口', () => {
  const { service } = makeService();
  service.open({});
  const win = FakeWindow.instances[0];
  service.trackJob('job-1');
  win.webContents.sent.length = 0;
  assert.equal(
    service.handleCoreEvent({ event: 'job.progress', job_id: 'job-1', percent: 40 }),
    true
  );
  assert.deepEqual(win.webContents.sent.at(-1), {
    channel: 'imagetools:event',
    payload: { event: 'job.progress', job_id: 'job-1', percent: 40 },
  });
  assert.equal(service.handleCoreEvent({ event: 'job.progress', job_id: 'other-job' }), false);
  assert.equal(win.webContents.sent.length, 1);
  // 关窗后不再投递
  win.close();
  assert.equal(service.handleCoreEvent({ event: 'job.done', job_id: 'job-1' }), false);
});

test('创建期事件缓冲：imageProcess 返回前发出的 job.log/progress/done 不丢，按序回放', async () => {
  // 真实核心在建任务的同一瞬间就写「开始处理」并可能推进进度甚至完成；
  // 主进程此时还没拿到 job_id（runProcess 要等 RPC 返回才 trackJob），旧实现直接丢弃。
  let svc = null;
  const { service, ipcMain } = makeService({
    bridge: {
      imageProcess: () => {
        // 同步发出：主进程此刻尚未掌握 job_id
        svc.handleCoreEvent({
          event: 'job.log',
          job_id: 'job-77',
          line: '[10:00:00] 开始处理 4 项 · 尺寸修改 · percent · 并发 1',
        });
        svc.handleCoreEvent({ event: 'job.progress', job_id: 'job-77', current: 0, total: 4 });
        svc.handleCoreEvent({ event: 'job.log', job_id: 'job-99', line: '别的窗口任务' });
        svc.handleCoreEvent({ event: 'job.done', job_id: 'job-77', job: { id: 'job-77' } });
        return { job_id: 'job-77' };
      },
    },
  });
  svc = service;
  service.registerIpc();
  service.open({});
  service.pathGuard.authorizeFile(IMG1);
  const win = FakeWindow.instances[0];
  win.webContents.sent.length = 0;

  const res = await handler(ipcMain, 'imagetools:runProcess')(trusted(service), {
    operation: 'resize',
    files: [IMG1],
    options: {},
  });
  assert.equal(res.job_id, 'job-77');
  const delivered = win.webContents.sent
    .filter((m) => m.channel === 'imagetools:event')
    .map((m) => m.payload);
  assert.deepEqual(
    delivered.map((p) => p.event + ':' + p.job_id),
    ['job.log:job-77', 'job.progress:job-77', 'job.done:job-77'],
    '本任务事件必须按到达顺序完整回放，其他任务事件丢弃'
  );
  assert.equal(
    delivered.some((p) => p.job_id === 'job-99'),
    false,
    '不得投递非本任务事件'
  );
  assert.equal(service.isJobTracked('job-77'), true);
  assert.equal(service.isJobTracked('job-99'), false);
});

test('创建期缓冲：imageProcess 失败时清理 pending，不遗留、不误投后续任务', async () => {
  let svc = null;
  let fail = true;
  const { service, ipcMain } = makeService({
    bridge: {
      imageProcess: () => {
        svc.handleCoreEvent({ event: 'job.log', job_id: 'job-err', line: '开始处理 1 项' });
        if (fail) throw new Error('核心拒绝：磁盘已满');
        return { job_id: 'job-err' };
      },
    },
  });
  svc = service;
  service.registerIpc();
  service.open({});
  service.pathGuard.authorizeFile(IMG1);
  const win = FakeWindow.instances[0];
  const run = handler(ipcMain, 'imagetools:runProcess');

  await assert.rejects(
    () => run(trusted(service), { operation: 'convert', files: [IMG1], options: {} }),
    /磁盘已满/
  );
  win.webContents.sent.length = 0;
  // 失败后 pending 必须已清理：同任务号的迟到事件不得被缓冲，也不得投递
  assert.equal(service.handleCoreEvent({ event: 'job.log', job_id: 'job-err', line: '迟到' }), false);
  assert.equal(win.webContents.sent.length, 0, '失败后不得遗留或误投 buffered 事件');
  assert.equal(service.isJobTracked('job-err'), false);

  // 下一轮成功：只回放本轮事件，上一轮的遗留不得混进来
  fail = false;
  const res = await run(trusted(service), { operation: 'convert', files: [IMG1], options: {} });
  assert.equal(res.job_id, 'job-err');
  const delivered = win.webContents.sent
    .filter((m) => m.channel === 'imagetools:event')
    .map((m) => m.payload.line);
  assert.deepEqual(delivered, ['开始处理 1 项'], '只回放本轮 buffered 事件');
});

test('创建期缓冲：已有 pending 时拒绝并发创建第二个任务（明确报错）', async () => {
  let release = null;
  let svc = null;
  const { service, ipcMain } = makeService({
    bridge: {
      imageProcess: () => {
        svc.handleCoreEvent({ event: 'job.log', job_id: 'job-slow', line: '开始处理 1 项' });
        return new Promise((resolve) => {
          release = () => resolve({ job_id: 'job-slow' });
        });
      },
    },
  });
  svc = service;
  service.registerIpc();
  service.open({});
  service.pathGuard.authorizeFile(IMG1);
  const run = handler(ipcMain, 'imagetools:runProcess');

  const first = run(trusted(service), { operation: 'convert', files: [IMG1], options: {} });
  await new Promise((r) => setTimeout(r, 30));
  await assert.rejects(
    () => run(trusted(service), { operation: 'convert', files: [IMG1], options: {} }),
    /任务|并发|进行中/,
    'must reject a second concurrent creation explicitly'
  );
  release();
  const res = await first;
  assert.equal(res.job_id, 'job-slow');
  assert.equal(service.isJobTracked('job-slow'), true);
});

test('创建期缓冲：core.exit 仍立即转发，关窗/dispose 清理 pending 不泄漏', async () => {
  let svc = null;
  let release = null;
  const { service } = makeService({
    bridge: {
      imageProcess: () => {
        svc.handleCoreEvent({ event: 'job.log', job_id: 'job-x', line: '开始处理 1 项' });
        return new Promise((resolve) => {
          release = () => resolve({ job_id: 'job-x' });
        });
      },
    },
  });
  svc = service;
  service.open({});
  service.pathGuard.authorizeFile(IMG1);
  const win = FakeWindow.instances[0];

  const pendingRun = service.runProcess({ operation: 'convert', files: [IMG1], options: {} });
  await new Promise((r) => setTimeout(r, 30));
  // core.exit 不受缓冲影响，必须立刻投递
  win.webContents.sent.length = 0;
  assert.equal(service.handleCoreEvent({ event: 'core.exit', code: 0 }), true);
  assert.equal(win.webContents.sent.at(-1).payload.event, 'core.exit');

  // 关窗：pending 必须清理，之后迟到的 buffered 事件不得再投递
  win.close();
  release();
  await pendingRun;
  win.webContents.sent.length = 0;
  assert.equal(service.handleCoreEvent({ event: 'job.log', job_id: 'job-x', line: '迟到' }), false);
  assert.equal(win.webContents.sent.length, 0);

  // dispose 同样不泄漏
  service.open({});
  service.pathGuard.authorizeFile(IMG1);
  const run2 = service.runProcess({ operation: 'convert', files: [IMG1], options: {} });
  await new Promise((r) => setTimeout(r, 30));
  service.dispose();
  release();
  await run2;
  assert.equal(service.handleCoreEvent({ event: 'job.log', job_id: 'job-x', line: '迟到2' }), false);
});

test('创建期缓冲：建任务期间关窗后不得把任务号登记回新窗口（无跨窗口泄漏）', async () => {
  let svc = null;
  let release = null;
  const { service } = makeService({
    bridge: {
      imageProcess: () => {
        svc.handleCoreEvent({ event: 'job.log', job_id: 'job-leak', line: '开始处理 1 项' });
        return new Promise((resolve) => {
          release = () => resolve({ job_id: 'job-leak' });
        });
      },
    },
  });
  svc = service;
  service.open({});
  service.pathGuard.authorizeFile(IMG1);
  const oldWin = FakeWindow.instances[0];

  const pendingRun = service.runProcess({ operation: 'convert', files: [IMG1], options: {} });
  await new Promise((r) => setTimeout(r, 30));
  // 建任务期间关掉旧窗，再开一个新窗（同一 service 实例，jobs 已在关窗时清空）
  oldWin.close();
  service.open({});
  const newWin = FakeWindow.instances.at(-1);
  assert.notEqual(newWin, oldWin);
  release();
  await pendingRun;

  newWin.webContents.sent.length = 0;
  // 旧窗口的任务号不得被登记：迟到事件既不能投给新窗口，也不能被当成已跟踪
  assert.equal(service.isJobTracked('job-leak'), false, '关窗后不得重新登记旧任务号');
  assert.equal(service.handleCoreEvent({ event: 'job.log', job_id: 'job-leak', line: '迟到3' }), false);
  assert.equal(newWin.webContents.sent.length, 0, '旧任务事件不得投给新窗口');
});

test('核心冷启动：未就绪的桥调用按退避重试，就绪后自动成功', async () => {
  let attempts = 0;
  const delays = [];
  const { service, ipcMain } = makeService({
    bridge: {
      imageList: async () => {
        attempts += 1;
        if (attempts <= 3) throw new Error('核心进程未启动');
        return { directory: '/x', files: [] };
      },
    },
  });
  // 注入等待函数：只记录退避时长，不真的等
  service._retryWait = async (ms) => delays.push(ms);
  service.registerIpc();
  service.open({});
  service.pathGuard.authorizeFile(IMG1);
  const res = await handler(ipcMain, 'imagetools:list')(trusted(service), IMG1);
  assert.deepEqual(res, { directory: '/x', files: [] });
  assert.equal(attempts, 4, '前 3 次未就绪应重试，第 4 次成功');
  assert.equal(delays.length, 3);
  assert.ok(
    delays.every((ms, i) => ms > 0 && (i === 0 || ms >= delays[i - 1])),
    '退避应递增: ' + JSON.stringify(delays)
  );
  assert.ok(delays.at(-1) <= 5000, '单次退避有上限，不会卡住界面: ' + delays.at(-1));
});

test('核心冷启动：超过重试上限后把最后一次错误抛给渲染层（不无限等待）', async () => {
  let attempts = 0;
  const { service, ipcMain } = makeService({
    bridge: {
      imageInspect: async () => {
        attempts += 1;
        throw new Error('核心进程未启动');
      },
    },
  });
  const delays = [];
  service._retryWait = async (ms) => delays.push(ms);
  service.registerIpc();
  service.open({});
  service.pathGuard.authorizeFile(IMG1);
  await assert.rejects(
    () => handler(ipcMain, 'imagetools:inspect')(trusted(service), IMG1),
    /核心进程未启动/
  );
  assert.equal(attempts, delays.length + 1, '每次重试都有对应退避');
  assert.ok(attempts > 3, '应确实重试了多次: ' + attempts);
  assert.ok(attempts <= 20, '重试次数有界: ' + attempts);
  assert.ok(
    delays.reduce((sum, ms) => sum + ms, 0) <= 40000,
    '总等待有界（不超过 40 秒）: ' + delays.reduce((sum, ms) => sum + ms, 0)
  );
});

test('核心冷启动：非「未就绪」类错误立即抛出，不做无谓重试', async () => {
  let attempts = 0;
  const { service, ipcMain } = makeService({
    bridge: {
      imageInspect: async () => {
        attempts += 1;
        throw new Error('IMG_UNSUPPORTED: 暂不支持该格式');
      },
    },
  });
  service._retryWait = async () => {
    throw new Error('不应等待');
  };
  service.registerIpc();
  service.open({});
  service.pathGuard.authorizeFile(IMG1);
  await assert.rejects(
    () => handler(ipcMain, 'imagetools:inspect')(trusted(service), IMG1),
    /IMG_UNSUPPORTED/
  );
  assert.equal(attempts, 1, '内容类错误只试一次');
});

test('配置：getConfig 只回图片段；saveQuickPhrases 校验条数/长度/非法字符', async () => {
  const { service, ipcMain, calls } = makeService();
  service.registerIpc();
  service.open({});
  const save = handler(ipcMain, 'imagetools:saveQuickPhrases');
  const ok = await save(trusted(service), ['_待复核', '客户 已核']);
  assert.deepEqual(ok.quick_phrases, ['_待复核', '客户 已核']);
  const call = calls.find((c) => c.method === 'saveConfig');
  assert.deepEqual(call.params, { image_tools: { quick_phrases: ['_待复核', '客户 已核'] } });

  await assert.rejects(() => save(trusted(service), ['a/b']), /非法字符/);
  await assert.rejects(() => save(trusted(service), ['a'.repeat(41)]), /40/);
  await assert.rejects(() => save(trusted(service), new Array(13).fill('x')), /12/);
  await assert.rejects(() => save(trusted(service), 'x'), /数组/);
  await assert.rejects(() => save(trusted(service), ['  ']), /不能为空/);
});

test('showItem：只允许定位已授权文件的所在目录', async () => {
  const { service, ipcMain, calls } = makeService();
  service.registerIpc();
  service.open({});
  const show = handler(ipcMain, 'imagetools:showItem');
  await assert.rejects(() => show(trusted(service), SECRET), /未授权/);
  service.pathGuard.authorizeFile(IMG1);
  await show(trusted(service), IMG1);
  assert.deepEqual(
    calls.find((c) => c.method === 'showItemInFolder'),
    {
      method: 'showItemInFolder',
      params: realOrEmpty(IMG1),
    }
  );
  // 目录本身也可定位（打开所在文件夹）
  await show(trusted(service), DIR);
  assert.equal(calls.filter((c) => c.method === 'showItemInFolder').length, 2);
});

test('默认应用：Windows 打开 ms-settings 并报告不可静默改写；非 Windows 不碰系统设置', async () => {
  const win = makeService({ platform: 'win32' });
  win.service.registerIpc();
  win.service.open({});
  const open = handler(win.ipcMain, 'imagetools:openDefaultSettings');
  const res = await open(trusted(win.service));
  assert.equal(res.ok, true);
  assert.deepEqual(
    win.calls.filter((c) => c.method === 'openExternal'),
    [{ method: 'openExternal', params: 'ms-settings:defaultapps' }]
  );

  const linux = makeService({ platform: 'linux' });
  linux.service.registerIpc();
  linux.service.open({});
  const resLinux = await handler(
    linux.ipcMain,
    'imagetools:openDefaultSettings'
  )(trusted(linux.service));
  assert.equal(resLinux.ok, false, 'Linux 不打开 Windows 设置');
  assert.deepEqual(
    linux.calls.filter((c) => c.method === 'openExternal'),
    []
  );
});

test('Linux 设为默认：仅在用户调用时执行 xdg-mime default，并逐项汇报失败', async () => {
  const calls = [];
  const { service, ipcMain } = makeService({
    platform: 'linux',
    desktopFile: 'uvmp-toolkit.desktop',
    execFile: (cmd, args, cb) => {
      calls.push([cmd].concat(args));
      if (args[2] === 'image/webp') cb(new Error('用户目录只读'));
      else cb(null, '');
    },
  });
  service.registerIpc();
  service.open({});
  const setDefault = handler(ipcMain, 'imagetools:setDefaultLinux');
  const res = await setDefault(trusted(service));
  assert.equal(res.ok, false);
  assert.deepEqual(res.applied, ['image/jpeg', 'image/png']);
  assert.deepEqual(res.failed, [{ mime: 'image/webp', error: '用户目录只读' }]);
  assert.deepEqual(
    calls.map((c) => c.slice(0, 4)),
    [
      ['xdg-mime', 'default', 'uvmp-toolkit.desktop', 'image/jpeg'],
      ['xdg-mime', 'default', 'uvmp-toolkit.desktop', 'image/png'],
      ['xdg-mime', 'default', 'uvmp-toolkit.desktop', 'image/webp'],
    ]
  );

  // 非 Linux 平台拒绝写默认项
  const win = makeService({ platform: 'win32' });
  win.service.registerIpc();
  win.service.open({});
  const resWin = await handler(win.ipcMain, 'imagetools:setDefaultLinux')(trusted(win.service));
  assert.equal(resWin.ok, false);
  assert.deepEqual(win.execCalls, []);
});

// ---- Windows 默认应用真实状态：读 HKCU\...\FileExts\.<ext>\UserChoice /v ProgId ----
const PROGID = 'uvmp-toolkit.image';
const WIN_EXTS = ['.jpg', '.jpeg', '.png', '.webp'];

// 构造 reg.exe 假实现：progIds 为 { '.jpg': 'uvmp-toolkit.image' | null }，null 表示 UserChoice 键不存在；
// installed 为 true/false/null（null 表示读取失败，无法确定）。
function regFake({ progIds = {}, installed = true, regMissing = false, failAll = null } = {}) {
  return (cmd, args, cb) => {
    if (cmd !== 'reg') return cb(Object.assign(new Error('未知命令'), { code: 'ENOENT' }));
    if (regMissing) return cb(Object.assign(new Error('spawn reg ENOENT'), { code: 'ENOENT' }));
    if (failAll) return cb(Object.assign(new Error(failAll), { code: 2 }));
    const key = String(args[1] || '');
    if (key === 'HKCU\\Software\\Classes' && typeof installed === 'string') {
      // 父键探测：区分「注册表可读」与「注册表也读不了」
      return installed === 'garbled-with-parent-ok'
        ? cb(null, '\r\nHKEY_CURRENT_USER\\Software\\Classes\r\n')
        : cb(Object.assign(new Error('\uFFFD\uFFFD\uFFFD\uFFFD'), { code: 1 }));
    }
    if (key.endsWith('\\' + PROGID)) {
      if (installed === null) return cb(Object.assign(new Error('访问被拒绝'), { code: 2 }));
      if (typeof installed === 'string') {
        return cb(Object.assign(new Error('\uFFFD\uFFFD\uFFFD\uFFFD'), { code: 1 }));
      }
      return installed
        ? cb(null, '\r\nHKEY_CURRENT_USER\\Software\\Classes\\' + PROGID + '\r\n')
        : cb(Object.assign(new Error('找不到'), { code: 1 }));
    }
    const m = /FileExts\\?(\.[A-Za-z]+)\\UserChoice$/i.exec(key);
    if (!m) return cb(Object.assign(new Error('找不到'), { code: 1 }));
    const ext = m[1].toLowerCase();
    if (!(ext in progIds) || progIds[ext] === null) {
      return cb(Object.assign(new Error('错误: 系统找不到指定的注册表项或值。'), { code: 1 }));
    }
    cb(
      null,
      '\r\nHKEY_CURRENT_USER\\Software\\Microsoft\\Windows\\CurrentVersion\\Explorer\\FileExts\\' +
        ext +
        '\\UserChoice\r\n    ProgId    REG_SZ    ' +
        progIds[ext] +
        '\r\n\r\n'
    );
  };
}

async function winStatus(regOptions) {
  const { service, ipcMain, execCalls } = makeService({
    platform: 'win32',
    execFile: regFake(regOptions),
  });
  service.registerIpc();
  service.open({});
  const status = await handler(ipcMain, 'imagetools:getDefaultStatus')(trusted(service));
  return { status, execCalls };
}

test('Windows 默认状态：四个扩展名都指向本工具 → default，installed=true', async () => {
  const { status, execCalls } = await winStatus({
    progIds: Object.fromEntries(WIN_EXTS.map((ext) => [ext, PROGID])),
    installed: true,
  });
  assert.equal(status.platform, 'win32');
  assert.equal(status.supported, true);
  assert.equal(status.managed, false, '默认应用仍由系统掌控，本工具不改写');
  assert.equal(status.status, 'default');
  assert.equal(status.default, true);
  assert.equal(status.partial, false);
  assert.equal(status.installed, true);
  assert.equal(status.reason, '');
  assert.deepEqual(
    status.associations.map((a) => [a.ext, a.status, a.progId]),
    WIN_EXTS.map((ext) => [ext, 'default', PROGID])
  );
  // 真的调过 reg query（4 个扩展名 + 1 次 ProgID 登记检查）
  const queries = execCalls.filter((c) => c.cmd === 'reg');
  assert.equal(queries.length, WIN_EXTS.length + 1);
  for (const ext of WIN_EXTS) {
    assert.ok(
      queries.some((c) => c.args[1].includes('FileExts\\' + ext + '\\UserChoice')),
      '应查询 ' + ext
    );
  }
  assert.deepEqual(queries[0].args.slice(0, 2), [
    'query',
    'HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Explorer\\FileExts\\.jpg\\UserChoice',
  ]);
  assert.deepEqual(queries[0].args.slice(2), ['/v', 'ProgId']);
});

test('Windows 默认状态：部分指向 → partial，并给出逐项明细', async () => {
  const { status } = await winStatus({
    progIds: { '.jpg': PROGID, '.jpeg': 'MSPaint.PNG', '.png': PROGID, '.webp': 'ChromeHTML' },
    installed: true,
  });
  assert.equal(status.status, 'partial');
  assert.equal(status.default, false);
  assert.equal(status.partial, true);
  const byExt = Object.fromEntries(status.associations.map((a) => [a.ext, a]));
  assert.equal(byExt['.jpg'].status, 'default');
  assert.equal(byExt['.png'].status, 'default');
  assert.equal(byExt['.jpeg'].status, 'other');
  assert.equal(byExt['.jpeg'].progId, 'MSPaint.PNG');
  assert.equal(byExt['.webp'].progId, 'ChromeHTML');
});

test('Windows 默认状态：全部由其它应用持有 → other（明确未指向本工具）', async () => {
  const { status } = await winStatus({
    progIds: {
      '.jpg': 'PhotoViewer.FileAssoc.Jpeg',
      '.jpeg': 'Photoshop',
      '.png': 'Photos',
      '.webp': 'ChromeHTML',
    },
    installed: true,
  });
  assert.equal(status.status, 'other');
  assert.equal(status.default, false);
  assert.equal(status.partial, false);
  assert.equal(status.reason, '');
});

test('Windows 默认状态：UserChoice 不存在 → unknown + 原因，不得伪装成未设', async () => {
  const { status } = await winStatus({
    progIds: Object.fromEntries(WIN_EXTS.map((ext) => [ext, null])),
    installed: true,
  });
  assert.equal(status.supported, true);
  assert.equal(status.status, 'unknown');
  assert.equal(status.default, false, 'unknown 不得报成已设');
  assert.equal(status.partial, false);
  assert.ok(status.associations.every((a) => a.status === 'unknown'));
  assert.ok(
    status.associations.every((a) => /UserChoice/.test(a.reason)),
    '逐项给出原因'
  );
  assert.match(status.reason, /UserChoice|无法确定/);
});

test('Windows 默认状态：找不到 reg.exe → unknown 并说明原因，supported 仍为 true', async () => {
  const { status } = await winStatus({ regMissing: true });
  assert.equal(status.supported, true);
  assert.equal(status.status, 'unknown');
  assert.equal(status.default, false);
  assert.equal(status.installed, null, '读不到就不假装未登记');
  assert.ok(status.associations.every((a) => /reg\.exe/.test(a.reason)));
  assert.match(status.reason, /reg\.exe/);
});

test('Windows 默认状态：reg 读取失败（非「找不到」）→ unknown 且带失败原因', async () => {
  const { status } = await winStatus({ failAll: '注册表访问被拒绝' });
  assert.equal(status.status, 'unknown');
  assert.equal(status.default, false);
  assert.ok(status.associations.every((a) => /访问被拒绝/.test(a.reason)));
});

test('Windows 默认状态：ProgID 类未登记 → installed=false（便携版/未安装）', async () => {
  const { status } = await winStatus({
    progIds: Object.fromEntries(WIN_EXTS.map((ext) => [ext, PROGID])),
    installed: false,
  });
  assert.equal(status.status, 'default');
  assert.equal(status.installed, false);
});

test('Windows 默认状态：错误文案认不出时用父键探测判定「未登记」，父键也读不了则 unknown', async () => {
  // 中文系统 reg.exe 输出是 GBK，node 解出来是乱码，文案匹配不可靠：
  // ProgID 键读失败 → 探测父键 HKCU\Software\Classes；父键可读 => 明确未登记
  const parentReadable = await winStatus({
    progIds: { '.jpg': PROGID, '.jpeg': PROGID, '.png': PROGID, '.webp': PROGID },
    installed: 'garbled-with-parent-ok',
  });
  assert.equal(parentReadable.status.installed, false);

  const parentUnreadable = await winStatus({
    progIds: { '.jpg': PROGID, '.jpeg': PROGID, '.png': PROGID, '.webp': PROGID },
    installed: 'garbled-parent-fail',
  });
  assert.equal(parentUnreadable.status.installed, null, '父键也读不了时不得断言未登记');
});

test('Windows 默认状态：未知状态夹在中间时不谎称全部指向本工具', async () => {
  const { status } = await winStatus({
    progIds: { '.jpg': PROGID, '.jpeg': PROGID, '.png': null, '.webp': 'ChromeHTML' },
    installed: true,
  });
  assert.equal(status.status, 'partial', '有确定指向本工具的项即为部分');
  assert.equal(status.default, false);
  const unknown = status.associations.filter((a) => a.status === 'unknown');
  assert.equal(unknown.length, 1);
  assert.equal(unknown[0].ext, '.png');
});

test('Linux 默认状态：查询三个 MIME 的默认项并给出是否已指向本工具', async () => {
  const { service, ipcMain } = makeService({
    platform: 'linux',
    desktopFile: 'uvmp-toolkit.desktop',
    execFile: (cmd, args, cb) => {
      if (args[0] === 'query')
        cb(null, args[2] === 'image/png' ? 'uvmp-toolkit.desktop\n' : 'org.gnome.eog.desktop\n');
      else cb(null, '');
    },
  });
  service.registerIpc();
  service.open({});
  const status = await handler(ipcMain, 'imagetools:getDefaultStatus')(trusted(service));
  assert.equal(status.platform, 'linux');
  assert.equal(status.supported, true);
  assert.deepEqual(status.defaults, {
    'image/jpeg': 'org.gnome.eog.desktop',
    'image/png': 'uvmp-toolkit.desktop',
    'image/webp': 'org.gnome.eog.desktop',
  });
  assert.deepEqual(status.isDefault, {
    'image/jpeg': false,
    'image/png': true,
    'image/webp': false,
  });
  assert.equal(status.desktopFile, 'uvmp-toolkit.desktop');

  // Windows 也支持查询（真实读 UserChoice）；只有既非 Windows 也非 Linux 的平台才 supported:false
  const other = makeService({ platform: 'darwin' });
  other.service.registerIpc();
  other.service.open({});
  const otherStatus = await handler(
    other.ipcMain,
    'imagetools:getDefaultStatus'
  )(trusted(other.service));
  assert.equal(otherStatus.platform, 'darwin');
  assert.equal(otherStatus.supported, false);
  assert.equal(otherStatus.managed, false);
  assert.equal(otherStatus.status, 'unsupported');
});

test('渲染层 open：只允许切到已授权文件，不能借此授权任意磁盘路径', async () => {
  const { service, ipcMain } = makeService();
  service.registerIpc();
  service.open({ files: [IMG1] });
  const win = FakeWindow.instances[0];
  win.webContents.emit('did-finish-load');
  win.webContents.sent.length = 0;
  const open = handler(ipcMain, 'imagetools:open');
  const res = await open(trusted(service), { files: [SECRET, IMG1] });
  assert.equal(res.open, true);
  const payload = win.webContents.sent.find((m) => m.channel === 'imagetools:open').payload;
  assert.deepEqual(payload.files, [realOrEmpty(IMG1)], '未授权路径不得随 open 进入授权表');
  assert.equal(service.pathGuard.check(SECRET), '', '渲染层 open 不授予新目录');
});

test('窗口控制：最小化/最大化切换/关闭/版本，最大化事件走 imagetools:max', async () => {
  const { service, ipcMain } = makeService();
  service.registerIpc();
  service.open({});
  const win = FakeWindow.instances[0];
  const call = (channel, ...args) => handler(ipcMain, channel)(trusted(service), ...args);

  assert.equal(await call('imagetools:winIsMax'), false);
  await call('imagetools:winMinimize');
  assert.ok(win.calls.includes('minimize'));
  await call('imagetools:winToggleMax');
  assert.equal(win.isMaximized(), true);
  assert.deepEqual(win.webContents.sent.at(-1), { channel: 'imagetools:max', payload: true });
  await call('imagetools:winToggleMax');
  assert.equal(win.isMaximized(), false);
  assert.deepEqual(win.webContents.sent.at(-1), { channel: 'imagetools:max', payload: false });
  assert.equal(await call('imagetools:appVersion'), '2.1.1');
  await call('imagetools:winClose');
  assert.equal(win.destroyed, true);
  // 来源校验：主窗口不能驱动图片窗口
  await assert.rejects(
    () => handler(ipcMain, 'imagetools:winClose')({ sender: new FakeWebContents() }),
    /来源/
  );
});

test('去重与清理：dispose 关窗、撤销路径授权、清任务跟踪；关窗即撤销授权', () => {
  const { service } = makeService();
  service.open({ files: [IMG1] });
  const win = FakeWindow.instances[0];
  const real = realOrEmpty(IMG1);
  assert.equal(service.pathGuard.check(real), real);
  win.close();
  assert.equal(service.pathGuard.check(real), '', '关窗即撤销授权，旧 URL 不可再读');
  assert.equal(service.win, null);
  assert.equal(service.tokens, undefined, '服务不再持有令牌表');

  service.open({ files: [IMG1] });
  assert.equal(service.pathGuard.check(real), real, '重开后重新授权');
  service.trackJob('job-9');
  service.dispose();
  assert.equal(service.win, null);
  assert.equal(service.pathGuard.check(real), '');
  assert.equal(service.isJobTracked('job-9'), false);
});

test('窗口连点：第二实例打开新图片时复用窗口并聚焦，不叠加窗口', () => {
  const { service } = makeService();
  service.open({ files: [IMG1] });
  const win = FakeWindow.instances[0];
  win.webContents.emit('did-finish-load');
  service.openPaths([IMG2]);
  assert.equal(FakeWindow.instances.length, 1);
  assert.ok(win.calls.includes('show'));
  const openEvents = win.webContents.sent.filter((m) => m.channel === 'imagetools:open');
  assert.deepEqual(openEvents.at(-1).payload.files, [realOrEmpty(IMG2)]);
});
