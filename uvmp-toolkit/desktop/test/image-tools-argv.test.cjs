// 图片窗口启动参数解析回归：开发态/打包态、空格与中文路径、file URI、第二实例与开关过滤。
const test = require('node:test');
const assert = require('node:assert/strict');
const os = require('os');
const path = require('path');
const { pathToFileURL } = require('url');

const { parseImageArgs, isViewablePath, stripFileUri } = require('../electron/imagetools/argv.cjs');

const EXE = 'C:\\Program Files\\Shade 壹匣\\uvmp-toolkit.exe';
const APP_DIR = path.resolve('/opt/app/desktop');
const CWD = path.resolve(os.tmpdir(), 'uvmp argv 工作目录');
const DIR = path.resolve(os.tmpdir(), '图片 样本 目录');
const IMG_JPG = path.join(DIR, '照片 A.JPG');
const IMG_PNG = path.join(DIR, '带 空格 的图.png');
const IMG_WEBP = path.join(DIR, '图三.webp');

test('打包态：exe 之后的位置参数即待打开图片（空格/中文/反斜杠保持）', () => {
  const files = parseImageArgs([EXE, IMG_JPG, IMG_PNG], {
    isPackaged: true,
    appPath: APP_DIR,
  });
  assert.deepEqual(
    files,
    [IMG_JPG, IMG_PNG].map((p) => path.resolve(p))
  );
});

test('开发态：跳过 electron 的 app 路径（. 与绝对目录）只取其后参数', () => {
  assert.deepEqual(
    parseImageArgs(['electron.exe', '.', IMG_JPG], {
      isPackaged: false,
      appPath: APP_DIR,
      cwd: APP_DIR,
    }),
    [path.resolve(IMG_JPG)]
  );
  assert.deepEqual(
    parseImageArgs(['electron.exe', APP_DIR, IMG_PNG], {
      isPackaged: false,
      appPath: APP_DIR,
      cwd: path.resolve('/'),
    }),
    [path.resolve(IMG_PNG)]
  );
  // 没有 app 路径时只过滤图片后缀，不会把开关当文件
  assert.deepEqual(
    parseImageArgs(['electron.exe', IMG_WEBP], { isPackaged: false, appPath: APP_DIR }),
    [path.resolve(IMG_WEBP)]
  );
});

test('开关参数被忽略：Chromium/Electron 开关及其值不当作文件', () => {
  const files = parseImageArgs(
    ['electron.exe', '.', '--no-sandbox', '--inspect=9229', '--user-data-dir=C:\\tmp\\p', IMG_JPG],
    { isPackaged: false, appPath: APP_DIR, cwd: APP_DIR }
  );
  assert.deepEqual(files, [path.resolve(IMG_JPG)]);
});

test('-- 之后的内容一律按路径处理（含 - 开头的文件名）', () => {
  const dash = path.join(DIR, '-以横线开头.jpg');
  assert.deepEqual(
    parseImageArgs([EXE, '--no-sandbox', '--', dash], { isPackaged: true, appPath: APP_DIR }),
    [path.resolve(dash)]
  );
  // -- 之前的孤立开关仍然忽略
  assert.deepEqual(
    parseImageArgs([EXE, '--', '--looks-like-switch.png'], { isPackaged: true, appPath: APP_DIR }),
    [path.resolve('--looks-like-switch.png')]
  );
});

test('file URI：解码空格与中文，兼容 file:///C:/ 形态', () => {
  const uri = pathToFileURL(IMG_JPG).href;
  assert.match(uri, /%20/);
  assert.equal(stripFileUri(uri), path.resolve(IMG_JPG));
  assert.deepEqual(parseImageArgs([EXE, uri], { isPackaged: true }), [path.resolve(IMG_JPG)]);
  // 非 file 协议、坏 URI 一律丢弃
  assert.equal(stripFileUri('https://example.com/a.jpg'), '');
  assert.equal(stripFileUri('file://%E0%A4%A'), '');
  assert.deepEqual(parseImageArgs([EXE, 'https://example.com/a.jpg'], { isPackaged: true }), []);
});

test('第二实例：按 cwd 解析相对路径并去重', () => {
  const files = parseImageArgs(['uvmp-toolkit.exe', '子目录/图 A.jpg', '子目录/图 A.jpg'], {
    isPackaged: true,
    cwd: CWD,
  });
  assert.deepEqual(files, [path.join(CWD, '子目录', '图 A.jpg')]);
  // 同一文件以绝对路径 + file URI 各给一次也算一个
  const dup = parseImageArgs([EXE, IMG_PNG, pathToFileURL(IMG_PNG).href], {
    isPackaged: true,
    appPath: APP_DIR,
  });
  assert.deepEqual(dup, [path.resolve(IMG_PNG)]);
});

test('只放行受支持的查看格式（大小写不敏感，HEIC/PSD/PDF 不接收）', () => {
  const exts = ['.jpg', '.jpeg', '.png', '.gif', '.webp', '.bmp', '.ico', '.avif', '.tif', '.tiff'];
  for (const ext of exts) assert.equal(isViewablePath('图' + ext), true, ext + ' 应放行');
  assert.equal(isViewablePath('图.JPEG'), true);
  for (const ext of ['.heic', '.psd', '.pdf', '.svg', '.txt', '']) {
    assert.equal(isViewablePath('图' + ext), false, ext + ' 不应放行');
  }
  const files = parseImageArgs(
    [
      EXE,
      path.join(DIR, '说明.txt'),
      path.join(DIR, '相册.heic'),
      IMG_JPG,
      path.join(DIR, '无后缀'),
    ],
    { isPackaged: true }
  );
  assert.deepEqual(files, [path.resolve(IMG_JPG)]);
});

test('入参防御：空 argv、非数组、非字符串项都不抛异常', () => {
  assert.deepEqual(parseImageArgs([]), []);
  assert.deepEqual(parseImageArgs(null), []);
  assert.deepEqual(parseImageArgs([EXE, null, 42, {}, ''], { isPackaged: true }), []);
});

test('数量上限：一次传入过多文件时截断到 200，避免刷爆窗口', () => {
  const many = [];
  for (let i = 0; i < 260; i += 1) many.push(path.join(DIR, '图' + i + '.jpg'));
  assert.equal(parseImageArgs([EXE, ...many], { isPackaged: true }).length, 200);
});
