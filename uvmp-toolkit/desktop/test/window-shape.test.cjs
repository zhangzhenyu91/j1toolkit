// 圆角窗口形状回归：区域几何、原生外框优先/回退、最大化/全屏切换与重算时机。
// 真机 Windows/Linux 的 setShape 行为在此用假窗口替身断言（不含原生绘制）。
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');

const {
  WINDOW_RADIUS,
  roundedWindowShape,
  shapeForWindow,
  applyWindowShape,
  createShapeScheduler,
  displayShapeKey,
} = require('../electron/window-shape.cjs');

const DESKTOP = path.join(__dirname, '..');

function fakeWindow({
  size = [1160, 780],
  maximized = false,
  fullscreen = false,
  destroyed = false,
  throwOnShape = false,
} = {}) {
  const calls = [];
  const win = {
    calls,
    size,
    maximized,
    fullscreen,
    destroyed,
    contentReads: 0,
    isDestroyed: () => win.destroyed,
    // 内容区只是原生外框查询失败时的回退值，单独计数以便断言回退是否真的发生
    getContentSize: () => {
      win.contentReads += 1;
      return win.size;
    },
    isMaximized: () => win.maximized,
    isFullScreen: () => win.fullscreen,
    setShape: (rects) => {
      if (throwOnShape) throw new Error('setShape 失败');
      calls.push(rects);
    },
  };
  return win;
}

// 注入的原生外框查询：返回 DIP 外框并记录调用次数（Windows 最大化不应派生查询）。
function nativeOuter(bounds) {
  const query = () => {
    query.calls.push(bounds);
    return bounds;
  };
  query.calls = [];
  return query;
}

// 手动时钟：只跑当前排队中的定时器，便于断言「立即一次 + 落定后复核」。
function manualClock() {
  let seq = 0;
  const tasks = new Map();
  return {
    tasks,
    set(fn, delay) {
      seq += 1;
      tasks.set(seq, { fn, delay, at: 0 });
      return seq;
    },
    clear(id) {
      tasks.delete(id);
    },
    advance() {
      const pending = [...tasks.values()];
      tasks.clear();
      for (const task of pending) task.fn();
      return pending.length;
    },
  };
}

test('区域覆盖整窗：每行都有矩形、四角对称、边缘不越过窗口', () => {
  const width = 1160;
  const height = 780;
  const rects = roundedWindowShape(width, height, WINDOW_RADIUS);

  // 展开成逐行视图：带合并后仍必须每行恰好一层、无空洞、无重叠
  const rowOf = new Array(height).fill(null);
  for (const r of rects) {
    for (let y = r.y; y < r.y + r.height; y += 1) {
      assert.equal(rowOf[y], null, `第 ${y} 行被重复覆盖（区域重叠）`);
      rowOf[y] = r;
    }
  }
  assert.ok(rowOf.every(Boolean), '区域存在未覆盖的行');

  for (const r of rects) {
    assert.ok(r.height >= 1);
    assert.ok(r.x >= 0 && r.width >= 0);
    assert.ok(r.x + r.width <= width, `第 ${r.y} 行右边界越过窗口`);
    assert.ok(r.y + r.height <= height);
  }
  // 带合并的收益：矩形数量远小于行数（逐行 780 块在 resize 时每次上传，且高缩放下会重叠）
  assert.ok(rects.length <= 32, `矩形未合并：${rects.length}`);

  // 左右对称 + 上下对称：任何一致的窗口裁剪都不该只丢两个角
  for (let y = 0; y < height; y += 1) {
    const top = rowOf[y];
    const bottom = rowOf[height - 1 - y];
    assert.equal(top.x, bottom.x, `第 ${y} 行上下不对称`);
    assert.equal(top.width, bottom.width, `第 ${y} 行上下不对称`);
  }

  // 四角确实被裁出圆角：首行、末行各内缩 WINDOW_RADIUS，中部贴边
  assert.equal(rowOf[0].x, WINDOW_RADIUS);
  assert.equal(rowOf[height - 1].x, WINDOW_RADIUS);
  assert.equal(rowOf[0].width, width - WINDOW_RADIUS * 2);
  assert.equal(rowOf[height - 1].width, width - WINDOW_RADIUS * 2);
  const middle = rowOf[Math.floor(height / 2)];
  assert.equal(middle.x, 0);
  assert.equal(middle.width, width);
});

