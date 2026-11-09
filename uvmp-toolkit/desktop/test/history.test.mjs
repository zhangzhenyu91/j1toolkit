import test from 'node:test';
import assert from 'node:assert/strict';

import {
  mergeHistory,
  filterHistory,
  validHistoryDate,
  historyStatusKey,
  historyStatusText,
} from '../src/lib/history.mjs';

function record(jobID, time, status = 'success', result = {}, error = '') {
  return { job_id: jobID, time, status, result, error };
}

test('合并六个工具记录并按时间倒序，同时间按任务号倒序', () => {
  const merged = mergeHistory({
    daily_sync: [record('20261031000001-01', '2026-10-31 08:00:00')],
    tree_sync: [
      record('20261031000002-01', '2026-10-31 09:00:00'),
      record('20261031000003-01', '2026-10-30 09:00:00'),
    ],
    pcd_export: [record('20261031000002-01', '2026-10-31 09:00:00')],
  });
  assert.deepEqual(
    merged.map((entry) => entry.rec.job_id),
    ['20261031000002-01', '20261031000002-01', '20261031000001-01', '20261031000003-01']
  );
  assert.equal(merged[0].appID, 'pcd_export');
  assert.equal(merged[0].appName, '轨迹导出');
});

test('工具、状态、关键词与日期筛选', () => {
  const merged = mergeHistory({
    daily_sync: [
      record('20261031000001-01', '2026-10-31 08:00:00'),
      record('20261030000001-01', '2026-10-30 08:00:00', 'failed', {}, 'U盘未挂载'),
    ],
    course_autoplay: [
      record('20261031000002-01', '2026-10-31 09:00:00', 'success', { paused: true }),
    ],
  });
  assert.equal(filterHistory(merged, { appID: 'daily_sync' }).length, 2);
  assert.equal(filterHistory(merged, { status: 'failed' }).length, 1);
  assert.equal(filterHistory(merged, { status: 'paused' }).length, 1);
  assert.equal(filterHistory(merged, { keyword: 'u盘' }).length, 1);
  assert.equal(filterHistory(merged, { date: '2026-10-31' }).length, 2);
  assert.equal(filterHistory(merged, { date: '2026-13-01' }).length, 0);

  const withSummary = [
    {
      appID: 'pcd_export',
      appName: '轨迹导出',
      summary: '导出完成：共处理 2 单；未找到 1 单',
      rec: record('20261031000003-01', '2026-10-31 10:00:00'),
    },
  ];
  assert.equal(filterHistory(withSummary, { keyword: '共处理 2 单' }).length, 1);
});

test('日期只接受 YYYY-MM-DD 合法值', () => {
  assert.equal(validHistoryDate(''), true);
  assert.equal(validHistoryDate('2026-10-31'), true);
  assert.equal(validHistoryDate('2026-2-1'), false);
  assert.equal(validHistoryDate('2026-02-30'), false);
});

test('状态键、中文与主题映射一致', () => {
  assert.equal(historyStatusKey(record('1', 't', 'failed')), 'failed');
  assert.equal(historyStatusKey(record('1', 't', 'success', { paused: true })), 'paused');
  assert.deepEqual(historyStatusText(record('1', 't', 'success')), {
    key: 'success',
    text: '成功',
    theme: 'success',
  });
  assert.deepEqual(historyStatusText(record('1', 't', 'failed')), {
    key: 'failed',
    text: '失败',
    theme: 'danger',
  });
  assert.deepEqual(historyStatusText(record('1', 't', 'cancelled')), {
    key: 'cancelled',
    text: '已取消',
    theme: 'default',
  });
});
