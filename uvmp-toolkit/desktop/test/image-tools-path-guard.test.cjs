// 图片路径授权与 shade-image 路径协议回归：目录越界、符号链接、文件被替换/删除、
// URL 往返（CJK/空格/#/?/%）、HEAD/流式读取与非法 URL 拒绝。不使用任何令牌。
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

const {
  VIEW_EXTENSIONS,
  createPathGuard,
  buildImageProtocolUrl,
  parseImageProtocolUrl,
  imageMime,
  createImageProtocolHandler,
  resolveRealisticPath,
  // 真实路径期望值统一走产品口径：Windows 下 native realpath 会把 8.3 短名归一成长名。
  // 测试侧若用 legacy fs.realpathSync，在短名 TEMP（CI 的 C:\Users\RUNNER~1\…）下会把短名当期望，误报失败。
  realOrEmpty,
} = require('../electron/imagetools/path-guard.cjs');

const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'uvmp-imgguard-'));
const DIR_A = path.join(TMP, '相册 A');
const DIR_B = path.join(TMP, '相册 B');
const SUB = path.join(DIR_A, '子目录');
const OUTSIDE = path.join(TMP, '外部');
for (const d of [DIR_A, DIR_B, SUB, OUTSIDE]) fs.mkdirSync(d, { recursive: true });
const w = (p, body = 'x') => {
  fs.mkdirSync(path.dirname(p), { recursive: true });
  fs.writeFileSync(p, body);
  return p;
};
const A1 = w(path.join(DIR_A, '一.jpg'));
const A2 = w(path.join(DIR_A, '二.png'));
const A_TXT = w(path.join(DIR_A, '说明.txt'));
const SUB_IMG = w(path.join(SUB, '三.webp'));
const B1 = w(path.join(DIR_B, '别的.png'));
const SECRET = w(path.join(OUTSIDE, '私密.jpg'));
test.after(() => fs.rmSync(TMP, { recursive: true, force: true }));

const imageUrl = (p) => 'shade-image://local?path=' + encodeURIComponent(p);

test('查看格式集合：10 个扩展名，不含 HEIC/PSD/PDF/SVG', () => {
  const exts = [...VIEW_EXTENSIONS].sort();
  assert.deepEqual(exts, [
    '.avif',
    '.bmp',
    '.gif',
    '.ico',
    '.jpeg',
    '.jpg',
    '.png',
    '.tif',
    '.tiff',
    '.webp',
  ]);
  for (const bad of ['.heic', '.psd', '.pdf', '.svg', '.txt']) {
    assert.equal(VIEW_EXTENSIONS.has(bad), false, bad + ' 不在查看范围');
  }
});

test('选择文件：只授权其父目录，不放开整个盘', () => {
  const guard = createPathGuard();
  const real = guard.authorizeFile(A1);
  assert.equal(real, realOrEmpty(A1));
  assert.equal(guard.check(A1), realOrEmpty(A1));
  // 同目录其它图片可读（看图需要切图）
  assert.equal(guard.check(A2), realOrEmpty(A2));
  // 子目录内图片也可读（授权目录为包含关系）
  assert.equal(guard.check(SUB_IMG), realOrEmpty(SUB_IMG));
  // 目录外、同目录非图片文件、其它盘目录一律拒绝
  assert.equal(guard.check(B1), '');
  assert.equal(guard.check(SECRET), '');
  assert.equal(guard.check(A_TXT), '');
  // 非法入参
  assert.equal(guard.check(''), '');
  assert.equal(guard.check(null), '');
  assert.equal(guard.check(DIR_A), '', '目录不是文件，check 拒绝');
});

