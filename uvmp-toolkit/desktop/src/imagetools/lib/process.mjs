// 图片处理的纯逻辑：格式边界、可处理判定、任务摘要、进度与结果统计。
// 与核心 engine 的口径保持一致：查看 8 种格式（10 个扩展名）、批处理仅静态
// JPEG / PNG / WebP / BMP / TIFF，输出固定 JPEG / PNG / WebP。

export const VIEW_EXTS = [
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
];

// 可批处理的 5 种静态格式（7 个扩展名）
export const BATCH_FORMATS = ['JPEG', 'PNG', 'WEBP', 'BMP', 'TIFF'];

export const BATCH_EXTS = ['.jpg', '.jpeg', '.png', '.webp', '.bmp', '.tif', '.tiff'];

export const OPERATIONS = ['convert', 'compress', 'resize'];

export const OPERATION_TABS = [
  { key: 'convert', label: '格式转换' },
  { key: 'compress', label: '压缩体积' },
  { key: 'resize', label: '尺寸修改' },
];

export const OUTPUT_FORMATS = {
  jpeg: { key: 'jpeg', label: 'JPEG', ext: '.jpg', desc: '通用照片 · 体积小' },
  png: { key: 'png', label: 'PNG', ext: '.png', desc: '无损 · 保留透明' },
  webp: { key: 'webp', label: 'WebP', ext: '.webp', desc: '同画质更省空间' },
};

export const COMPRESS_LEVELS = [
  { key: 'high', label: '高质量', quality: 92, desc: '适合留档' },
  { key: 'balanced', label: '均衡', quality: 80, desc: '汇报够用', recommended: true },
  { key: 'small', label: '小体积', quality: 65, desc: '仅传送预览' },
];

export const TRANSPARENT_OPTIONS = [
  { key: 'keep', label: '保留透明' },
  { key: 'white', label: '填充白色' },
  { key: 'gray', label: '填充浅灰 #F7F8FA' },
];

export const RESIZE_MODES = [
  { key: 'pixels', label: '像素' },
  { key: 'percent', label: '百分比' },
  { key: 'long_edge', label: '长边' },
];

export const UNSUPPORTED_NOTE =
  'HEIC / RAW / PSD / EPS / PDF / SVG 暂不承诺预览与批处理；动画 GIF / WebP 跳过且不压成首帧。';

export function extLabel(ext) {
  return String(ext || '')
    .replace(/^\./, '')
    .toUpperCase();
}

export function isBatchExt(ext) {
  return BATCH_EXTS.includes(String(ext || '').toLowerCase());
}

/**
 * 单项文件能否进入批处理：不支持格式、动画图、仅查看格式都给出可读原因。
 * 跳过项由核心再次复核，这里只用于列表标签与「开始处理」的可行性判断。
 */
export function classifyFile(file) {
  const ext = String((file && file.ext) || '').toLowerCase();
  const label = extLabel(ext);
  if (!file || file.supported === false || !VIEW_EXTS.includes(ext)) {
    return {
      processable: false,
      reason: label ? `${label} 不在批处理范围` : '该格式不在批处理范围',
    };
  }
  if (file.too_large) {
    return { processable: false, reason: '文件超过 200 MB / 1.5 亿像素上限' };
  }
  if (file.animated) {
    return { processable: false, reason: '动画图：整条跳过，不压成首帧' };
  }
  if (ext === '.gif') {
    return { processable: false, reason: '静态 GIF 不在首版批处理范围' };
  }
  if (!BATCH_EXTS.includes(ext)) {
    return { processable: false, reason: `${label} 仅查看，不参与批处理` };
  }
  return { processable: true, reason: '可处理' };
}

/** 本次任务的摘要：随页签、目标格式、质量与档位实时联动。 */
export function describeTask({ operation, options = {}, processable = 0, skipped = 0 }) {
  let head = '';
  if (operation === 'convert') {
    const format = OUTPUT_FORMATS[options.format] || OUTPUT_FORMATS.jpeg;
    head = `格式转换 · ${format.label} 质量 ${options.quality}`;
  } else if (operation === 'compress') {
    const level = COMPRESS_LEVELS.find((item) => item.key === options.level) || COMPRESS_LEVELS[1];
    head = `压缩体积 · ${level.label}（质量 ${level.quality}）`;
  } else {
    const mode = RESIZE_MODES.find((item) => item.key === options.mode) || RESIZE_MODES[0];
    head = `尺寸修改 · ${mode.label}`;
  }
  return `${head} · ${processable} 项可处理${skipped ? ` · 跳过 ${skipped} 项` : ''}`;
}

