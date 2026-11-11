// 机巡密码脱敏与启动前等待：核心回传掩码/空值时不得回显，留空表示不修改；
// 上一个任务的 finished 不能成为永久等待点。
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  readSecretField,
  resolveSecret,
  secretHint,
  isMaskedSecret,
  carrySecret,
  stripSecretPlaceholders,
} from '../src/lib/secret-field.mjs';
import { waitFinished } from '../src/lib/wait-finished.mjs';

test('掩码与空值都不当作明文回显，但仍标记已保存', () => {
  assert.deepEqual(readSecretField({ password: '******' }), { clear: '', hasStored: true });
  assert.deepEqual(readSecretField({ password: '******' }), { clear: '', hasStored: true });
  assert.deepEqual(readSecretField({ password: '', has_password: true }), {
    clear: '',
    hasStored: true,
  });
  assert.deepEqual(readSecretField({ password: '' }), { clear: '', hasStored: false });
  assert.deepEqual(readSecretField({}), { clear: '', hasStored: false });
  assert.deepEqual(readSecretField({ password: 'real-secret' }), {
    clear: 'real-secret',
    hasStored: true,
  });
  assert.equal(isMaskedSecret('******'), true);
  assert.equal(isMaskedSecret(''), false);
});

test('留空保留已保存的明文，用户输入优先，拿不到时交给后端不改', () => {
  assert.deepEqual(resolveSecret('', 'old-secret'), { value: 'old-secret', keepExisting: true });
  assert.deepEqual(resolveSecret('new-secret', 'old-secret'), {
    value: 'new-secret',
    keepExisting: false,
  });
  assert.deepEqual(resolveSecret('******', 'old-secret'), {
    value: 'old-secret',
    keepExisting: true,
  });
  assert.deepEqual(resolveSecret('', ''), { value: '', keepExisting: true });
});

test('提示文案区分已保存与未保存', () => {
  assert.match(secretHint(true), /留空则不修改/);
  assert.match(secretHint(false), /请填写/);
});

test('保存实例参数时密码按「新值优先、未填沿用、掩码丢弃」合并', () => {
  assert.deepEqual(carrySecret({ password: 'old', outdir: 'D:/a' }, { outdir: 'D:/b' }), {
    password: 'old',
    outdir: 'D:/b',
  });
  assert.deepEqual(carrySecret({ password: 'old' }, { password: 'new' }), { password: 'new' });
  assert.deepEqual(carrySecret({ password: '******' }, { outdir: 'D:/b' }), { outdir: 'D:/b' });
  assert.deepEqual(carrySecret({}, { outdir: 'D:/b' }), { outdir: 'D:/b' });
});

test('启动参数里空串/掩码密码一律删除，真实密码原样保留', () => {
  // 核心 masked_view 把密码清空并置 has_password：空串不是密码，带下去会覆盖配置里的真实值。
  assert.deepEqual(stripSecretPlaceholders({ password: '', has_password: true, username: 'a' }), {
    has_password: true,
    username: 'a',
  });
  assert.deepEqual(stripSecretPlaceholders({ password: '******' }), {});
  assert.deepEqual(stripSecretPlaceholders({ password: 'real' }), { password: 'real' });
  assert.deepEqual(stripSecretPlaceholders({}), {});
  assert.deepEqual(stripSecretPlaceholders(), {});
  const source = { password: '', username: 'a' };
  assert.notEqual(stripSecretPlaceholders(source), source);
  assert.equal(source.password, '');
});

test('waitFinished 在任务结束时立即返回 true', async () => {
  let release;
  const job = { finished: new Promise((resolve) => (release = resolve)) };
  const pending = waitFinished(job, 1000);
  release();
  assert.equal(await pending, true);
});

test('waitFinished 对永不结束的任务按超时返回 false，不永久挂起', async () => {
  const job = { finished: new Promise(() => {}) };
  const started = Date.now();
  assert.equal(await waitFinished(job, 120), false);
  assert.ok(Date.now() - started >= 100);
});

test('waitFinished 兼容缺失 finished 的旧任务对象', async () => {
  assert.equal(await waitFinished(null, 10), true);
  assert.equal(await waitFinished({}, 10), true);
});
