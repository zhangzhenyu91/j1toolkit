// 安全日用户模板纯逻辑：章节校验/排序、表单校验、默认模板与列表归一。
// 与后端 templates.go 同口径；前端只做提交前拦截，服务端仍为最终裁判。

export const MAX_TEMPLATE_NAME = 64;
export const MAX_SECTIONS = 12;
export const MAX_SECTION_TITLE = 64;
export const MAX_TEMPLATES = 50;

export const RESERVED_SECTION_KEYS = ['date', 'class', 'sections', 'url', 'record_id', 'batch_id'];

// 新建表单的可编辑草稿，不是内置模板，也不会自动持久化。
export const DEFAULT_SECTION_DRAFT = Object.freeze([
  { key: 'accident_process', title: '一、客观还原事故经过' },
  { key: 'cause_analysis', title: '二、分层剖析事故原因' },
  { key: 'self_check', title: '三、对照自查举一反三' },
  { key: 'rectification_list', title: '四、制定整改清单' },
  { key: 'prevention_measures', title: '五、讨论防范措施' },
  { key: 'supervision_inspection', title: '六、定期督导检查防范措施落实情况和整改完成情况' },
]);

const KEY_RE = /^[a-z][a-z0-9_]{0,31}$/;

export function sectionKeyError(key) {
  const k = String(key ?? '').trim();
  if (!k) return '章节标识不能为空';
  if (RESERVED_SECTION_KEYS.includes(k)) return `章节标识不能使用保留字：${k}`;
  if (!KEY_RE.test(k)) return '章节标识须小写字母开头，仅小写字母/数字/下划线，最多 32 字符';
  return '';
}

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
    if ([...title].length > MAX_SECTION_TITLE)
      return `章节标题最多 ${MAX_SECTION_TITLE} 字：${key}`;
  }
  return '';
}

export function validateTemplateForm({ name, rows, apiKey, isNew } = {}) {
  const cleanName = String(name ?? '').trim();
  if (!cleanName) return '请输入模板名称';
  if ([...cleanName].length > MAX_TEMPLATE_NAME) return `模板名称最多 ${MAX_TEMPLATE_NAME} 字`;
  const sectionErr = validateSections(rows);
  if (sectionErr) return sectionErr;
  const key = String(apiKey ?? '').trim();
  if (isNew && !key) return '请填写 Dify API Key';
  if (key && (/[\r\n]/.test(key) || key.length > 512)) return 'Dify API Key 格式不正确';
  return '';
}

export function templateCreateLimitError(templates) {
  const list = Array.isArray(templates) ? templates.filter(Boolean) : [];
  if (list.length >= MAX_TEMPLATES) {
    return `模板已达上限（最多 ${MAX_TEMPLATES} 套），请先删除不再使用的模板后再新建`;
  }
  return '';
}

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

export function addSectionRow(rows) {
  const list = Array.isArray(rows) ? rows.slice() : [];
  return list.concat({ key: '', title: '' });
}

export function removeSectionRow(rows, index) {
  const list = Array.isArray(rows) ? rows.slice() : [];
  const at = Number(index);
  if (!Number.isInteger(at) || at < 0 || at >= list.length) return list;
  list.splice(at, 1);
  return list;
}

export function templatePayload(rows) {
  return (Array.isArray(rows) ? rows : []).map((row) => ({
    key: String(row && row.key ? row.key : '').trim(),
    title: String(row && row.title ? row.title : '').trim(),
  }));
}

// 生效模板：当前选择仍存在优先，其次用户默认和首个用户模板；零模板返回空。
export function resolveTemplateId(templates, preferred) {
  const list = Array.isArray(templates) ? templates.filter(Boolean) : [];
  const wanted = String(preferred ?? '').trim();
  if (wanted && list.some((t) => t.id === wanted)) return wanted;
  const fallback = list.find((t) => t.isDefault && t.id);
  if (fallback) return fallback.id;
  return list.length && list[0].id ? list[0].id : '';
}

export function normalizeTemplateList(payload) {
  const templates = Array.isArray(payload && payload.templates)
    ? payload.templates.filter(Boolean)
    : [];
  let defaultId = String((payload && payload.defaultId) || '').trim();
  if (!defaultId || !templates.some((t) => t.id === defaultId)) {
    const marked = templates.find((t) => t.isDefault && t.id);
    defaultId = marked ? marked.id : (templates[0] && templates[0].id) || '';
  }
  return { templates, defaultId };
}

export function templateSubmitError({
  metaReady,
  templateLoading,
  templateError,
  templateId,
} = {}) {
  if (!metaReady) return '表单数据加载中，请稍候';
  if (templateLoading) return '模板加载中，请稍候';
  if (templateError) return String(templateError);
  if (!templateId) return '请先创建并选择记录模板';
  return '';
}
