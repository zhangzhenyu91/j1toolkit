import test from 'node:test';
import assert from 'node:assert/strict';

import {
  restorePcdParams,
  restoreDailyParams,
  restoreSyncParams,
  restoreCourseParams,
} from '../src/lib/restore-params.mjs';

test('轨迹导出参数回填只接受类型正确的字段', () => {
  const restored = restorePcdParams({
    outdir: 'D:/out',
    batch_name: '批次 A',
    batch_subdir: false,
    export_pcd: true,
    export_track: false,
    calibrate_noon: true,
    codes: [{ seq: 1, code: 'A1' }],
    codes_text: 123,
  });
  assert.equal(restored.outdir, 'D:/out');
  assert.equal(restored.batchName, '批次 A');
  assert.deepEqual(restored.options, {
    batchSubdir: false,
    pcd: true,
    track: false,
    calibrateNoon: true,
  });
  assert.equal(restored.codes.length, 1);
  assert.equal(restored.codesText, '');
  assert.equal(restored.xlsxPath, '');
});

test('派车同步/同步任务参数回填', () => {
  assert.deepEqual(restoreDailyParams({ date: '2026-10-01' }), { date: '2026-10-01' });
  assert.deepEqual(restoreDailyParams({ date: 20261001 }), { date: '' });
  assert.deepEqual(restoreSyncParams({ nightly: true }), { action: 'nightly' });
  assert.deepEqual(restoreSyncParams({ tick: true }), { action: 'tick' });
  assert.deepEqual(restoreSyncParams({}), { action: 'tick' });
});

test('学堂参数回填数字兼容 JSON float 与字符串', () => {
  assert.deepEqual(restoreCourseParams({ scan_only: true, timeout: 90.0, max_attempts: '5' }), {
    scanOnly: true,
    timeout: 90,
    maxAttempts: 5,
  });
  assert.deepEqual(restoreCourseParams({}), { scanOnly: false, timeout: 120, maxAttempts: 3 });
  assert.deepEqual(restoreCourseParams({ timeout: 0, max_attempts: -2 }), {
    scanOnly: false,
    timeout: 120,
    maxAttempts: 3,
  });
});