test('最大化 / 全屏：区域与尺寸快照无关，直接恢复整块矩形', () => {
  // 旧实现按 getSize() 拼一块「当前读到尺寸」的矩形：最大化动画未落定时读到的是旧尺寸，
  // 窗口右侧与底部于是被裁掉（关闭按钮被切、两角变直角），且不会再自愈。
  // Windows 用空列表（Electron 文档与 DesktopWindowTreeHostWin::SetShape 都明确等于恢复矩形）。
  assert.deepEqual(
    shapeForWindow({ width: 1160, height: 780, maximized: true, platform: 'win32' }),
    []
  );
  assert.deepEqual(
    shapeForWindow({ width: 1160, height: 780, fullscreen: true, platform: 'win32' }),
    []
  );
  // Linux 的 X11Window::SetShape 没有空列表快速路径（空数组会走成空区域），必须显式整块矩形
  assert.deepEqual(
    shapeForWindow({ width: 1920, height: 1040, maximized: true, platform: 'linux' }),
    [{ x: 0, y: 0, width: 1920, height: 1040 }]
  );
  // 普通态才按原生外框尺寸生成圆角（带合并后应远少于行数）
  const normal = shapeForWindow({ width: 1160, height: 780, platform: 'win32' });
  assert.ok(normal.length > 2 && normal.length < 780);
  assert.equal(
    normal.reduce((sum, r) => sum + r.height, 0),
    780,
    '合并后每行仍需被覆盖一次'
  );
});

test('尺寸未知（0 / 非法）时不设置区域，宁可保持原状也不裁内容', () => {
  assert.equal(shapeForWindow({ width: 0, height: 0 }), null);
  assert.equal(shapeForWindow({ width: -1, height: 780 }), null);
  assert.equal(shapeForWindow({ width: 1160, height: 0 }), null);
  assert.equal(shapeForWindow({}), null);

  const win = fakeWindow({ size: [0, 0] });
  assert.equal(applyWindowShape(win, { platform: 'win32', nativeBounds: () => null }), false);
  assert.equal(win.calls.length, 0);
});

test('普通态按原生窗口外框尺寸生成圆角区域（不是内容区）', () => {
  const win = fakeWindow({ size: [1160, 780] });
  const outer = nativeOuter({ x: 0, y: 0, width: 1280, height: 800 });
  assert.equal(applyWindowShape(win, { platform: 'win32', nativeBounds: outer }), true);
  assert.equal(outer.calls.length, 1);
  assert.equal(win.contentReads, 0, '外框可用时不应再读内容区');
  assert.equal(win.calls.length, 1);
  const rects = win.calls[0];
  assert.equal(
    rects.reduce((sum, r) => sum + r.height, 0),
    800,
    '应覆盖原生外框的 800 行，而不是内容区 780 行'
  );
  assert.equal(rects[0].width, 1280 - WINDOW_RADIUS * 2);
  assert.equal(rects[rects.length - 1].x, WINDOW_RADIUS);
});

test('外框大于内容区（Windows thickFrame / Linux CSD）：区域铺满外框且不越过最右/最底', () => {
  // 原生窗口 1178x818 是物理换算后的 DIP 外框，内容区 1160x780；按内容区生成会留下
  // 右侧 18、底部 38 的直角未裁区，正是用户看到的「关闭按钮被切/越界」。
  const win = fakeWindow({ size: [1160, 780] });
  const outer = { x: -20, y: -40, width: 1178, height: 818 };
  assert.equal(applyWindowShape(win, { platform: 'win32', nativeBounds: () => outer }), true);
  const rects = win.calls[0];
  const rows = rects.reduce((sum, r) => sum + r.height, 0);
  assert.equal(rows, 818, '高度必须按外框 818 铺满');
  assert.equal(rects[0].width, 1178 - WINDOW_RADIUS * 2);
  for (const r of rects) {
    assert.ok(r.x >= 0 && r.x + r.width <= 1178, `第 ${r.y} 行越过外框右缘`);
    assert.ok(r.y >= 0 && r.y + r.height <= 818, `第 ${r.y} 行越过外框下缘`);
  }
});

