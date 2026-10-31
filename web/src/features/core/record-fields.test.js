// 树障/交跨共享字段逻辑测试（node --test）：字段表参数化后，冲突对照行、数字等值与状态行口径
// 必须与后端 diff 一致；两组业务字段不得互相复制实现。
import assert from 'node:assert/strict';
import test from 'node:test';

import {
  CROSSING_FIELDS,
  NUMERIC_KEYS_CROSSING,
  NUMERIC_KEYS_TREE,
  TREE_FIELDS,
  buildConflictRows,
  cellVal,
  dash,
  fmtDateCN,
  fmtDeg,
  fmtKv,
  fmtM,
  parseDispositionFilename,
  sameVal,
} from './record-fields.js';

test('空值与单位展示：dash / kV / m / ° 幂等', () => {
  assert.equal(dash('  '), '—');
  assert.equal(dash('A'), 'A');
  assert.equal(fmtKv('110'), '110kV');
  assert.equal(fmtKv('110kV'), '110kV');
  assert.equal(fmtM('8.5'), '8.5m');
  assert.equal(fmtM('8.5m'), '8.5m');
  assert.equal(fmtDeg('45'), '45°');
  assert.equal(fmtDeg('45°'), '45°');
});

test('数字等值：3 与 3.0 视为相同，文本字段逐字比较', () => {
  assert.equal(sameVal('tree_count', '3', '3.0', NUMERIC_KEYS_TREE), true);
  assert.equal(sameVal('vertical', 12, '12.00', NUMERIC_KEYS_TREE), true);
  assert.equal(sameVal('vertical', '12', '13', NUMERIC_KEYS_TREE), false);
  assert.equal(sameVal('line_name', '3', '3.0', NUMERIC_KEYS_TREE), false);
  assert.equal(sameVal('cross_angle', '45', '45.0', NUMERIC_KEYS_CROSSING), true);
});

test('电压列格式化走 kV，其余走 dash', () => {
  assert.equal(cellVal('voltage_level', '35'), '35kV');
  assert.equal(cellVal('line_name', null), '—');
});

test('树障冲突行：15 业务列 + 状态行，差异字段标红数据可判定', () => {
  const conflict = {
    id: 7,
    line_name: '北山线',
    section: '12#-13#',
    voltage_level: '110',
    tree_count: '3',
    created_at: '2026-10-31 09:00:00',
    server_snapshot: { line_name: '北山线', section: '12#-13#', voltage_level: '110' },
    intranet_snapshot: {
      line_name: '北山线',
      section: '12#-13#',
      voltage_level: '110',
      tree_count: '3.0',
    },
    record_status: 'unhandled',
  };
  const rows = buildConflictRows(conflict, {
    fields: TREE_FIELDS.bizFields,
    numericKeys: NUMERIC_KEYS_TREE,
    showStatus: true,
  });
  assert.equal(rows.length, TREE_FIELDS.bizFields.length + 1);
  assert.equal(rows[0].key, 'voltage_level');
  assert.equal(rows[0].server, '110kV');
  assert.equal(rows[0].intranet, '110kV');
  const countRow = rows.find((r) => r.key === 'tree_count');
  assert.equal(countRow.diff, false, '3 与 3.0 不算差异');
  const statusRow = rows[rows.length - 1];
  assert.equal(statusRow.key, 'status');
  assert.equal(statusRow.server, '未处理');
  assert.equal(statusRow.intranet, '未处理');
});

test('交跨冲突行：13 业务列，不渲染状态行', () => {
  const rows = buildConflictRows(
    {
      id: 8,
      line_name: '东河线',
      intranet_snapshot: { line_name: '东河线', remark: '已核对' },
    },
    { fields: CROSSING_FIELDS.bizFields, numericKeys: NUMERIC_KEYS_CROSSING }
  );
  assert.equal(rows.length, CROSSING_FIELDS.bizFields.length);
  assert.equal(
    rows.some((r) => r.key === 'status'),
    false
  );
  const remark = rows.find((r) => r.key === 'remark');
  assert.equal(remark.diff, true);
  assert.equal(remark.intranet, '已核对');
});

test('Content-Disposition 文件名解析：优先 UTF-8 filename*，回退英文 filename 与默认名', () => {
  assert.equal(
    parseDispositionFilename("attachment; filename*=UTF-8''%E6%A0%91%E6%9C%A8.xlsx", 'x.xlsx'),
    '树木.xlsx'
  );
  assert.equal(parseDispositionFilename('attachment; filename="a.xlsx"', 'x.xlsx'), 'a.xlsx');
  assert.equal(parseDispositionFilename('', 'x.xlsx'), 'x.xlsx');
  assert.equal(parseDispositionFilename("filename*=UTF-8''bad%ZZ", 'x.xlsx'), 'bad%ZZ');
});

test('日期中文与点分展示', () => {
  assert.equal(fmtDateCN('2026-10-31'), '2026 年 10 月 31 日');
  assert.equal(fmtDateCN(''), '');
});
