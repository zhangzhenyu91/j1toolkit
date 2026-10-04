// 题库搜题：OCR 文本与题库的模糊匹配（纯函数，无 Electron 依赖，可 node 直测）。
// 算法移植自嗖嗖搜题 TextSearchUtilities.cs 的「共同字符数」，并做两点改进：
//   1. 归一化：全角转半角后白名单过滤（仅留中日韩文字+字母+数字），OCR 误插的空格/标点不再干扰；
//   2. 阈值兜底：得分 = 命中字符数 / 题干去重字符数（题干覆盖率，抗框内杂字稀释），
//      低于阈值判「未匹配」，修复原软件「不中也硬显示最佳猜测」的误导。
'use strict';

// 保留字符：CJK 统一表意文字（含扩展A与兼容字）、字母、数字
const KEEP_RE = /[0-9A-Za-z一-鿿㐀-䶿豈-﫿]/;

function normalize(text) {
  const s = String(text || '');
  let out = '';
  for (const ch of s) {
    let code = ch.codePointAt(0);
    if (code >= 0xff01 && code <= 0xff5e) code -= 0xfee0; // 全角 ASCII/数字 → 半角
    if (code === 0x3000) continue;                          // 全角空格
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
// questions: [{id, content, ...}] → [{q, norm, chars}]
function buildIndex(questions) {
  const index = [];
  for (const q of questions) {
    const norm = normalize(q.content);
    if (!norm) continue;
    index.push({ q, norm, chars: charSet(norm) });
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
  if (!norm) return null;
  const ocrChars = charSet(norm);
  let best = null;
  let bestCommon = 0;
  for (const item of index) {
    let common = 0;
    // 遍历较小集合，O(n)
    const [small, big] = ocrChars.size <= item.chars.size ? [ocrChars, item.chars] : [item.chars, ocrChars];
    for (const ch of small) if (big.has(ch)) common++;
    if (common > bestCommon) {
      bestCommon = common;
      best = item;
    }
  }
  if (!best || bestCommon < 6) return { question: null, score: 0, common: bestCommon, norm };
  // 覆盖率口径：命中字符数 / 题干去重字符数。扫描框常同时框住选项/邻题/界面杂字，
  // 用题干覆盖率（而非原软件的原始共字数）可抗杂字稀释，且短题不会被长题挤掉。
  const score = bestCommon / best.chars.size;
  if (score < threshold) return { question: null, score, common: bestCommon, norm, near: best.q };
  return { question: best.q, score, common: bestCommon, norm };
}

module.exports = { normalize, buildIndex, findBest };
