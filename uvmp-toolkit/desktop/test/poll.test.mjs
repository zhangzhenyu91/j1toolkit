// 轮询失败计数：偶发一次失败不能停任务，连续失败到上限必须停轮询并告知，
// 否则「服务已挂」时界面会永远显示执行中或无限重试。
import test from 'node:test';
import assert from 'node:assert/strict';

import { createFailureTracker } from '../src/lib/poll.mjs';

test('连续失败到上限才判定耗尽，成功一次即清零', () => {
  const tracker = createFailureTracker(3);
  assert.equal(tracker.fail(), false);
  assert.equal(tracker.fail(), false);
  assert.equal(tracker.ok(), 0);
  assert.equal(tracker.count, 0);
  assert.equal(tracker.fail(), false);
  assert.equal(tracker.fail(), false);
  assert.equal(tracker.fail(), true);
  assert.equal(tracker.exhausted, true);
  tracker.reset();
  assert.equal(tracker.exhausted, false);
  assert.equal(tracker.count, 0);
});

test('非法上限按 1 处理，保证至少能停一次', () => {
  const tracker = createFailureTracker(0);
  assert.equal(tracker.limit, 1);
  assert.equal(tracker.fail(), true);
  const fallback = createFailureTracker();
  assert.equal(fallback.limit, 5);
});