test('原生外框不可用：查询返回空/抛错/非正尺寸时回退内容区尺寸', () => {
  const cases = [
    ['返回 null', () => null],
    ['返回空对象', () => ({})],
    ['返回零尺寸', () => ({ x: 0, y: 0, width: 0, height: 0 })],
    [
      '抛错',
      () => {
        throw new Error('powershell 不可用');
      },
    ],
  ];
  for (const [label, nativeBounds] of cases) {
    const win = fakeWindow({ size: [1160, 780] });
    assert.equal(applyWindowShape(win, { platform: 'win32', nativeBounds }), true, label);
    assert.equal(win.calls.length, 1, label);
    assert.equal(
      win.calls[0].reduce((sum, r) => sum + r.height, 0),
      780,
      `${label}：应回退内容区高度`
    );
    assert.equal(win.contentReads, 1, `${label}：应读一次内容区`);
  }
});

test('最大化 / 全屏窗口：Windows 传空列表且不查原生外框，Linux 传外框整块矩形', () => {
  const maxOuter = nativeOuter({ x: 0, y: 0, width: 1920, height: 1040 });
  const max = fakeWindow({ size: [1920, 1040], maximized: true });
  assert.equal(applyWindowShape(max, { platform: 'win32', nativeBounds: maxOuter }), true);
  assert.deepEqual(max.calls, [[]]);
  assert.equal(maxOuter.calls.length, 0, 'Windows 最大化不依赖尺寸，不应派生子进程查询');

  const fullOuter = nativeOuter({ x: 0, y: 0, width: 1924, height: 1080 });
  const full = fakeWindow({ size: [1920, 1040], fullscreen: true });
  assert.equal(applyWindowShape(full, { platform: 'linux', nativeBounds: fullOuter }), true);
  assert.deepEqual(full.calls, [[{ x: 0, y: 0, width: 1924, height: 1080 }]]);

  // Linux 外框查不到时仍按内容区给出显式矩形，不传空数组（空区域会让整窗不可见）
  const fallback = fakeWindow({ size: [1920, 1040], maximized: true });
  assert.equal(applyWindowShape(fallback, { platform: 'linux', nativeBounds: () => null }), true);
  assert.deepEqual(fallback.calls, [[{ x: 0, y: 0, width: 1920, height: 1040 }]]);
});

test('macOS / 已销毁窗口 / setShape 异常：安全跳过且不中断主流程', () => {
  const mac = fakeWindow();
  assert.equal(applyWindowShape(mac, { platform: 'darwin', nativeBounds: () => null }), false);
  assert.equal(mac.calls.length, 0);

  const dead = fakeWindow({ destroyed: true });
  assert.equal(applyWindowShape(dead, { platform: 'win32', nativeBounds: () => null }), false);
  assert.equal(dead.calls.length, 0);

  assert.equal(applyWindowShape(null, { platform: 'win32' }), false);

  const boom = fakeWindow({ throwOnShape: true });
  const origError = console.error;
  console.error = () => {};
  try {
    assert.equal(applyWindowShape(boom, { platform: 'win32', nativeBounds: () => null }), false);
  } finally {
    console.error = origError;
  }
});

