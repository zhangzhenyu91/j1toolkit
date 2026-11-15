// 就地重命名：文件名校验、快捷短语校验与错误码到五类反馈的映射。
// 只改主名、扩展名固定；冲突绝不覆盖，失败保留输入内容。

export const DEFAULT_PHRASES = ['_已核验', '_2026秋检', '_宣传素材', '_终稿'];
export const MAX_PHRASES = 12;
export const MAX_PHRASE_LEN = 40;
export const MAX_BASE_LEN = 120;

/**
 * 配置里的快捷短语：空数组是合法持久状态（用户主动删光了），必须原样保留；
 * 只有缺字段 / 非数组（读取失败、旧配置）才回落默认。
 */
export function phrasesFromConfig(value) {
  return Array.isArray(value) ? value.slice() : DEFAULT_PHRASES.slice();
}

// 与壳层/核心一致的文件名非法字符（含控制字符）
const ILLEGAL_NAME = /[\\/:*?"<>|\u0000-\u001f]/;

/** 校验主文件名：返回 null 表示可用，否则返回就地反馈的标题与说明。 */
export function validateBaseName(name) {
  const text = String(name == null ? '' : name);
  if (!text.trim()) {
    return {
      title: '主文件名为空',
      body: '至少输入 1 个可见字符；扩展名会自动保留，不会变成只有扩展名的文件。',
    };
  }
  if (ILLEGAL_NAME.test(text)) {
    return {
      title: '名称含非法字符',
      body: '名称不能包含 \\ / : * ? " < > |，也不能以空格或句点结尾。',
    };
  }
  if (text !== text.trim()) {
    return {
      title: '名称不能以空格开头或结尾',
      body: '去掉首尾空格后重试；扩展名会自动保留。',
    };
  }
  if (text.endsWith('.')) {
    return {
      title: '名称不能以句点结尾',
      body: '句点只用于分隔扩展名；去掉末尾句点后重试。',
    };
  }
  if (text.length > MAX_BASE_LEN) {
    return {
      title: '名称过长',
      body: `主文件名最多 ${MAX_BASE_LEN} 个字符，请缩短后重试。`,
    };
  }
  return null;
}

/** 校验快捷短语：返回 null 表示可用，否则返回一句可直接展示的错误。 */
export function validatePhrase(text, existing = []) {
  const value = String(text == null ? '' : text).trim();
  if (!value) return '快捷短语不能为空';
  if (value.length > MAX_PHRASE_LEN) return `单条短语最多 ${MAX_PHRASE_LEN} 字`;
  if (ILLEGAL_NAME.test(value)) return '快捷短语不能包含 \\ / : * ? " < > | 等文件名非法字符';
  if (existing.some((item) => item === value)) return '已有同名短语';
  return null;
}

/** 校验整份短语列表（设置页保存前）。 */
export function validatePhrases(list) {
  const items = Array.isArray(list) ? list : [];
  if (items.length > MAX_PHRASES) return `快捷短语最多 ${MAX_PHRASES} 条`;
  const seen = [];
  for (let i = 0; i < items.length; i += 1) {
    const error = validatePhrase(items[i], seen);
    if (error) return `第 ${i + 1} 条：${error}`;
    seen.push(String(items[i]).trim());
  }
  return null;
}

const FEEDBACK = {
  IMG_RENAME_CONFLICT: {
    title: '同名文件已存在',
    body: '未做任何修改。请换一个名称或先处理同名文件。',
  },
  FILE_NOT_FOUND: {
    title: '文件已被删除',
    body: '保存时原文件已不在目录中，无法改名；输入内容已保留，可复制名称后重新打开其他图片。',
  },
  IMG_RENAME_DENIED: {
    title: '文件只读或被占用',
    body: '该文件在系统中为只读，或被其他程序打开；请关闭后重试，或先取消只读属性。',
  },
  IMG_RENAME_FAILED: {
    title: '改名失败',
    body: '系统拒绝了这次改名，原文件保持不变；请稍后重试或换一个名称。',
  },
  IMG_RENAME_ENGINE: {
    title: '改名服务不可用',
    body: '当前核心缺少通用改名引擎，请重启客户端后再试。',
  },
  PARAM_INVALID: {
    title: '名称不符合要求',
    body: '请检查文件名是否为空、是否含非法字符，或是否以空格/句点结尾。',
  },
};

/** 错误码 + 核心原文 → 面板内的就地反馈（右对齐反馈块，不弹模态框）。 */
export function renameFeedback(code, message) {
  const base = FEEDBACK[code] || {
    title: '改名未完成',
    body: '改名没有成功，原文件保持不变；请重试或换一个名称。',
  };
  const detail = String(message == null ? '' : message).trim();
  if (!detail) return { title: base.title, body: base.body };
  return { title: base.title, body: `${detail}；${base.body}` };
}

/** 从 IPC 错误文本里取核心错误码（如 IMG_RENAME_CONFLICT）；取不到返回空串。 */
export function parseErrorCode(error) {
  const text = String((error && (error.message || error)) || '');
  const matched = /\b([A-Z][A-Z0-9_]{2,})\b/.exec(text);
  return matched ? matched[1] : '';
}

/** 主文件名（去掉扩展名），用于重命名输入框初值。 */
export function baseNameOf(fileName) {
  const text = String(fileName == null ? '' : fileName);
  const at = text.lastIndexOf('.');
  if (at <= 0) return text;
  return text.slice(0, at);
}

/** 输入的主名与当前文件名的主名是否相同（忽略首尾空格；扩展名不参与）。 */
export function isSameBaseName(fileName, nextBase) {
  return baseNameOf(fileName) === String(nextBase == null ? '' : nextBase).trim();
}

/** 扩展名（含点号，小写）；无扩展名返回空串。 */
export function extNameOf(fileName) {
  const text = String(fileName == null ? '' : fileName);
  const at = text.lastIndexOf('.');
  if (at <= 0 || at === text.length - 1) return '';
  return text.slice(at).toLowerCase();
}
