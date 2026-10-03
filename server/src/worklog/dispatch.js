// 派车单对齐：解析派车系统导出表（.xls/.xlsx，每个驾驶员一份、可多份合并），按「日期＋用车人」与当天出车卡片配对，
// 比对车牌号码、用车人集合、目的地与派车单号，产出差异清单（本模块只读对齐不自动改；更正走既有 PUT /logs/{id}，表格有系统无的补建走 POST /logs，车牌入字典走 POST /admin/vehicles）
// 对齐口径（详见《开发指南》7.6）：
//   · 仅「派车单类型＝用车申请调度」的行参与（维保调度等忽略）
//   · 导入行再按班组字典过滤：用车人中无本班成员（启用中）的行整行剔除（他班派车混入不进清单），全剔空报 400
//   · 匹配键：日期（预计用车时间只取到天，忽略时分配对）＋用车人（空格分隔姓名集合）
//   · 同日内按用车人重合度贪心配对（重合最多者成对；有 1 个对上即匹配，0 重合不配）
//   · 目的地模糊比对：表格目的地常省略「市/县」等行政区字样（如卡片「孝义市」表格写「吕梁市孝义」），
//     两侧去除「中国/省/市/县/区」后互相包含即视为一致（destSame）；表格目的地为空不约束
//   · 派车单号比对：卡片 dispatch_order_no vs 表格派车单号精确比对；表格单号为空不约束（同目的地口径）
//   · 差异类型：车牌不一致 / 用车人多出·缺少 / 目的地不一致 / 派车单号不一致（matched）；表格有系统无（sheetOnly）；系统有表格无（entryOnly，仅出车卡片）
const XLSX = require('xlsx');
const { pool } = require('../db');

const DISPATCH_TYPE = '用车申请调度';

// 预计用车时间 → YYYY-MM-DD（只取到天，忽略时分）。
// 导出表该列格式不固定（2026-08-19 14:30 / 2026/8/19 / 2026年8月19日 / Excel 序列数），
// 仅按 YYYY-MM-DD 前缀截取会把这些误判为「无法识别」而跳过整行，故逐格式归一：
// ① 年[-/年.]月[-/月.]日 文本；② 5 位 Excel 序列数（单元格为 General 时导出为数字文本，SSF 口径含 1900 闰年修正）
function parseSheetDate(raw) {
  const s = String(raw || '').trim();
  if (!s) return '';
  const m = /(\d{4})\s*[-/年.]\s*(\d{1,2})\s*[-/月.]\s*(\d{1,2})/.exec(s);
  if (m) return `${m[1]}-${m[2].padStart(2, '0')}-${m[3].padStart(2, '0')}`;
  if (/^\d{5}(\.\d+)?$/.test(s)) {
    const dc = XLSX.SSF.parse_date_code(Number(s));
    if (dc) return `${dc.y}-${String(dc.m).padStart(2, '0')}-${String(dc.d).padStart(2, '0')}`;
  }
  return '';
}

// 必需列：关键四列固定列位（用户指定口径，严格遵循，不按表头名定位）——B=车牌号 / F=用车人 / G=预计用车时间 / K=目的地；
// 辅助列（驾驶员/用车事由/派车单号/派车单类型）仅展示与过滤用，仍按表头名定位（缺列置空，派车单类型缺失才报错——行过滤无从谈起）
const COL = { plate: 1, members: 5, time: 6, to: 10 }; // 0 基列索引：B/F/G/K
const AUX = ['驾驶员', '用车事由', '派车单号', '派车单类型'];

// 解析单份派车单 → { rows, skipped }；缺「派车单类型」表头列抛错（消息带文件名）
function parseFile(buffer, fileName) {
  let rows;
  try {
    const wb = XLSX.read(buffer, { type: 'buffer' });
    rows = XLSX.utils.sheet_to_json(wb.Sheets[wb.SheetNames[0]], { header: 1, raw: false, defval: '' });
  } catch (e) {
    const err = new Error(`「${fileName}」解析失败，请使用派车系统原始导出表`);
    err.status = 400; // 路由仅把 400 转友好文案，其余按 500
    throw err;
  }
  if (!rows.length) {
    const err = new Error(`「${fileName}」为空表`);
    err.status = 400;
    throw err;
  }

  const header = (rows[0] || []).map((c) => String(c).trim());
  const aux = {};
  for (const name of AUX) aux[name] = header.indexOf(name);
  if (aux['派车单类型'] < 0) {
    const err = new Error(`「${fileName}」缺少必需列「派车单类型」，请使用派车系统原始导出表`);
    err.status = 400;
    throw err;
  }

  const out = [];
  let skipped = 0;
  for (let i = 1; i < rows.length; i += 1) {
    const cells = (Array.isArray(rows[i]) ? rows[i] : []).map((c) => String(c).trim());
    if (cells.every((c) => !c)) continue; // 空行
    if (cells[aux['派车单类型']] !== DISPATCH_TYPE) continue; // 维保调度等非出工行忽略
    const date = parseSheetDate(cells[COL.time]); // G 列，只取到天，忽略时分
    if (!date) {
      skipped += 1; // 预计用车时间无法识别
      continue;
    }
    out.push({
      file: fileName,
      date,
      plate: cells[COL.plate] || '', // B 列
      driver: aux['驾驶员'] >= 0 ? cells[aux['驾驶员']] : '',
      members: String(cells[COL.members] || '').split(/\s+/).filter(Boolean), // F 列
      from: '', // 出发地不参与对齐（设计稿仅展示目的地）
      to: cells[COL.to] || '', // K 列
      reason: aux['用车事由'] >= 0 ? cells[aux['用车事由']] : '',
      orderNo: aux['派车单号'] >= 0 ? cells[aux['派车单号']] : '',
    });
  }
  return { rows: out, skipped };
}