test('选择目录：目录内（含子目录）图片可读，越界路径与穿越写法拒绝', () => {
  const guard = createPathGuard();
  assert.equal(guard.authorizeDirectory(DIR_A), realOrEmpty(DIR_A));
  assert.equal(guard.check(A1), realOrEmpty(A1));
  assert.equal(guard.check(SUB_IMG), realOrEmpty(SUB_IMG));
  assert.equal(guard.check(B1), '');
  // ../ 穿越最终 realpath 落在授权目录外
  assert.equal(guard.check(path.join(DIR_A, '..', '相册 B', '别的.png')), '');
  assert.equal(guard.check(path.join(DIR_A, '..', '外部', '私密.jpg')), '');
  // 前缀相似的目录不得误放行
  const sibling = path.join(TMP, '相册 A-备份');
  fs.mkdirSync(sibling, { recursive: true });
  const sib = w(path.join(sibling, 'x.jpg'));
  assert.equal(guard.check(sib), '');
  // 目录授权失败：不存在 / 是文件
  assert.equal(guard.authorizeDirectory(path.join(TMP, '不存在')), '');
  assert.equal(guard.authorizeDirectory(A1), '');
});

test('符号链接：选到指向外部的链接即拒绝；授权后目标被换成外部链接也失效', () => {
  const guard = createPathGuard();
  const link = path.join(DIR_A, '链接.jpg');
  try {
    fs.symlinkSync(SECRET, link);
    fs.symlinkSync(path.join(OUTSIDE), path.join(DIR_A, '外链目录'));
  } catch (_e) {
    return; // 无符号链接权限的环境跳过
  }
  assert.equal(guard.authorizeFile(link), '', '指向外部的链接不授权');
  assert.equal(guard.check(link), '');

  // 正常文件先授权，再被替换成外部链接
  const swap = w(path.join(DIR_A, 'swap.jpg'));
  guard.authorizeFile(swap);
  assert.notEqual(guard.check(swap), '');
  fs.rmSync(swap);
  fs.symlinkSync(SECRET, swap);
  assert.equal(guard.check(swap), '', 'realpath 变化后授权失效');

  // 目录内通过符号链接目录逃逸
  assert.equal(guard.check(path.join(DIR_A, '外链目录', '私密.jpg')), '');
});

test('文件删除与后缀伪装：授权后文件被删除或改名成非图片后缀即拒绝', () => {
  const guard = createPathGuard();
  const gone = w(path.join(DIR_A, '会删除.png'));
  guard.authorizeFile(gone);
  fs.rmSync(gone);
  assert.equal(guard.check(gone), '');

  // 指向 .txt 的 .jpg 链接：真实后缀不在白名单
  const fake = path.join(DIR_A, '伪装.jpg');
  try {
    fs.symlinkSync(A_TXT, fake);
  } catch (_e) {
    return;
  }
  assert.equal(guard.check(fake), '');
});

test('精确文件授权（仅用于核心返回的预览缓存）与授权清理', () => {
  const guard = createPathGuard();
  const cache = w(path.join(OUTSIDE, 'preview-1.png'));
  assert.equal(guard.grantFile(cache), realOrEmpty(cache));
  assert.equal(guard.check(cache), realOrEmpty(cache), '精确授权可读');
  assert.equal(guard.check(SECRET), '', '只授权那一个文件，不放开所在目录');
  guard.clear();
  assert.equal(guard.check(cache), '');
});

test('授权目录数量有上限，最早授权被淘汰', () => {
  const guard = createPathGuard({ maxDirs: 2 });
  const dirs = [];
  for (let i = 0; i < 3; i += 1) {
    const d = path.join(TMP, '容量' + i);
    fs.mkdirSync(d, { recursive: true });
    dirs.push(w(path.join(d, 'x.jpg')));
  }
  for (const f of dirs) guard.authorizeFile(f);
  assert.equal(guard.check(dirs[0]), '', '最早授权已淘汰');
  assert.notEqual(guard.check(dirs[1]), '');
  assert.notEqual(guard.check(dirs[2]), '');
});

