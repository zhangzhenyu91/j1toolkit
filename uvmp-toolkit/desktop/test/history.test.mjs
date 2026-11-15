import test from 'node:test';
import assert from 'node:assert/strict';

import {
  mergeHistory,
  filterHistory,
  validHistoryDate,
  historyStatusKey,
  historyStatusText,
  historySummary,
  HISTORY_TOOL_OPTIONS,
} from '../src/lib/history.mjs';
import { BUSINESS_IDS } from '../src/lib/navigation.mjs';

function record(jobID, time, status = 'success', result = {}, error = '') {
  return { job_id: jobID, time, status, result, error };
}

test('合并全部业务应用记录并按时间倒序，同时间按任务号倒序', () => {
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

test('执行历史工具清单来自业务应用：含批量重命名，不含独立窗口入口', () => {
  const ids = HISTORY_TOOL_OPTIONS.filter((item) => item.id).map((item) => item.id);
  assert.deepEqual(ids, BUSINESS_IDS);
  assert.ok(ids.includes('batch_rename'));
  assert.ok(!ids.includes('image_tools'));
});

test('历史摘要与页面结果卡同源：批量重命名用同一份计数', () => {
  const result = { success: 2, failed: 1, skipped: 3, unchanged: 4, not_run: 5 };
  assert.equal(
    historySummary('batch_rename', result),
    '成功 2，失败 1，跳过 3，未变化 4，未执行 5'
  );
  assert.equal(historySummary('batch_rename', {}), '未执行');
  // 其他应用仍走原摘要口径
  assert.equal(historySummary('quiz_search', { count: 3 }), '题库搜题 3 题');
});

test('历史摘要不会空白：result 为空时回退错误原因或「未执行」', () => {
  assert.equal(historySummary('batch_rename', {}, ''), '未执行');
  assert.equal(historySummary('batch_rename', null, '磁盘已满'), '磁盘已满');
  assert.equal(historySummary('batch_rename', {}, '  核心退出  '), '核心退出');
  // 有计数时仍以计数为准，错误不覆盖真实结果
  assert.equal(historySummary('batch_rename', { success: 1 }, '磁盘已满'), '成功 1');
  // 其他应用同样不得空白
  assert.equal(historySummary('defect_review', {}, ''), '未执行');
  assert.equal(historySummary('defect_review', {}, '登录失效'), '登录失效');
});
