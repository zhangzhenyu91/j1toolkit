// 派车对齐对照弹层预填测试（node --test）：目的地模糊比对、差异字段按表格预填、
// 车牌/目的地不在字典不误选、用车人字典外姓名不点亮。
import assert from 'node:assert/strict';
import test from 'node:test';

import {
  alignFormDefaults,
  destSame,
  entryFormDefaults,
  findDestination,
  findVehicle,
  memberIdsByName,
} from './align.js';

const meta = {
  vehicles: [{ id: 1, plate_no: '晋A12345' }],
  destinations: [
    { id: 2, name: '孝义市' },
    { id: 3, name: '交城县' },
  ],
  members: [
    { id: 10, name: '赵登' },
    { id: 11, name: '郑海楠' },
  ],
};

test('目的地模糊比对：行政区字样差异视为一致，空表格目的地不约束', () => {
  assert.equal(destSame('孝义市', '吕梁市孝义'), true);
  assert.equal(destSame('孝义市', ''), true);
  assert.equal(destSame('', '孝义市'), false);
  assert.equal(destSame('孝义市', '交城县'), false);
  assert.equal(destSame('孝义市', '吕梁市交城'), false);
});

test('车牌/目的地查字典：空值不误命中首项', () => {
  assert.equal(findVehicle(meta, ''), null);
  assert.equal(findVehicle(meta, '晋B00001'), null);
  assert.equal(findVehicle(meta, '晋A12345').id, 1);
  assert.equal(findDestination(meta, ''), null);
  assert.equal(findDestination(meta, '吕梁市孝义').id, 2);
  assert.equal(findDestination(meta, '某地'), null);
});

test('用车人按姓名点亮：字典外姓名丢弃', () => {
  assert.deepEqual(memberIdsByName(meta, ['郑海楠', '外班甲', '赵登']), [10, 11]);
  assert.deepEqual(memberIdsByName(meta, []), []);
});

test('matched 差异字段按表格预填，其余保留卡片现值', () => {
  const item = {
    kind: 'matched',
    date: '2026-08-18',
    sheet: {
      plate: '晋A12345',
      members: ['赵登', '郑海楠'],
      to: '吕梁市交城',
      orderNo: 'PC-001',
    },
    entry: {
      vehicle_id: 9,
      destination_id: 2,
      order_no: 'PC-000',
      members: [{ member_id: 10, name: '赵登' }],
    },
    diffs: { plate: true, dest: true, orderNo: true, missing: ['郑海楠'], extra: [] },
  };
  assert.deepEqual(alignFormDefaults(item, meta), {
    vehicle_id: 1,
    destination_id: 3,
    dispatch_order_no: 'PC-001',
    member_ids: [10],
  });
});

test('matched 一致字段不被表格空值覆盖', () => {
  const item = {
    kind: 'matched',
    date: '2026-08-18',
    sheet: { plate: '晋A12345', members: ['赵登'], to: '', orderNo: '' },
    entry: {
      vehicle_id: 9,
      destination_id: 2,
      order_no: 'PC-000',
      members: [{ member_id: 12, name: '王五' }],
    },
    diffs: { plate: false, dest: false, orderNo: false, missing: [], extra: [] },
  };
  assert.deepEqual(alignFormDefaults(item, meta), {
    vehicle_id: 9,
    destination_id: 2,
    dispatch_order_no: 'PC-000',
    member_ids: [12],
  });
});

test('sheetOnly 按表格预填建卡表单，字典外车牌/目的地不误选', () => {
  const hit = {
    kind: 'sheetOnly',
    date: '2026-08-19',
    sheet: {
      plate: '晋A12345',
      members: ['赵登', '外班甲'],
      to: '吕梁市孝义',
      orderNo: 'PC-002',
    },
  };
  assert.deepEqual(alignFormDefaults(hit, meta), {
    vehicle_id: 1,
    destination_id: 2,
    dispatch_order_no: 'PC-002',
    member_ids: [10],
  });

  const miss = {
    kind: 'sheetOnly',
    date: '2026-08-19',
    sheet: { plate: '晋B00001', members: ['赵登'], to: '某地', orderNo: '' },
  };
  assert.deepEqual(alignFormDefaults(miss, meta), {
    vehicle_id: 0,
    destination_id: 0,
    dispatch_order_no: '',
    member_ids: [10],
  });
});

test('entryFormDefaults 保留卡片现值（其他卡片编辑）', () => {
  assert.deepEqual(
    entryFormDefaults({
      vehicle_id: 9,
      destination_id: 3,
      order_no: 'PC-9',
      members: [{ member_id: 12, name: '王强' }],
    }),
    {
      vehicle_id: 9,
      destination_id: 3,
      dispatch_order_no: 'PC-9',
      member_ids: [12],
    }
  );
  assert.deepEqual(entryFormDefaults(null), {
    vehicle_id: 0,
    destination_id: 0,
    dispatch_order_no: '',
    member_ids: [],
  });
});

test('缺少 item / meta 时返回空表单', () => {
  assert.deepEqual(alignFormDefaults(null, meta), {
    vehicle_id: 0,
    destination_id: 0,
    dispatch_order_no: '',
    member_ids: [],
  });
  assert.deepEqual(
    alignFormDefaults({ kind: 'sheetOnly', sheet: { plate: '晋A12345' } }, undefined),
    { vehicle_id: 0, destination_id: 0, dispatch_order_no: '', member_ids: [] }
  );
});
