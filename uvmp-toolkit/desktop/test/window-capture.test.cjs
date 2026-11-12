// 真实桌面窗口截图回归：整屏帧裁剪几何、显示器/缩放换算、屏幕源核对与失败即报错。
// 全部用注入的假 capturer/display/win，不连 Electron 运行时，也不读取真实屏幕。
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

const { captureWindowDesktop } = require('../electron/window-capture.cjs');

// 允许冒烟模式；单个用例（隐私边界）会临时删除后再恢复
process.env.UVMP_CI_SMOKE = '1';

function pngWith(marker = 0) {
  return Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, marker]);
}

function fakeImage({ width, height, png = pngWith(), cropFactory = null } = {}) {
  const state = { cropRects: [] };
  const image = {
    state,
    getSize: () => ({ width, height }),
    isEmpty: () => width <= 0 || height <= 0,
    crop(rect) {
      state.cropRects.push(rect);
      if (cropFactory) return cropFactory(rect);
      return fakeImage({ width: rect.width, height: rect.height, png });
    },
    toPNG: () => png,
  };
  return image;
}

function source(displayId, image) {
  return { display_id: displayId, thumbnail: image };
}

function fakeCapturer(sources, { throws = null } = {}) {
  const calls = [];
  return {
    calls,
    async getSources(opts) {
      calls.push(opts);
      if (throws) throw new Error(throws);
      return sources;
    },
  };
}

function fakeWin({
  bounds = { x: 100, y: 80, width: 1160, height: 780 },
  contentSize = [1160, 780],
  contentBounds = undefined,
  destroyed = false,
  minimized = false,
  visible = true,
  title = '不应出现在日志里的窗口标题',
} = {}) {
  const win = {
    getBounds: () => ({ ...bounds }),
    getContentSize: () => contentSize,
    isDestroyed: () => destroyed,
    isMinimized: () => minimized,
    isVisible: () => visible,
    getTitle: () => title,
  };
  if (contentBounds !== undefined) {
    win.getContentBounds =
      typeof contentBounds === 'function' ? contentBounds : () => ({ ...contentBounds });
  }
  return win;
}

function fakeDisplay({
  id = 1,
  bounds = { x: 0, y: 0, width: 1920, height: 1080 },
  scaleFactor = 1,
} = {}) {
  return {
    id,
    scaleFactor,
    bounds: { ...bounds },
    size: { width: bounds.width, height: bounds.height },
  };
}

function tempDir(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'window-capture-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  return dir;
}

async function rejectsWithCode(promise, code) {
  await assert.rejects(promise, (err) => {
    assert.equal(err.code, code, `期望 ${code}，实际 ${err.code}: ${err.message}`);
    return true;
  });
}

test('100%：按窗口 DIP 边界裁整屏帧，保存 1:1 PNG 并返回尺寸/DOM', async (t) => {
  const dir = tempDir(t);
  const outputPath = path.join(dir, 'window.png');
  const display = fakeDisplay();
  const image = fakeImage({ width: 1920, height: 1080, png: pngWith(7) });
  const capturer = fakeCapturer([source('1', image)]);
  const dom = {
    titlebarButtons: [
      { action: 'min', rect: { x: 0, y: 0, width: 44, height: 32 } },
      { action: 'max', bounds: { x: 44, y: 0, width: 44, height: 32 } },
      { action: 'close', x: 88, y: 0, width: 44, height: 32 },
      { action: 'bad' },
    ],
  };
  const logs = [];

  const result = await captureWindowDesktop({
    win: fakeWin({ contentSize: [1158, 760] }),
    display,
    outputPath,
    dom,
    capturer,
    log: (line) => logs.push(line),
  });

  assert.deepEqual(capturer.calls, [
    { types: ['screen'], thumbnailSize: { width: 1920, height: 1080 } },
  ]);
  assert.deepEqual(image.state.cropRects, [{ x: 100, y: 80, width: 1160, height: 780 }]);
  assert.deepEqual(result, {
    outputPath,
    width: 1160,
    height: 780,
    bounds: { x: 100, y: 80, width: 1160, height: 780 },
    scaleFactor: 1,
    dom,
  });
  assert.equal(result.dom, dom);
  assert.deepEqual(fs.readFileSync(outputPath), pngWith(7));

  assert.equal(logs.length, 1);
  assert.match(logs[0], /窗口 DIP 1160x780 @ \(100,80\)/);
  assert.match(logs[0], /内容 1158x760/);
  assert.match(logs[0], /DPI 96（scale 1）/);
  assert.match(logs[0], /截图 1160x780 px/);
  assert.match(logs[0], /min\(0,0,44,32\) max\(44,0,44,32\) close\(88,0,44,32\)/);
  assert.match(logs[0], /bad\(边界无效\)/);
  assert.doesNotMatch(logs[0], /不应出现在日志里的窗口标题/);
});