test('调度：状态切换立即应用，尺寸落定后按最终尺寸复核（最大化动画不再裁边）', () => {
  const clock = manualClock();
  const win = fakeWindow();
  const { schedule } = createShapeScheduler(win, {
    platform: 'win32',
    nativeBounds: () => null,
    setTimer: clock.set,
    clearTimer: clock.clear,
  });

  // 最大化：不等防抖，立刻恢复整块矩形，避免还原动画期间窗口被旧尺寸区域裁掉
  win.maximized = true;
  win.size = [1920, 1040];
  schedule({ immediate: true });
  assert.equal(win.calls.length, 1);
  assert.deepEqual(win.calls[0], []);

  // 还原：窗口尺寸在动画结束后才落定，复核时读到最终尺寸并重新裁圆角
  win.maximized = false;
  win.size = [1160, 780];
  schedule({ immediate: true });
  const totalRows = (rects) => rects.reduce((sum, r) => sum + r.height, 0);
  assert.equal(totalRows(win.calls[1]), 780, '还原后应重新裁圆角');
  clock.advance();
  assert.equal(win.calls.length, 4, '应补两次落定复核');
  assert.equal(totalRows(win.calls[2]), 780);
  assert.equal(win.calls[2][0].width, 1160 - 20);
  assert.equal(totalRows(win.calls[3]), 780);
});

test('调度：拖拽缩放走防抖，尺寸最终落定后再复核', () => {
  const clock = manualClock();
  const win = fakeWindow();
  const calls = [];
  const { schedule } = createShapeScheduler(win, {
    platform: 'linux',
    debounce: 60,
    settleDelays: [120],
    setTimer: clock.set,
    clearTimer: clock.clear,
    apply: (target) => {
      calls.push([...target.size]);
      return true;
    },
  });

  win.size = [1200, 800];
  schedule();
  win.size = [1240, 820];
  schedule();
  assert.equal(calls.length, 0, '拖拽期间不逐帧 setShape');

  clock.advance(); // 防抖 + 落定复核
  assert.deepEqual(calls, [
    [1240, 820],
    [1240, 820],
  ]);
});

test('调度：连续事件只保留最新一轮定时器，stop 后不再触发', () => {
  const clock = manualClock();
  const win = fakeWindow();
  const { schedule, stop } = createShapeScheduler(win, {
    platform: 'win32',
    nativeBounds: () => null,
    setTimer: clock.set,
    clearTimer: clock.clear,
  });

  schedule();
  const afterFirst = clock.tasks.size;
  assert.ok(afterFirst > 0);
  schedule();
  schedule();
  assert.equal(clock.tasks.size, afterFirst, `定时器泄漏：${clock.tasks.size}`);

  stop();
  assert.equal(clock.tasks.size, 0);
  const before = win.calls.length;
  clock.advance();
  assert.equal(win.calls.length, before);
});

test('CSS：圆角半径与原生区域同源，窗口按钮盒模型确定且图标不被圆角裁到', () => {
  const css = fs.readFileSync(path.join(DESKTOP, 'src', 'theme.css'), 'utf8');
  const block = (selector) => {
    const m = css.match(new RegExp(`\\${selector}\\s*\\{([^}]*)\\}`));
    assert.ok(m, `theme.css 缺少 ${selector} 规则`);
    return m[1];
  };

  const shell = block('.app-shell');
  const radius = Number((shell.match(/border-radius:\s*(\d+)px/) || [])[1]);
  assert.equal(radius, WINDOW_RADIUS, 'CSS 圆角与 setShape 半径必须同值，否则角上会露窗底色或被切');
  assert.match(shell, /overflow:\s*hidden/);
  // 最大化：CSS 与原生区域同时摊平，任一残留都会在贴边处露出一圈底色或圆角
  const maximizedShell = css.match(/\.app-shell\.is-maximized\s*\{([^}]*)\}/);
  assert.ok(maximizedShell, 'theme.css 缺少 .app-shell.is-maximized 规则');
  assert.match(maximizedShell[1], /border-radius:\s*0/);
  assert.match(maximizedShell[1], /border-color:\s*transparent/);

  const btn = block('.tb-btn');
  assert.match(btn, /width:\s*44px/, '窗口按钮宽度被改动需同步本回归');
  // 不显式钉住盒模型时，按钮依赖 UA 的 padding（1px 6px）与 box-sizing；换平台/被全局重置
  // 改写后实际外框会变，最右一枚会顶出窗口右缘。
  assert.match(btn, /box-sizing:\s*border-box/);
  assert.match(btn, /padding:\s*0(?![.\d])/);
  assert.match(btn, /flex:\s*0 0 auto/, '按钮不得被 flex 压缩，否则三枚按钮宽度不一致');
});

