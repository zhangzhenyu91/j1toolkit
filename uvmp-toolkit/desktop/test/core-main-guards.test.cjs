// 主进程守卫回归测试：导航/新窗口/权限白名单、Linux 自启 Exec 引号、渲染进程崩溃恢复。
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { pathToFileURL } = require('url');
const { EventEmitter } = require('events');

const {
  isAllowedAppUrl,
  isPermissionAllowed,
  installWebContentsGuards,
  installPermissionGuards,
  createPickedFileRegistry,
  quoteDesktopExec,
  buildAutostartEntry,
  createCrashRecovery,
} = require('../electron/window-state.cjs');

const DEV_URL = 'http://127.0.0.1:5173';

// 临时搭一个「安装包 dist」目录：已知页面 + assets 资源 + 一个非白名单页面 + 目录外文件。
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'uvmp-guard-'));
const DIST = path.join(TMP, 'dist');
const OUTSIDE = path.join(TMP, 'secret');
fs.mkdirSync(path.join(DIST, 'assets'), { recursive: true });
fs.mkdirSync(OUTSIDE, { recursive: true });
for (const name of ['index.html', 'result.html', 'scanbox.html', 'screensaver.html']) {
  fs.writeFileSync(path.join(DIST, name), '<html></html>');
}
fs.writeFileSync(path.join(DIST, 'assets', 'app.js'), '// app');
fs.writeFileSync(path.join(DIST, 'assets', 'chunk.css'), '/* css */');
fs.writeFileSync(path.join(DIST, 'unknown.html'), '<html></html>');
fs.writeFileSync(path.join(DIST, 'notes.txt'), 'x');
fs.writeFileSync(path.join(OUTSIDE, 'index.html'), '<html></html>');
test.after(() => fs.rmSync(TMP, { recursive: true, force: true }));

const fileUrl = (p) => pathToFileURL(p).href;

test('file:// 导航：生产仅放行 dist 内已知页面与 assets 资源', () => {
  const allowed = (p) => isAllowedAppUrl(fileUrl(p), { appDir: DIST });
  assert.equal(allowed(path.join(DIST, 'index.html')), true);
  assert.equal(allowed(path.join(DIST, 'result.html')), true);
  assert.equal(allowed(path.join(DIST, 'scanbox.html')), true);
  assert.equal(allowed(path.join(DIST, 'screensaver.html')), true);
  assert.equal(allowed(path.join(DIST, 'assets', 'app.js')), true, 'assets 资源放行');
  assert.equal(allowed(path.join(DIST, 'assets', 'chunk.css')), true);

  assert.equal(allowed(path.join(DIST, 'unknown.html')), false, '未知 HTML 不放行');
  assert.equal(allowed(path.join(DIST, 'notes.txt')), false, 'dist 根目录非资源文件不放行');
  assert.equal(allowed(path.join(OUTSIDE, 'index.html')), false, 'dist 外同名页面不放行');
  assert.equal(
    allowed(path.join(DIST, '..', 'secret', 'index.html')),
    false,
    '路径穿越不得绕过包含校验'
  );
  assert.equal(allowed(path.join(TMP, 'dist-backup', 'index.html')), false, '前缀相似的目录不放行');
  // 悬浮窗带 query 加载（loadFile 的 query 也会出现在 file:// 上）：路径部分合法即放行
  assert.equal(
    isAllowedAppUrl(fileUrl(path.join(DIST, 'scanbox.html')) + '?box=A', { appDir: DIST }),
    true
  );
  assert.equal(
    isAllowedAppUrl(fileUrl(path.join(DIST, 'index.html')) + '#/settings', { appDir: DIST }),
    true
  );
  // 编码过的穿越同样还原后校验
  assert.equal(isAllowedAppUrl('file:///etc/passwd', { appDir: DIST }), false);
  assert.equal(
    isAllowedAppUrl(new URL('..%2fsecret%2findex.html', fileUrl(path.join(DIST, '/'))).href, {
      appDir: DIST,
    }),
    false
  );
});

test('file:// 导航：未提供 appDir 时一律拒绝（不再全放行）', () => {
  const url = fileUrl(path.join(DIST, 'index.html'));
  assert.equal(isAllowedAppUrl(url, {}), false);
  assert.equal(isAllowedAppUrl('file:///opt/app/dist/index.html', ''), false);
  // 符号链接逃逸：指向 dist 外部的 link.html 即使扩展名合法也不放行
  const link = path.join(DIST, 'link.html');
  try {
    fs.symlinkSync(path.join(OUTSIDE, 'index.html'), link);
  } catch (_e) {
    return; // 无符号链接权限的环境跳过
  }
  assert.equal(isAllowedAppUrl(fileUrl(link), { appDir: DIST }), false);
});