// 目的地模糊一致：表格目的地常省略行政区字样或带上级前缀（如卡片「孝义市」表格写「吕梁市孝义」、
// 卡片「交城县」表格写「吕梁市交城」），两侧去除「中国/省/市/县/区」后互相包含即视为一致；
// 归一化后不足 2 字回退原文包含（防单字误配）；表格目的地为空不约束，表格有而卡片未选 → 不一致
function destSame(cardDest, sheetTo) {
  const card = String(cardDest || '').trim();
  const sheet = String(sheetTo || '').trim();
  if (!sheet) return true;
  if (!card) return false;
  if (card === sheet || card.includes(sheet) || sheet.includes(card)) return true;
  const nc = card.replace(/中国|省|市|县|区/g, '');
  const ns = sheet.replace(/中国|省|市|县|区/g, '');
  if (nc.length < 2 || ns.length < 2) return false;
  return nc.includes(ns) || ns.includes(nc);
}

// 取日期范围内全部出车卡片（未出车卡片无车牌/用车人，不参与对齐）
async function loadEntries(teamId, minDate, maxDate) {
  const [entries] = await pool.query(
    `SELECT e.id, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, e.patrol_content,
            e.vehicle_id, v.plate_no, e.destination_id, d.name AS destination_name, e.dispatch_order_no
     FROM worklog_entry e
     LEFT JOIN worklog_vehicle v ON v.id = e.vehicle_id
     LEFT JOIN worklog_destination d ON d.id = e.destination_id
     WHERE e.team_id = ? AND e.log_date BETWEEN ? AND ? AND e.vehicle_id IS NOT NULL
     ORDER BY e.created_at, e.id`,
    [teamId, minDate, maxDate]
  );
  if (!entries.length) return [];
  const ids = entries.map((e) => e.id);
  const [members] = await pool.query(
    `SELECT em.entry_id, em.member_id, m.name
     FROM worklog_entry_member em JOIN worklog_member m ON m.id = em.member_id
     WHERE em.entry_id IN (?) ORDER BY em.sort, em.id`,
    [ids]
  );
  const memberMap = {};
  members.forEach((m) => {
    (memberMap[m.entry_id] = memberMap[m.entry_id] || []).push({ member_id: m.member_id, name: m.name });
  });
  entries.forEach((e) => {
    e.memberList = memberMap[e.id] || [];
  });
  return entries;
}

// 同日内贪心配对：候选对按用车人重合人数降序，依次成对（双方均未配对时生效）；0 重合不配对
function pairOneDay(rows, entries) {
  const pairs = [];
  for (let r = 0; r < rows.length; r += 1) {
    for (let e = 0; e < entries.length; e += 1) {
      const names = new Set(entries[e].memberList.map((m) => m.name));
      const overlap = rows[r].members.filter((n) => names.has(n)).length;
      if (overlap > 0) pairs.push({ r, e, overlap });
    }
  }
  pairs.sort((a, b) => b.overlap - a.overlap || a.r - b.r || a.e - b.e);
  const rowUsed = new Set();
  const entryUsed = new Set();
  const matched = [];
  for (const p of pairs) {
    if (rowUsed.has(p.r) || entryUsed.has(p.e)) continue;
    rowUsed.add(p.r);
    entryUsed.add(p.e);
    matched.push({ row: rows[p.r], entry: entries[p.e] });
  }
  return {
    matched,
    sheetOnly: rows.filter((_, i) => !rowUsed.has(i)),
    entryOnly: entries.filter((_, i) => !entryUsed.has(i)),
  };
}

