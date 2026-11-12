// 读取原生窗口外框（含 Windows thickFrame 的不可见缩放边、Linux CSD 的阴影/边框）。
//
// 背景：Electron 45 的 getContentSize() 只是内容区，getBounds() 也不保证等于原生外框；
// 而 setShape 的区域原点和基准是原生窗口左上角与原生外框尺寸。圆角区域若按内容区生成，
// 右侧/底部会留下一圈未裁的直角。本模块直接向系统取真实外框：
//   - Windows：getNativeWindowHandle() 取 HWND，用系统 PowerShell P/Invoke GetWindowRect；
//   - Linux X11：getNativeWindowHandle() 是 XID，用 xwininfo -id <xid> -stats 取几何；
// 两者得到的都是物理像素，再按窗口所在显示器的 scaleFactor 换算成 DIP。
//
// 约束：任何失败（平台不支持、缺工具、命令超时、输出不合法、拿不到显示器缩放）都返回 null，
// 由调用方回退到内容区尺寸；不引入 npm 包，不执行 shell，所有外部命令都带超时上限。
const fs = require('fs');
const path = require('path');
const { execFileSync: defaultExecFileSync } = require('child_process');

const COMMAND_TIMEOUT = 3000;
const MAX_OUTPUT = 64 * 1024;
const XWININFO_CANDIDATES = ['/usr/bin/xwininfo', '/bin/xwininfo', '/usr/local/bin/xwininfo'];

// 缓存的是「外框相对内容区的内缩量」，不是外框本身：内缩量在同一窗口样式 + 同一 DPI 下是常数，
// 缩放窗口时外框 = 内容区 + 内缩量，不必每次都启动子进程（PowerShell 每次要重新编译 P/Invoke
// 声明，数百毫秒的同步阻塞会直接卡住主进程）。DPI 变化由缓存键覆盖；长时间运行后样式可能被
// 系统改掉（主题/边框设置），因此再带一个保守的过期时间。
const CACHE_TTL = 60000;
const MAX_INSET = 128;
const cache = new Map();

