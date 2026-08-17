// 出工日志：记录验证状态（verify_passed）与未通过明细（verify_reasons）计算，logs / day-status / report 共用；
// 个人口径 myReportReasons 仅 /report scope=mine 使用（/day-status scope=mine 为 index.js 内联自算，未复用本模块）
// 规则（见《开发指南》7.1）：① 未出车不验证（exempt）；② 目的地已选且有用车人（巡视内容按需求可空，不计入）；
// ③ 至少一张水印照片且全部已通过；④ 用车人名单与全部照片人名并集一致；⑤ 多张照片施工内容一致；⑥ 全部用车人已打卡
//
// 商旅打卡开启（SGCC_CLOCKIN_ENABLED=true）后切换为新 6 条（design/sgcc-clockin.html 汇总前核验口径）：
// a. 已绑定商旅的用车人均完成两次打卡（开始+结束，未绑定不参与）；b. 用车人均已上传水印照片；
// c. 同记录不同水印照片施工内容一致；d. 水印照片地点均包含派车目的地；e. 水印照片拍摄时间均为记录当天；
// f. 用车人当日费用信息均已填写且为 伙食60/交通0（业务固定口径；规则 a 不约束未绑定者，但费用未绑定者同样要补录，故全员约束）
// （d/e 即照片级核验 date_ok/dest_ok；非水印照片 is_watermark=0 不参与；旧 checked 打卡开关不再计入）
const config = require('../config');
function sgccOn() { return !!(config.sgcc && config.sgcc.enabled); }
// 参与验证的照片集：商旅打卡开启后剔除非水印照片
function wmPhotos(entry) {
  const photos = entry.photos || [];
  return sgccOn() ? photos.filter((p) => p.is_watermark !== 0) : photos;
}

// 规则 f：当日费用信息达标 = 已填写且 伙食补助=60、交通费=0（业务固定口径；entry.feeMap 由 loadEntries 装配）
function feeOk(f) {
  return !!f && Number(f.foodFee) === 60 && Number(f.transitFee) === 0;
}

// 单张照片水印信息核验（Dify 只返回识别结果，日期/地点比对在后端，见《开发指南》7.2）：
// 日期相符 = 识别拍摄时间 time 包含记录日期 logDate（YYYY.MM.DD 点分格式，调用方已 dots() 格式化）；
// 地点相符 = 识别地点 location 包含派车目的地（目的地为空不约束，卡片级规则另判「未选择目的地」）；
// 两者皆符 → passed，否则 → mismatch
function checkWatermark({ time, location, logDate, destination }) {
  const dateOk = typeof time === 'string' && !!logDate && time.includes(logDate);
  const dest = String(destination || '').trim();
  const destOk = !dest || String(location || '').includes(dest);
  return { dateOk, destOk, status: dateOk && destOk ? 'passed' : 'mismatch' };
}