// 对齐主流程：多文件合并解析 → 剔除非本班用车人行 → 范围内出车卡片 → 按日配对 → 差异清单（按日期倒序）
async function align(team, files) {
  const allRows = [];
  let skipped = 0;
  for (const f of files) {
    const r = parseFile(f.buffer, f.name);
    allRows.push(...r.rows);
    skipped += r.skipped;
  }
  if (!allRows.length) {
    const err = new Error('未识别到有效派车行（仅「派车单类型＝用车申请调度」的行参与对齐）');
    err.status = 400;
    throw err;
  }

  // 剔除「用车人中无本班成员（启用中字典）」的行：他班派车混入导出件时不进清单，剔除数入 summary.outside
  const [memRows] = await pool.query('SELECT name FROM worklog_member WHERE status = 1 AND team_id = ?', [team.id]);
  const teamNames = new Set(memRows.map((m) => m.name));
  const teamRows = allRows.filter((r) => r.members.some((n) => teamNames.has(n)));
  const outside = allRows.length - teamRows.length;
  if (!teamRows.length) {
    const err = new Error(`导入行剔除后无剩余：全部 ${allRows.length} 行的用车人均不含本班成员，请确认导出件属于本班组`);
    err.status = 400;
    throw err;
  }

  const dates = teamRows.map((r) => r.date).sort();
  const entries = await loadEntries(team.id, dates[0], dates[dates.length - 1]);

  const byDate = {};
  teamRows.forEach((r) => {
    (byDate[r.date] = byDate[r.date] || { rows: [], entries: [] }).rows.push(r);
  });
  entries.forEach((e) => {
    (byDate[e.log_date] = byDate[e.log_date] || { rows: [], entries: [] }).entries.push(e);
  });

  const items = [];
  let consistent = 0;
  let plateCnt = 0;
  let memberCnt = 0;
  let destCnt = 0;
  let orderNoCnt = 0;
  for (const date of Object.keys(byDate)) {
    const { rows, entries: ents } = byDate[date];
    const { matched, sheetOnly, entryOnly } = pairOneDay(rows, ents);
    for (const { row, entry } of matched) {
      const entryNames = entry.memberList.map((m) => m.name);
      const missing = row.members.filter((n) => !entryNames.includes(n)); // 表格有、卡片缺
      const extra = entryNames.filter((n) => !row.members.includes(n)); // 卡片有、表格无
      const plateDiff = row.plate !== (entry.plate_no || '');
      const destDiff = !destSame(entry.destination_name, row.to);
      // 派车单号比对：表格单号为空不约束（同目的地口径）；更正走对照弹层直接改（PUT /logs/{id} 带 dispatch_order_no）
      const orderNoDiff = row.orderNo ? row.orderNo !== (entry.dispatch_order_no || '') : false;
      if (!plateDiff && !missing.length && !extra.length && !destDiff && !orderNoDiff) {
        consistent += 1;
        continue;
      }
      if (plateDiff) plateCnt += 1;
      if (missing.length || extra.length) memberCnt += 1;
      if (destDiff) destCnt += 1;
      if (orderNoDiff) orderNoCnt += 1;
      items.push({
        kind: 'matched',
        date,
        sheet: row,
        entry: {
          id: entry.id,
          vehicle_id: entry.vehicle_id,
          plate: entry.plate_no || '',
          destination_id: entry.destination_id,
          destination: entry.destination_name || '',
          order_no: entry.dispatch_order_no || '',
          patrol_content: entry.patrol_content || '', // PUT 整卡更新需带回，避免误清巡视内容
          members: entry.memberList,
        },
        diffs: { plate: plateDiff, missing, extra, dest: destDiff, orderNo: orderNoDiff },
      });
    }
    sheetOnly.forEach((row) => items.push({ kind: 'sheetOnly', date, sheet: row, entry: null }));
    entryOnly.forEach((entry) =>
      items.push({
        kind: 'entryOnly',
        date,
        sheet: null,
        entry: {
          id: entry.id,
          plate: entry.plate_no || '',
          destination: entry.destination_name || '',
          members: entry.memberList,
        },
      })
    );
  }
  items.sort((a, b) => (a.date < b.date ? 1 : a.date > b.date ? -1 : 0));

  return {
    summary: {
      imported: teamRows.length, // 参与对齐的有效行（已过滤非用车申请调度、非本班用车人）
      skipped, // 预计用车时间无法识别而被跳过的行
      outside, // 用车人中无本班成员被剔除的行（他班派车混入）
      files: files.length,
      consistent,
      plate: plateCnt,
      members: memberCnt,
      destination: destCnt,
      orderNo: orderNoCnt, // 派车单号不一致（表格单号为空不约束）
      sheetOnly: items.filter((i) => i.kind === 'sheetOnly').length,
      entryOnly: items.filter((i) => i.kind === 'entryOnly').length,
    },
    items,
  };
}

module.exports = { align, destSame, loadEntries };
