// 批量重命名页面的纯函数层：展示分组、混合选择去重、预览参数与结果映射。
// 冲突判定、新名生成与两阶段提交全部在 Python 后端（toolkit/fileops/naming.py）；
// 页面只渲染 preview/run 的结论，不重复实现改名规则，避免前后端口径漂移。
import { baseOutcome, friendlyTaskError } from './task-result.mjs';

export const APP_ID = 'batch_rename';
export const CONFLICT_MODES = ['skip', 'suffix'];

const IMAGE_EXTS = new Set([
  '.jpg',
  '.jpeg',
  '.png',
  '.webp',
  '.gif',
  '.bmp',
  '.ico',
  '.avif',
  '.tif',
  '.tiff',
]);
const DOC_EXTS = new Set(['.doc', '.docx', '.rtf', '.odt', '.pdf']);
const SHEET_EXTS = new Set(['.xls', '.xlsx', '.csv', '.ods']);
const TEXT_EXTS = new Set(['.txt', '.md', '.log', '.ini']);
const KIND_LABEL = { image: '图片', doc: '文档', sheet: '表格', text: '文本', other: '文件' };
const RESULT_TAG = {
  success: { theme: 'success', text: '成功' },
  failed: { theme: 'danger', text: '失败' },
  skipped: { theme: 'warning', text: '跳过' },
  unchanged: { theme: 'default', text: '未变化' },
  not_run: { theme: 'default', text: '未执行' },
};

function extOf(name) {
  const text = String(name == null ? '' : name);
  const dot = text.lastIndexOf('.');
  return dot > 0 ? text.slice(dot).toLowerCase() : '';
}

export function kindOf(name) {
  const ext = extOf(name);
  if (IMAGE_EXTS.has(ext)) return 'image';
  if (DOC_EXTS.has(ext)) return 'doc';
  if (SHEET_EXTS.has(ext)) return 'sheet';
  if (TEXT_EXTS.has(ext)) return 'text';
  return 'other';
}

export function typeText(name) {
  const ext = extOf(name);
  const label = KIND_LABEL[kindOf(name)];
  return (ext ? ext.slice(1).toUpperCase() : '无扩展名') + ' · ' + label;
}

export function splitName(name) {
  const text = String(name == null ? '' : name);
  const dot = text.lastIndexOf('.');
  if (dot <= 0) return { main: text, ext: '' };
  return { main: text.slice(0, dot), ext: text.slice(dot) };
}

function toBool(value, fallback = false) {
  if (value === undefined || value === null) return fallback;
  if (typeof value === 'string') {
    return !['', '0', 'false', 'no', 'off'].includes(value.trim().toLowerCase());
  }
  return Boolean(value);
}

function toInt(value, fallback) {
  const number = Number(value);
  return Number.isFinite(number) ? Math.round(number) : fallback;
}

function toCount(value) {
  const number = toInt(value, 0);
  return number > 0 ? number : 0;
}

export function createRules() {
  return {
    replace: [{ find: '', replace: '', case_sensitive: false }],
    prefix: '',
    suffix: '',
    keep_extension: true,
    sequence: { enabled: false, base: '', start: 1, step: 1, digits: 3 },
    conflict: 'skip',
  };
}

function normalizeSequence(sequence) {
  const raw = sequence && typeof sequence === 'object' ? sequence : {};
  return {
    enabled: toBool(raw.enabled, false),
    base: typeof raw.base === 'string' ? raw.base.trim() : '',
    start: toInt(raw.start, 1),
    step: Math.max(1, toInt(raw.step, 1)),
    digits: Math.min(9, Math.max(1, toInt(raw.digits, 3))),
  };
}

function isPathLike(value) {
  return typeof value === 'string' && value.trim() !== '';
}

/**
 * 路径比较键：与选择阶段相同的 Windows 口径（去首尾空白 + 大小写不敏感）。
 * 选择时按这个键去重，回看结果、移除条目时也必须用同一个键，否则大小写差异会误删。
 */
export function pathKey(value) {
  return String(value == null ? '' : value)
    .trim()
    .toLowerCase();
}

/**
 * 混合选择：把新选中的文件并入现有列表，按 Windows 口径（大小写不敏感）去重。
 * 返回合并后的列表、本次新增数量与被忽略的重复数量，供页面给出明确提示。
 */
export function mergePickedFiles(existing = [], picked = []) {
  const collect = (list) => {
    const seen = new Set();
    const files = [];
    for (const item of Array.isArray(list) ? list : []) {
      if (!isPathLike(item)) continue;
      const text = item.trim();
      const key = pathKey(text);
      if (seen.has(key)) continue;
      seen.add(key);
      files.push(text);
    }
    return files;
  };
  const base = collect(existing);
  const incoming = collect(picked);
  const merged = collect([...base, ...incoming]);
  const added = merged.length - base.length;
  return { files: merged, added, duplicates: incoming.length - added };
}