/** 「开始处理」是否可用与不可用原因（0 项禁用）。 */
export function describeAction({ processable = 0, skipped = 0 }) {
  if (processable > 0) {
    return {
      canStart: true,
      reason: `本次 ${processable} 项可处理${skipped ? ` · 跳过 ${skipped} 项` : ''}`,
    };
  }
  return {
    canStart: false,
    reason: '暂无可处理项，请先添加静态 JPEG / PNG / WebP / BMP / TIFF 图片',
  };
}

/**
 * 把「用户传入的已授权路径」解析成可入列表的条目（处理页 addPaths 的纯逻辑）。
 *
 * 为什么需要它：渲染层拿到的路径可能是 Windows 8.3 短名（C:\Users\RUNNER~1\…），
 * 而核心列举返回的是长名。用传入路径直接去查核心列举的键永远不命中，
 * 旧实现就把它当成「已不在原位置」全部跳过（v2.4.3 CI：列表 0 行）。
 * 正确做法是让核心给出**规范路径**（api.inspect → 主进程仍校验目录授权），
 * 再用规范路径查 known / 去重 / 写 entries；权威身份只看规范路径，
 * 绝不退化成 basename 或包含关系匹配。
 *
 * 返回 { entries, missing }：
 *   - entries: [{ path: 规范路径, info: 核心元数据 }]，同一规范路径只出现一次；
 *   - missing: 原始输入路径（列举该目录成功、但核心也认不出 → 真缺失，由调用方提示并跳过）。
 *
 * 注意 inspect 会因**文件无法解码**而失败（如坏图、超大图），那并不代表文件不存在。
 * 所以 inspect 失败时还要用核心列举给出的规范目录 + 精确文件名再确认一次：
 * 命中就说明文件确实在（存在性以核心列举为准），只有两边都认不出才是真缺失。
 *
 * opts:
 *   picked     原始输入路径数组
 *   known      Map<规范路径, 元数据>（核心列举结果）
 *   listedDirs Set<目录>（已成功列举过的目录；用原始目录判断，缺失才算数）
 *   knownDirs  Map<原始目录, 核心返回的规范目录>（可选；inspect 失败时用它做精确兜底）
 *   dirOf      (path) => 所在目录
 *   inspect    async (path) => 核心规范路径或空串（无授权/不存在时为空；解码失败会抛错）
 *   existing   已入列表的规范路径集合（可选，去重用）
 */