function computeVerifyPassed(entry) {
  if (!entry.vehicle_id) return 'exempt';
  if (!entry.destination_id) return 'failed';
  if (!entry.members.length) return 'failed';

  if (sgccOn()) {
    // a. 已绑定商旅的用车人均完成两次打卡（开始+结束）
    const ck = entry.clockinMap || {};
    const twoDone = entry.members.every((m) => {
      if (!m.sgccBound) return true; // 未绑定不参与
      const c = ck[m.member_id] || {};
      return !!c[1] && !!c[2];
    });
    if (!twoDone) return 'failed';
    // f. 用车人费用信息均已填写且达标（伙食60/交通0；全员约束，未绑定商旅者费用同样要补录）
    const fees = entry.feeMap || {};
    if (entry.members.some((m) => !feeOk(fees[m.member_id]))) return 'failed';
    // b. 用车人均已上传水印照片（非水印不计）
    const photos = wmPhotos(entry);
    const photoNames = new Set();
    photos.forEach((p) => (p.members || []).forEach((n) => photoNames.add(n)));
    if (entry.members.some((m) => !photoNames.has(m.name))) return 'failed';
    // d/e. 至少一张水印照片且全部核验通过（passed 已含日期/地点相符）
    if (!photos.length) return 'failed';
    if (!photos.every((p) => p.verify_status === 'passed')) return 'failed';
    // c. 多张水印照片施工内容一致
    const contents = new Set(photos.map((p) => p.work_content));
    if (contents.size > 1) return 'failed';
    return 'passed';
  }

  if (entry.members.some((m) => !m.checked)) return 'failed';
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
// 商旅打卡开启后按新 6 条（a~f）措辞输出，供「汇总前核验」仅问题记录展示
function computeFailReasons(entry) {
  if (!entry.vehicle_id) return []; // 免验证
  const reasons = [];
  if (!entry.destination_id) reasons.push('未选择目的地');
  if (!entry.members.length) reasons.push('未选择用车人');

  if (sgccOn()) {
    // a. 两次打卡（仅已绑定商旅的用车人）
    const ck = entry.clockinMap || {};
    entry.members.forEach((m) => {
      if (!m.sgccBound) return;
      const c = ck[m.member_id] || {};
      if (!c[1] && !c[2]) reasons.push(`${m.name}未打卡（缺开始与结束）`);
      else if (!c[1]) reasons.push(`${m.name}缺「开始打卡」`);
      else if (!c[2]) reasons.push(`${m.name}缺「结束打卡」`);
    });
    // f. 费用信息（全员约束，口径同状态函数；未绑定商旅者费用同样要补录）
    const fees = entry.feeMap || {};
    entry.members.forEach((m) => {
      if (!feeOk(fees[m.member_id])) reasons.push(`${m.name}费用信息未填写或不为 伙食60/交通0`);
    });
    // b. 水印照片人名覆盖用车人（非水印不计）
    const photos = wmPhotos(entry);
    const photoNames = new Set();
    photos.forEach((p) => (p.members || []).forEach((n) => photoNames.add(n)));
    const missing = entry.members.filter((m) => !photoNames.has(m.name)).map((m) => m.name);
    if (missing.length) reasons.push(`${missing.join('、')}未上传水印照片`);
    if (!photos.length) return reasons; // 无水印照片时不再判定 d/e/c
    // d/e. 照片级逐项（日期不符=规则 e；地点不符=规则 d）
    photos.forEach((p) => {
      const names = (p.members || []).join('、') || '未署名';
      photoIssues(p).forEach((t) => reasons.push(`${names}的水印照片${t}`));
    });
    // c. 施工内容一致性（同状态函数的顺序语义：全部通过后才纳入判定）
    if (photos.every((p) => p.verify_status === 'passed')) {
      const contents = new Set(photos.map((p) => p.work_content));
      if (contents.size > 1) reasons.push('多张水印照片施工内容不一致');
    }
    return reasons;
  }

  const unchecked = entry.members.filter((m) => !m.checked).map((m) => m.name);
  if (unchecked.length) reasons.push(`${unchecked.join('、')}未打卡`);
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
// 商旅打卡开启后：「我未打卡」按两次打卡（开始/结束）判定，非水印照片不计入；另加规则 f「我的费用信息未填写或不达标」
function myReportReasons(entry, me) {
  const reasons = [];
  const photos = wmPhotos(entry);
  const myRow = entry.members.find((m) => m.member_id === me.id);
  if (sgccOn()) {
    if (myRow && myRow.sgccBound) {
      const c = (entry.clockinMap || {})[me.id] || {};
      if (!c[1]) reasons.push('我缺「开始打卡」');
      if (!c[2]) reasons.push('我缺「结束打卡」');
    }
    // f. 我的费用信息（全员约束：不论是否绑定商旅，费用均须填写且达标）
    if (myRow && !feeOk((entry.feeMap || {})[me.id])) reasons.push('我的费用信息未填写或不达标');
  } else if (myRow && !myRow.checked) reasons.push('我未打卡');
  if (myRow && !photos.some((p) => (p.members || []).includes(me.name))) {
    reasons.push('我未上传水印照片');
  }
  photos.forEach((p) => {
    if (!(p.members || []).includes(me.name)) return;
    photoIssues(p).forEach((t) => reasons.push(`我的水印照片${t}`));
  });
  return reasons;
}

module.exports = { computeVerifyPassed, computeFailReasons, myReportReasons, checkWatermark };