test('CSS：标题栏右侧按钮组不得越出壳层内容区（X 不得压到窗口右缘/圆角）', () => {
  const css = fs.readFileSync(path.join(DESKTOP, 'src', 'theme.css'), 'utf8');
  const block = (selector) => {
    const m = css.match(new RegExp(`\\${selector}\\s*\\{([^}]*)\\}`));
    assert.ok(m, `theme.css 缺少 ${selector} 规则`);
    return m[1];
  };
  const titlebar = block('.titlebar');
  const right = block('.tb-right');

  // 左侧信息可收缩；右侧按钮组固定宽度并保留 8px 边距，避免 X 压到圆角弧区。
  assert.match(titlebar, /display:\s*flex/);
  assert.match(right, /margin-right:\s*8px/, '关闭按钮组须保留 8px 右侧安全边距');
  assert.doesNotMatch(right, /margin-right:\s*-\d/, '按钮组不得用负 margin 越出壳层');
  assert.doesNotMatch(right, /transform:\s*translate/, '按钮组不得用变换越出壳层');
  assert.match(right, /margin-left:\s*auto/);
  assert.match(right, /flex:\s*0 0 auto|flex-shrink:\s*0/, '按钮组不得被压缩');
});

test('main.cjs 接线：形状走 window-shape 单一来源，状态切换立即重算，全屏也覆盖', () => {
  const src = fs.readFileSync(path.join(DESKTOP, 'electron', 'main.cjs'), 'utf8');

  assert.match(src, /require\('\.\/window-shape\.cjs'\)/);
  assert.doesNotMatch(src, /function roundedWindowShape/, '形状几何不得在 main.cjs 复制一份');
  assert.match(src, /createShapeScheduler\(win/);
  // 最大化/还原/全屏：立即应用（不防抖），否则动画期间会用旧尺寸把右侧与底部裁掉
  for (const evt of ['maximize', 'unmaximize', 'enter-full-screen', 'leave-full-screen']) {
    assert.match(
      src,
      new RegExp(`win\\.on\\('${evt}',[^\\n]*schedule\\(\\{ immediate: true \\}\\)`),
      `${evt} 未立即重算窗口形状`
    );
  }
  assert.match(src, /win\.on\('resize',[^\n]*schedule\(\)\)/, 'resize 应走防抖');
  // Linux CSD 的外框可能随焦点变化：激活态切换需清内缩缓存并重算，不能沿用旧尺寸。
  for (const evt of ['focus', 'blur']) {
    assert.match(
      src,
      new RegExp(`win\\.on\\('${evt}',[^\\n]*onActivationChanged\\)`),
      `${evt} 未重算原生外框`
    );
  }
  assert.match(src, /clearNativeBoundsCache\(\)/);
  // 换屏 / 改缩放必须重算：区域按 DIP 生成，Chromium 再按所在显示器缩放换算成物理像素
  assert.match(src, /displayShapeKey\(/);
  assert.match(src, /display-metrics-changed/);
  assert.match(src, /win\.on\('move',[^\n]*onDisplayChanged\)/);
  // 保持不透明窗口：transparent 会让 Windows 的 maximize/unmaximize 失效（v2.3.13 已踩坑）
  assert.doesNotMatch(src, /transparent:\s*true/);
});

test('displayShapeKey：显示器/缩放变化才改变，其余情况稳定可去重', () => {
  const a = { id: 1, scaleFactor: 1, size: { width: 1920, height: 1080 } };
  assert.equal(displayShapeKey(a), displayShapeKey({ ...a }));
  assert.notEqual(displayShapeKey(a), displayShapeKey({ ...a, scaleFactor: 1.25 }));
  assert.notEqual(displayShapeKey(a), displayShapeKey({ ...a, id: 2 }));
  assert.notEqual(
    displayShapeKey(a),
    displayShapeKey({ ...a, size: { width: 2560, height: 1440 } })
  );
  assert.equal(displayShapeKey(null), '');
});
