import test from 'node:test';
import assert from 'node:assert/strict';

import {
  saveJobParams,
  readJobParams,
  deleteJobParams,
  deleteAppJobParams,
} from '../src/lib/job-params.mjs';

function fakeStorage() {
  const map = new Map();
  return {
    getItem: (key) => (map.has(key) ? map.get(key) : null),
    setItem: (key, value) => map.set(key, String(value)),
    removeItem: (key) => map.delete(key),
    _map: map,
  };
}

test('保存后可读回参数快照，读取结果是隔离副本', () => {
  const storage = fakeStorage();
  assert.equal(
    saveJobParams(storage, '20261031000001-01', 'pcd_export', {
      outdir: 'D:/out',
      codes: [{ seq: 1, code: 'A1' }],
    }),
    true
  );
  const read = readJobParams(storage, '20261031000001-01');
  assert.equal(read.appID, 'pcd_export');
  assert.equal(read.params.outdir, 'D:/out');
  read.params.outdir = 'E:/changed';
  assert.equal(readJobParams(storage, '20261031000001-01').params.outdir, 'D:/out');
});

test('敏感键（含嵌套）不保存，非法应用不保存', () => {
  const storage = fakeStorage();
  assert.equal(
    saveJobParams(storage, '20261031000001-01', 'pcd_export', {
      outdir: 'D:/out',
      sso: { username: 'u' },
    }),
    false
  );
  assert.equal(readJobParams(storage, '20261031000001-01'), null);
  assert.equal(saveJobParams(storage, '20261031000002-01', 'quiz_search', { text: 'abc' }), false);
});

test('超过 512 KiB 的快照拒绝保存', () => {
  const storage = fakeStorage();
  const big = 'x'.repeat(520 * 1024);
  assert.equal(
    saveJobParams(storage, '20261031000001-01', 'pcd_export', { codes_text: big }),
    false
  );
  assert.equal(readJobParams(storage, '20261031000001-01'), null);
});

test('单条与整应用删除会同步清理索引', () => {
  const storage = fakeStorage();
  saveJobParams(storage, '20261031000001-01', 'pcd_export', { outdir: 'D:/a' });
  saveJobParams(storage, '20261031000002-01', 'pcd_export', { outdir: 'D:/b' });
  saveJobParams(storage, '20261031000003-01', 'tree_sync', { local_path: 'D:/c.xlsx' });

  deleteJobParams(storage, '20261031000001-01');
  assert.equal(readJobParams(storage, '20261031000001-01'), null);
  assert.ok(readJobParams(storage, '20261031000002-01'));

  deleteAppJobParams(storage, 'pcd_export');
  assert.equal(readJobParams(storage, '20261031000002-01'), null);
  assert.ok(readJobParams(storage, '20261031000003-01'));

  deleteJobParams(storage, '20261031000003-01');
  assert.equal(readJobParams(storage, '20261031000003-01'), null);
});
