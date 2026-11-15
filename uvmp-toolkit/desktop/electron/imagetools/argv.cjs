// 图片窗口启动参数解析：把冷启动/第二实例的 argv 还原成待打开图片列表。
// 纯 node，不 import electron，便于回归测试。
const path = require('path');
const { fileURLToPath } = require('url');
const { VIEW_EXTENSIONS } = require('./path-guard.cjs');

// 一次最多接收的图片数：装订/拖拽/批量一次性传入过多文件时避免把窗口拖死。
const MAX_FILES = 200;

// file:// URI 还原成真实路径：只接受本机（无主机名或 localhost）的 file 地址。
// Chromium 在 Windows 上会给出 file:///C:/... 形态，fileURLToPath 会还原成 C:\...。
function stripFileUri(raw) {
  const text = String(raw == null ? '' : raw).trim();
  if (!/^file:/i.test(text)) return '';
  try {
    const url = new URL(text);
    if (url.protocol !== 'file:') return '';
    const host = String(url.hostname || '').toLowerCase();
    if (host && host !== 'localhost') return '';
    const p = fileURLToPath(url);
    return p ? path.resolve(p) : '';
  } catch {
    return '';
  }
}

function isViewablePath(p) {
  const ext = path.extname(String(p == null ? '' : p)).toLowerCase();
  return VIEW_EXTENSIONS.has(ext);
}

function resolveRealInput(raw, cwd) {
  const text = String(raw == null ? '' : raw).trim();
  if (!text) return '';
  // 带协议头的输入：file: 解码，其余协议（http/data/...）一律丢弃；
  // Windows 盘符（C:\ / c:/）也是「单字母:」形态，必须先排除。
  if (/^[a-z][a-z0-9+.-]*:/i.test(text) && !/^[a-zA-Z]:[\\/]/.test(text)) {
    return stripFileUri(text);
  }
  return path.resolve(cwd || process.cwd(), text);
}

/**
 * 解析图片窗口的启动参数。
 * opts:
 *   isPackaged  打包态（exe 后直接是文件）还是开发态（electron + app 路径）
 *   appPath     app.getAppPath()：开发态需要跳过这个应用目录参数
 *   cwd         相对路径的基准（第二实例传 workingDirectory）
 *   maxFiles    覆盖默认上限（测试用）
 *
 * 规则：
 *   - argv[0] 恒为可执行文件，跳过
 *   - `--` 之后一律按路径处理，之前形如 `-x`/`--x` 的开关忽略
 *   - 开发态跳过应用路径参数（`.`、绝对目录、.asar），只认其后参数
 *   - 只保留受支持的图片后缀，relative 路径按 cwd 解析，file URI 解码
 *   - 同路径去重并保序；单次数量有上限
 */
function parseImageArgs(argv, opts = {}) {
  if (!Array.isArray(argv)) return [];
  const { isPackaged = true, appPath = '', cwd = process.cwd() } = opts;
  const limit = Number.isFinite(opts.maxFiles) && opts.maxFiles > 0 ? opts.maxFiles : MAX_FILES;
  const base = cwd || process.cwd();
  const appKey = appPath ? path.resolve(appPath) : '';
  const seen = new Set();
  const out = [];
  let afterDash = false;
  let skipAppPath = !isPackaged && !!appKey;

  for (let i = 1; i < argv.length; i += 1) {
    const raw = argv[i];
    if (typeof raw !== 'string' || !raw) continue;
    if (!afterDash && raw === '--') {
      afterDash = true;
      continue;
    }
    if (!afterDash && raw.startsWith('-')) continue;

    const resolved = resolveRealInput(raw, base);
    if (!resolved) continue;
    if (skipAppPath) {
      const key = process.platform === 'win32' ? resolved.toLowerCase() : resolved;
      if (
        key === (process.platform === 'win32' ? appKey.toLowerCase() : appKey) ||
        /\.asar$/i.test(raw)
      ) {
        skipAppPath = false;
        continue;
      }
    }
    if (!isViewablePath(resolved)) continue;
    const dedupeKey = process.platform === 'win32' ? resolved.toLowerCase() : resolved;
    if (seen.has(dedupeKey)) continue;
    seen.add(dedupeKey);
    out.push(resolved);
    if (out.length >= limit) break;
  }
  return out;
}

module.exports = { parseImageArgs, isViewablePath, stripFileUri, MAX_FILES };