/**
 * 从列表里移除一个条目：只按完整路径精确比对（同一套 pathKey 口径）。
 * 不得退回文件名或目录名——不同目录下的同名文件必须各自独立，否则会连坐删除。
 */
export function removeFile(files, path) {
  const list = Array.isArray(files) ? files : [];
  const target = pathKey(path);
  if (!target) return list.slice();
  return list.filter((item) => pathKey(item) !== target);
}

/** 预览/执行共用的后端参数：任意文件优先于目录，空查找组不下发，冲突模式收敛到白名单。 */
export function buildRenameParams({ files = [], directory = '', rules = createRules() } = {}) {
  const source = rules && typeof rules === 'object' ? rules : createRules();
  const list = (Array.isArray(files) ? files : []).filter(isPathLike).map((item) => item.trim());
  const replace = (Array.isArray(source.replace) ? source.replace : [])
    .filter((pair) => pair && typeof pair.find === 'string' && pair.find.trim() !== '')
    .map((pair) => ({
      find: pair.find,
      replace: typeof pair.replace === 'string' ? pair.replace : '',
      case_sensitive: pair.case_sensitive === true,
    }));
  return {
    files: list,
    directory: list.length ? '' : String(directory || '').trim(),
    replace,
    prefix: String(source.prefix || '').trim(),
    suffix: String(source.suffix || '').trim(),
    keep_extension: source.keep_extension !== false,
    sequence: normalizeSequence(source.sequence),
    conflict: CONFLICT_MODES.includes(source.conflict) ? source.conflict : 'skip',
  };
}

/**
 * 进度条的分母：后端 ``run`` 的 total 就是 ``len(rows)``（含会被跳过与未变化的项），
 * 页面不能用「可提交数」当分母，否则取消/部分失败时百分比与待处理数都对不上。
 */
export function progressPlan(preview) {
  const data = preview && typeof preview === 'object' ? preview : {};
  return {
    total: toCount(data.total),
    planned: toCount(data.can),
    skip: toCount(data.skip),
    same: toCount(data.same),
  };
}

/** 结果计数：结果卡、页内历史与跨页历史共用同一份口径。 */
export function renameCounts(result) {
  const data = result && typeof result === 'object' ? result : {};
  const counts = {
    success: toCount(data.success),
    failed: toCount(data.failed),
    skipped: toCount(data.skipped),
    unchanged: toCount(data.unchanged),
    not_run: toCount(data.not_run),
  };
  return { ...counts, total: Object.values(counts).reduce((sum, n) => sum + n, 0) };
}

export function previewCounts(preview) {
  const data = preview && typeof preview === 'object' ? preview : {};
  return {
    total: toCount(data.total),
    can: toCount(data.can),
    skip: toCount(data.skip),
    same: toCount(data.same),
    warnings: (Array.isArray(data.warnings) ? data.warnings : []).filter(
      (line) => typeof line === 'string' && line.trim() !== ''
    ),
  };
}

/** 预检行 → 状态标签；非法名/互撞/磁盘同名必须分开，用户才知道要改什么。 */
export function rowTag(row) {
  const data = row && typeof row === 'object' ? row : {};
  const status = String(data.status || '');
  const flags = Array.isArray(data.flags) ? data.flags : [];
  const reason = String(data.reason || '');
  if (status === 'same') return { theme: 'default', text: '不修改' };
  if (status === 'skip') {
    if (reason.startsWith('名称非法')) return { theme: 'danger', text: '非法名称' };
    if (reason.includes('互撞')) return { theme: 'danger', text: '目标互撞' };
    if (reason.includes('已存在')) return { theme: 'danger', text: '磁盘已有' };
    return { theme: 'warning', text: '跳过' };
  }
  if (flags.includes('cycle')) return { theme: 'primary', text: '循环改名' };
  if (flags.includes('case_only')) return { theme: 'primary', text: '仅大小写' };
  return { theme: 'success', text: '正常' };
}

export function rowNameTone(row) {
  const data = row && typeof row === 'object' ? row : {};
  const status = String(data.status || '');
  const flags = Array.isArray(data.flags) ? data.flags : [];
  if (status === 'same') return 'same';
  if (status === 'skip') return 'bad';
  if (flags.includes('cycle') || flags.includes('case_only')) return 'info';
  return 'ok';
}

export function rulesSummary(rules) {
  const data = rules && typeof rules === 'object' ? rules : {};
  const pairs = (Array.isArray(data.replace) ? data.replace : []).filter(
    (pair) => pair && typeof pair.find === 'string' && pair.find.trim() !== ''
  ).length;
  const bits = [];
  if (pairs) bits.push(`${pairs} 组替换`);
  if (String(data.prefix || '').trim()) bits.push('前缀');
  if (String(data.suffix || '').trim()) bits.push('后缀');
  if (normalizeSequence(data.sequence).enabled) bits.push('统一主名 + 序号');
  if (data.keep_extension === false) bits.push('不保留扩展名');
  return bits.length ? bits.join(' · ') : '未设置规则';
}

