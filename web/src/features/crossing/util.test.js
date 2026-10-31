// 交跨特有展示/统计逻辑测试（node --test）：字段表迁移到共享模块后，隐患统计与线路排序不变。
import assert from 'node:assert/strict';
import test from 'node:test';

import {
  BIZ_FIELDS,
  CROSSING_FIELDS,
  SOURCE_TEXT,
  fmtField,
  hasHazard,
  lineStats,
  sortLineGroups,
} from './util.js';

test('业务字段表与共享配置同源（13 列）', () => {
  assert.equal(BIZ_FIELDS, CROSSING_FIELDS.bizFields);
  assert.equal(BIZ_FIELDS.length, 13);
  assert.equal(SOURCE_TEXT.crane, '群捕获直推');
});

test('隐患判定：空/否/无 均视为无隐患', () => {
  assert.equal(hasHazard({ has_hazard: '是' }), true);
  assert.equal(hasHazard({ has_hazard: '否' }), false);
  assert.equal(hasHazard({ has_hazard: '无' }), false);
  assert.equal(hasHazard({}), false);
});

test('字段格式化：电压/距离/角度补单位', () => {
  assert.equal(fmtField('voltage_level', '110'), '110kV');
  assert.equal(fmtField('cross_vertical', '8.5'), '8.5m');
  assert.equal(fmtField('cross_angle', '45'), '45°');
  assert.equal(fmtField('remark', ''), '—');
});

test('线路统计与排序：隐患数 > 总数 > 电压 > 名称', () => {
  const list = [
    { line_name: 'B线', voltage_level: '35', records: [{ has_hazard: '是' }] },
    {
      line_name: 'A线',
      voltage_level: '110',
      records: [{ has_hazard: '是' }, { has_hazard: '否' }],
    },
    { line_name: 'C线', voltage_level: '110', records: [{ has_hazard: '否' }] },
  ];
  assert.deepEqual(lineStats(list[1].records), { hz: 1, total: 2 });
  assert.deepEqual(
    sortLineGroups(list).map((g) => g.line_name),
    ['A线', 'B线', 'C线']
  );
});
