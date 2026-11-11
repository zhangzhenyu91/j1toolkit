// 参数快照索引：索引写失败必须回滚已写入的快照（否则留下读不到索引的孤儿数据），
// 索引条数有上限，超限时连同对应快照一起淘汰，避免 localStorage 无限增长。
import test from 'node:test';
import assert from 'node:assert/strict';

import { saveJobParams, readJobParams, MAX_INDEX } from '../src/lib/job-params.mjs';

const INDEX_KEY = 'shell.job_params.index.v1';

function storageWith({ failIndex = false } = {}) {
  const map = new Map();
  let indexFailures = failIndex ? Infinity : 0;
  return {
    getItem: (key) => (map.has(key) ? map.get(key) : null),
    setItem: (key, value) => {
      if (key === INDEX_KEY && indexFailures > 0) {
        indexFailures -= 1;
        throw new Error('QuotaExceededError');
      }
      map.set(key, String(value));
    },
    removeItem: (key) => map.delete(key),
    _map: map,
  };
}

test('索引写失败时回滚快照并返回 false', () => {
  const storage = storageWith({ failIndex: true });
  const saved = saveJobParams(storage, '20261031000001-01', 'pcd_export', { outdir: 'D:/out' });
  assert.equal(saved, false);
  assert.equal(storage.getItem('shell.job_params.20261031000001-01.v1'), null);
  assert.equal(readJobParams(storage, '20261031000001-01'), null);
});

test('索引条数超上限时淘汰最旧记录及其快照', () => {
  const storage = storageWith();
  const total = MAX_INDEX + 5;
  for (let i = 0; i < total; i += 1) {
    const jobID = `2026103100${String(i).padStart(4, '0')}-01`;
    assert.equal(saveJobParams(storage, jobID, 'pcd_export', { outdir: `D:/${i}` }), true);
  }
  const index = JSON.parse(storage.getItem(INDEX_KEY) || '[]');
  assert.equal(index.length, MAX_INDEX);
  assert.equal(index.includes('pcd_export:20261031000000-01'), false);
  assert.equal(readJobParams(storage, '20261031000000-01'), null);
  assert.ok(readJobParams(storage, `2026103100${String(total - 1).padStart(4, '0')}-01`));
  assert.equal(storage.getItem('shell.job_params.20261031000000-01.v1'), null);
});
