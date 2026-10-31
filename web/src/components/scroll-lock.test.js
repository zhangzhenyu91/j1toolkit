// 共享滚动锁测试（node --test）：引用计数，未激活的弹层不得释放他人的滚动锁。
import assert from 'node:assert/strict';
import test from 'node:test';

import { createScrollLock } from './scroll-lock.js';

function fakeTarget(initial = '') {
  return { style: { overflow: initial } };
}

test('单个持有者：加锁隐藏滚动，释放恢复原值', () => {
  const body = fakeTarget('auto');
  const lock = createScrollLock(body);
  const release = lock.acquire();
  assert.equal(body.style.overflow, 'hidden');
  assert.equal(lock.count, 1);
  release();
  assert.equal(body.style.overflow, 'auto');
  assert.equal(lock.count, 0);
});

test('两个弹层同时打开：先关的不得提前释放滚动锁', () => {
  const body = fakeTarget('');
  const lock = createScrollLock(body);
  const releaseA = lock.acquire();
  const releaseB = lock.acquire();
  assert.equal(body.style.overflow, 'hidden');
  releaseA();
  assert.equal(body.style.overflow, 'hidden', 'A 关闭后 B 仍在，页面必须保持锁定');
  assert.equal(lock.count, 1);
  releaseB();
  assert.equal(body.style.overflow, '');
});

test('滚动锁代际：上一代票据不得解锁新一代持有者', () => {
  const body = fakeTarget('');
  const lock = createScrollLock(body);
  const oldRelease = lock.acquire();
  oldRelease();
  const releaseNew = lock.acquire();
  assert.equal(body.style.overflow, 'hidden');
  oldRelease(); // 上一代闭包重复调用（如旧弹层延迟卸载）
  oldRelease();
  assert.equal(body.style.overflow, 'hidden', '新一代仍持有滚动锁');
  assert.equal(lock.count, 1);
  releaseNew();
  assert.equal(body.style.overflow, '');
});

test('Drawer 与旧弹层混用：先开弹层、再开抽屉，弹层关闭不得解锁抽屉', () => {
  const body = fakeTarget('');
  const lock = createScrollLock(body);
  const releaseDialog = lock.acquire();
  const releaseDrawer = lock.acquire();
  releaseDialog();
  assert.equal(body.style.overflow, 'hidden');
  assert.equal(lock.count, 1);
  releaseDrawer();
  assert.equal(body.style.overflow, '');
});

test('重复释放同一票据无副作用，不会扣减他人计数', () => {
  const body = fakeTarget('');
  const lock = createScrollLock(body);
  const releaseA = lock.acquire();
  const releaseB = lock.acquire();
  releaseA();
  releaseA(); // 幂等
  assert.equal(lock.count, 1);
  assert.equal(body.style.overflow, 'hidden');
  releaseB();
  assert.equal(lock.count, 0);
  assert.equal(body.style.overflow, '');
});