test('图片协议 URL：真实路径编码后往返一致（CJK/空格/#/&/%）', () => {
  // Windows 文件名不允许 ?，这类字符只验证 URL 编解码本身（下面单独覆盖）
  const weird = w(path.join(DIR_A, '带 空格 #井号 &与 %百分号.png'));
  for (const p of [A1, weird, SUB_IMG]) {
    const real = realOrEmpty(p);
    const url = buildImageProtocolUrl(real);
    assert.equal(url, imageUrl(real));
    assert.match(url, /^shade-image:\/\/local\?path=/);
    assert.equal(parseImageProtocolUrl(url), real, 'URL 必须还原出真实路径');
  }
  // 特殊字符确实被百分号编码（不会被当作查询分隔符/片段）
  const encoded = buildImageProtocolUrl(realOrEmpty(weird));
  assert.ok(encoded.includes('%20'), '空格需编码');
  assert.ok(encoded.includes('%23'), '# 需编码');
  assert.ok(encoded.includes('%26'), '& 需编码');
  assert.ok(encoded.includes('%25'), '% 需编码');
  // ? / = / + / 引号同样只以编码形态出现
  const synthetic = path.join(DIR_A, "a?b=c+d 引号'x.png");
  const syntheticUrl = buildImageProtocolUrl(synthetic);
  assert.ok(syntheticUrl.includes('%3F'), '? 需编码');
  assert.ok(syntheticUrl.includes('%3D'), '= 需编码');
  assert.ok(syntheticUrl.includes('%2B'), '+ 需编码');
  assert.equal(parseImageProtocolUrl(syntheticUrl), synthetic, '合成路径同样往返一致');
  assert.equal(buildImageProtocolUrl(''), '');
  assert.equal(buildImageProtocolUrl(null), '');
});

test('图片协议 URL：不含令牌，旧令牌形态与非法形态一律拒绝', () => {
  // 旧实现签发的 48 位十六进制令牌不再被接受
  assert.equal(parseImageProtocolUrl('shade-image://' + 'a'.repeat(48)), '');
  assert.equal(parseImageProtocolUrl('shade-image:' + 'a'.repeat(48)), '');
  // 宿主/参数/编码不合法
  assert.equal(parseImageProtocolUrl('shade-image://'), '');
  assert.equal(parseImageProtocolUrl('shade-image://local'), '');
  assert.equal(parseImageProtocolUrl('shade-image://local?path='), '');
  assert.equal(parseImageProtocolUrl('shade-image://local?path'), '');
  assert.equal(parseImageProtocolUrl('shade-image://other?path=' + encodeURIComponent(A1)), '');
  assert.equal(parseImageProtocolUrl(imageUrl(A1) + '&x=1'), '');
  assert.equal(parseImageProtocolUrl('shade-image://local?path=%E0%A4%A'), '', '坏编码');
  assert.equal(
    parseImageProtocolUrl('shade-image://local?path=' + A1),
    '',
    '未编码的绝对路径不接受'
  );
  // 其他协议（含 file:// 与非标准 URL）不解析
  assert.equal(parseImageProtocolUrl('file:///etc/passwd'), '');
  assert.equal(parseImageProtocolUrl('file:///' + A1.replace(/\\/g, '/')), '');
  assert.equal(parseImageProtocolUrl(''), '');
  assert.equal(parseImageProtocolUrl(null), '');
  assert.equal(parseImageProtocolUrl('https://example.com/a.jpg'), '');
});

test('图片协议 URL：接受 Chromium 规范化后的 host 后带斜杠形态', () => {
  // Chromium 对 standard scheme 会把 shade-image://local?path=… 补成 shade-image://local/?path=…
  // 真实窗口里 <img> 收到的就是后者；不认就等于预览全 404。
  const real = realOrEmpty(A1);
  const canonical = imageUrl(real).replace('shade-image://local?', 'shade-image://local/?');
  assert.equal(canonical, 'shade-image://local/?path=' + encodeURIComponent(real));
  assert.equal(parseImageProtocolUrl(canonical), real);
  // 多余斜杠仍拒绝（只容忍 host 后恰好一个）
  assert.equal(parseImageProtocolUrl('shade-image://local//?path=' + encodeURIComponent(real)), '');
});

