// 弹层焦点圈定/归还决策测试（node --test）：焦点在面板外时正向/反向都要拉回；
// 下层弹层先关时不得抢回上层焦点。
import assert from 'node:assert/strict';
import test from 'node:test';

import { resolveTabTarget, shouldRestoreFocus } from './modal-behavior.js';

const panel = { id: 'panel' };
const first = { id: 'first' };
const last = { id: 'last' };
const outside = { id: 'outside' };
const list = [first, last];

test('焦点在面板外：正向 Tab 拉回第一项，反向 Tab 拉回最后一项', () => {
  assert.equal(resolveTabTarget({ list, current: outside, panel, shiftKey: false }), 'first');
  assert.equal(resolveTabTarget({ list, current: outside, panel, shiftKey: true }), 'last');
  assert.equal(resolveTabTarget({ list, current: null, panel, shiftKey: false }), 'first');
});

test('焦点在首项反向 / 末项正向时首尾循环', () => {
  assert.equal(resolveTabTarget({ list, current: first, panel, shiftKey: true }), 'last');
  assert.equal(resolveTabTarget({ list, current: last, panel, shiftKey: false }), 'first');
  assert.equal(resolveTabTarget({ list, current: first, panel, shiftKey: false }), null);
  assert.equal(resolveTabTarget({ list, current: last, panel, shiftKey: true }), null);
});

test('面板无可聚焦元素时焦点回到面板本身', () => {
  assert.equal(resolveTabTarget({ list: [], current: outside, panel, shiftKey: false }), 'panel');
});

test('焦点归还：自身为栈顶或已无更高层才归还，下层先关不抢上层焦点', () => {
  assert.equal(shouldRestoreFocus(true, 1), true, '栈顶关闭后归还到下层');
  assert.equal(shouldRestoreFocus(false, 1), false, '下层先关不得抢上层焦点');
  assert.equal(shouldRestoreFocus(false, 0), true, '最后一个弹层关闭必须归还触发元素');
});
