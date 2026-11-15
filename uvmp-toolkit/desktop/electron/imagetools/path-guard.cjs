// 图片窗口的路径授权与 shade-image 协议。
// 设计目标：渲染层不持有 fs，也不能借图片协议读取任意文件。
//   - 选择文件：只授权其父目录（切图需要同目录兄弟文件）
//   - 选择目录：授权该目录（含子目录）
//   - 每次读取前重新 realpath 并复核：文件被删/被换成指向外部的符号链接即失效
//   - 协议 URL 只带真实路径（shade-image://local?path=<编码后路径>），
//     每次请求都交给 guard.check 复核授权，越界/外部替换一律 404；不签发令牌
//   - 真实路径口径：Windows 用 native realpath（8.3 短名先归一成长名），其他平台用 legacy realpath；
//     授权、复查、协议 URL 全部走同一个口径，同一目录的长短名不会被当成两个对象。
const fs = require('fs');
const path = require('path');
const { Readable } = require('stream');

// 查看格式：JPEG/PNG/GIF/WebP/BMP/ICO/AVIF/TIFF（8 种格式、10 个扩展名）。
// HEIC/RAW/PSD/EPS/PDF/SVG 不承诺，也不进协议白名单。
const VIEW_EXTENSIONS = new Set([
  '.jpg',
  '.jpeg',
  '.png',
  '.gif',
  '.webp',
  '.bmp',
  '.ico',
  '.avif',
  '.tif',
  '.tiff',
]);

const MIME_BY_EXT = new Map([
  ['.jpg', 'image/jpeg'],
  ['.jpeg', 'image/jpeg'],
  ['.png', 'image/png'],
  ['.gif', 'image/gif'],
  ['.webp', 'image/webp'],
  ['.bmp', 'image/bmp'],
  ['.ico', 'image/x-icon'],
  ['.avif', 'image/avif'],
  ['.tif', 'image/tiff'],
  ['.tiff', 'image/tiff'],
]);

function imageMime(p) {
  return MIME_BY_EXT.get(path.extname(String(p == null ? '' : p)).toLowerCase()) || '';
}

function caseKey(p) {
  return process.platform === 'win32' ? String(p).toLowerCase() : String(p);
}

function realOrEmpty(p) {
  const raw = String(p == null ? '' : p).trim();
  if (!raw) return '';
  try {
    // Windows 必须用 native realpath：legacy 版会把 8.3 短名原样返回，
    // 于是 C:\PROGRA~1 与 C:\Program Files 被当成两个目录（v2.4.2 CI：冷启动 argv 的
    // C:\Users\RUNNER~1\… 与核心返回的 C:\Users\runneradmin\… 互相判越界，预览/改名报「未授权的图片路径」）。
    // native 版在 Windows 走 GetFinalPathNameByHandle，长短名统一成长名；其他平台语义相同，沿用 legacy。
    const realpath = process.platform === 'win32' ? fs.realpathSync.native : fs.realpathSync;
    return realpath(raw);
  } catch {
    return '';
  }
}

function statOrNull(p) {
  try {
    return fs.statSync(p);
  } catch {
    return null;
  }
}

/**
 * 解析「可能尚不存在」的路径：从最深的已存在祖先取 realpath，再拼回剩余段。
 * 用于输出目录归属判断——授权目录内的符号链接指向外部时，必须按真实位置判越界。
 */
function resolveRealisticPath(p) {
  const resolved = path.resolve(String(p == null ? '' : p).trim() || '.');
  const parts = [];
  let current = resolved;
  for (;;) {
    const real = realOrEmpty(current);
    if (real) return parts.length ? path.join(real, ...parts.reverse()) : real;
    const parent = path.dirname(current);
    if (parent === current) return '';
    parts.push(path.basename(current));
    current = parent;
  }
}

// 严格包含（不含自身）：父目录授权后，子目录里的文件才算命中。
function isInside(parent, child) {
  const rel = path.relative(caseKey(parent), caseKey(child));
  if (!rel) return false;
  return rel !== '..' && !rel.startsWith('..' + path.sep) && !path.isAbsolute(rel);
}

/**
 * 路径授权表（每个图片窗口一份）。
 * maxDirs 限制授权目录数量，避免长时间使用无限累积；超出淘汰最早授权。
 */
