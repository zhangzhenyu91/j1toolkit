import test from 'node:test';
import assert from 'node:assert/strict';

import { createPreviewScheduler } from '../src/lib/preview-scheduler.mjs';

function fakeTimers() {
  let nextId = 1;
  const pending = new Map();
  return {
    setTimeout(fn, ms) {
      const id = nextId++;
      pending.set(id, { fn, ms });
      return id;
    },
    clearTimeout(id) {
      pending.delete(id);
    },
    get count() {
      return pending.size;
    },
    fire() {
      const entries = [...pending.values()];
      pending.clear();
      for (const entry of entries) entry.fn();
    },
  };
}

function setup(overrides = {}) {
  const timers = fakeTimers();
  const requests = [];
  const events = { preview: [], empty: [], errors: [], states: [] };
  const scheduler = createPreviewScheduler({
    delay: 400,
    setTimeout: timers.setTimeout,
    clearTimeout: timers.clearTimeout,
    request: (params) => {
      requests.push(params);
      return overrides.request
        ? overrides.request(params, requests.length)
        : Promise.resolve({ codes: [{ seq: 1, code: 'A1' }] });
    },
    onPreview: (result, meta) => events.preview.push({ result, meta }),
    onEmpty: (meta) => events.empty.push(meta),
    onError: (err, meta) => events.errors.push({ err, meta }),
    onState: (state) => events.states.push({ ...state }),
  });
  return { scheduler, timers, requests, events };
}

test('粘贴文本默认等待 400ms 防抖后才请求', async () => {
  const { scheduler, timers, requests, events } = setup();
  scheduler.setText('A1');
  assert.equal(requests.length, 0);
  assert.equal(timers.count, 1);
  assert.equal(scheduler.state.parsing, true);
  assert.equal(scheduler.state.pending, true);

  timers.fire();
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(requests, [{ codes_text: 'A1' }]);
  assert.equal(scheduler.state.parsing, false);
  assert.equal(events.preview.length, 1);
});

test('连续输入只请求最后一次，并丢弃迟到响应', async () => {
  const resolvers = [];
  const { scheduler, timers, requests, events } = setup({
    request: (params) => new Promise((resolve) => resolvers.push({ params, resolve })),
  });

  scheduler.setText('A1');
  scheduler.setText('A2');
  scheduler.setText('A3');
  assert.equal(timers.count, 1);
  timers.fire();
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(requests, [{ codes_text: 'A3' }]);

  // 新请求先完成，旧请求后完成也不得覆盖
  scheduler.parseNow();
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(requests, [{ codes_text: 'A3' }, { codes_text: 'A3' }]);
  const latest = resolvers[1];
  const stale = resolvers[0];
  latest.resolve({ codes: [{ seq: 1, code: 'NEW' }] });
  await new Promise((resolve) => setImmediate(resolve));
  stale.resolve({ codes: [{ seq: 1, code: 'OLD' }] });
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(events.preview.length, 1);
  assert.equal(events.preview[0].result.codes[0].code, 'NEW');
});

test('解析期间禁止开始，空结果明确回调', async () => {
  const { scheduler, timers, events } = setup({
    request: () => Promise.resolve({ codes: [] }),
  });
  scheduler.setText('A1');
  assert.equal(scheduler.state.parsing, true);
  timers.fire();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(scheduler.state.parsing, false);
  assert.equal(events.empty.length, 1);
  assert.equal(events.preview.length, 0);
});

test('手动解析会取消待执行的防抖并立即请求', async () => {
  const { scheduler, timers, requests } = setup();
  scheduler.setText('A1');
  assert.equal(timers.count, 1);
  const ok = scheduler.parseNow();
  assert.equal(ok, true);
  assert.equal(timers.count, 0);
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(requests, [{ codes_text: 'A1' }]);
});

test('选择 xlsx 后立即预览且不等待防抖；清空文本使迟到响应失效', async () => {
  const resolvers = [];
  const { scheduler, timers, requests, events } = setup({
    request: (params) => new Promise((resolve) => resolvers.push({ params, resolve })),
  });
  scheduler.previewFile({ xlsx_path: 'C:/tmp/x.xlsx' });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(timers.count, 0);
  assert.deepEqual(requests, [{ xlsx_path: 'C:/tmp/x.xlsx' }]);

  scheduler.setText('');
  assert.equal(scheduler.state.parsing, false);
  resolvers[0].resolve({ codes: [{ seq: 1, code: 'LATE' }] });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(events.preview.length, 0);
});

test('请求失败回调错误并解除解析状态', async () => {
  const { scheduler, events } = setup({
    request: () => Promise.reject(new Error('boom')),
  });
  scheduler.previewFile({ xlsx_path: 'bad.xlsx' });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(events.errors.length, 1);
  assert.equal(scheduler.state.parsing, false);
  assert.equal(events.errors[0].err.message, 'boom');
});