function resolvePowerShellPath() {
  const root = process.env.SystemRoot || process.env.WINDIR || 'C:\\Windows';
  return path.win32.join(root, 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe');
}

function resolveXwininfoPath() {
  for (const candidate of XWININFO_CANDIDATES) {
    try {
      if (fs.existsSync(candidate)) return candidate;
    } catch (_e) {
      // 单个候选路径不可访问时继续找下一个
    }
  }
  return null;
}

// 句柄缓冲区：Windows 是 HWND（x64 为指针宽度，旧式 32 位构建可能有负值），
// Linux 是 unsigned long 的 XID。统一转成 BigInt 后再格式化成命令参数。
function readHandle(win, { signed32 = false } = {}) {
  const raw = win.getNativeWindowHandle();
  if (!Buffer.isBuffer(raw) || raw.length < 4) return null;
  if (raw.length >= 8) return signed32 ? raw.readBigInt64LE(0) : raw.readBigUInt64LE(0);
  return signed32 ? BigInt(raw.readInt32LE(0)) : BigInt(raw.readUInt32LE(0));
}

function buildWindowsScript(handle) {
  // PowerShell 默认进程不是 per-monitor DPI aware，GetWindowRect 会被系统按 96 DPI 虚拟化；
  // 先切到 PER_MONITOR_AWARE_V2（-4），拿到的才是真实物理像素。
  // 全部类型用全限定名书写：Add-Type -MemberDefinition 把内容包进类体，using 指令的位置不可靠。
  return [
    "$ErrorActionPreference = 'Stop'",
    "Add-Type -Namespace ShadeNative -Name Win32 -MemberDefinition @'",
    'public struct RECT { public int Left; public int Top; public int Right; public int Bottom; }',
    '[System.Runtime.InteropServices.DllImport("user32.dll", SetLastError = true)]',
    'public static extern bool GetWindowRect(System.IntPtr hWnd, out RECT rect);',
    '[System.Runtime.InteropServices.DllImport("user32.dll")]',
    'public static extern System.IntPtr SetThreadDpiAwarenessContext(System.IntPtr dpiContext);',
    '[System.Runtime.InteropServices.DllImport("user32.dll")]',
    'public static extern bool SetProcessDpiAwarenessContext(System.IntPtr dpiContext);',
    "'@",
    'try {',
    '  [void][ShadeNative.Win32]::SetThreadDpiAwarenessContext([System.IntPtr]::new(-4))',
    '} catch {',
    '  try { [void][ShadeNative.Win32]::SetProcessDpiAwarenessContext([System.IntPtr]::new(-4)) } catch {}',
    '}',
    '$rect = [ShadeNative.Win32+RECT]::new()',
    `if (-not [ShadeNative.Win32]::GetWindowRect([System.IntPtr]::new([System.Int64]${handle}), [ref]$rect)) { exit 1 }`,
    "'{0} {1} {2} {3}' -f $rect.Left, $rect.Top, $rect.Right, $rect.Bottom",
  ].join('\n');
}

function parseWindowsRect(output) {
  const match = String(output || '').match(/^\s*(-?\d+)\s+(-?\d+)\s+(-?\d+)\s+(-?\d+)\s*$/m);
  if (!match) return null;
  const [left, top, right, bottom] = match.slice(1).map(Number);
  if (![left, top, right, bottom].every(Number.isSafeInteger)) return null;
  const width = right - left;
  const height = bottom - top;
  if (width <= 0 || height <= 0) return null;
  return { left, top, width, height };
}

function parseXwininfo(output) {
  const text = String(output || '');
  const pick = (pattern) => {
    const match = text.match(pattern);
    return match ? Number(match[1]) : null;
  };
  const left = pick(/^\s*Absolute upper-left X:\s*(-?\d+)\s*$/m);
  const top = pick(/^\s*Absolute upper-left Y:\s*(-?\d+)\s*$/m);
  const width = pick(/^\s*Width:\s*(\d+)\s*$/m);
  const height = pick(/^\s*Height:\s*(\d+)\s*$/m);
  if (![left, top, width, height].every(Number.isSafeInteger)) return null;
  if (width <= 0 || height <= 0) return null;
  return { left, top, width, height };
}

function normalizeTimeout(value) {
  const timeout = Number(value);
  if (!Number.isFinite(timeout) || timeout <= 0) return COMMAND_TIMEOUT;
  return Math.min(Math.floor(timeout), 10000);
}

function runCommand(execFileSync, file, args, timeout) {
  return execFileSync(file, args, {
    encoding: 'utf8',
    timeout,
    maxBuffer: MAX_OUTPUT,
    windowsHide: true,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
}

function readWindowsRect(win, execFileSync, powershellPath, timeout) {
  const handle = readHandle(win, { signed32: true });
  // 0 是空句柄（旧式 32 位构建里高位为负的合法句柄要保留符号）
  if (handle === null || handle === 0n) return null;
  const file = powershellPath || resolvePowerShellPath();
  if (!file || !path.win32.isAbsolute(file)) return null;
  const output = runCommand(
    execFileSync,
    file,
    ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command', buildWindowsScript(handle.toString())],
    timeout
  );
  return parseWindowsRect(output);
}

function readLinuxRect(win, execFileSync, xwininfoPath, timeout) {
  const xid = readHandle(win);
  if (xid === null || xid <= 0n) return null;
  // 原生 Wayland 下句柄不是 XID，xwininfo 通常会失败；失败自然回退到内容区尺寸。
  const file = xwininfoPath || resolveXwininfoPath();
  if (!file) return null;
  const output = runCommand(
    execFileSync,
    file,
    ['-id', `0x${xid.toString(16)}`, '-stats'],
    timeout
  );
  return parseXwininfo(output);
}

// 内容区（DIP）：优先 getContentBounds（含原点），否则用 getContentSize + getBounds 的原点。
function readContentRect(win) {
  if (typeof win.getContentBounds === 'function') {
    try {
      const raw = win.getContentBounds();
      const rect = {
        x: Number(raw && raw.x),
        y: Number(raw && raw.y),
        width: Number(raw && raw.width),
        height: Number(raw && raw.height),
      };
      if (isPositiveRect(rect)) return rect;
    } catch (_e) {
      // 继续用尺寸 + 窗口原点拼一个近似内容矩形
    }
  }
  if (typeof win.getContentSize !== 'function') return null;
  try {
    const size = win.getContentSize();
    if (!Array.isArray(size)) return null;
    const width = Number(size[0]);
    const height = Number(size[1]);
    let x = 0;
    let y = 0;
    if (typeof win.getBounds === 'function') {
      const bounds = win.getBounds();
      if (Number.isFinite(Number(bounds && bounds.x))) x = Number(bounds.x);
      if (Number.isFinite(Number(bounds && bounds.y))) y = Number(bounds.y);
    }
    const rect = { x, y, width, height };
    return isPositiveRect(rect) ? rect : null;
  } catch (_e) {
    return null;
  }
}

function isPositiveRect(rect) {
  return (
    rect &&
    Number.isFinite(rect.x) &&
    Number.isFinite(rect.y) &&
    Number.isFinite(rect.width) &&
    Number.isFinite(rect.height) &&
    rect.width > 0 &&
    rect.height > 0
  );
}

function resolveScreen() {
  try {
    const electron = require('electron');
    if (electron && typeof electron === 'object' && electron.screen) return electron.screen;
  } catch (_e) {
    // 单测环境没有 Electron，按“拿不到显示器”处理
  }
  return null;
}

function resolveScaleFactor(content, screenApi) {
  if (!screenApi) return null;
  let display = null;
  if (typeof screenApi.getDisplayMatching === 'function') {
    try {
      display = screenApi.getDisplayMatching(content);
    } catch (_e) {
      display = null;
    }
  }
  if (!display && typeof screenApi.getDisplayNearestPoint === 'function') {
    try {
      display = screenApi.getDisplayNearestPoint({
        x: content.x + content.width / 2,
        y: content.y + content.height / 2,
      });
    } catch (_e) {
      display = null;
    }
  }
  if (!display && typeof screenApi.getPrimaryDisplay === 'function') {
    try {
      display = screenApi.getPrimaryDisplay();
    } catch (_e) {
      display = null;
    }
  }
  const scale = Number(display && display.scaleFactor);
  if (!Number.isFinite(scale) || scale <= 0) return null;
  return scale;
}

// 物理像素 → DIP：原点向下取整，宽高向上取整，保证最右/最底物理像素不被裁掉；
// 负坐标（窗口在副屏左侧/上方）同样适用。
function physicalToDip(rect, scale) {
  const factor = Number(scale);
  if (!Number.isFinite(factor) || factor <= 0) return null;
  const x = Math.floor(rect.left / factor);
  const y = Math.floor(rect.top / factor);
  const width = Math.ceil(rect.width / factor);
  const height = Math.ceil(rect.height / factor);
  if (![x, y, width, height].every(Number.isSafeInteger) || width <= 0 || height <= 0) return null;
  return { x, y, width, height };
}

function cacheKey(win, platform, scaleFactor) {
  if (typeof win.getNativeWindowHandle !== 'function') return null;
  let handle = null;
  try {
    handle = win.getNativeWindowHandle();
  } catch (_e) {
    return null;
  }
  if (!Buffer.isBuffer(handle) || handle.length === 0) return null;
  return `${platform}:${handle.toString('hex')}:${scaleFactor}`;
}

function normalizeCacheTtl(value) {
  if (value === 0) return 0;
  const ttl = Number(value);
  if (!Number.isFinite(ttl) || ttl <= 0) return CACHE_TTL;
  return ttl;
}

// 内缩量必须落在合理区间，否则说明读到的是坏值（例如句柄失效拿到了别的窗口），不缓存。
// 允许小数：分数缩放下内容区与外框的 DIP 差可能不是整数。
function isPlausibleInsets(insets) {
  const values = [insets.left, insets.top, insets.right, insets.bottom];
  return values.every((value) => Number.isFinite(value) && Math.abs(value) <= MAX_INSET);
}

function readCachedInsets(key, now, ttl) {
  if (!key || ttl <= 0) return null;
  const entry = cache.get(key);
  if (!entry) return null;
  if (now - entry.at > ttl) {
    cache.delete(key);
    return null;
  }
  return { ...entry.insets };
}

function writeCachedInsets(key, insets, now, ttl) {
  if (!key || ttl <= 0 || !insets) return;
  if (cache.size > 32) cache.clear();
  cache.set(key, { at: now, insets: { ...insets } });
}

function clearNativeBoundsCache() {
  cache.clear();
}

// 外框 = 内容区 + 内缩量。内缩量只在窗口样式或 DPI 变化时改变，因此按句柄 + 平台 + 缩放缓存。
// 内缩量可能是小数（分数缩放），取整方向与物理像素换算一致：原点向下、宽高向上。
function outerFromInsets(content, insets) {
  const x = Math.floor(content.x - insets.left);
  const y = Math.floor(content.y - insets.top);
  const width = Math.ceil(content.width + insets.left + insets.right);
  const height = Math.ceil(content.height + insets.top + insets.bottom);
  if (![x, y, width, height].every(Number.isSafeInteger) || width <= 0 || height <= 0) {
    return null;
  }
  return { x, y, width, height };
}

/**
 * 读取当前窗口的真实原生外框（DIP）。
 *
 * @param {object} win Electron BrowserWindow（用到 getNativeWindowHandle/getContentBounds/getContentSize/getBounds）
 * @param {object} [options]
 * @param {string} [options.platform] 平台，默认 process.platform
 * @param {Function} [options.execFileSync] 可注入的命令执行器（单测用）
 * @param {object} [options.screen] 可注入的 Electron screen（单测用）
 * @param {string} [options.powershellPath] 可注入的 powershell.exe 绝对路径
 * @param {string} [options.xwininfoPath] 可注入的 xwininfo 路径
 * @param {number} [options.timeout] 单条命令超时（毫秒，默认 3000，上限 10000）
 * @param {number} [options.cacheTtl] 内缩量缓存毫秒数（默认 60000；传 0 关闭，单测用）
 * @param {Function} [options.now] 当前时间函数（单测用）
 * @returns {{x: number, y: number, width: number, height: number}|null} 失败返回 null
 */
function queryNativeWindowBounds(win, options = {}) {
  try {
    if (!win || (typeof win.isDestroyed === 'function' && win.isDestroyed())) return null;
    // 最小化时系统给的是屏幕外的占位矩形，据此算出的内缩量会污染缓存
    if (typeof win.isMinimized === 'function' && win.isMinimized()) return null;
    const platform = options.platform || process.platform;
    if (platform !== 'win32' && platform !== 'linux') return null;
    const content = readContentRect(win);
    if (!content) return null;
    const screenApi = options.screen || resolveScreen();
    const scaleFactor = resolveScaleFactor(content, screenApi);
    if (!scaleFactor) return null;

    const now = typeof options.now === 'function' ? options.now : Date.now;
    const ttl = normalizeCacheTtl(options.cacheTtl);
    const key = cacheKey(win, platform, scaleFactor);
    const cached = readCachedInsets(key, now(), ttl);
    if (cached) return outerFromInsets(content, cached);

    const execFileSync = options.execFileSync || defaultExecFileSync;
    const timeout = normalizeTimeout(options.timeout);
    const rect =
      platform === 'win32'
        ? readWindowsRect(win, execFileSync, options.powershellPath, timeout)
        : readLinuxRect(win, execFileSync, options.xwininfoPath, timeout);
    if (!rect) return null;
    const outer = physicalToDip(rect, scaleFactor);
    if (!outer) return null;

    // 命令执行期间窗口若正在动画，前后读到的内容区会不同，此时算出的内缩量不可信。
    const after = readContentRect(win);
    if (
      !after ||
      after.x !== content.x ||
      after.y !== content.y ||
      after.width !== content.width ||
      after.height !== content.height
    ) {
      return null;
    }

    const insets = {
      left: content.x - outer.x,
      top: content.y - outer.y,
      right: outer.x + outer.width - (content.x + content.width),
      bottom: outer.y + outer.height - (content.y + content.height),
    };
    if (!isPlausibleInsets(insets)) return null;
    writeCachedInsets(key, insets, now(), ttl);
    // 首次直接用系统给的外框（已是取整后的 DIP），避免再按内缩量重算一次
    return outer;
  } catch (_e) {
    // 外部命令超时/缺工具/输出异常一律回退，调用方保留内容区尺寸
    return null;
  }
}

module.exports = {
  queryNativeWindowBounds,
  physicalToDip,
  clearNativeBoundsCache,
};