test('150% 缩放：DIP 坐标乘 scaleFactor 后裁物理像素，DPI 随缩放', async (t) => {
  const dir = tempDir(t);
  const outputPath = path.join(dir, 'hidpi.png');
  const display = fakeDisplay({
    bounds: { x: 0, y: 0, width: 1280, height: 720 },
    scaleFactor: 1.5,
  });
  const image = fakeImage({ width: 1920, height: 1080 });
  const capturer = fakeCapturer([source('1', image)]);
  const logs = [];

  const result = await captureWindowDesktop({
    win: fakeWin({ bounds: { x: 120, y: 90, width: 800, height: 600 }, contentSize: [800, 600] }),
    display,
    outputPath,
    capturer,
    log: (line) => logs.push(line),
  });

  assert.deepEqual(image.state.cropRects, [{ x: 180, y: 135, width: 1200, height: 900 }]);
  assert.equal(result.width, 1200);
  assert.equal(result.height, 900);
  assert.equal(result.scaleFactor, 1.5);
  assert.match(logs[0], /DPI 144（scale 1.5）/);
});

test('副屏负坐标：按显示器 bounds 原点换算，不把负坐标当成越界', async (t) => {
  const dir = tempDir(t);
  const display = fakeDisplay({
    id: 3,
    bounds: { x: -1920, y: 0, width: 1920, height: 1080 },
  });
  const image = fakeImage({ width: 1920, height: 1080 });
  const capturer = fakeCapturer([source('3', image)]);

  const result = await captureWindowDesktop({
    win: fakeWin({ bounds: { x: -1820, y: 100, width: 1160, height: 780 } }),
    display,
    outputPath: path.join(dir, 'secondary.png'),
    capturer,
    log: () => {},
  });

  assert.deepEqual(image.state.cropRects, [{ x: 100, y: 100, width: 1160, height: 780 }]);
  assert.equal(result.bounds.x, -1820);
});

test('未传 display 时用注入的 screen 解析；单屏空 display_id 允许回退', async (t) => {
  const dir = tempDir(t);
  const display = fakeDisplay();
  const image = fakeImage({ width: 1920, height: 1080 });
  const capturer = fakeCapturer([source('', image)]);
  const screen = {
    getDisplayMatching: (bounds) => {
      assert.deepEqual(bounds, { x: 100, y: 80, width: 1160, height: 780 });
      return display;
    },
    getAllDisplays: () => [display],
  };

  const result = await captureWindowDesktop({
    win: fakeWin(),
    outputPath: path.join(dir, 'from-screen.png'),
    capturer,
    screen,
    log: () => {},
  });

  assert.equal(result.width, 1160);
  assert.equal(capturer.calls.length, 1);
});

test('多显示器：按 display_id 选源，不盲取 sources[0]', async (t) => {
  const dir = tempDir(t);
  const outputPath = path.join(dir, 'picked.png');
  const display = fakeDisplay({ id: 1 });
  const wrong = fakeImage({ width: 1920, height: 1080, png: pngWith(1) });
  const right = fakeImage({ width: 1920, height: 1080, png: pngWith(2) });
  const capturer = fakeCapturer([source('2', wrong), source('1', right)]);

  await captureWindowDesktop({
    win: fakeWin(),
    display,
    outputPath,
    capturer,
    displays: [display, fakeDisplay({ id: 2 })],
    log: () => {},
  });

  assert.deepEqual(wrong.state.cropRects, [], '不得裁剪错误显示器的源');
  assert.equal(right.state.cropRects.length, 1);
  assert.deepEqual(fs.readFileSync(outputPath), pngWith(2));
});

test('多显示器且无匹配 display_id：明确报错，不盲取', async (t) => {
  const dir = tempDir(t);
  const display = fakeDisplay({ id: 1 });
  const capturer = fakeCapturer([
    source('2', fakeImage({ width: 1920, height: 1080 })),
    source('3', fakeImage({ width: 1920, height: 1080 })),
  ]);

  await rejectsWithCode(
    captureWindowDesktop({
      win: fakeWin(),
      display,
      outputPath: path.join(dir, 'no-match.png'),
      capturer,
      displays: [display, fakeDisplay({ id: 2 }), fakeDisplay({ id: 3 })],
      log: () => {},
    }),
    'ERR_WINDOW_CAPTURE_SOURCE'
  );
  assert.equal(fs.existsSync(path.join(dir, 'no-match.png')), false);
});