test('file:// 导航：realpath 不可用（asar 虚拟路径）时按规范化路径校验', () => {
  // 打包后 dist 位于 app.asar 内，某些环境下 realpath 不可用：退化路径仍需通过包含+白名单校验
  const asarDist = path.join(TMP, 'resources', 'app.asar', 'dist');
  const url = (rel) => fileUrl(path.join(asarDist, rel));
  assert.equal(isAllowedAppUrl(url('index.html'), { appDir: asarDist }), true);
  assert.equal(isAllowedAppUrl(url('assets/app.js'), { appDir: asarDist }), true);
  assert.equal(isAllowedAppUrl(url('unknown.html'), { appDir: asarDist }), false);
  assert.equal(isAllowedAppUrl(url('../index.html'), { appDir: asarDist }), false);
  assert.equal(
    isAllowedAppUrl(fileUrl(path.join(TMP, 'resources', 'app.asar.unpacked', 'x.js')), {
      appDir: asarDist,
    }),
    false,
    'app.asar.unpacked 不在 dist 内'
  );
});

test('开发态导航：仅精确匹配 ELECTRON_DEV_URL 的 origin', () => {
  assert.equal(isAllowedAppUrl(DEV_URL + '/index.html', { devUrl: DEV_URL }), true);
  assert.equal(isAllowedAppUrl(DEV_URL + '/@vite/client', { devUrl: DEV_URL }), true);
  assert.equal(isAllowedAppUrl(DEV_URL, { devUrl: DEV_URL }), true);
  assert.equal(isAllowedAppUrl('http://127.0.0.1:5174/', { devUrl: DEV_URL }), false);
  assert.equal(isAllowedAppUrl('https://127.0.0.1:5173/', { devUrl: DEV_URL }), false, '协议不同');
  assert.equal(isAllowedAppUrl('http://localhost:5173/', { devUrl: DEV_URL }), false, '主机不同');
  // 开发态只走 Vite 地址：打包内 file:// 不因存在 appDir 而放行
  assert.equal(
    isAllowedAppUrl(fileUrl(path.join(DIST, 'index.html')), { devUrl: DEV_URL, appDir: DIST }),
    false
  );
});

test('导航白名单：拦截外链、内网地址、危险协议与非开发态 http', () => {
  const opts = { devUrl: DEV_URL, appDir: DIST };
  assert.equal(isAllowedAppUrl('https://example.com', opts), false);
  assert.equal(isAllowedAppUrl('http://10.20.30.40:8080/x', opts), false);
  assert.equal(
    isAllowedAppUrl('http://127.0.0.1:5173', { appDir: DIST }),
    false,
    '非开发态不放行 http'
  );
  assert.equal(isAllowedAppUrl('javascript:alert(1)', opts), false);
  assert.equal(isAllowedAppUrl('data:text/html,hi', opts), false);
  assert.equal(isAllowedAppUrl('', opts), false);
  assert.equal(isAllowedAppUrl(null, opts), false);
});

test('权限白名单：保留剪贴板写入/全屏/截屏，其余默认拒绝', () => {
  for (const p of ['clipboard-sanitized-write', 'fullscreen', 'display-capture']) {
    assert.equal(isPermissionAllowed(p), true, p + ' 应放行');
  }
  // 摄像头/麦克风链路由主进程 desktopCapturer 完成，渲染层不需要 media 权限
  for (const p of [
    'media',
    'geolocation',
    'notifications',
    'openExternal',
    'midi',
    'serial',
    'usb',
    '',
  ]) {
    assert.equal(isPermissionAllowed(p), false, p + ' 应拒绝');
  }
});

test('导航守卫：新窗口 deny、非法导航 preventDefault、webview 拒绝', () => {
  const app = new EventEmitter();
  const blocked = [];
  installWebContentsGuards(app, {
    devUrl: DEV_URL,
    appDir: DIST,
    onBlocked: (kind, url) => blocked.push([kind, url]),
  });
  const contents = new EventEmitter();
  let handler = null;
  contents.setWindowOpenHandler = (fn) => {
    handler = fn;
  };
  app.emit('web-contents-created', {}, contents);

  assert.deepEqual(handler({ url: 'https://example.com' }), { action: 'deny' });
  assert.deepEqual(handler({ url: DEV_URL }), { action: 'deny' }, '本地应用不新开窗口');

  const navEvent = (url) => {
    const calls = [];
    const ev = { preventDefault: () => calls.push('prevent') };
    contents.emit('will-navigate', ev, url);
    return calls;
  };
  assert.deepEqual(navEvent(DEV_URL + '/x'), []);
  assert.deepEqual(
    navEvent(fileUrl(path.join(DIST, 'index.html'))),
    ['prevent'],
    '开发态挡 file://'
  );
  assert.deepEqual(navEvent('https://evil.example/'), ['prevent']);

  const webviewCalls = [];
  contents.emit('will-attach-webview', { preventDefault: () => webviewCalls.push('prevent') }, {});
  assert.deepEqual(webviewCalls, ['prevent']);

  assert.deepEqual(
    blocked.map((b) => b[0]),
    ['window-open', 'window-open', 'navigate', 'navigate', 'webview']
  );
});

