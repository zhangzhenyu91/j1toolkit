// 出工日志纯逻辑测试（node --test）：未出车判定、核验清单行（含「仅我」原因与备注）、
// 键盘编辑键、删除照片确认文案。不直接加载 SFC。
import assert from 'node:assert/strict';
import test from 'node:test';

import {
  deletePhotoConfirmText,
  isActivationKey,
  isNoCar,
  reportRowChips,
  reportRows,
} from './logic.js';

test('vehicle_id=0 与 null 均为未出车（站内作业）', () => {
  assert.equal(isNoCar(0), true);
  assert.equal(isNoCar('0'), true);
  assert.equal(isNoCar(null), true);
  assert.equal(isNoCar(undefined), true);
  assert.equal(isNoCar(1), false);
  assert.equal(isNoCar('12'), false);
});

test('核验清单保留仅我原因行与有备注行，不再只筛 verify=failed', () => {
  const list = [
    { id: 1, verify: 'failed', reasons: ['未选择目的地'], remark: '' },
    {
      id: 2,
      verify: 'failed',
      reasons: [],
      remark: '已口头说明',
      remark_has_files: false,
    },
    { id: 3, verify: 'passed', reasons: ['我的费用信息未填写或不达标'], remark: '' },
    { id: 4, verify: 'passed', reasons: [], remark: '备注：天气原因', remark_has_files: true },
  ];
  assert.deepEqual(
    reportRows(list).map((it) => it.id),
    [1, 2, 3, 4]
  );
  assert.deepEqual(reportRowChips(list[1]), ['有备注']);
  assert.deepEqual(reportRowChips(list[2]), ['我的费用信息未填写或不达标']);
  assert.deepEqual(reportRowChips(list[3]), ['有备注']);
  assert.deepEqual(reportRowChips({ verify: 'failed' }), ['未通过']);
  // 整体通过、无原因也无备注：不得谎报「有备注」。
  assert.deepEqual(reportRowChips({ verify: 'passed', reasons: [], remark: '' }), []);
  assert.deepEqual(reportRowChips({ verify: 'passed' }), []);
});

test('键盘编辑仅响应 Enter / Space，不劫持普通按键', () => {
  for (const key of ['Enter', ' ', 'Spacebar']) assert.equal(isActivationKey(key), true, key);
  for (const key of ['a', 'Tab', 'Escape', 'ArrowDown']) {
    assert.equal(isActivationKey(key), false, key);
  }
});

test('删除照片确认文案带序号与所属人，空照片不抛错', () => {
  assert.match(deletePhotoConfirmText({ id: 9, members: ['张三', '李四'] }), /张三、李四/);
  assert.match(deletePhotoConfirmText({ id: 9 }), /删除这张照片/);
  assert.match(deletePhotoConfirmText(null), /删除这张照片/);
});
