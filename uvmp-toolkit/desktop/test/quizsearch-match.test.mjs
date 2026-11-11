// 题库搜题匹配：覆盖率选优与短题命中（纯函数，node 直测）。
import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const { buildIndex, findBest, normalize } = require('../electron/quizsearch/match.cjs');

function q(content) {
  return { id: content, content, answer: 'A', type: 'single', options: [], analysis: '' };
}

test('归一化：全角转半角、去掉标点与空白、统一大写', () => {
  assert.equal(normalize('窗体 ＡＢＣ，１２３！'), '窗体ABC123');
  assert.equal(normalize('  检修（一班）\n 安规 '), '检修一班安规');
  assert.equal(normalize(''), '');
});

test('按题干覆盖率选最佳：短题不被长题按共字数挤掉', () => {
  // 长题命中 20 字但题干 60 字（覆盖率 0.33），短题命中 8 字、题干 8 字（覆盖率 1.0）
  const long = q(
    '安全生产法规定生产经营单位的主要负责人对本单位安全生产工作负有下列职责并履行职责'
  );
  const short = q('安全生产人人有责');
  const index = buildIndex([long, short]);
  const hit = findBest(index, '安全生产人人有责', 0.6);
  assert.ok(hit.question);
  assert.equal(hit.question.content, short.content);
  assert.ok(hit.score > 0.99);
});

test('短题（题干去重字符不足 6 个）也能命中', () => {
  const index = buildIndex([
    q('三违是指'),
    q('安全生产法规定生产经营单位应当建立健全全员安全生产责任制'),
  ]);
  const hit = findBest(index, '三违是指违章指挥违章作业违反劳动纪律', 0.6);
  assert.ok(hit.question, '短题不应被共字数下限挡掉');
  assert.equal(hit.question.content, '三违是指');
});

test('框内混入选项与界面杂字时，覆盖率仍能命中', () => {
  const index = buildIndex([
    q('我国安全生产工作的方针是'),
    q('安全生产工作应当以人为本坚持安全发展坚持安全第一预防为主综合治理'),
  ]);
  const text =
    '12. 我国安全生产工作的方针是？ A. 安全第一预防为主综合治理 B. 质量第一 C. 效率优先 下一题';
  const hit = findBest(index, text, 0.6);
  assert.ok(hit.question);
  assert.equal(hit.question.content, '我国安全生产工作的方针是');
});

test('阈值以下不硬给答案，但回传最接近项', () => {
  const index = buildIndex([q('我国安全生产工作的方针是安全第一预防为主综合治理')]);
  const hit = findBest(index, '完全不相关的天气与交通信息', 0.6);
  assert.equal(hit.question, null);
  assert.equal(hit.score, 0);
  assert.equal(hit.near, undefined);
});

test('部分命中但低于阈值：返回 near 供页面提示', () => {
  const index = buildIndex([q('电力安全工作规程规定高压设备上工作应填用第一种工作票')]);
  const hit = findBest(index, '电力安全工作规程规定高压设备上工作', 0.9);
  assert.equal(hit.question, null);
  assert.ok(hit.near);
  assert.ok(hit.score > 0.6 && hit.score < 0.9);
});

test('空索引与空文本安全返回', () => {
  assert.equal(findBest([], '任意题干', 0.6), null);
  const index = buildIndex([q('安全生产人人有责')]);
  assert.equal(findBest(index, '   ', 0.6), null);
});

test('超短题（2 字）不会因偶发同字给出 100% 假阳性', () => {
  const index = buildIndex([q('三违')]);
  const hit = findBest(index, '违章作业与违反劳动纪律', 0.6);
  assert.equal(hit.question, null);
});

test('覆盖率相同时取共字更多者（更具体的那道题）', () => {
  const index = buildIndex([q('安全生产'), q('安全生产人人有责')]);
  const hit = findBest(index, '安全生产人人有责的说法', 0.6);
  assert.equal(hit.question.content, '安全生产人人有责');
});

test('归一化保留常见中文标点范围外的汉字，丢掉 PUA 与谚文', () => {
  // 私用区（E000–F8FF）与谚文（AC00–D7AF）不属于题库用字符：必须过滤
  assert.equal(normalize('\uE000甲\uAC00乙'), '甲乙');
  assert.equal(normalize('\uF8FF\uD7AF'), '');
  // 正常中文（含扩展 A、兼容表意文字）仍保留
  assert.equal(normalize('㐀豈'), '㐀豈');
  assert.equal(normalize('𠀀'), '', '扩展 B 及以上的补充平面不在保留范围');
  assert.equal(normalize('安全生产法'), '安全生产法');
});

test('PUA/谚文字符混入题干不会误命中题库', () => {
  const index = buildIndex([q('安全生产人人有责')]);
  // 纯 PUA + 谚文杂字归一化后为空：按空文本处理，不给答案
  const hit = findBest(index, '\uE001\uE002\uAC01\uAC02', 0.6);
  assert.equal(hit, null);
  // 与正常题干混排时，杂字不计入共字，覆盖率不被抬高
  const mixed = findBest(index, '安全\uE001\uAC01生产人人有责', 0.6);
  assert.ok(mixed.question);
  assert.equal(mixed.score, 1);
});