export async function resolvePickedFiles({
  picked,
  known,
  listedDirs,
  knownDirs,
  dirOf,
  inspect,
  existing,
} = {}) {
  const map = known instanceof Map ? known : new Map();
  const listed = listedDirs instanceof Set ? listedDirs : new Set();
  const dirOfFn = typeof dirOf === 'function' ? dirOf : () => '';
  const inspectFn = typeof inspect === 'function' ? inspect : null;
  const seen = new Set(existing instanceof Set ? existing : []);
  const entries = [];
  const missing = [];
  const list = (Array.isArray(picked) ? picked : []).filter(
    (item) => typeof item === 'string' && item
  );

  // 按目录做有界缓存：同一目录内多个短名共享一次换算，避免逐文件重复 RPC
  const canonicalCache = new Map();
  const knownDirMap = knownDirs instanceof Map ? knownDirs : new Map();

  const baseNameOf = (p) => String(p).split(/[\\/]/).pop() || '';
  // 「规范目录 + 精确文件名」拼接：分隔符必须跟随规范目录的实际形态。
  // 写死 '\\' 会让 POSIX（Linux/麒麟，listing.directory 用 '/'）拼出 '…图片\06-坏图.jpg' 永不命中，
  // 于是坏图在 POSIX 上仍被判「已不在原位置」并从列表丢掉。已有尾分隔符（根目录 '/'、'C:\'）不再重复拼。
  const joinCanonical = (dir, name) => {
    if (!dir || !name) return '';
    if (/[\\/]$/.test(dir)) return dir + name;
    return dir + (dir.includes('\\') ? '\\' : '/') + name;
  };

  for (const raw of list) {
    let canonical = '';
    if (map.has(raw)) {
      canonical = raw; // 已经是核心规范路径：常见路径零额外调用
    } else if (canonicalCache.has(raw)) {
      canonical = canonicalCache.get(raw);
    } else if (inspectFn) {
      try {
        canonical = String((await inspectFn(raw)) || '');
      } catch (e) {
        canonical = '';
      }
      canonicalCache.set(raw, canonical);
    }
    if (!canonical) {
      // inspect 失败不等于文件不存在：坏图/超大图会让 inspect 报解码错误。
      // 用核心列举给出的规范目录 + 精确文件名再确认一次（存在性以核心列举为准）。
      const canonDir = knownDirMap.get(dirOfFn(raw)) || '';
      const byListing = joinCanonical(canonDir, baseNameOf(raw));
      if (byListing && map.has(byListing)) {
        canonical = byListing;
      } else if (listed.has(dirOfFn(raw))) {
        // 只有「该目录确实列举成功」才判缺失：列举失败属于元数据不可用，按传入路径兜底展示
        if (!missing.includes(raw)) missing.push(raw);
        continue;
      } else {
        canonical = raw;
      }
    }
    if (seen.has(canonical)) continue;
    seen.add(canonical);
    entries.push({
      path: canonical,
      info: map.get(canonical) || {},
      // 无法从核心拿到元数据时，调用方按原路径兜底展示
      input: raw,
    });
  }
  return { entries, missing };
}

export function formatConflictPolicy() {
  return '同名文件自动追加 (2)，不提供原地覆盖';
}

export function outputPolicyText() {
  return '输出另存到原目录下的新子目录或自定义目录，绝不覆盖原图；同名冲突自动追加 (2)。';
}

/** 默认输出目录：原目录下的 processed_日期。 */
export function defaultOutputDir(directory, now = new Date()) {
  const base = String(directory || '').replace(/[\\/]+$/, '');
  if (!base) return '';
  const sep = base.includes('\\') ? '\\' : '/';
  const date = now instanceof Date ? now : new Date(now);
  const stamp =
    `${date.getFullYear()}` +
    `${String(date.getMonth() + 1).padStart(2, '0')}` +
    `${String(date.getDate()).padStart(2, '0')}`;
  return `${base}${sep}processed_${stamp}`;
}

/** 结果统计：成功 / 失败 / 跳过 / 总数。 */
export function classifyRows(rows) {
  const list = Array.isArray(rows) ? rows : [];
  let success = 0;
  let failed = 0;
  let skipped = 0;
  for (const row of list) {
    if (!row) continue;
    if (row.status === 'success') success += 1;
    else if (row.status === 'skipped') skipped += 1;
    else failed += 1;
  }
  return { success, failed, skipped, total: list.length };
}

export function progressPercent({ current = 0, total = 0 } = {}) {
  const done = Number(current) || 0;
  const all = Number(total) || 0;
  if (all <= 0) return 0;
  return Math.max(0, Math.min(100, Math.round((done / all) * 100)));
}

/** 结果基调：全成功 success / 部分失败 partial / 全失败 failed。 */
export function resultTone(counts) {
  const failed = Number((counts && counts.failed) || 0);
  const success = Number((counts && counts.success) || 0);
  if (failed > 0) return success > 0 ? 'partial' : 'failed';
  return 'success';
}

const OUTCOME_TONE = {
  done: 'success',
  partial: 'warn',
  failed: 'danger',
  cancelled: 'default',
  error: 'danger',
};

/**
 * 任务结束态判定：以 job.status 与结果行一起决定标题/标签，
 * 保证「全失败 / 取消 / 核心退出 / 全部跳过」都不会被误报成「全部成功」。
 */