test('协议处理：授权文件按 URL 流式返回，HEAD 带响应头无正文', async () => {
  const guard = createPathGuard();
  const handler = createImageProtocolHandler({ guard });
  const body = 'fake-png-bytes';
  const file = w(path.join(DIR_A, '流.png'), body);
  guard.authorizeFile(file);
  const url = imageUrl(realOrEmpty(file));

  const res = await handler(new Request(url));
  assert.equal(res.status, 200);
  assert.equal(res.headers.get('content-type'), 'image/png');
  assert.equal(res.headers.get('content-length'), String(body.length));
  assert.equal(res.headers.get('cache-control'), 'no-store');
  assert.equal(res.headers.get('x-content-type-options'), 'nosniff');
  assert.equal(await res.text(), body);

  // 大文件按块流式读取（不是一次性读整块字符串）
  const big = w(path.join(DIR_A, '大图.png'), 'A'.repeat(512 * 1024));
  guard.authorizeFile(big);
  const bigRes = await handler(new Request(imageUrl(realOrEmpty(big))));
  assert.equal(bigRes.status, 200);
  assert.equal(bigRes.headers.get('content-length'), String(512 * 1024));
  assert.ok(bigRes.body, '应有可读流');
  if (typeof bigRes.body.getReader === 'function') {
    const reader = bigRes.body.getReader();
    let chunks = 0;
    for (;;) {
      const { done } = await reader.read();
      if (done) break;
      chunks += 1;
    }
    assert.ok(chunks >= 1, '流至少产出一个块');
  } else {
    assert.equal((await bigRes.arrayBuffer()).byteLength, 512 * 1024);
  }

  const head = await handler(new Request(url, { method: 'HEAD' }));
  assert.equal(head.status, 200);
  assert.equal(head.headers.get('content-type'), 'image/png');
  assert.equal(head.headers.get('content-length'), String(body.length));
  assert.equal(await head.text(), '', 'HEAD 不返回正文');

  assert.equal(imageMime('x.JPEG'), 'image/jpeg');
  assert.equal(imageMime('x.tiff'), 'image/tiff');
  assert.equal(imageMime('x.ico'), 'image/x-icon');
  assert.equal(imageMime('x.txt'), '');
});

test('协议处理：未授权、越界、符号链接替换、非法格式与坏 URL 一律 404', async () => {
  const guard = createPathGuard();
  const handler = createImageProtocolHandler({ guard });
  guard.authorizeFile(A1);

  // 未授权（外部目录）→ 404：URL 里带着真实路径也没用，每次请求重新走 guard.check
  assert.equal((await handler(new Request(imageUrl(realOrEmpty(SECRET))))).status, 404);
  // 目录穿越写法
  const traversal = path.join(DIR_A, '..', '外部', '私密.jpg');
  assert.equal((await handler(new Request(imageUrl(traversal)))).status, 404);
  // 同目录非图片文件
  assert.equal((await handler(new Request(imageUrl(realOrEmpty(A_TXT))))).status, 404);
  // 目录本身
  assert.equal((await handler(new Request(imageUrl(realOrEmpty(DIR_A))))).status, 404);
  // 旧令牌 / file:// / 空 URL
  assert.equal((await handler(new Request('shade-image://' + 'b'.repeat(48)))).status, 404);
  assert.equal((await handler(new Request('file:///etc/passwd'))).status, 404);
  assert.equal((await handler(new Request('shade-image://local'))).status, 404);
  assert.equal((await handler({ url: '', method: 'GET' })).status, 404);

  // 授权后文件被替换成指向外部的符号链接：URL 未变也必须立即 404
  const swap = w(path.join(DIR_A, 'swap-stream.jpg'), 'jpg');
  guard.authorizeFile(swap);
  const swapUrl = imageUrl(realOrEmpty(swap));
  assert.equal((await handler(new Request(swapUrl))).status, 200);
  fs.rmSync(swap);
  try {
    fs.symlinkSync(SECRET, swap);
  } catch (_e) {
    return;
  }
  assert.equal((await handler(new Request(swapUrl))).status, 404, '外部替换后拒绝');

  // 删除后 404
  const gone = w(path.join(DIR_A, '会消失.png'), 'png');
  guard.authorizeFile(gone);
  const goneUrl = imageUrl(realOrEmpty(gone));
  assert.equal((await handler(new Request(goneUrl))).status, 200);
  fs.rmSync(gone);
  assert.equal((await handler(new Request(goneUrl))).status, 404);
});

