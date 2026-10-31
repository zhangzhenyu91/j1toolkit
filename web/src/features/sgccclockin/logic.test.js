// 商旅打卡纯逻辑测试（node --test）：费用弹层请求代际（旧响应不得覆盖新成员数据）、
// 费用响应解析与失败回滚。不直接加载 SFC。
import assert from 'node:assert/strict';
import test from 'node:test';

import {
  applyFeeResponse,
  feeRollback,
  parseFeeData,
  parseFeeTemplate,
} from './logic.js';

test('费用模板组件 id=10 解析 foodFee/arrive，坏 JSON 按无值处理', () => {
  assert.deepEqual(
    parseFeeTemplate({
      dtComponentList: [{ id: 10, value: '{"foodFee":30,"arrive":12}' }],
    }),
    { food: '30', transit: '12' }
  );
  assert.deepEqual(
    parseFeeTemplate({ dtComponentList: [{ id: 10, value: '{bad' }] }),
    { food: '', transit: '' }
  );
  assert.deepEqual(parseFeeTemplate(null), { food: '', transit: '' });
});

test('费用响应解析：本地摘要优先，缺失回落模板，非法值回空', () => {
  const data = {
    local: { food_fee: 50.5, transit_fee: 0 },
    clockTemplate: { dtComponentList: [{ id: 10, value: '{"foodFee":30,"arrive":12}' }] },
    costAlloc: { label: '成本中心' },
  };
  assert.deepEqual(parseFeeData(data), {
    food: '50.5',
    transit: '0',
    alloc: { label: '成本中心' },
  });
  assert.deepEqual(parseFeeData({ clockTemplate: data.clockTemplate }), {
    food: '30',
    transit: '12',
    alloc: null,
  });
  assert.deepEqual(parseFeeData({ local: { food_fee: 'x' } }), {
    food: '',
    transit: '',
    alloc: null,
  });
});

test('费用请求代际：旧请求响应被丢弃，当前请求才落地', () => {
  const data = { local: { food_fee: 10, transit_fee: 2 } };
  assert.equal(applyFeeResponse(1, 2, data), null);
  assert.deepEqual(applyFeeResponse(2, 2, data), {
    food: '10',
    transit: '2',
    alloc: null,
  });
});

test('费用保存失败回滚：恢复保存前输入并附错误文案', () => {
  assert.deepEqual(feeRollback({ food: '30', transit: '12' }, '网络异常'), {
    food: '30',
    transit: '12',
    error: '网络异常',
  });
});
