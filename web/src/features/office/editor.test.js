// ONLYOFFICE 编辑器销毁纯逻辑测试（node --test）：离开承载页必须销毁实例，销毁异常吞掉。
import assert from 'node:assert/strict';
import test from 'node:test';

import { destroyEditorSafely } from './editor.js';

test('销毁编辑器：调用一次 destroyEditor 并返回 true', () => {
  let calls = 0;
  const editor = {
    destroyEditor() {
      calls += 1;
    },
  };
  assert.equal(destroyEditorSafely(editor), true);
  assert.equal(calls, 1);
});

test('销毁编辑器：空实例返回 false 不抛错', () => {
  assert.equal(destroyEditorSafely(null), false);
  assert.equal(destroyEditorSafely({}), false);
});

test('销毁编辑器：实例内部异常不向外抛', () => {
  const editor = {
    destroyEditor() {
      throw new Error('oo internal');
    },
  };
  assert.equal(destroyEditorSafely(editor), false);
});