test('屏幕源尺寸与显示器物理尺寸不一致：拒绝截图（否则会保存缩放图）', async (t) => {
  const dir = tempDir(t);
  const outputPath = path.join(dir, 'mismatch.png');
  const display = fakeDisplay();
  const image = fakeImage({ width: 1280, height: 720 });
  const capturer = fakeCapturer([source('1', image)]);

  await rejectsWithCode(
    captureWindowDesktop({
      win: fakeWin(),
      display,
      outputPath,
      capturer,
      log: () => {},
    }),
    'ERR_WINDOW_CAPTURE_SOURCE_SIZE'
  );
  assert.deepEqual(image.state.cropRects, []);
  assert.equal(fs.existsSync(outputPath), false);
});

test('窗口超出指定显示器：先报错，不取屏幕源也不落盘', async (t) => {
  const dir = tempDir(t);
  const outputPath = path.join(dir, 'offscreen.png');
  const capturer = fakeCapturer([source('1', fakeImage({ width: 1920, height: 1080 }))]);

  await rejectsWithCode(
    captureWindowDesktop({
      win: fakeWin({ bounds: { x: 1800, y: 80, width: 1160, height: 780 } }),
      display: fakeDisplay(),
      outputPath,
      capturer,
      log: () => {},
    }),
    'ERR_WINDOW_CAPTURE_OFFSCREEN'
  );
  assert.equal(capturer.calls.length, 0);
  assert.equal(fs.existsSync(outputPath), false);
});

test('窗口已销毁 / 最小化 / 不可见：明确报错', async (t) => {
  const dir = tempDir(t);
  const cases = [
    { win: null },
    { win: fakeWin({ destroyed: true }) },
    { win: fakeWin({ minimized: true }) },
    { win: fakeWin({ visible: false }) },
  ];
  for (const [index, item] of cases.entries()) {
    await rejectsWithCode(
      captureWindowDesktop({
        win: item.win,
        display: fakeDisplay(),
        outputPath: path.join(dir, `bad-${index}.png`),
        capturer: fakeCapturer([]),
        log: () => {},
      }),
      'ERR_WINDOW_CAPTURE_WINDOW'
    );
  }
});

test('未指定 outputPath：直接报错，默认不写临时文件', async () => {
  const capturer = fakeCapturer([source('1', fakeImage({ width: 1920, height: 1080 }))]);
  await rejectsWithCode(
    captureWindowDesktop({
      win: fakeWin(),
      display: fakeDisplay(),
      capturer,
      log: () => {},
    }),
    'ERR_WINDOW_CAPTURE_OUTPUT'
  );
  assert.equal(capturer.calls.length, 0);

  await rejectsWithCode(
    captureWindowDesktop({
      win: fakeWin(),
      display: fakeDisplay(),
      outputPath: '   ',
      capturer,
      log: () => {},
    }),
    'ERR_WINDOW_CAPTURE_OUTPUT'
  );
});

test('非 UVMP_CI_SMOKE：拒绝真实屏幕采集（隐私边界）', async (t) => {
  const dir = tempDir(t);
  const old = process.env.UVMP_CI_SMOKE;
  delete process.env.UVMP_CI_SMOKE;
  try {
    await rejectsWithCode(
      captureWindowDesktop({
        win: fakeWin(),
        display: fakeDisplay(),
        outputPath: path.join(dir, 'disabled.png'),
        capturer: fakeCapturer([source('1', fakeImage({ width: 1920, height: 1080 }))]),
        log: () => {},
      }),
      'ERR_WINDOW_CAPTURE_DISABLED'
    );
  } finally {
    process.env.UVMP_CI_SMOKE = old || '1';
  }
  assert.equal(fs.existsSync(path.join(dir, 'disabled.png')), false);
});

test('裁剪结果尺寸不符：报错且不保存（防止 nativeImage.crop 静默夹取）', async (t) => {
  const dir = tempDir(t);
  const outputPath = path.join(dir, 'crop-mismatch.png');
  const image = fakeImage({
    width: 1920,
    height: 1080,
    cropFactory: (rect) => fakeImage({ width: rect.width - 1, height: rect.height }),
  });

  await rejectsWithCode(
    captureWindowDesktop({
      win: fakeWin(),
      display: fakeDisplay(),
      outputPath,
      capturer: fakeCapturer([source('1', image)]),
      log: () => {},
    }),
    'ERR_WINDOW_CAPTURE_CROP'
  );
  assert.equal(fs.existsSync(outputPath), false);
});

