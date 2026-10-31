// 安全日记录模板纯逻辑：章节校验/排序、模板表单校验、默认模板与列表归一。
// 校验口径与计划一致（每套 1–12 段、docx ≤10MB），并与后端 go/internal/apps/safeday/templates.go
// 的 key 规则/保留字/标题上限保持同口径；前端只做提交前拦截，服务端仍为最终裁判。
// 本文件不依赖 Vue/网络，便于 node --test 直接断言。

export const BUILTIN_TEMPLATE_ID = 'builtin';
export const BUILTIN_TEMPLATE_NAME = '内置模板';
export const MAX_TEMPLATE_NAME = 64;
export const MAX_SECTIONS = 12;
export const MAX_SECTION_TITLE = 64;
export const MAX_DOCX_MB = 10;
export const MAX_DOCX_SIZE = MAX_DOCX_MB * 1024 * 1024;
// 自定义模板套数上限（与后端 templates.go maxTemplates 同口径；内置模板不计入）。
export const MAX_TEMPLATES = 50;

// 与后端回调信封字段冲突的保留 key（date/class/sections/url）。
export const RESERVED_SECTION_KEYS = ['date', 'class', 'sections', 'url'];

// 内置静态模板六段（仅用于新建动态模板时的章节初值，可增删改）。
export const BUILTIN_SECTIONS = Object.freeze([
  { key: 'accident_process', title: '客观还原事故经过' },
  { key: 'cause_analysis', title: '分层剖析事故原因' },
  { key: 'self_check', title: '对照自查举一反三' },
  { key: 'rectification_list', title: '制定整改清单' },
  { key: 'prevention_measures', title: '讨论防范措施' },
  { key: 'supervision_inspection', title: '定期督导检查防范措施落实情况和整改完成情况' },
]);

const KEY_RE = /^[a-z][a-z0-9_]{0,31}$/;

// sectionKeyError 章节标识校验（空串表示通过）。
export function sectionKeyError(key) {
  const k = String(key ?? '').trim();
  if (!k) return '章节标识不能为空';
  if (RESERVED_SECTION_KEYS.includes(k)) return `章节标识不能使用保留字：${k}`;
  if (!KEY_RE.test(k)) return '章节标识须小写字母开头，仅小写字母/数字/下划线，最多 32 字符';
  return '';
}

// validateSections 章节行整体校验：非空、段数上限、key 合法且不重复、标题非空且不超长。
export function validateSections(rows) {
  const list = Array.isArray(rows) ? rows.filter(Boolean) : [];
  if (!list.length) return '请至少添加一个章节';
  if (list.length > MAX_SECTIONS) return `最多 ${MAX_SECTIONS} 个章节`;
  const seen = new Set();
  for (const row of list) {
    const key = String(row.key ?? '').trim();
    const keyErr = sectionKeyError(key);
    if (keyErr) return keyErr;
    if (seen.has(key)) return `章节标识重复：${key}`;
    seen.add(key);
    const title = String(row.title ?? '').trim();
    if (!title) return `请填写章节标题：${key}`;
    if ([...title].length > MAX_SECTION_TITLE) {
      return `章节标题最多 ${MAX_SECTION_TITLE} 字：${key}`;
    }
  }
  return '';
}

// validateDocxFile docx 文件校验（扩展名/空文件/大小）。
export function validateDocxFile(file) {
  if (!file) return '请上传模板 docx 文件';
  if (!/\.docx$/i.test(String(file.name || ''))) return '仅支持 .docx 文件';
  const size = Number(file.size || 0);
  if (size <= 0) return '模板文件为空';
  if (size > MAX_DOCX_SIZE) return `模板文件不能超过 ${MAX_DOCX_MB}MB`;
  return '';
}

