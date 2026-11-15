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

export const FORMAT_BOUNDARY_NOTE =
  '可查看 JPEG / PNG / GIF / WebP / BMP / ICO / AVIF / TIFF；批处理仅静态 JPEG / PNG / WebP / BMP / TIFF，输出 JPEG / PNG / WebP。';

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