export function resolveOutcome({ status = 'success', rows = [] } = {}) {
  const counts = classifyRows(rows);
  const code = String(status || 'success');
  if (code === 'cancelled') {
    return { state: 'cancelled', title: '任务已取消', tag: '已取消', tone: 'default', counts };
  }
  if (code === 'error' || code === 'core.exit' || code === 'interrupted') {
    return { state: 'error', title: '任务已中断', tag: '已中断', tone: 'danger', counts };
  }
  if (code === 'failed' && counts.failed === 0) {
    return { state: 'failed', title: '处理失败', tag: '全部失败', tone: 'danger', counts };
  }
  const tone = resultTone(counts);
  if (tone === 'failed') {
    return { state: 'failed', title: '处理失败', tag: '全部失败', tone: 'danger', counts };
  }
  if (tone === 'partial') {
    return { state: 'partial', title: '处理完成（有失败）', tag: '部分失败', tone: 'warn', counts };
  }
  if (counts.success === 0 && counts.skipped > 0) {
    // 全是被跳过的文件：不能算成功
    return { state: 'done', title: '处理完成', tag: '全部跳过', tone: 'default', counts };
  }
  return { state: 'done', title: '处理完成', tag: '全部成功', tone: 'success', counts };
}

/** 结果基调 → 标签配色类名（与 theme 的 tag 语义一致）。 */
export function outcomeTagClass(tone) {
  if (tone === 'danger') return 'is-danger';
  if (tone === 'warn') return 'is-warn';
  if (tone === 'success') return 'is-success';
  return 'is-info';
}

export function resultSummary(counts) {
  const data = counts || { success: 0, failed: 0, skipped: 0, total: 0 };
  return `成功 ${data.success} · 失败 ${data.failed} · 跳过 ${data.skipped}`;
}

/** 按档位估算输出体积：只作参考，界面必须标注「实际以执行结果为准」。 */
export function estimateOutput(rows, operation, options = {}) {
  const list = Array.isArray(rows) ? rows : [];
  let before = 0;
  let after = 0;
  for (const row of list) {
    if (!row || !row.processable) continue;
    const size = Number(row.size) || 0;
    before += size;
    const ext = String(row.ext || '').toLowerCase();
    let factor = 0.6;
    if (operation === 'compress') {
      const level =
        COMPRESS_LEVELS.find((item) => item.key === options.level) || COMPRESS_LEVELS[1];
      if (ext === '.png') factor = 0.85;
      else if (ext === '.bmp' || ext === '.tif' || ext === '.tiff') factor = 0.25;
      else factor = level.quality >= 90 ? 0.55 : level.quality >= 80 ? 0.42 : 0.32;
    } else if (operation === 'convert') {
      const target = String(options.format || 'jpeg');
      if (target === 'png') factor = 1.15;
      else if (ext === '.bmp' || ext === '.tif' || ext === '.tiff') factor = 0.3;
      else factor = (Number(options.quality) || 88) >= 90 ? 0.6 : 0.45;
    } else {
      factor = 0.7;
    }
    after += Math.round(size * factor);
  }
  return { before, after };
}

/** 尺寸修改的示例换算：按首张可处理图的像素尺寸。 */
export function describeResize(sample, options = {}) {
  const width = Number((sample && sample.width) || 0);
  const height = Number((sample && sample.height) || 0);
  if (!width || !height) return '各文件按同一规则分别换算';
  const mode = options.mode;
  if (mode === 'percent') {
    const percent = Number(options.percent) || 100;
    const w = Math.max(1, Math.round((width * percent) / 100));
    const h = Math.max(1, Math.round((height * percent) / 100));
    return `${width} × ${height} → ${w} × ${h} px`;
  }
  if (mode === 'long_edge') {
    const edge = Number(options.long_edge) || 0;
    if (!edge) return '请输入长边像素';
    const ratio = edge / Math.max(width, height);
    const w = Math.max(1, Math.round(width * ratio));
    const h = Math.max(1, Math.round(height * ratio));
    return `${width} × ${height} → ${w} × ${h} px`;
  }
  const targetW = Number(options.width) || 0;
  const targetH = Number(options.height) || 0;
  if (!targetW && !targetH) return '请输入目标宽或高';
  const keep = options.keepRatio !== false;
  let w = targetW || width;
  let h = targetH || height;
  if (keep) {
    const scale = Math.min(
      targetW ? targetW / width : Infinity,
      targetH ? targetH / height : Infinity
    );
    const ratio = Number.isFinite(scale) ? scale : 1;
    w = Math.max(1, Math.round(width * ratio));
    h = Math.max(1, Math.round(height * ratio));
  }
  return `${width} × ${height} → ${w} × ${h} px`;
}