// validateTemplateForm 新建/编辑表单校验（返回首个错误，空串表示通过）。
// 新建：名称/章节/API Key/docx 均必填；编辑：API Key 与 docx 留空表示保持原值。
export function validateTemplateForm({ name, rows, apiKey, isNew, docx } = {}) {
  const cleanName = String(name ?? '').trim();
  if (!cleanName) return '请输入模板名称';
  if ([...cleanName].length > MAX_TEMPLATE_NAME) return `模板名称最多 ${MAX_TEMPLATE_NAME} 字`;
  const sectionErr = validateSections(rows);
  if (sectionErr) return sectionErr;
  const key = String(apiKey ?? '').trim();
  if (isNew && !key) return '请填写 Dify API Key';
  if (key && (/[\r\n]/.test(key) || key.length > 512)) return 'Dify API Key 格式不正确';
  if (isNew) return validateDocxFile(docx);
  return docx ? validateDocxFile(docx) : '';
}

// templateCreateLimitError 新建模板套数拦截（内置模板不计入；空串表示允许新建）。
export function templateCreateLimitError(templates) {
  const list = Array.isArray(templates) ? templates.filter(Boolean) : [];
  const custom = list.filter((t) => t.mode !== 'static').length;
  if (custom >= MAX_TEMPLATES) {
    return `自定义模板已达上限（最多 ${MAX_TEMPLATES} 套），请先删除不再使用的模板后再新建`;
  }
  return '';
}

// moveSection 章节上移/下移（delta = -1 / 1），返回新数组；越界原样返回副本。
export function moveSection(rows, index, delta) {
  const list = Array.isArray(rows) ? rows.slice() : [];
  const from = Number(index);
  const to = from + Number(delta);
  if (!Number.isInteger(from) || from < 0 || from >= list.length) return list;
  if (!Number.isInteger(to) || to < 0 || to >= list.length) return list;
  const [item] = list.splice(from, 1);
  list.splice(to, 0, item);
  return list;
}

// addSectionRow 追加空白章节行，返回新数组。
export function addSectionRow(rows) {
  const list = Array.isArray(rows) ? rows.slice() : [];
  return list.concat({ key: '', title: '' });
}

// removeSectionRow 删除指定章节行，返回新数组。
export function removeSectionRow(rows, index) {
  const list = Array.isArray(rows) ? rows.slice() : [];
  const at = Number(index);
  if (!Number.isInteger(at) || at < 0 || at >= list.length) return list;
  list.splice(at, 1);
  return list;
}

// templatePayload 章节行转为提交体（去 uid、去空白、保持顺序）。
export function templatePayload(rows) {
  return (Array.isArray(rows) ? rows : []).map((row) => ({
    key: String(row && row.key ? row.key : '').trim(),
    title: String(row && row.title ? row.title : '').trim(),
  }));
}

// resolveTemplateId 生效模板：优先当前选择（仍存在），其次默认模板，最后内置模板。
export function resolveTemplateId(templates, preferred) {
  const list = Array.isArray(templates) ? templates.filter(Boolean) : [];
  const wanted = String(preferred ?? '').trim();
  if (wanted && list.some((t) => t.id === wanted)) return wanted;
  const fallback = list.find((t) => t.isDefault && t.id);
  if (fallback) return fallback.id;
  if (list.some((t) => t.id === BUILTIN_TEMPLATE_ID)) return BUILTIN_TEMPLATE_ID;
  if (list.length && list[0].id) return list[0].id;
  return BUILTIN_TEMPLATE_ID;
}

// normalizeTemplateList 归一模板列表响应：templates 数组 + 有效 defaultId（缺失回落内置）。
export function normalizeTemplateList(payload) {
  const templates = Array.isArray(payload && payload.templates)
    ? payload.templates.filter(Boolean)
    : [];
  let defaultId = String((payload && payload.defaultId) || '').trim();
  if (!defaultId || !templates.some((t) => t.id === defaultId)) {
    const marked = templates.find((t) => t.isDefault && t.id);
    if (marked) defaultId = marked.id;
    else if (templates.some((t) => t.id === BUILTIN_TEMPLATE_ID)) defaultId = BUILTIN_TEMPLATE_ID;
    else defaultId = (templates[0] && templates[0].id) || BUILTIN_TEMPLATE_ID;
  }
  return { templates, defaultId };
}