test('协议处理：必须传入路径授权表；服务不再导出令牌表', () => {
  assert.throws(() => createImageProtocolHandler({}), /授权表/);
  assert.throws(() => createImageProtocolHandler(), /授权表/);
  const mod = require('../electron/imagetools/path-guard.cjs');
  assert.equal(mod.createImageTokenRegistry, undefined, '不得保留令牌表兼容层');
});

// ---- Windows 8.3 短名：同一真实目录的长名/短名必须是同一个授权对象 ----
// v2.4.2 CI：冷启动 argv 给的是 C:\Users\RUNNER~1\...，核心随后返回 C:\Users\runneradmin\...，
// 预览与改名因此报「未授权的图片路径」。根因是 realOrEmpty 用了 legacy fs.realpathSync——
// 它把输入的短名原样返回，不做 8.3 → 长名归一（fs.realpathSync.native 才会归一）。
// 这里用系统只读目录 C:\Program Files（短名 C:\PROGRA~1）做真实回归，不写任何文件；
// 拿不到短名（8.3 被关闭/非 Windows）时跳过，不伪造成通过。
const isWin = process.platform === 'win32';
const PROG_LONG = 'C:\\Program Files';
const PROG_SHORT = 'C:\\PROGRA~1';
const hasProgShort =
  isWin &&
  fs.existsSync(PROG_LONG) &&
  fs.existsSync(PROG_SHORT) &&
  PROG_SHORT !== fs.realpathSync.native(PROG_SHORT);

test(
  'Windows 真实路径口径：8.3 短名先归一成长名（native realpath）',
  { skip: !hasProgShort },
  () => {
    if (typeof realOrEmpty !== 'function') {
      assert.fail('realOrEmpty 未导出，无法验证真实路径口径');
    }
    assert.equal(realOrEmpty(PROG_SHORT), fs.realpathSync.native(PROG_SHORT));
    assert.equal(realOrEmpty(PROG_SHORT), realOrEmpty(PROG_LONG), '长短名必须归一为同一真实路径');
    assert.equal(realOrEmpty(PROG_SHORT), PROG_LONG);
    // 「最深已存在祖先」的拼接同理：短名祖先也要拼成长名
    assert.equal(
      resolveRealisticPath(PROG_SHORT + '\\uvmp-not-created-子目录'),
      path.join(PROG_LONG, 'uvmp-not-created-子目录')
    );
  }
);

test(
  'Windows 8.3 短名：授权一次目录后长短名都命中，且是同一个授权对象',
  { skip: !hasProgShort },
  () => {
    const guard = createPathGuard();
    const viaShort = guard.authorizeDirectory(PROG_SHORT);
    assert.equal(viaShort, PROG_LONG, '授权短名也要登记成长名');
    // 长名侧复查：同一个目录
    assert.equal(guard.checkDirectory(PROG_LONG), PROG_LONG);
    assert.equal(guard.checkDirectory(PROG_SHORT), PROG_LONG, '短名复查也必须命中');
    assert.equal(guard.dirCount, 1, '长短名不得占两条授权（否则会被 maxDirs 淘汰误伤）');

    // 反向：先授权长名，短名同样命中
    const guard2 = createPathGuard();
    assert.equal(guard2.authorizeDirectory(PROG_LONG), PROG_LONG);
    assert.equal(guard2.checkDirectory(PROG_SHORT), PROG_LONG);
    assert.equal(guard2.dirCount, 1);

    // 目录边界不放宽：同盘其它目录（含短名形态）仍被拒绝
    assert.equal(guard.checkDirectory('C:\\Windows'), '');
    assert.equal(guard.checkDirectory(path.join(PROG_LONG, '..', 'Windows')), '');
    if (fs.existsSync('C:\\PROGRA~2')) {
      assert.equal(guard.checkDirectory('C:\\PROGRA~2'), '', '相邻短名目录不得误放行');
    }
    // clear 语义不变
    guard.clear();
    assert.equal(guard.checkDirectory(PROG_LONG), '');
    assert.equal(guard.checkDirectory(PROG_SHORT), '');
  }
);