test('导航守卫：生产态放行 dist 已知页面、拦截 dist 外 file://', () => {
  const app = new EventEmitter();
  const blocked = [];
  installWebContentsGuards(app, {
    devUrl: '',
    appDir: DIST,
    onBlocked: (kind, url) => blocked.push([kind, url]),
  });
  const contents = new EventEmitter();
  contents.setWindowOpenHandler = () => ({ action: 'deny' });
  app.emit('web-contents-created', {}, contents);
  const navEvent = (url) => {
    const calls = [];
    contents.emit('will-navigate', { preventDefault: () => calls.push('prevent') }, url);
    return calls;
  };
  assert.deepEqual(navEvent(fileUrl(path.join(DIST, 'screensaver.html'))), []);
  assert.deepEqual(navEvent(fileUrl(path.join(DIST, 'assets', 'app.js'))), []);
  assert.deepEqual(navEvent(fileUrl(path.join(OUTSIDE, 'index.html'))), ['prevent']);
  assert.deepEqual(navEvent(fileUrl(path.join(DIST, 'unknown.html'))), ['prevent']);
  assert.deepEqual(
    blocked.map((b) => b[0]),
    ['navigate', 'navigate']
  );
});

test('导航守卫：DevTools 内容不受导航拦截（否则开发者工具自身导航被打死）', () => {
  const app = new EventEmitter();
  installWebContentsGuards(app, { devUrl: DEV_URL, appDir: DIST });
  const contents = new EventEmitter();
  contents.getType = () => 'devtools';
  contents.setWindowOpenHandler = () => {
    throw new Error('不应给 devtools 装窗口打开拦截');
  };
  app.emit('web-contents-created', {}, contents);
  const calls = [];
  contents.emit(
    'will-navigate',
    { preventDefault: () => calls.push('prevent') },
    'devtools://devtools/x'
  );
  assert.deepEqual(calls, []);
});

test('文件授权登记：仅放行本窗口 pickFile 选择且后缀合法的路径', () => {
  const dir = path.join(TMP, 'books');
  fs.mkdirSync(dir, { recursive: true });
  const xls = path.join(dir, '题库.xls');
  const xlsx = path.join(dir, '题库.xlsx');
  const csv = path.join(dir, '题库.csv');
  const py = path.join(dir, 'script.py');
  for (const p of [xls, xlsx, csv, py]) fs.writeFileSync(p, 'x');
  const other = path.join(dir, 'other.xlsx');
  fs.writeFileSync(other, 'x');

  const registry = createPickedFileRegistry();
  const exts = ['.xls', '.xlsx', '.csv'];
  assert.equal(registry.authorize(xls, exts), '', '未选择前不可读');

  registry.allow(xls);
  registry.allow(xlsx);
  registry.allow(csv);
  registry.allow(py);
  assert.equal(registry.authorize(xls, exts), fs.realpathSync(xls));
  assert.equal(registry.authorize(xlsx, exts), fs.realpathSync(xlsx));
  assert.equal(registry.authorize(csv, exts), fs.realpathSync(csv));
  assert.equal(registry.authorize(py, exts), '', '后缀不在白名单内仍拒绝');
  assert.equal(registry.authorize(other, exts), '', '同目录未选择文件拒绝');
  assert.equal(registry.authorize('', exts), '');
  assert.equal(registry.authorize(null, exts), '');

  // 选择后被替换为指向外部的符号链接：realpath 不一致，读取授权失效
  const swap = path.join(dir, 'swap.xlsx');
  fs.writeFileSync(swap, 'x');
  registry.allow(swap);
  fs.rmSync(swap);
  try {
    fs.symlinkSync(path.join(OUTSIDE, 'index.html'), swap);
  } catch (_e) {
    return;
  }
  assert.equal(registry.authorize(swap, exts), '', 'realpath 变化后不得继续放行');
});

