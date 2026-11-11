// 壳层工具（纯 node，不 import electron，便于回归测试）：
//   - showMainWindow：统一“显示主窗口”语义（最小化先 restore 再 show/focus）
//   - 导航/新窗口/权限守卫的注册逻辑与判定规则
//   - pickFile 授权路径登记（限制 readFile 只能读刚选中的文件）
//   - Linux 自启 desktop 文件生成
//   - 渲染进程崩溃恢复状态机
// 由 main.cjs 调用，测试注入假 app/session/浏览器内容。

// 顶部需要 node 内置模块：fileURLToPath 用于把 file:// 还原成真实路径再校验包含关系。
const fs = require('fs');
const path = require('path');
const { fileURLToPath } = require('url');

function showMainWindow(win) {
  if (!win || typeof win.isDestroyed !== 'function' || win.isDestroyed()) return false;
  if (typeof win.isMinimized === 'function' && win.isMinimized()) {
    win.restore();
  }
  win.show();
  win.focus();
  return true;
}

// ---- 导航白名单 ----
// 客户端只加载两类地址：打包内 dist 的已知页面/资源，以及开发态 Vite 地址（ELECTRON_DEV_URL）。
// 其余（外链、内网 http、data:、javascript:、dist 外的 file://）一律拦下，避免渲染层被诱导
// 用 file:// 读取本机任意文件、或把本地应用导航到外部页面。

// dist 根目录允许的页面：即 vite 多页入口（index/result/scanbox/screensaver）。
const ALLOWED_PAGES = new Set(['index.html', 'result.html', 'scanbox.html', 'screensaver.html']);
// dist/assets 下允许的资源后缀（构建产物只有这些类型）。
const ALLOWED_ASSET_EXT = new Set([
  '.js',
  '.mjs',
  '.css',
  '.png',
  '.jpg',
  '.jpeg',
  '.gif',
  '.svg',
  '.webp',
  '.ico',
  '.woff',
  '.woff2',
  '.ttf',
  '.otf',
  '.json',
  '.map',
]);

function isPathInside(parent, child) {
  const rel = path.relative(parent, child);
  return !!rel && rel !== '..' && !rel.startsWith('..' + path.sep) && !path.isAbsolute(rel);
}

// file:// 只在打包态（无 devUrl、有 appDir）放行，且必须落在 dist 内：
//   - dist 根：仅白名单页面
//   - dist/assets：仅白名单后缀，防止被改写为任意脚本/页面之外的资源读取
// 含路径先 realpath 再校验，符号链接指向 dist 外同样拒绝；realpath 不可用（如 asar 打包）
// 时退化为规范化路径校验，包含关系与白名单仍然生效。
function realOrResolved(p) {
  try {
    return fs.realpathSync(p);
  } catch {
    try {
      return path.resolve(p);
    } catch {
      return '';
    }
  }
}

function isAllowedFileUrl(url, appDir) {
  if (!appDir) return false;
  let filePath;
  try {
    filePath = fileURLToPath(url);
  } catch {
    return false;
  }
  if (!filePath) return false;
  const root = realOrResolved(appDir);
  const real = realOrResolved(filePath);
  if (!root || !real) return false;
  if (!isPathInside(root, real)) return false;
  const parts = path.relative(root, real).split(path.sep);
  if (parts.length === 1) return ALLOWED_PAGES.has(parts[0].toLowerCase());
  if (parts[0] === 'assets' && parts.length >= 2) {
    return ALLOWED_ASSET_EXT.has(path.extname(real).toLowerCase());
  }
  return false;
}

/**
 * 顶层导航是否放行。
 * 开发态（给出可解析的 devUrl）只认该地址的精确 origin；打包态只认 appDir（dist）内的已知页面/资源。
 * options: { devUrl, appDir }
 */
