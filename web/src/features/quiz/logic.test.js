// 题库纯逻辑测试（node --test）：删/清选项后答案按原下标重映射、乱序作答恢复映射、
// pagehide 监听清理。不直接加载 SFC。
import assert from 'node:assert/strict';
import test from 'node:test';

import {
  answerAfterOptionClear,
  answerAfterOptionDelete,
  bindPagehide,
  compactOptionsWithAnswer,
  mapOriginalToDisplay,
  mapSelectedToOriginal,
} from './logic.js';

test('删除选项：被删字母移除，后续字母按原下标上移', () => {
  // A B C D，答案 C/D，删除 B（index=1）→ C→B、D→C
  assert.equal(answerAfterOptionDelete('CD', 1), 'BC');
  // 答案包含被删字母本身 → 移除；原 C 上移为 B（如删除 B 前选 A/C，结果为 A/B）
  assert.equal(answerAfterOptionDelete('ABC', 1), 'AB');
  // 最后一个选项删除只移除自身
  assert.equal(answerAfterOptionDelete('CD', 3), 'C');
  // 首位删除：B→A、C→B、D→C
  assert.equal(answerAfterOptionDelete('BCD', 0), 'ABC');
});

test('清空选项：只移除该选项字母，后续字母下标不变', () => {
  assert.equal(answerAfterOptionClear('AC', 2), 'A');
  assert.equal(answerAfterOptionClear('ABC', 1), 'AC');
  assert.equal(answerAfterOptionClear('A', 1), 'A');
  assert.equal(answerAfterOptionClear('', 0), '');
});

test('保存压缩空选项：答案按旧下标→新下标映射，不能静默指向别的选项', () => {
  // 甲/乙/丙/丁，清空乙（B）后保存：旧 A/C 应变成新 A/B（甲+丙），而不是甲+丁。
  assert.deepEqual(compactOptionsWithAnswer(['甲', '', '丙', '丁'], 'AC'), {
    options: ['甲', '丙', '丁'],
    answer: 'AB',
  });
  // 答案本身指向被清空项：丢弃该字母
  assert.deepEqual(compactOptionsWithAnswer(['甲', ' ', '丙'], 'B'), {
    options: ['甲', '丙'],
    answer: '',
  });
  // 连续空项、首尾空项都按保留顺序压缩
  assert.deepEqual(compactOptionsWithAnswer(['', '甲', '', '乙', '  '], 'BD'), {
    options: ['甲', '乙'],
    answer: 'AB',
  });
  // 全空：返回空选项与空答案，交由保存校验提示
  assert.deepEqual(compactOptionsWithAnswer(['', '  '], 'A'), { options: [], answer: '' });
});

test('乱序作答恢复：展示字母 ↔ 原始字母双向映射', () => {
  // 展示位 0/1/2 对应原选项 2/0/1
  const order = [2, 0, 1];
  assert.equal(mapSelectedToOriginal(['B', 'C'], order), 'AB');
  // 原始 A/B 在展示顺序里的位置是 B/C
  assert.equal(mapOriginalToDisplay('AB', order), 'BC');
  // 无乱序时原样
  assert.equal(mapSelectedToOriginal(['A', 'B'], null), 'AB');
  assert.equal(mapOriginalToDisplay('AB', null), 'AB');
  // 非法字母忽略
  assert.equal(mapSelectedToOriginal(['Z'], order), '');
});

test('pagehide 监听可清理且重复清理无副作用', () => {
  const calls = [];
  const target = {
    addEventListener(type, fn) {
      calls.push(['add', type, fn]);
    },
    removeEventListener(type, fn) {
      calls.push(['remove', type, fn]);
    },
  };
  const handler = () => {};
  const unbind = bindPagehide(target, handler);
  assert.deepEqual(calls[0], ['add', 'pagehide', handler]);
  unbind();
  assert.deepEqual(calls[1], ['remove', 'pagehide', handler]);
  unbind();
  assert.equal(calls.length, 2);
});
