// 出工日志纯逻辑：未出车判定、核验清单行、键盘编辑键与删除照片确认文案。
// 视图与组件只消费本文件，node:test 直接导入，不加载 SFC。

// isNoCar 未出车（站内作业）：服务端统一以 vehicle_id=0 表示未出车，
// 历史/兼容数据可能为 null，两者都必须按未出车处理（不参与验证）。
export function isNoCar(vehicleId) {
  return vehicleId == null || Number(vehicleId) === 0;
}

// reportRows 汇总前核验清单行：服务端已按口径（未通过 ∪ 有备注；scope=mine 时为「仅我」原因）
// 过滤，前端不得再按 verify==='failed' 二次筛选，否则会丢掉个人原因行与有备注行。
export function reportRows(list) {
  return Array.isArray(list) ? list.filter(Boolean) : [];
}

// reportRowChips 单行原因标签：优先服务端 reasons；无原因时仅在确有备注时标「有备注」，
// 整体通过且无原因/备注返回空数组（不得谎报有备注）；未通过无原因才标「未通过」。
export function reportRowChips(item) {
  const it = item || {};
  const reasons = Array.isArray(it.reasons) ? it.reasons.filter(Boolean) : [];
  if (reasons.length) return reasons;
  if (it.remark || it.remark_has_files) return ['有备注'];
  if (it.verify === 'failed') return ['未通过'];
  return [];
}

// isActivationKey 键盘激活键（Enter / Space），用于卡片编辑入口的可访问键盘操作。
export function isActivationKey(key) {
  return key === 'Enter' || key === ' ' || key === 'Spacebar';
}

// deletePhotoConfirmText 删除照片二次确认文案（带所属人，便于确认是否删错）。
export function deletePhotoConfirmText(photo) {
  const members = (photo && Array.isArray(photo.members) ? photo.members : []).filter(Boolean);
  if (members.length) return `确定删除这张照片（${members.join('、')}）？删除后不可恢复。`;
  return '确定删除这张照片？删除后不可恢复。';
}
