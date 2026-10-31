import test from 'node:test';
import assert from 'node:assert/strict';

import {
  friendlyTaskError,
  shouldExpandLog,
  statusTheme,
  pcdOutcome,
  dailyOutcome,
  syncOutcome,
  courseOutcome,
  formatHistorySummary,
} from '../src/lib/task-result.mjs';

function job(status, result = {}, error = '') {
  return { status, result, error };
}

test('失败状态一律自动展开排错日志，成功/取消/暂停不展开', () => {
  assert.equal(shouldExpandLog({ status: '失败', failed: true }), true);
  assert.equal(shouldExpandLog({ status: '启动失败', failed: true }), true);
  assert.equal(shouldExpandLog({ status: '成功', failed: false }), false);
  assert.equal(shouldExpandLog({ status: '已取消', failed: false }), false);
  assert.equal(shouldExpandLog({ status: '已暂停', failed: false }), false);
  assert.equal(statusTheme('成功'), 'success');
  assert.equal(statusTheme('失败'), 'danger');
  assert.equal(statusTheme('已暂停'), 'warning');
  assert.equal(statusTheme('执行中'), 'primary');
});

test('常见技术错误翻译为下一步业务提示，原文不丢给用户摘要', () => {
  assert.match(friendlyTaskError('SSO login failed'), /账号和密码/);
  assert.match(friendlyTaskError('U盘未挂载'), /U 盘/);
  assert.match(friendlyTaskError('playwright 连接失败'), /浏览器/);
  assert.match(friendlyTaskError('request timeout'), /超时/);
  assert.match(friendlyTaskError('TLS certificate error'), /证书/);
  assert.match(friendlyTaskError('no such file'), /文件或目录/);
  assert.match(friendlyTaskError('完全未知的错误'), /排错日志/);
});

test('轨迹导出终态摘要与产物目录', () => {
  const ok = pcdOutcome(
    job('success', {
      total: 12,
      pcd_ok: 11,
      pcd_fail: 1,
      track_ok: 10,
      track_fail: 2,
      not_found: ['A1'],
      dir: 'D:/out',
    })
  );
  assert.equal(ok.status, '成功');
  assert.equal(
    ok.summary,
    '导出完成：共处理 12 单；派车单成功 11 / 失败 1；轨迹成功 10 / 失败 2；未找到 1 单'
  );
  assert.deepEqual(ok.artifact, { label: '打开产物目录', path: 'D:/out' });
  assert.equal(ok.failed, false);

  const failed = pcdOutcome(job('failed', {}, 'SSO 登录失败'));
  assert.equal(failed.failed, true);
  assert.match(failed.summary, /账号和密码/);
});

test('派车同步/树障交跨同步终态表述', () => {
  const daily = dailyOutcome(
    job('success', { count: 12, file: '派车单.xlsx', dest: 'U盘', dir: 'D:/out' })
  );
  assert.equal(daily.summary, '导出完成：12 条派车单 → 派车单.xlsx（写入 U盘）');

  const skipped = syncOutcome(job('success', { skipped: 'usb_absent', nightly: true }));
  assert.equal(skipped.status, '完成');
  assert.equal(skipped.summary, '本次未执行：U 盘未挂载（已跳过）（夜间兜底）');

  const replace = syncOutcome(job('success', { kind: 'replace', file: '新表.xlsx' }), {
    localPath: 'D:/内网/统计表.xlsx',
  });
  assert.match(replace.summary, /U 盘新表已覆盖内网表/);
  assert.equal(replace.artifact.path, 'D:/内网');
});

test('同步任务仅提供 action 时仍按快照/替换生成摘要与 U 盘产物入口', () => {
  const snapshot = syncOutcome(
    job('success', { action: 'snapshot', nightly: true, file: '树障统计表.xlsx' }),
    { usbMount: 'E:/usb' }
  );
  assert.equal(snapshot.summary, '同步完成：内网表已拷贝到 U 盘 → 树障统计表.xlsx（夜间兜底）');
  assert.deepEqual(snapshot.artifact, { label: '打开 U 盘目录', path: 'E:/usb' });

  const replace = syncOutcome(job('success', { action: 'replace', file: '新表.xlsx' }), {
    localPath: 'D:/内网/统计表.xlsx',
  });
  assert.match(replace.summary, /U 盘新表已覆盖内网表/);
  assert.equal(replace.artifact.path, 'D:/内网');
});

test('失败但没有 error 时仍给出业务摘要', () => {
  const pcd = pcdOutcome(
    job('failed', {
      total: 2,
      pcd_ok: 1,
      pcd_fail: 1,
      track_ok: 1,
      track_fail: 1,
    })
  );
  assert.equal(pcd.failed, true);
  assert.match(pcd.summary, /导出未完成/);
  assert.match(pcd.summary, /共处理 2 单/);

  const daily = dailyOutcome(job('failed', { count: 3, file: '派车单.xlsx' }));
  assert.equal(daily.failed, true);
  assert.match(daily.summary, /导出未完成/);
  assert.match(daily.summary, /3 条派车单/);

  const sync = syncOutcome(job('failed', { kind: 'snapshot', file: '树障.xlsx' }), {
    usbMount: 'E:/usb',
  });
  assert.equal(sync.failed, true);
  assert.match(sync.summary, /同步未完成/);
  assert.match(sync.summary, /树障\.xlsx/);

  const course = courseOutcome(job('failed', { total: 2, completed: 1, pending: 1 }));
  assert.match(course.summary, /执行失败/);
  assert.match(course.summary, /总数 2/);
});

test('学堂刷课终态区分暂停、未完成、完成和失败', () => {
  const counts = { total: 10, completed: 6, pending: 4, skipped: 0 };
  assert.match(courseOutcome(job('running', { ...counts, paused: true })).summary, /已暂停/);
  assert.match(courseOutcome(job('success', counts)).summary, /仍有未完成课程/);
  assert.equal(
    courseOutcome(job('success', { total: 10, completed: 10, pending: 0, skipped: 1 })).summary,
    '学习完成（全部达到 100%）：总数 10；完成 10；未完成 0；跳过 1（跳过 1 门）'
  );
  assert.match(courseOutcome(job('failed', counts, '浏览器未就绪')).summary, /浏览器/);
});

test('执行历史摘要按应用格式化', () => {
  assert.match(formatHistorySummary('pcd_export', { total: 2 }), /共处理 2 单/);
  assert.match(formatHistorySummary('daily_sync', { count: 0 }), /当日无派车数据/);
  assert.match(formatHistorySummary('tree_sync', { skipped: 'no_command' }), /U 盘无同步指令/);
  assert.match(formatHistorySummary('quiz_search', {}), /题库/);
});
