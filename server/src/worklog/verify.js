// 出工日志：记录验证状态（verify_passed）计算，logs 与 day-status 共用
// 规则（见《开发指南》7.1）：① 未出车不验证（exempt）；② 目的地已选且有用车人（巡视内容按需求可空，不计入）；
// ③ 至少一张水印照片且全部已通过；④ 用车人名单与全部照片人名并集一致；⑤ 多张照片施工内容一致
function computeVerifyPassed(entry) {
  if (!entry.vehicle_id) return 'exempt';
  if (!entry.destination_id) return 'failed';
  if (!entry.members.length) return 'failed';
  if (!entry.photos.length) return 'failed';
  if (!entry.photos.every((p) => p.verify_status === 'passed')) return 'failed';

  const memberNames = new Set(entry.members.map((m) => m.name));
  const photoNames = new Set();
  entry.photos.forEach((p) => (p.members || []).forEach((n) => photoNames.add(n)));
  if (memberNames.size !== photoNames.size) return 'failed';
  for (const n of memberNames) {
    if (!photoNames.has(n)) return 'failed';
  }

  const contents = new Set(entry.photos.map((p) => p.work_content));
  if (contents.size > 1) return 'failed';
  return 'passed';
}

module.exports = { computeVerifyPassed };
