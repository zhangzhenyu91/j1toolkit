// 题库纯逻辑：选项增删/清空后的答案字母重映射、乱序展示与原始答案双向映射、
// pagehide 监听清理。视图只消费本文件，node:test 直接导入，不加载 SFC。

export const OPTION_LETTERS = 'ABCDEF';

// normalizeLetters 规范化答案字母：大写、去重、按字母序输出。
function normalizeLetters(value) {
  const seen = new Set();
  String(value || '')
    .toUpperCase()
    .split('')
    .forEach((l) => {
      if (OPTION_LETTERS.indexOf(l) >= 0) seen.add(l);
    });
  return [...seen].sort().join('');
}

// answerAfterOptionDelete 删除第 index 个选项后的答案：
// 被删选项对应的字母移除；其后的字母整体上移一位（按原下标重映射）。
export function answerAfterOptionDelete(answer, index) {
  const i = Number(index);
  if (!Number.isInteger(i) || i < 0) return normalizeLetters(answer);
  return normalizeLetters(answer)
    .split('')
    .filter((l) => l !== OPTION_LETTERS[i])
    .map((l) => {
      const pos = OPTION_LETTERS.indexOf(l);
      return pos > i ? OPTION_LETTERS[pos - 1] : l;
    })
    .sort()
    .join('');
}

// answerAfterOptionClear 清空（文本置空）第 index 个选项后的答案：
// 只移除该选项字母，后续选项下标不变，因此不做位移。
export function answerAfterOptionClear(answer, index) {
  const i = Number(index);
  const letter = Number.isInteger(i) && i >= 0 ? OPTION_LETTERS[i] : '';
  return normalizeLetters(answer)
    .split('')
    .filter((l) => l !== letter)
    .join('');
}

// mapSelectedToOriginal 展示位字母 → 原始选项字母（提交答案用）。
// order[展示位] = 原始下标；无乱序（null）时原样返回。
export function mapSelectedToOriginal(selected, order) {
  const out = [];
  (selected || []).forEach((l) => {
    const displayIndex = OPTION_LETTERS.indexOf(String(l).toUpperCase());
    if (displayIndex < 0) return;
    const origIndex = Array.isArray(order) ? order[displayIndex] : displayIndex;
    if (!Number.isInteger(origIndex) || origIndex < 0) return;
    out.push(OPTION_LETTERS[origIndex]);
  });
  return normalizeLetters(out.join(''));
}

// mapOriginalToDisplay 原始答案字母 → 展示位字母（渲染「你选」与历史恢复用）。
// 无乱序（null）时原样返回。
export function mapOriginalToDisplay(original, order) {
  const out = [];
  String(original || '')
    .toUpperCase()
    .split('')
    .forEach((l) => {
      const origIndex = OPTION_LETTERS.indexOf(l);
      if (origIndex < 0) return;
      const displayIndex = Array.isArray(order) ? order.indexOf(origIndex) : origIndex;
      if (displayIndex < 0) return;
      out.push(OPTION_LETTERS[displayIndex]);
    });
  return normalizeLetters(out.join(''));
}

// compactOptionsWithAnswer 保存前压缩选项：trim 后丢弃空项，并按「旧下标 → 新下标」
// 重写答案字母（被丢弃的字母直接删除，后续字母随保留顺序下移）。
// 防止「甲/乙/丙/丁 清空乙后保存」把旧答案 AC 静默指向甲+丁。
export function compactOptionsWithAnswer(options, answer) {
  const list = Array.isArray(options) ? options : [];
  const cleaned = [];
  const mapping = {};
  list.forEach((text, oldIndex) => {
    const value = String(text == null ? '' : text).trim();
    if (!value) return;
    const oldLetter = OPTION_LETTERS[oldIndex];
    const newLetter = OPTION_LETTERS[cleaned.length];
    if (oldLetter && newLetter) mapping[oldLetter] = newLetter;
    cleaned.push(value);
  });
  const nextAnswer = normalizeLetters(answer)
    .split('')
    .map((l) => mapping[l] || '')
    .join('');
  return { options: cleaned, answer: nextAnswer };
}

// bindPagehide 绑定 pagehide 并返回幂等清理函数（组件卸载时必须解绑，避免旧实例继续补发断点）。
export function bindPagehide(target, handler) {
  if (!target || typeof target.addEventListener !== 'function') return () => {};
  target.addEventListener('pagehide', handler);
  let bound = true;
  return () => {
    if (!bound) return;
    bound = false;
    if (typeof target.removeEventListener === 'function') {
      target.removeEventListener('pagehide', handler);
    }
  };
}