test('裁剪结果不是 PNG：报错且不保存', async (t) => {
  const dir = tempDir(t);
  const outputPath = path.join(dir, 'bad.png');
  const image = fakeImage({ width: 1920, height: 1080, png: Buffer.from('not-a-png') });

  await rejectsWithCode(
    captureWindowDesktop({
      win: fakeWin(),
      display: fakeDisplay(),
      outputPath,
      capturer: fakeCapturer([source('1', image)]),
      log: () => {},
    }),
    'ERR_WINDOW_CAPTURE_PNG'
  );
  assert.equal(fs.existsSync(outputPath), false);
});

test('getSources 失败 / 空源列表：明确报错', async (t) => {
  const dir = tempDir(t);
  await rejectsWithCode(
    captureWindowDesktop({
      win: fakeWin(),
      display: fakeDisplay(),
      outputPath: path.join(dir, 'get-sources-fail.png'),
      capturer: fakeCapturer([], { throws: '屏幕采集被拒绝' }),
      log: () => {},
    }),
    'ERR_WINDOW_CAPTURE_SOURCE'
  );
  await rejectsWithCode(
    captureWindowDesktop({
      win: fakeWin(),
      display: fakeDisplay(),
      outputPath: path.join(dir, 'empty-sources.png'),
      capturer: fakeCapturer([]),
      log: () => {},
    }),
    'ERR_WINDOW_CAPTURE_SOURCE'
  );
});

test('输出目录不存在时自动创建；未提供 DOM 时返回 null 并标记日志', async (t) => {
  const dir = tempDir(t);
  const outputPath = path.join(dir, 'nested', 'deep', 'window.png');
  const logs = [];

  const result = await captureWindowDesktop({
    win: fakeWin(),
    display: fakeDisplay(),
    outputPath,
    capturer: fakeCapturer([source('1', fakeImage({ width: 1920, height: 1080 }))]),
    log: (line) => logs.push(line),
  });

  assert.equal(result.dom, null);
  assert.equal(fs.existsSync(outputPath), true);
  assert.match(logs[0], /标题栏按钮 未提供/);
});

test('padding：DIP 向外扩展裁剪范围，含阴影外框，返回 captureBounds', async (t) => {
  const dir = tempDir(t);
  const outputPath = path.join(dir, 'padding.png');
  const display = fakeDisplay();
  const image = fakeImage({ width: 1920, height: 1080 });
  const capturer = fakeCapturer([source('1', image)]);
  const logs = [];

  const result = await captureWindowDesktop({
    win: fakeWin(),
    display,
    outputPath,
    padding: 8,
    capturer,
    log: (line) => logs.push(line),
  });

  assert.deepEqual(image.state.cropRects, [{ x: 92, y: 72, width: 1176, height: 796 }]);
  assert.deepEqual(result.captureBounds, { x: 92, y: 72, width: 1176, height: 796 });
  assert.deepEqual(result.bounds, { x: 100, y: 80, width: 1160, height: 780 });
  assert.equal(result.width, 1176);
  assert.equal(result.height, 796);
  assert.match(logs[0], /padding 8 DIP → 截图范围 1176x796 @ \(92,72\)/);
  assert.match(logs[0], /截图 1176x796 px/);
});

test('padding + 高缩放 + 负坐标副屏：按显示器原点换算物理像素', async (t) => {
  const dir = tempDir(t);
  const display = fakeDisplay({
    id: 3,
    bounds: { x: -1920, y: 0, width: 1920, height: 1080 },
    scaleFactor: 1.5,
  });
  const image = fakeImage({ width: 2880, height: 1620 });
  const capturer = fakeCapturer([source('3', image)]);
  const logs = [];

  const result = await captureWindowDesktop({
    win: fakeWin({ bounds: { x: -1820, y: 100, width: 1160, height: 780 } }),
    display,
    outputPath: path.join(dir, 'padding-secondary.png'),
    padding: 10,
    capturer,
    log: (line) => logs.push(line),
  });

  // 扩展后 DIP 起点 (-1830,90)，相对屏幕原点 (-1920,0) 为 (90,90)，乘 1.5 得 (135,135)
  assert.deepEqual(image.state.cropRects, [{ x: 135, y: 135, width: 1770, height: 1200 }]);
  assert.deepEqual(result.captureBounds, { x: -1830, y: 90, width: 1180, height: 800 });
  assert.equal(result.width, 1770);
  assert.equal(result.height, 1200);
  assert.match(logs[0], /DPI 144（scale 1.5）/);
});

