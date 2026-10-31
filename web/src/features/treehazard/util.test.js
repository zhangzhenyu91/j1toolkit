// 树障特有展示/统计逻辑测试（node --test）：字段表迁移到共享模块后，线路排序与阈值判定不变。
import assert from 'node:assert/strict';
import test from 'node:test';

import { BIZ_FIELDS, TREE_FIELDS, lineStats, overThreshold, sortLineGroups } from './util.js';

test('业务字段表与共享配置同源（15 列）', () => {
  assert.equal(BIZ_FIELDS, TREE_FIELDS.bizFields);
  assert.equal(BIZ_FIELDS.length, 15);
});

test('阈值判定：vertical_num 非空且 ≤ 阈值', () => {
  assert.equal(overThreshold({ vertical_num: 3 }, 5), true);
  assert.equal(overThreshold({ vertical_num: 6 }, 5), false);
  assert.equal(overThreshold({ vertical_num: null }, 5), false);
});

test('线路统计与排序：要紧数 > 未处理数 > 电压 > 名称', () => {
  const list = [
    { line_name: 'B线', voltage_level: '35', records: [{ status: 'unhandled', vertical_num: 9 }] },
    { line_name: 'A线', voltage_level: '110', records: [{ status: 'unhandled', vertical_num: 2 }] },
    { line_name: 'C线', voltage_level: '110', records: [{ status: 'handled' }] },
  ];
  assert.deepEqual(lineStats(list[1].records, 5), { unh: 1, key: 1, total: 1 });
  assert.deepEqual(
    sortLineGroups(list, 5).map((g) => g.line_name),
    ['A线', 'B线', 'C线']
  );
});