function isAllowedAppUrl(rawUrl, options = {}) {
  const { devUrl, appDir } = options || {};
  const raw = String(rawUrl || '').trim();
  if (!raw) return false;
  let url;
  try {
    url = new URL(raw);
  } catch {
    return false;
  }
  if (url.protocol === 'file:') {
    // 开发态只走 Vite 地址，不允许回到 file://（避免绕过 devUrl 白名单）
    if (devUrl) return false;
    return isAllowedFileUrl(url, appDir);
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') return false;
  if (!devUrl) return false;
  let dev;
  try {
    dev = new URL(devUrl);
  } catch {
    return false;
  }
  if (dev.protocol !== 'http:' && dev.protocol !== 'https:') return false;
  return url.origin === dev.origin;
}

// ---- 权限白名单 ----
// 渲染层是本地应用、无远程内容：默认全拒，只放行业务确实需要的几项。
// 剪贴板写入=集群页复制文本；全屏=实时大屏；display-capture=机巡截屏链路。
// 摄像头/麦克风（media）、定位、通知等一律不授予。
const ALLOWED_PERMISSIONS = new Set(['clipboard-sanitized-write', 'fullscreen', 'display-capture']);

function isPermissionAllowed(permission) {
  return ALLOWED_PERMISSIONS.has(String(permission || ''));
}

/**
 * 给所有 webContents 装导航守卫：
 *   - 新窗口（window.open / target=_blank）一律 deny（本地应用无外链需求）
 *   - 顶层导航只允许 dist 内已知页面/资源与开发态精确 origin
 *   - 禁止挂载 <webview>
 * app 需提供 on('web-contents-created', (event, contents) => ...)。
 */
function installWebContentsGuards(app, { devUrl, appDir, onBlocked } = {}) {
  const block = (kind, url) => {
    if (typeof onBlocked === 'function') onBlocked(kind, url);
  };
  app.on('web-contents-created', (_event, contents) => {
    if (!contents) return;
    // DevTools 前端是 devtools:// 页面，拦导航会把它自己打死；它不承载业务内容。
    if (typeof contents.getType === 'function' && contents.getType() === 'devtools') return;
    if (typeof contents.setWindowOpenHandler === 'function') {
      contents.setWindowOpenHandler(({ url }) => {
        block('window-open', url);
        return { action: 'deny' };
      });
    }
    if (typeof contents.on === 'function') {
      contents.on('will-navigate', (event, url) => {
        if (isAllowedAppUrl(url, { devUrl, appDir })) return;
        event.preventDefault();
        block('navigate', url);
      });
      contents.on('will-attach-webview', (event, webPreferences) => {
        event.preventDefault();
        block('webview', webPreferences && webPreferences.src);
      });
    }
  });
}

/** 默认会话的权限守卫：请求与检查两条通道都走同一白名单。 */
function installPermissionGuards(session, { allowed = ALLOWED_PERMISSIONS } = {}) {
  if (!session) return;
  const allow = (permission) => allowed.has(String(permission || ''));
  if (typeof session.setPermissionRequestHandler === 'function') {
    session.setPermissionRequestHandler((_contents, permission, callback) =>
      callback(allow(permission))
    );
  }
  if (typeof session.setPermissionCheckHandler === 'function') {
    session.setPermissionCheckHandler((_contents, permission) => allow(permission));
  }
}

// ---- 文件选择授权 ----
// 渲染层无 fs，题库导入需要主进程读 Excel 字节；但 readFile 不能变成任意文件读取 API。
// 只放行「本窗口文件对话框刚选中」的路径：pickFile 时登记，readFile 时用 realpath 严格比对，
// 选择后被替换为符号链接/文件已变更即失效。容量有上限，避免长会话无限累积。
const PICKED_FILE_LIMIT = 20;

function createPickedFileRegistry({ max = PICKED_FILE_LIMIT } = {}) {
  const allowed = new Map(); // realpath -> 最近一次登记顺序
  const limit = Math.max(1, Number(max) || PICKED_FILE_LIMIT);
  const resolveReal = (p) => {
    const raw = String(p || '').trim();
    if (!raw) return '';
    try {
      return fs.realpathSync(raw);
    } catch {
      return '';
    }
  };
  const normalizeExts = (exts) =>
    new Set(
      (Array.isArray(exts) ? exts : []).map((e) => String(e || '').toLowerCase()).filter(Boolean)
    );
  return {
    /** 登记一次文件对话框选择；返回真实路径（无法解析时返回空串）。 */
    allow(p) {
      const real = resolveReal(p);
      if (!real) return '';
      allowed.delete(real);
      allowed.set(real, Date.now());
      while (allowed.size > limit) allowed.delete(allowed.keys().next().value);
      return real;
    },
    /** 校验读取授权：仅当 realpath 与登记一致、且后缀在 exts 内时返回真实路径。 */
    authorize(p, exts) {
      const real = resolveReal(p);
      if (!real || !allowed.has(real)) return '';
      const suffixOk = normalizeExts(exts).has(path.extname(real).toLowerCase());
      return suffixOk ? real : '';
    },
    get size() {
      return allowed.size;
    },
  };
}

// ---- Linux 自启 desktop 文件 ----
// Exec 按 Desktop Entry 规范：含空格/中文的路径必须整体双引号，并转义 " ` $ \。
// 不引号时 GNOME/KDE 会把路径按空格拆成多个参数，结果开机静默不启动（已踩坑）。
function quoteDesktopExec(execPath) {
  const p = String(execPath || '');
  return '"' + p.replace(/(["`$\\])/g, '\\$1') + '"';
}

function buildAutostartEntry({ execPath, name }) {
  return (
    '[Desktop Entry]\nType=Application\nName=' +
    String(name || 'Shade 壹匣 - 内网') +
    '\nExec=' +
    quoteDesktopExec(execPath) +
    '\nX-GNOME-Autostart-enabled=true\n'
  );
}

/**
 * 渲染进程崩溃恢复：首次崩溃提示用户并自动 reload，连续崩溃超上限则停止自动重载，
 * 避免崩溃循环刷对话框、把主进程拖死。
 * reason 为 Electron render-process-gone 的 details.reason。
 */
function createCrashRecovery({ reload, notify, maxReloads = 3 } = {}) {
  let crashes = 0;
  return {
    get crashes() {
      return crashes;
    },
    handle(reason) {
      if (reason === 'clean-exit') return { action: 'ignore' };
      crashes += 1;
      if (crashes > maxReloads) {
        if (notify) notify('exhausted', reason);
        return { action: 'give-up' };
      }
      if (crashes === 1 && notify) notify('first', reason);
      if (reload) reload();
      return { action: 'reload', attempt: crashes };
    },
  };
}

module.exports = {
  showMainWindow,
  isAllowedAppUrl,
  isAllowedFileUrl,
  isPermissionAllowed,
  installWebContentsGuards,
  installPermissionGuards,
  createPickedFileRegistry,
  quoteDesktopExec,
  buildAutostartEntry,
  createCrashRecovery,
  ALLOWED_PERMISSIONS,
  ALLOWED_PAGES,
};
