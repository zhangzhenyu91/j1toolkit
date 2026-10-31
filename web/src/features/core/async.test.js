// 请求代际 / 轮询退避纯逻辑测试（node --test）。
import assert from 'node:assert/strict';
import test from 'node:test';

import { createGeneration, createResourceGates, pollDelay, pollExpired } from './async.js';

test('请求代际：只有最新一代响应可落地', () => {
  const gen = createGeneration();
  const first = gen.next();
  const second = gen.next();
  assert.equal(gen.isCurrent(first), false);
  assert.equal(gen.isCurrent(second), true);
});

test('请求代际：invalidate 作废在途请求', () => {
  const gen = createGeneration();
  const token = gen.next();
  gen.invalidate();
  assert.equal(gen.isCurrent(token), false);
  assert.equal(gen.isCurrent(gen.next()), true);
});

test('轮询退避：无失败用基础间隔，连续失败指数增长并封顶', () => {
  assert.equal(pollDelay(0), 1000);
  assert.equal(pollDelay(1), 2000);
  assert.equal(pollDelay(2), 4000);
  assert.equal(pollDelay(3), 8000);
  assert.equal(pollDelay(4), 10000);
  assert.equal(pollDelay(20, { baseMs: 1500, maxMs: 15000 }), 15000);
});

test('轮询总时限：到点即超时，关闭时限时不超时', () => {
  assert.equal(pollExpired(1000, 4000, 3000), true);
  assert.equal(pollExpired(1000, 3999, 3000), false);
  assert.equal(pollExpired(1000, 10 ** 9, 0), false);
});

test('双 emit 组合：month 与 logs 独立代际，先 logs 后 month 的异步响应都能落地', async () => {
  const gates = createResourceGates(['month', 'logs']);
  const applied = [];
  async function loadMonth(token, delay) {
    await new Promise((r) => setTimeout(r, delay));
    if (gates.isCurrent(token)) applied.push('month');
  }
  async function loadLogs(token, delay) {
    await new Promise((r) => setTimeout(r, delay));
    if (gates.isCurrent(token)) applied.push('logs');
  }
  // MonthCalendar shiftMonth 先 emit month 再 emit selected：两个监听分别取自己的票据。
  const monthTicket = gates.next('month');
  const logsTicket = gates.next('logs');
  // 模拟月历请求慢、日志请求快：month 响应后到也必须能刷新 dayStatus/stats。
  await Promise.all([loadMonth(monthTicket, 20), loadLogs(logsTicket, 1)]);
  assert.deepEqual(applied.sort(), ['logs', 'month']);
});

test('同资源连续请求：后发请求作废先发请求', () => {
  const gates = createResourceGates(['logs']);
  const first = gates.next('logs');
  const second = gates.next('logs');
  assert.equal(gates.isCurrent(first), false);
  assert.equal(gates.isCurrent(second), true);
});

test('invalidateAll：切班组/范围时一次作废全部资源，只保留随后新票据', () => {
  const gates = createResourceGates(['month', 'logs', 'report', 'photos']);
  const tickets = gates.names.map((name) => gates.next(name));
  tickets.forEach((t) => assert.equal(gates.isCurrent(t), true));
  gates.invalidateAll();
  tickets.forEach((t) => assert.equal(gates.isCurrent(t), false));
  const fresh = gates.next('month');
  assert.equal(gates.isCurrent(fresh), true);
});
