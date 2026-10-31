// 商旅打卡纯逻辑：费用模板/响应解析、费用弹层请求代际与保存失败回滚。
// 视图只消费本文件，node:test 直接导入，不加载 SFC。

// numText 数字归一：有限数字转字符串，非法值回空串。
function numText(v) {
  const n = Number(v);
  return Number.isFinite(n) ? String(n) : '';
}

// parseFeeTemplate 商旅模板补助组件（id=10）解析：value 为 JSON 字符串 {foodFee, arrive}。
export function parseFeeTemplate(clockTemplate) {
  const comps = (clockTemplate && clockTemplate.dtComponentList) || [];
  for (const c of comps) {
    if (c && c.id === 10 && c.value) {
      try {
        const v = JSON.parse(c.value);
        return { food: numText(v.foodFee), transit: numText(v.arrive) };
      } catch (e) {
        return { food: '', transit: '' };
      }
    }
  }
  return { food: '', transit: '' };
}

// parseFeeData 费用接口响应 → 表单值：本地摘要优先，缺失回落模板；成本分配只读透传。
export function parseFeeData(data) {
  const d = data || {};
  const tpl = parseFeeTemplate(d.clockTemplate);
  const local = d.local || null;
  return {
    food: local ? numText(local.food_fee) : tpl.food || '0',
    transit: local ? numText(local.transit_fee) : tpl.transit || '0',
    alloc: d.costAlloc || null,
  };
}

// applyFeeResponse 仅当前代际的响应允许落地；旧请求（切换了成员/日期）返回 null 丢弃。
export function applyFeeResponse(token, current, data) {
  if (token !== current) return null;
  return parseFeeData(data);
}

// feeRollback 保存失败回滚：恢复保存前输入并附错误文案。
export function feeRollback(prev, message) {
  return {
    food: (prev && prev.food) || '',
    transit: (prev && prev.transit) || '',
    error: message || '保存失败',
  };
}
