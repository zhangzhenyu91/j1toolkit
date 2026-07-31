// 出工日志：记录验证状态（verify_passed）与未通过明细（verify_reasons）计算，logs / day-status / report 共用
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

// 单张照片未通过项（与小程序 mapPhoto 同口径，含历史数据 date_mismatch/dest_mismatch 回退判定）
// passed 返回 []；pending=验证中；failed=验证失败；mismatch 按 date_ok/dest_ok 逐项列出
function photoIssues(p) {
  if (p.verify_status === 'pending') return ['验证中'];
  if (p.verify_status === 'failed') return ['验证失败'];
  if (p.verify_status === 'passed') return [];
  const bad = [];
  const dateBad = p.date_ok === 0 || (p.date_ok == null && p.verify_status === 'date_mismatch');
  const destBad = p.dest_ok === 0 || (p.dest_ok == null && p.verify_status === 'dest_mismatch');
  if (dateBad) bad.push('日期不符');
  if (destBad) bad.push('地点不符');
  return bad.length ? bad : ['未通过验证'];
}

// 记录未通过明细：逐条列出全部不满足项（与 computeVerifyPassed 同口径；passed/exempt 返回 []）
// 与状态函数的差异：状态短路返回，本函数不短路，把所有不满足的规则都列出来
function computeFailReasons(entry) {
  if (!entry.vehicle_id) return []; // 免验证
  const reasons = [];
  if (!entry.destination_id) reasons.push('未选择目的地');
  if (!entry.members.length) reasons.push('未选择用车人');
  if (!entry.photos.length) {
    reasons.push('未上传水印照片');
    return reasons; // 无照片时不再判定人名/施工内容
  }
  entry.photos.forEach((p) => {
    const names = (p.members || []).join('、') || '未署名';
    photoIssues(p).forEach((t) => reasons.push(`${names}的水印照片${t}`));
  });
  const memberNames = new Set(entry.members.map((m) => m.name));
  const photoNames = new Set();
  entry.photos.forEach((p) => (p.members || []).forEach((n) => photoNames.add(n)));
  const missing = [...memberNames].filter((n) => !photoNames.has(n));
  if (missing.length) reasons.push(`${missing.join('、')}未上传水印照片`);
  // 兜底：正常流程照片人名 ⊆ 用车人，仅成员改名等历史数据才可能出现
  const extra = [...photoNames].filter((n) => !memberNames.has(n));
  if (extra.length) reasons.push(`照片人名「${extra.join('、')}」不在用车人名单中`);
  // 施工内容一致性同状态函数的顺序语义：仅在照片全部通过后才纳入判定
  if (entry.photos.every((p) => p.verify_status === 'passed')) {
    const contents = new Set(entry.photos.map((p) => p.work_content));
    if (contents.size > 1) reasons.push('多张水印照片施工内容不一致');
  }
  return reasons;
}

// 个人口径报告原因：我未打卡 / 我未上传水印照片（用车人含我但无照片含我名字）/ 我的水印照片未通过项（验证中、验证失败、日期/地点不符）
function myReportReasons(entry, me) {
  const reasons = [];
  const myRow = entry.members.find((m) => m.member_id === me.id);
  if (myRow && !myRow.checked) reasons.push('我未打卡');
  if (myRow && !entry.photos.some((p) => (p.members || []).includes(me.name))) {
    reasons.push('我未上传水印照片');
  }
  entry.photos.forEach((p) => {
    if (!(p.members || []).includes(me.name)) return;
    photoIssues(p).forEach((t) => reasons.push(`我的水印照片${t}`));
  });
  return reasons;
}

module.exports = { computeVerifyPassed, computeFailReasons, photoIssues, myReportReasons };