test('padding 后越界：明确报错，不取屏幕源也不落盘', async (t) => {
  const dir = tempDir(t);
  const outputPath = path.join(dir, 'padding-offscreen.png');
  const capturer = fakeCapturer([source('1', fakeImage({ width: 1920, height: 1080 }))]);

  await assert.rejects(
    captureWindowDesktop({
      win: fakeWin({ bounds: { x: 0, y: 0, width: 1160, height: 780 } }),
      display: fakeDisplay(),
      outputPath,
      padding: 10,
      capturer,
      log: () => {},
    }),
    (err) => {
      assert.equal(err.code, 'ERR_WINDOW_CAPTURE_OFFSCREEN');
      assert.equal(err.details.padding, 10);
      assert.deepEqual(err.details.captureBounds, { x: -10, y: -10, width: 1180, height: 800 });
      assert.ok(err.details.overflow.left >= 10);
      return true;
    }
  );
  assert.equal(capturer.calls.length, 0);
  assert.equal(fs.existsSync(outputPath), false);
});

test('padding 非法（负数/非数字）与默认值：0 时结果与旧行为一致', async (t) => {
  const dir = tempDir(t);
  const capturer = fakeCapturer([source('1', fakeImage({ width: 1920, height: 1080 }))]);
  for (const padding of [-1, Number.NaN, 'abc', Number.POSITIVE_INFINITY]) {
    await rejectsWithCode(
      captureWindowDesktop({
        win: fakeWin(),
        display: fakeDisplay(),
        outputPath: path.join(dir, `bad-padding-${String(padding)}.png`),
        padding,
        capturer,
        log: () => {},
      }),
      'ERR_WINDOW_CAPTURE_PADDING'
    );
  }
  assert.equal(capturer.calls.length, 0);

  const image = fakeImage({ width: 1920, height: 1080 });
  const result = await captureWindowDesktop({
    win: fakeWin(),
    display: fakeDisplay(),
    outputPath: path.join(dir, 'padding-zero.png'),
    padding: 0,
    capturer: fakeCapturer([source('1', image)]),
    log: () => {},
  });
  assert.equal('captureBounds' in result, false);
  assert.deepEqual(image.state.cropRects, [{ x: 100, y: 80, width: 1160, height: 780 }]);
});

test('日志读 getContentBounds() 并只打印边界类信息', async (t) => {
  const dir = tempDir(t);
  const outputPath = path.join(dir, 'content-bounds.png');
  const logs = [];

  const result = await captureWindowDesktop({
    win: fakeWin({
      contentBounds: { x: 101, y: 112, width: 1158, height: 748 },
    }),
    display: fakeDisplay(),
    outputPath,
    padding: 4,
    capturer: fakeCapturer([source('1', fakeImage({ width: 1920, height: 1080 }))]),
    log: (line) => logs.push(line),
  });

  assert.equal(logs.length, 1);
  assert.match(logs[0], /内容区 1158x748 @ \(101,112\)/);
  assert.match(logs[0], /内容 1160x780/);
  assert.match(logs[0], /标题栏按钮 未提供/);
  assert.doesNotMatch(logs[0], /不应出现在日志里的窗口标题/);
  assert.equal(logs[0].includes(outputPath), false, '日志不得输出完整路径');
  assert.equal(logs[0].includes(dir), false);
  assert.deepEqual(result.bounds, { x: 100, y: 80, width: 1160, height: 780 });
});

test('getContentBounds 缺失或抛错：日志标记未提供，截图照常完成', async (t) => {
  const dir = tempDir(t);
  const logs = [];

  await captureWindowDesktop({
    win: fakeWin(),
    display: fakeDisplay(),
    outputPath: path.join(dir, 'no-content-bounds.png'),
    capturer: fakeCapturer([source('1', fakeImage({ width: 1920, height: 1080 }))]),
    log: (line) => logs.push(line),
  });
  assert.match(logs[0], /内容区 未提供/);

  logs.length = 0;
  await captureWindowDesktop({
    win: fakeWin({
      contentBounds: () => {
        throw new Error('getContentBounds 不可用');
      },
    }),
    display: fakeDisplay(),
    outputPath: path.join(dir, 'throwing-content-bounds.png'),
    capturer: fakeCapturer([source('1', fakeImage({ width: 1920, height: 1080 }))]),
    log: (line) => logs.push(line),
  });
  assert.match(logs[0], /内容区 未提供/);
});
