// 出工日志日期工具测试（node --test）：统一本地日历日/月、中文日期、点分展示与月平移，
// 禁止 UTC 换算导致凌晨或月末退一天。
import assert from 'node:assert/strict';
import test from 'node:test';

import {
  clampDayToMonth,
  dateDot,
  fmtDateCN,
  localDay,
  localMonth,
  monthLabel,
  shiftMonth,
} from './date.js';

test('localDay 按本机时区拼年月日（不受 UTC 换算影响）', () => {
  assert.equal(localDay(new Date(2026, 0, 1, 0, 30)), '2026-01-01');
  assert.equal(localDay(new Date(2026, 9, 31, 23, 59)), '2026-10-31');
});

test('localMonth 取本地年月', () => {
  assert.equal(localMonth(new Date(2026, 0, 31, 0, 5)), '2026-01');
});

test('fmtDateCN 中文日期；空值返回空串，不输出 NaN', () => {
  assert.equal(fmtDateCN('2026-10-31'), '2026 年 10 月 31 日');
  assert.equal(fmtDateCN(''), '');
  assert.equal(fmtDateCN('2026-01'), '');
});

test('dateDot 点分展示与月标题', () => {
  assert.equal(dateDot('2026-10-31'), '2026.10.31');
  assert.equal(dateDot(''), '');
  assert.equal(monthLabel('2026-10'), '2026 年 10 月');
});

test('shiftMonth 跨年平移并返回新选中日（同日或月末）', () => {
  assert.deepEqual(shiftMonth('2026-01', -1, '2026-01-31'), {
    month: '2025-12',
    selected: '2025-12-31',
  });
  assert.deepEqual(shiftMonth('2026-01', 1, '2026-01-31'), {
    month: '2026-02',
    selected: '2026-02-28',
  });
  assert.deepEqual(shiftMonth('2026-10', 1, ''), {
    month: '2026-11',
    selected: '2026-11-01',
  });
});

test('clampDayToMonth 月末裁剪', () => {
  assert.equal(clampDayToMonth('2026-02', '31'), '2026-02-28');
  assert.equal(clampDayToMonth('2028-02', '31'), '2028-02-29');
  assert.equal(clampDayToMonth('2026-10', '5'), '2026-10-05');
});