export function conflictTip(rules) {
  return rules && rules.conflict === 'suffix'
    ? '冲突自动追加序号 · 仍不覆盖'
    : '冲突默认跳过 · 执行前二次确认';
}

export function sequenceExample(rules, sampleName = '') {
  const data = rules && typeof rules === 'object' ? rules : {};
  const sequence = normalizeSequence(data.sequence);
  const ext = data.keep_extension === false ? '' : splitName(sampleName).ext;
  const base = sequence.base || '文件';
  const number = String(Math.max(0, sequence.start)).padStart(sequence.digits, '0');
  return `${String(data.prefix || '').trim()}${base}_${number}${String(data.suffix || '').trim()}${ext}`;
}

function countText(counts, { withZero = true } = {}) {
  const parts = [];
  const push = (label, value) => {
    if (value || withZero) parts.push(`${label} ${value}`);
  };
  push('成功', counts.success);
  push('失败', counts.failed);
  push('跳过', counts.skipped);
  push('未变化', counts.unchanged);
  push('未执行', counts.not_run);
  return parts.join('，');
}

/** 任务终态 → 结果卡：成功/取消/失败都给出可读摘要，成功且同目录时给「打开原目录」。 */
export function batchOutcome(job) {
  const result = (job && job.result) || {};
  const counts = renameCounts(result);
  const outcome = baseOutcome(job);
  if (result.paused) {
    outcome.status = '已暂停';
    outcome.failed = false;
    outcome.summary = '已暂停：' + countText(counts);
    return outcome;
  }
  if (job && job.status === 'cancelled') {
    outcome.status = '已取消';
    // 取消时已完成的项保留新名，失败与未执行必须一起说清，不能只报「已完成 N」
    outcome.failed = false;
    const parts = [`已完成 ${counts.success}`];
    if (counts.failed) parts.push(`失败 ${counts.failed}`);
    parts.push(`未执行 ${counts.not_run}`);
    outcome.summary = '已取消：' + parts.join('，');
    return outcome;
  }
  if (job && job.status === 'success' && !job.error) {
    outcome.status = '成功';
    outcome.failed = false;
    const detail = countText(counts, { withZero: false });
    outcome.summary = '重命名完成：' + (detail || '成功 0');
    if (result.output_dir) {
      outcome.artifact = { label: '打开原目录', path: String(result.output_dir) };
    }
    return outcome;
  }
  if (job && (job.status === 'failed' || job.error)) {
    outcome.status = '失败';
    outcome.failed = true;
    const detail = countText(counts, { withZero: false });
    outcome.summary =
      '重命名未完成：' +
      (detail || '成功 0') +
      (job.error ? '；' + friendlyTaskError(job.error) : '');
  }
  return outcome;
}

// 跨页执行历史与页内结果共用同一份计数与文案，避免两处数字对不上
export function formatBatchHistory(result) {
  return countText(renameCounts(result), { withZero: false });
}

/** 结果行：状态标签、原名称、目标名称与原因；未执行与失败都保留原名便于交班核对。 */
export function resultRows(result) {
  const rows = result && Array.isArray(result.rows) ? result.rows : [];
  return rows.map((row, index) => {
    const data = row && typeof row === 'object' ? row : {};
    const key = String(data.result || 'not_run');
    return {
      key: `${data.path || ''}::${index}`,
      result: RESULT_TAG[key] ? key : 'not_run',
      name: String(data.name || ''),
      new_name: String(data.new_name || data.name || ''),
      message: String(data.message || data.reason || ''),
      status: RESULT_TAG[key] || RESULT_TAG.not_run,
    };
  });
}

export function restoreBatchParams(params = {}) {
  const data = params && typeof params === 'object' ? params : {};
  const defaults = createRules();
  const files = (Array.isArray(data.files) ? data.files : [])
    .filter(isPathLike)
    .map((item) => item.trim());
  const replace = Array.isArray(data.replace)
    ? data.replace
        .filter((pair) => pair && typeof pair === 'object' && typeof pair.find === 'string')
        .map((pair) => ({
          find: pair.find,
          replace: typeof pair.replace === 'string' ? pair.replace : '',
          case_sensitive: pair.case_sensitive === true,
        }))
    : defaults.replace;
  return {
    files,
    directory: typeof data.directory === 'string' ? data.directory.trim() : '',
    rules: {
      replace,
      prefix: typeof data.prefix === 'string' ? data.prefix : '',
      suffix: typeof data.suffix === 'string' ? data.suffix : '',
      keep_extension: data.keep_extension !== false,
      sequence: normalizeSequence(data.sequence),
      conflict: CONFLICT_MODES.includes(data.conflict) ? data.conflict : 'skip',
    },
  };
}