function createPathGuard({ maxDirs = 60, maxFiles = 200 } = {}) {
  const dirLimit = Math.max(1, Number(maxDirs) || 60);
  const fileLimit = Math.max(1, Number(maxFiles) || 200);
  const dirs = new Map(); // realpath -> 最近登记顺序
  const files = new Map(); // realpath -> 最近登记顺序（仅核心返回的预览缓存）

  function remember(map, key, limit) {
    map.delete(key);
    map.set(key, Date.now());
    while (map.size > limit) map.delete(map.keys().next().value);
  }

  function rememberDir(real) {
    if (real) remember(dirs, real, dirLimit);
    return real;
  }

  return {
    /** 选择目录：登记真实目录路径；不是目录/不可读时返回空串。 */
    authorizeDirectory(p) {
      const real = realOrEmpty(p);
      if (!real) return '';
      const st = statOrNull(real);
      if (!st || !st.isDirectory()) return '';
      return rememberDir(real);
    },

    /**
     * 选择文件：只授权其父目录。
     * 若选中的是指向父目录之外的符号链接，则拒绝——否则一次误点就会授权目标所在目录。
     */
    authorizeFile(p) {
      const real = realOrEmpty(p);
      if (!real) return '';
      const st = statOrNull(real);
      if (!st || !st.isFile()) return '';
      const parentReal = realOrEmpty(path.dirname(String(p).trim()));
      if (!parentReal) return '';
      if (!isInside(parentReal, real)) return '';
      rememberDir(parentReal);
      return real;
    },

    /**
     * 精确授权单个文件（仅用于核心返回的预览缓存：缓存目录不在用户选择范围内）。
     * 只影响这一个文件，不放开其所在目录。
     */
    grantFile(p) {
      const real = realOrEmpty(p);
      if (!real) return '';
      const st = statOrNull(real);
      if (!st || !st.isFile()) return '';
      remember(files, real, fileLimit);
      return real;
    },

    /** 校验文件读取：realpath 后必须仍在已授权目录内（或精确授权），且后缀受支持。 */
    check(p) {
      const real = realOrEmpty(p);
      if (!real) return '';
      if (!VIEW_EXTENSIONS.has(path.extname(real).toLowerCase())) return '';
      const st = statOrNull(real);
      if (!st || !st.isFile()) return '';
      if (files.has(real)) return real;
      for (const dir of dirs.keys()) {
        if (isInside(dir, real)) return real;
      }
      return '';
    },

    /** 校验目录本身（打开所在文件夹用）：只认用户选择过的那个目录。 */
    checkDirectory(p) {
      const real = realOrEmpty(p);
      if (!real) return '';
      const st = statOrNull(real);
      if (!st || !st.isDirectory()) return '';
      return dirs.has(real) ? real : '';
    },

    /** 已授权目录快照（输出目录归属判断用）。 */
    authorizedDirectories() {
      return [...dirs.keys()];
    },

    clear() {
      dirs.clear();
      files.clear();
    },

    get dirCount() {
      return dirs.size;
    },

    get fileCount() {
      return files.size;
    },
  };
}

/** 把真实路径编码进协议 URL：shade-image://local?path=<encodeURIComponent(realPath)>。 */
function buildImageProtocolUrl(realPath) {
  const text = String(realPath == null ? '' : realPath).trim();
  if (!text) return '';
  return 'shade-image://local?path=' + encodeURIComponent(text);
}

// 查询值只允许 encodeURIComponent 会产出的字符（%XX 编码 + 未转义的 URL 安全字符）。
// 未编码的绝对路径（含 / \ : 空格等）与拼接额外参数的 URL 一律不认。
const ENCODED_PATH_RE = /^[A-Za-z0-9\-_.!~*'()%]+$/;

/**
 * 解析 shade-image://local?path=<编码后真实路径>；非法形态返回空串。
 * Chromium 对 standard scheme 会把 URL 规范化成 shade-image://local/?path=…（host 后补 /），
 * 两种情况都要认，否则真实窗口里 <img> 一律 404。
 */
function parseImageProtocolUrl(raw) {
  const text = String(raw == null ? '' : raw).trim();
  const m = /^shade-image:\/\/local\/?\?path=([^&]+)$/.exec(text);
  if (!m) return '';
  if (!ENCODED_PATH_RE.test(m[1])) return '';
  let decoded = '';
  try {
    decoded = decodeURIComponent(m[1]);
  } catch {
    return '';
  }
  if (!decoded || !path.isAbsolute(decoded)) return '';
  return decoded;
}

function notFound() {
  return new Response('', { status: 404, headers: { 'cache-control': 'no-store' } });
}

/**
 * shade-image 协议处理器：URL 里的真实路径每次请求都过一遍 guard.check
 * （realpath + 目录授权 + 格式白名单 + 必须是文件），通过后才流式返回字节。
 * 不暴露目录列表；未授权、越界、符号链接替换、非法格式一律 404。
 */
function createImageProtocolHandler({ guard } = {}) {
  if (!guard) throw new Error('协议处理器需要路径授权表');
  return async function handleImageRequest(request) {
    let rawUrl = '';
    try {
      rawUrl = request && request.url ? String(request.url) : '';
    } catch {
      return notFound();
    }
    const target = parseImageProtocolUrl(rawUrl);
    if (!target) return notFound();
    const real = guard.check(target);
    if (!real) return notFound();
    const mime = imageMime(real);
    if (!mime) return notFound();
    let stat;
    try {
      stat = fs.statSync(real);
    } catch {
      return notFound();
    }
    if (!stat.isFile()) return notFound();
    const headers = {
      'content-type': mime,
      'content-length': String(stat.size),
      'cache-control': 'no-store',
      'x-content-type-options': 'nosniff',
    };
    if (request.method === 'HEAD') return new Response(null, { status: 200, headers });
    const stream = fs.createReadStream(real);
    stream.on('error', () => {});
    // Electron 的 protocol.handle 接受 Web Response；Node 流需转成 Web 流，按块流式返回。
    return new Response(Readable.toWeb(stream), { status: 200, headers });
  };
}

module.exports = {
  VIEW_EXTENSIONS,
  imageMime,
  createPathGuard,
  buildImageProtocolUrl,
  parseImageProtocolUrl,
  createImageProtocolHandler,
  isInside,
  resolveRealisticPath,
  // 真实路径口径的唯一实现：测试与冒烟断言都用它，避免测试侧再用 legacy realpath 把短名当期望。
  realOrEmpty,
};