// 文件级回归（与 CI 故障同形）：在临时目录造一个含空格的长名目录，取它的 8.3 短名，
// 用短名授权、再用长名读同一个图片文件。CI 里正是「冷启动 argv 短名授权 → 核心返回长名」。
// 短名由系统 API 给出（Scripting.FileSystemObject），拿不到就跳过，不伪造成通过。
function windowsShortPath(longPath) {
  const { spawnSync } = require('child_process');
  const script = [
    '$f = New-Object -ComObject Scripting.FileSystemObject;',
    `$f.GetFolder('${String(longPath).replace(/'/g, "''")}').ShortPath`,
  ].join(' ');
  const r = spawnSync('powershell', ['-NoProfile', '-NonInteractive', '-Command', script], {
    encoding: 'utf8',
    windowsHide: true,
    timeout: 20000,
  });
  if (r.status !== 0) return '';
  return String(r.stdout || '').trim();
}

const SHORT_DIR_LONG = path.join(TMP, 'Long Named Album Dir');
fs.mkdirSync(SHORT_DIR_LONG, { recursive: true });
const SHORT_IMG = w(path.join(SHORT_DIR_LONG, 'photo one.jpg'));
const SHORT_DIR_8_3 = isWin ? windowsShortPath(SHORT_DIR_LONG) : '';

test(
  'Windows 8.3 短名（文件级）：短名授权的目录能被长名图片命中，反之亦然',
  { skip: !isWin || !SHORT_DIR_8_3 || SHORT_DIR_8_3 === SHORT_DIR_LONG },
  () => {
    const shortImg = path.join(SHORT_DIR_8_3, 'photo one.jpg');
    // 前提：短名路径确实指向同一个文件（否则这条回归没有意义）
    assert.ok(fs.existsSync(shortImg), `短名路径不可用：${shortImg}`);
    const canonical = fs.realpathSync.native(SHORT_IMG);

    const guard = createPathGuard();
    // ① 用短名选文件 → 授权父目录 → 长名可读（CI 冷启动 argv 短名、核心长名的方向）
    assert.equal(guard.authorizeFile(shortImg), canonical, '短名选文件也要归一成长名');
    assert.equal(guard.check(SHORT_IMG), canonical, '长名复查必须命中');
    assert.equal(guard.check(shortImg), canonical, '短名复查也必须命中');
    assert.equal(guard.dirCount, 1, '长短名不得占两条授权');

    // ② 反向：长名选目录 → 短名可读
    const guard2 = createPathGuard();
    assert.equal(
      guard2.authorizeDirectory(SHORT_DIR_LONG),
      canonical.replace(/\\photo one\.jpg$/, '')
    );
    assert.equal(guard2.check(shortImg), canonical);

    // ③ 边界不放宽：同级的另一个长名目录仍拒绝
    const otherDir = path.join(TMP, 'Another Long Dir');
    fs.mkdirSync(otherDir, { recursive: true });
    const other = w(path.join(otherDir, 'other.jpg'));
    assert.equal(guard.check(other), '');
    assert.equal(guard.check(SHORT_IMG), canonical, '拒绝别的目录后原授权仍然有效');
  }
);
