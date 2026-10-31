// 出工日志纯逻辑测试（node --test）：未出车判定、核验清单行（含「仅我」原因与备注）、
// 键盘编辑键、删除照片确认文案。不直接加载 SFC。
import assert from 'node:assert/strict';
import test from 'node:test';

import {
  deletePhotoConfirmText,
  entryAfterRefresh,
  isActivationKey,
  isNoCar,
  photoOpFeedback,
  photoOpPhase,
  photoOpStart,
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

test('删除照片任务有进行中、成功、失败与查询中断文案', () => {
  assert.deepEqual(photoOpFeedback('delete'), {
    text: '解除商旅关联中',
    success: '已删除',
    failure: '删除失败，请重试',
    timeout: '任务状态查询中断，请刷新确认是否已删除',
  });
  assert.equal(photoOpFeedback('resync').text, '商旅重新同步中');
  assert.match(photoOpFeedback('resync').timeout, /刷新确认同步结果/);
});

test('照片任务 unknown（不存在/过期/无权）先于 done 判为待确认，不得当成功', () => {
  // 后端新契约：任务不存在、过期或无权查看时 status/step 为 unknown，
  // 即便同时残留 done/ok 字段也不得按完成收尾。
  assert.equal(photoOpPhase({ step: 'done', status: 'ok' }), 'done');
  assert.equal(photoOpPhase({ step: 'unknown', status: 'unknown' }), 'unknown');
  assert.equal(photoOpPhase({ status: 'unknown' }), 'unknown');
  assert.equal(photoOpPhase({ step: 'unknown' }), 'unknown');
  assert.equal(photoOpPhase({ step: 'done', status: 'unknown' }), 'unknown');
  assert.equal(photoOpPhase({ step: 'unknown', status: 'ok' }), 'unknown');
  assert.equal(photoOpPhase({ step: 'failed', failed: true, status: 'unknown' }), 'unknown');
  assert.equal(photoOpPhase({ step: 'failed', failed: true }), 'failed');
  assert.equal(photoOpPhase({ failed: true }), 'failed');
  assert.equal(photoOpPhase({ step: 'running', status: 'running' }), 'running');
  assert.equal(photoOpPhase(null), 'running');
});

test('照片任务开始必须返回轮询入参，锁带 opId 与 photoId，已有他任务锁也不能丢弃收尾', () => {
  const task = photoOpStart({ opId: 7, entryId: 3, photoId: 9, kind: 'delete' });
  assert.deepEqual(task, {
    entryId: 3,
    opId: 7,
    kind: 'delete',
    lock: { opId: 7, photoId: 9, pct: 0, text: '解除商旅关联中' },
  });
  // 缺 opId / entryId 时不登记任务（同步删除或无效事件）。
  assert.equal(photoOpStart({ opId: 0, entryId: 3 }), null);
  assert.equal(photoOpStart({ opId: 7 }), null);
  assert.equal(photoOpStart(), null);
  // 未带 kind 的时间线按重新同步处理；无 photoId 时锁不写该字段。
  const resync = photoOpStart({ opId: 8, entryId: 3 });
  assert.equal(resync.kind, 'resync');
  assert.equal(resync.lock.opId, 8);
  assert.equal(resync.lock.text, '商旅重新同步中');
  assert.equal('photoId' in resync.lock, false);
});

test('日志刷新后照片抽屉换用新 entry，已删除照片不残留', () => {
  const oldEntry = { id: 3, photos: [{ id: 9 }] };
  const fresh = { id: 3, photos: [] };
  assert.equal(entryAfterRefresh([{ id: 2 }, fresh], oldEntry), fresh);
  assert.equal(entryAfterRefresh([{ id: 2 }], oldEntry), null);
  assert.equal(entryAfterRefresh(null, oldEntry), null);
  assert.equal(entryAfterRefresh([fresh], null), null);
});
