// 无边框窗口的圆角形状（Windows / Linux 没有原生圆角，用 setShape 裁窗口区域）。
// setShape 的区域原点与基准是**原生窗口外框**，不是 getContentSize() 的内容区：
// Windows thickFrame 的不可见缩放边、Linux CSD 的阴影/边框都会让原生外框大于内容区，
// 按内容区生成区域只会覆盖左上角一块，右侧/底部留下未裁的直角；反过来，区域比原生窗口大
// 时多出的部分会被系统裁掉，角上可能露出窗底色。因此本模块优先用 window-native-bounds.cjs
// 读取真实外框（物理像素已换算成 DIP），失败时才退回内容区尺寸（旧行为）。
// 透明窗口会让 Windows 的 maximize/unmaximize 失效（v2.3.13 CI 已踩坑），所以保持不透明窗口，
// 只在窗口区域上做圆角，并遵守以下约束：
//   1) 最大化 / 全屏恢复矩形：Windows 传空列表（Electron 文档 + DesktopWindowTreeHostWin::SetShape
//      的空列表快速路径都明确是「恢复矩形」）；Linux 的 X11Window::SetShape 没有这个快速路径，
//      空列表会走成空区域（可能整窗不可见），因此改传原生外框的整块矩形，不赌未验证的行为；
//   2) 普通态按最新原生外框尺寸重算，尺寸落定后再复核（窗口动画与 DPI 切换都会改尺寸）；
//   3) 原生外框与回退内容尺寸都未知时不设置区域——宁可保持上一次的形状，也不把内容裁掉。
const { queryNativeWindowBounds, clearNativeBoundsCache } = require('./window-native-bounds.cjs');

const WINDOW_RADIUS = 10;

// 拖拽缩放走防抖；状态切换与跨屏缩放变化立即应用，再在尺寸落定后复核。
const RESIZE_DEBOUNCE = 60;
const SETTLE_DELAYS = [120, 400];

// 圆角区域按「行内缩」合并成带：相同内缩的连续行合成一个矩形。
// 好处：区域从每行一块（780+ 块）降到十几块，resize 时不必反复上传上千个矩形，输出也稳定。
// Windows 在缩放 >100% 时会把每个矩形逐条乘缩放并 roundOut 再 Union（Skia 的
// SkRegion::setRects 是二分求并集，重叠不会丢块），因此合并后多出来的只是每处「内缩过渡」
// 最多 1 个物理像素的取整差，不影响圆角观感。
function roundedWindowShape(width, height, radius = WINDOW_RADIUS) {
  const r = Math.max(0, Math.min(radius, Math.floor(width / 2), Math.floor(height / 2)));
  const rowInset = (y) => {
    if (y < r) {
      const dy = r - y;
      return Math.round(r - Math.sqrt(Math.max(0, r * r - dy * dy)));
    }
    if (y >= height - r) {
      const dy = y - (height - r - 1);
      return Math.round(r - Math.sqrt(Math.max(0, r * r - dy * dy)));
    }
    return 0;
  };
  const rects = [];
  let y = 0;
  while (y < height) {
    const x = rowInset(y);
    let end = y + 1;
    while (end < height && rowInset(end) === x) end += 1;
    rects.push({ x, y, width: Math.max(0, width - x * 2), height: end - y });
    y = end;
  }
  return rects;
}

// 返回矩形列表；尺寸未知返回 null（调用方保持原状）。
// width/height 为原生窗口外框的 DIP 尺寸；最大化时 Windows 不依赖尺寸，Linux 用整块矩形。
function shapeForWindow({
  width,
  height,
  maximized = false,
  fullscreen = false,
  radius = WINDOW_RADIUS,
  platform = process.platform,
} = {}) {
  const w = Math.floor(Number(width) || 0);
  const h = Math.floor(Number(height) || 0);
  if (w <= 0 || h <= 0) return null;
  if (maximized || fullscreen) {
    // Windows：空列表即矩形（与尺寸无关，动画期间不会裁错）；Linux：显式整块矩形（见文件头）。
    return platform === 'win32' ? [] : [{ x: 0, y: 0, width: w, height: h }];
  }
  return roundedWindowShape(w, h, radius);
}

