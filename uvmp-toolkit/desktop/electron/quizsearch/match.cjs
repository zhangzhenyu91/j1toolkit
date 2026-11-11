// 题库搜题：OCR 文本与题库的模糊匹配（纯函数，无 Electron 依赖，可 node 直测）。
// 算法移植自嗖嗖搜题 TextSearchUtilities.cs 的「共同字符数」，并做三点改进：
//   1. 归一化：全角转半角后白名单过滤（仅留中日韩文字+字母+数字），OCR 误插的空格/标点不再干扰；
//   2. 选优：按「题干覆盖率」挑最佳（命中字符数 / 题干去重字符数），而非原始共字数——
//      扫描框常同时框住选项/邻题/界面杂字，共字数会系统性偏向长题，短题永远选不中；
//   3. 阈值兜底：覆盖率低于阈值判「未匹配」，修复原软件「不中也硬显示最佳猜测」的误导。
'use strict';

// 保留字符：CJK 统一表意文字（基本区 + 扩展 A + 兼容表意文字）、字母、数字。
// 兼容表意文字段（F900–FAFF）不是连续区间：F900–FA6D 才是汉字，FA6E–FA6F 未分配、
// FA70–FAD9 是汉字、FADA–FAFF 未分配。之前笼统写成「豈-﫿」（8C48–FAFF）会把
// E000–F8FF 私用区与 AC00–D7AF 谚文一并放行，OCR 杂字就能伪造共字。
const KEEP_RE = /[0-9A-Za-z\u3400-\u4DBF\u4E00-\u9FFF\uF900-\uFA6D\uFA70-\uFAD9]/;

function normalize(text) {
  const s = String(text || '');
  let out = '';
  for (const ch of s) {
    let code = ch.codePointAt(0);
    if (code >= 0xff01 && code <= 0xff5e) code -= 0xfee0; // 全角 ASCII/数字 → 半角
    if (code === 0x3000) continue; // 全角空格
    const c = String.fromCodePoint(code);
    if (KEEP_RE.test(c)) out += c;
  }
  return out.toUpperCase();
}

// 字符串 → 去重字符 Set
function charSet(text) {
  return new Set(text);
}

// 为一批题目建匹配索引：每题预存归一化题干与字符集
// questions: [{id, content, ...}] → [{q, norm, chars, need}]
// need：最低共字门槛——短题按比例放宽（题干 4 字只需 3 字命中），长题仍要求 6 字起步；
// 下限 3 字挡住「2 字题干被偶发同字命中且覆盖率 100%」的假阳性。
function buildIndex(questions) {
  const index = [];
  for (const q of questions) {
    const norm = normalize(q.content);
    if (!norm) continue;
    const chars = charSet(norm);
    index.push({
      q,
      norm,
      chars,
      need: Math.min(6, Math.max(3, Math.ceil(chars.size * 0.6))),
    });
  }
  return index;
}

/**
 * 在索引中找最佳匹配。
 * @param index   buildIndex 产物
 * @param ocrText OCR 原始文本（内部归一化）
 * @param threshold 命中阈值（0~1，默认 0.6）
 * @returns {question, score, common, norm}；低于阈值返回 {question:null, near} 供页面提示
 */
function findBest(index, ocrText, threshold = 0.6) {
  const norm = normalize(ocrText);
  if (!norm || !Array.isArray(index) || index.length === 0) return null;
  const ocrChars = charSet(norm);
  let best = null;
  let bestScore = 0;
  let bestCommon = 0;
  for (const item of index) {
    let common = 0;
    // 遍历较小集合，O(n)
    const [small, big] =
      ocrChars.size <= item.chars.size ? [ocrChars, item.chars] : [item.chars, ocrChars];
    for (const ch of small) if (big.has(ch)) common++;
    if (common < 2 || common < item.need) continue;
    const score = common / item.chars.size;
    // 覆盖率相同（浮点等值）时取共字更多者：更具体的那道题
    if (score > bestScore + 1e-9 || (Math.abs(score - bestScore) <= 1e-9 && common > bestCommon)) {
      best = item;
      bestScore = score;
      bestCommon = common;
    }
  }
  if (!best) return { question: null, score: 0, common: 0, norm };
  if (bestScore < threshold) {
    return { question: null, score: bestScore, common: bestCommon, norm, near: best.q };
  }
  return { question: best.q, score: bestScore, common: bestCommon, norm };
}

module.exports = { normalize, buildIndex, findBest };
