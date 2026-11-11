// 启动防双击：按钮 disabled 挡不住「回车 / 快速连点」，启动流程含 await 预检，
// 需要同步占位；流程结束（成功或抛错）都必须释放，不能把页面锁死。
import test from 'node:test';
import assert from 'node:assert/strict';

import { createSingleFlight } from '../src/lib/single-flight.mjs';

test('同一时刻只执行一次启动流程，完成后可再次启动', async () => {
  const starter = createSingleFlight();
  let calls = 0;
  let release;
  const gate = new Promise((resolve) => {
    release = resolve;
  });
  const first = starter.run(async () => {
    calls += 1;
    await gate;
    return 'first';
  });
  const second = starter.run(async () => {
    calls += 1;
    return 'second';
  });
  assert.equal(starter.busy, true);
  assert.equal(await second, undefined);
  release();
  assert.equal(await first, 'first');
  assert.equal(calls, 1);
  assert.equal(starter.busy, false);
  assert.equal(await starter.run(async () => 'again'), 'again');
});

test('启动流程抛错后占位必须释放', async () => {
  const starter = createSingleFlight();
  await assert.rejects(() => starter.run(async () => Promise.reject(new Error('预检失败'))));
  assert.equal(starter.busy, false);
  assert.equal(await starter.run(async () => 'ok'), 'ok');
});