// 区域尺寸优先取真实原生外框；查询失败（不支持平台 / 缺工具 / 超时 / 输出异常）时
// 回退 getContentSize()，保证旧环境仍能裁出一个大致正确的圆角。
// 宽高向上取整：DIP 坐标可能是小数（分数缩放），floor 会把最右/最底一行像素切在区域外。
function resolveShapeSize(target, { platform, nativeBounds }) {
  if (typeof nativeBounds === 'function') {
    try {
      const outer = nativeBounds(target, { platform });
      const width = Math.ceil(Number(outer && outer.width) || 0);
      const height = Math.ceil(Number(outer && outer.height) || 0);
      if (width > 0 && height > 0) return { width, height, source: 'native' };
    } catch (_e) {
      // 原生外框只用于提升覆盖范围，查询异常走内容区回退
    }
  }
  try {
    const [width, height] = target.getContentSize();
    return { width, height, source: 'content' };
  } catch (_e) {
    return null;
  }
}

function applyWindowShape(
  target,
  {
    platform = process.platform,
    radius = WINDOW_RADIUS,
    nativeBounds = queryNativeWindowBounds,
  } = {}
) {
  if (!target || target.isDestroyed()) return false;
  if (platform !== 'win32' && platform !== 'linux') return false;
  try {
    const maximized = target.isMaximized();
    const fullscreen = target.isFullScreen();
    // Windows 最大化/全屏只需要空列表（恢复矩形），不必为尺寸派生子进程。
    if (platform === 'win32' && (maximized || fullscreen)) {
      target.setShape([]);
      return true;
    }
    const size = resolveShapeSize(target, { platform, nativeBounds });
    if (!size) return false;
    const rects = shapeForWindow({
      width: size.width,
      height: size.height,
      maximized,
      fullscreen,
      radius,
      platform,
    });
    if (!rects) return false;
    target.setShape(rects);
    return true;
  } catch (e) {
    console.error('[window] 设置圆角失败', e);
    return false;
  }
}

// 重算调度：schedule({ immediate }) —— 状态切换 / 换屏立即应用，拖拽缩放走防抖；
// 两条路径都会在尺寸落定后再复核，保证区域始终跟着最终尺寸走。
function createShapeScheduler(
  target,
  {
    platform = process.platform,
    radius = WINDOW_RADIUS,
    debounce = RESIZE_DEBOUNCE,
    settleDelays = SETTLE_DELAYS,
    apply = applyWindowShape,
    nativeBounds = queryNativeWindowBounds,
    setTimer = setTimeout,
    clearTimer = clearTimeout,
  } = {}
) {
  let timers = [];
  const stop = () => {
    for (const timer of timers) clearTimer(timer);
    timers = [];
  };
  const run = () => apply(target, { platform, radius, nativeBounds });
  const later = (delay) => {
    const timer = setTimer(run, delay);
    if (timer && typeof timer.unref === 'function') timer.unref();
    timers.push(timer);
  };
  const schedule = ({ immediate = false } = {}) => {
    stop();
    let base = 0;
    if (immediate) {
      run();
    } else {
      later(debounce);
      base = debounce;
    }
    for (const delay of settleDelays) later(base + delay);
  };
  return { schedule, applyNow: () => schedule({ immediate: true }), stop };
}

// 换显示器 / 改缩放 / 改分辨率后重算一次区域：区域是 DIP，Chromium 按窗口所在显示器缩放换算，
// 但换屏过程中窗口尺寸与缩放会分几步更新，旧区域可能对不上新窗口（右侧被裁或角上露底色）。
function displayShapeKey(display) {
  if (!display) return '';
  const size = display.size || {};
  return [display.id, display.scaleFactor, size.width, size.height].join(':');
}

module.exports = {
  WINDOW_RADIUS,
  RESIZE_DEBOUNCE,
  SETTLE_DELAYS,
  roundedWindowShape,
  shapeForWindow,
  applyWindowShape,
  createShapeScheduler,
  displayShapeKey,
  clearNativeBoundsCache,
};