test('文件授权登记：容量上限淘汰最早授权', () => {
  const dir = path.join(TMP, 'many');
  fs.mkdirSync(dir, { recursive: true });
  const registry = createPickedFileRegistry({ max: 2 });
  const a = path.join(dir, 'a.xls');
  const b = path.join(dir, 'b.xls');
  const c = path.join(dir, 'c.xls');
  for (const p of [a, b, c]) fs.writeFileSync(p, 'x');
  registry.allow(a);
  registry.allow(b);
  registry.allow(c);
  assert.equal(registry.authorize(a, ['.xls']), '', '最早授权已淘汰');
  assert.equal(registry.authorize(b, ['.xls']), fs.realpathSync(b));
  assert.equal(registry.authorize(c, ['.xls']), fs.realpathSync(c));
});

test('main.cjs 接线：守卫带 dist 根、pickFile 登记授权、readFile 走授权校验', () => {
  const src = fs.readFileSync(path.join(__dirname, '..', 'electron', 'main.cjs'), 'utf8');
  assert.match(src, /APP_DIST_DIR = path\.join\(__dirname, '\.\.', 'dist'\)/);
  assert.match(src, /installWebContentsGuards\(app, \{[\s\S]*?appDir:/);
  assert.match(src, /const pickedFiles = createPickedFileRegistry\(\)/);
  assert.match(src, /'dialog:pickFile'[\s\S]*?pickedFiles\.allow\(/);
  assert.match(src, /'quizSearch:readFile'[\s\S]*?pickedFiles\.authorize\(/);
  assert.ok(
    !/quizSearch:readFile'[\s\S]{0,300}?readFileSync\(String\(p\)\)/.test(src),
    '不得直接读入参路径'
  );
});

test('权限守卫：请求与检查通道都按白名单放行', () => {
  let requestFn = null;
  let checkFn = null;
  const session = {
    setPermissionRequestHandler: (fn) => {
      requestFn = fn;
    },
    setPermissionCheckHandler: (fn) => {
      checkFn = fn;
    },
  };
  installPermissionGuards(session);
  assert.equal(typeof requestFn, 'function');
  assert.equal(typeof checkFn, 'function');

  const asked = [];
  requestFn({}, 'clipboard-sanitized-write', (ok) => asked.push(ok));
  requestFn({}, 'geolocation', (ok) => asked.push(ok));
  assert.deepEqual(asked, [true, false]);
  assert.equal(checkFn({}, 'fullscreen'), true);
  assert.equal(checkFn({}, 'notifications'), false);
});

test('Linux 自启 Exec：空格/中文路径整体加引号并转义特殊字符', () => {
  assert.equal(quoteDesktopExec('/opt/app/uvmp'), '"/opt/app/uvmp"');
  assert.equal(
    quoteDesktopExec('/home/u/Shade 壹匣 - 内网/app'),
    '"/home/u/Shade 壹匣 - 内网/app"'
  );
  assert.equal(quoteDesktopExec('/tmp/a"b'), '"/tmp/a\\"b"');
  assert.equal(quoteDesktopExec('/tmp/a$b`c\\d'), '"/tmp/a\\$b\\`c\\\\d"');

  const entry = buildAutostartEntry({
    execPath: '/home/u/Shade 壹匣 - 内网/uvmp',
    name: 'Shade 壹匣 - 内网',
  });
  assert.match(entry, /^\[Desktop Entry\]\nType=Application\n/);
  assert.match(entry, /^Name=Shade 壹匣 - 内网$/m);
  assert.match(entry, /^Exec="\/home\/u\/Shade 壹匣 - 内网\/uvmp"$/m, 'Exec 必须整体加引号');
  assert.match(entry, /X-GNOME-Autostart-enabled=true/);
  // 关键回归：未加引号时 Exec 行会被按空格拆成多个参数
  assert.ok(!/^Exec=[^"\n].* /m.test(entry));
});

test('崩溃恢复：首次提示并 reload，超过上限后停止自动重载', () => {
  const events = [];
  let reloads = 0;
  const recovery = createCrashRecovery({
    reload: () => {
      reloads += 1;
    },
    notify: (kind, reason) => events.push([kind, reason]),
    maxReloads: 2,
  });
  assert.deepEqual(recovery.handle('clean-exit'), { action: 'ignore' });
  assert.equal(reloads, 0, '正常退出不触发恢复');
  assert.equal(recovery.handle('crashed').action, 'reload');
  assert.equal(recovery.handle('oom').action, 'reload');
  assert.deepEqual(recovery.handle('crashed'), { action: 'give-up' });
  assert.equal(reloads, 2, '超限后不再自动 reload，避免崩溃循环');
  assert.deepEqual(events, [
    ['first', 'crashed'],
    ['exhausted', 'crashed'],
  ]);
  assert.equal(recovery.crashes, 3);
});
