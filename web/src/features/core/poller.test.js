// 共享轮询器测试（node --test）：取消立即唤醒、离页终止不再请求、
// 连续失败指数退避且成功归零、总时限到点抛超时、任务自身致命错误直接上抛。
import assert from 'node:assert/strict';
import test from 'node:test';

import { createPoller } from './poller.js';

// manualClock 手动时钟 + 可控 sleep：每次 wait 记录毫秒并立即/按需推进时间。
function manualPoller(options = {}) {
  let now = 0;
  const waits = [];
  const poller = createPoller({
    now: () => now,
    sleep: async (ms) => {
      waits.push(ms);
      now += ms;
    },
    ...options,
  });
  return {
    poller,
    waits,
    advance: (ms) => (now += ms),
    get now() {
      return now;
    },
  };
}

test('轮询成功：任务返回 done 后停止，只请求一次', async () => {
  const { poller, waits } = manualPoller({ intervalMs: 500 });
  let calls = 0;
  const result = await poller.poll(async () => {
    calls += 1;
    return { done: true, value: 42 };
  });
  assert.equal(calls, 1);
  assert.equal(result.value, 42);
  assert.deepEqual(waits, []);
});

test('轮询未完成：按固定间隔继续，取消后立即停止且不再请求', async () => {
  const { poller, waits } = manualPoller({ intervalMs: 500 });
  let calls = 0;
  const loop = poller.poll(async () => {
    calls += 1;
    if (calls === 2) poller.cancel();
    return {};
  });
  const result = await loop;
  assert.equal(calls, 2);
  assert.equal(result.cancelled, true);
  assert.deepEqual(waits, [500]);
});

test('连续失败指数退避，成功一次后归零', async () => {
  const { poller, waits } = manualPoller({ intervalMs: 600, baseMs: 1000, maxMs: 8000 });
  let calls = 0;
  const result = await poller.poll(async () => {
    calls += 1;
    if (calls <= 3) throw new Error('网络抖动');
    return { done: true };
  });
  assert.deepEqual(waits, [2000, 4000, 8000]);
  assert.equal(result.done, true);
  assert.equal(poller.failures, 0);
});

test('总时限到点抛超时错误，错误码可识别', async () => {
  const { poller } = manualPoller({ intervalMs: 1000, limitMs: 2500 });
  await assert.rejects(
    poller.poll(async () => ({})),
    (err) => {
      assert.equal(err.code, 'poll_timeout');
      assert.match(err.message, /超时/);
      return true;
    }
  );
});

test('任务自身致命错误直接上抛（不重试）', async () => {
  const { poller } = manualPoller();
  const fatal = new Error('生成失败');
  fatal.code = 'job_failed';
  fatal.fatal = true;
  await assert.rejects(
    poller.poll(async () => {
      throw fatal;
    }),
    (err) => err === fatal
  );
});

test('cancel 唤醒正在等待的轮询，离页终止不留下悬挂 Promise', async () => {
  let releaseSleep;
  const poller = createPoller({
    intervalMs: 10000,
    sleep: () =>
      new Promise((resolve) => {
        releaseSleep = resolve;
      }),
  });
  let calls = 0;
  const loop = poller.poll(async () => {
    calls += 1;
    return {};
  });
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(calls, 1);
  poller.cancel();
  releaseSleep();
  const result = await loop;
  assert.equal(result.cancelled, true);
  assert.equal(calls, 1);
});

test('reset 重新计时并清空失败计数（新任务复用实例）', async () => {
  const { poller } = manualPoller({ intervalMs: 1000, limitMs: 1000 });
  await assert.rejects(poller.poll(async () => ({})));
  poller.reset();
  const result = await poller.poll(async () => ({ done: true }));
  assert.equal(result.done, true);
  assert.equal(poller.failures, 0);
});
