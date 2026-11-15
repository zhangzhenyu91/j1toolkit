// 图片工具窗口的展示格式化：分钟级时间、文件大小、像素比例与按目录分段的路径省略。
// 只做纯字符串/数值换算，不读取文件系统，便于静态测试直接引用。

const pad2 = (value) => String(value).padStart(2, '0');

/** 修改时间：按本地时区显示到分钟（秒级只存不显）。 */
export function formatMtime(seconds) {
  const value = Number(seconds);
  if (!Number.isFinite(value) || value <= 0) return '—';
  const date = new Date(value * 1000);
  if (Number.isNaN(date.getTime())) return '—';
  return (
    `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())} ` +
    `${pad2(date.getHours())}:${pad2(date.getMinutes())}`
  );
}

const floorTo = (value, digits) => {
  const factor = 10 ** digits;
  return Math.floor(value * factor) / factor;
};

/** 人类可读大小：B / KB / MB / GB，KB 起保留 1 位小数（截断，与设计稿口径一致）。 */
export function formatSize(bytes) {
  const value = Number(bytes);
  if (!Number.isFinite(value) || value <= 0) return '—';
  if (value < 1024) return `${Math.round(value)} B`;
  if (value < 1024 * 1024) return `${floorTo(value / 1024, 1).toFixed(1)} KB`;
  if (value < 1024 * 1024 * 1024) return `${floorTo(value / (1024 * 1024), 1).toFixed(1)} MB`;
  return `${floorTo(value / (1024 * 1024 * 1024), 2).toFixed(2)} GB`;
}

/** 精确字节数：千分位 + 单位，用于信息栏「大小」的次级说明。 */
export function formatBytesExact(bytes) {
  const value = Number(bytes);
  if (!Number.isFinite(value) || value < 0) return '—';
  return `${Math.round(value).toLocaleString('en-US')} 字节`;
}

/** 像素尺寸：1920 × 1280 px。 */
export function formatPixels(width, height) {
  const w = Number(width) || 0;
  const h = Number(height) || 0;
  if (!w || !h) return '—';
  return `${w} × ${h} px`;
}

/** 万像素：只用于信息栏的次级说明。 */
export function formatMegapixels(width, height) {
  const w = Number(width) || 0;
  const h = Number(height) || 0;
  if (!w || !h) return '';
  return `${Math.round((w * h) / 10000)} 万像素`;
}

const COMMON_RATIOS = [
  [1, 1],
  [4, 3],
  [3, 2],
  [16, 9],
  [3, 4],
  [2, 3],
  [9, 16],
  [5, 4],
  [4, 5],
  [21, 9],
];

function gcd(a, b) {
  return b === 0 ? a : gcd(b, a % b);
}

/** 画面比例：优先给常见比例（3:2），否则约分；约分后仍很长时给小数比。 */
export function formatAspect(width, height) {
  const w = Math.round(Number(width) || 0);
  const h = Math.round(Number(height) || 0);
  if (!w || !h) return '—';
  const ratio = w / h;
  for (const [a, b] of COMMON_RATIOS) {
    if (Math.abs(ratio - a / b) / (a / b) <= 0.02) return `${a}:${b}`;
  }
  const divisor = gcd(w, h);
  const left = w / divisor;
  const right = h / divisor;
  if (left <= 40 && right <= 40) return `${left}:${right}`;
  return `${ratio.toFixed(2)}:1`;
}

/** 取文件名（同时兼容 Windows 反斜杠与 POSIX 斜杠）。 */
export function basenameOf(raw) {
  const text = String(raw == null ? '' : raw);
  const parts = text.split(/[\\/]+/).filter(Boolean);
  return parts.length ? parts[parts.length - 1] : '';
}

/** 取小写扩展名（含点号）；无扩展名返回空串。 */
export function extOf(raw) {
  const name = basenameOf(raw);
  const at = name.lastIndexOf('.');
  if (at <= 0 || at === name.length - 1) return '';
  return name.slice(at).toLowerCase();
}

/**
 * 截断主名：保留扩展名与末尾序号（-03），只从主名中间截掉。
 * 仅在「连文件名本身都放不下」时使用。
 */
export function truncateName(name, budget) {
  const text = String(name == null ? '' : name);
  const limit = Math.max(1, Math.floor(Number(budget) || 1));
  if (text.length <= limit) return text;
  const matched = /^(.*?)(-\d+)?(\.[^.]+)$/.exec(text);
  const stem = matched ? matched[1] : text;
  const seq = matched ? matched[2] || '' : '';
  const ext = matched ? matched[3] : '';
  const room = Math.max(1, limit - seq.length - ext.length);
  const cut = stem.slice(-room) || stem.slice(-1);
  return `${cut}${seq}${ext}`;
}

/**
 * 路径左省略：按目录分段丢弃前段，绝不把目录名从中间切断；末尾目录与文件名优先。
 * 连文件名都放不下时只截主名，并保留扩展名与序号。
 */
export function elidePath(raw, maxLen = 34) {
  const text = String(raw == null ? '' : raw).trim();
  if (!text) return '';
  const budget = Math.max(8, Math.floor(Number(maxLen) || 34));
  if (text.length <= budget) return text;
  const sep = text.includes('\\') ? '\\' : '/';
  const parts = text.split(/[\\/]+/).filter((part) => part.length > 0);
  if (parts.length <= 1) return truncateName(text, budget);
  const kept = [];
  for (let i = parts.length - 1; i >= 0; i -= 1) {
    const next = [parts[i]].concat(kept);
    const candidate = `…${sep}${next.join(sep)}`;
    if (kept.length && candidate.length > budget) break;
    kept.unshift(parts[i]);
    if (kept.length === 1 && candidate.length > budget) {
      return truncateName(parts[i], budget);
    }
  }
  const elided = `…${sep}${kept.join(sep)}`;
  if (elided.length <= budget) return elided;
  return truncateName(kept[kept.length - 1], budget);
}
