// 派车单对齐：解析派车系统导出表（.xls/.xlsx，每个驾驶员一份、可多份合并），按「日期＋用车人」与当天出车卡片配对，
// 比对车牌号码、用车人集合与目的地，产出差异清单（本模块只读对齐不自动改；更正走既有 PUT /logs/{id}，表格有系统无的补建走 POST /logs，车牌入字典走 POST /admin/vehicles）
// 对齐口径（设计稿 design/worklog-dispatch-align.html 已审批）：
//   · 仅「派车单类型＝用车申请调度」的行参与（维保调度等忽略）
//   · 匹配键：日期（预计用车时间的日期部分）＋用车人（空格分隔姓名集合）
//   · 同日内按用车人重合度贪心配对（重合最多者成对，0 重合不配）
//   · 目的地模糊比对：表格目的地常省略「市/县」等行政区字样（如卡片「孝义市」表格写「吕梁市孝义」），
//     两侧去除「中国/省/市/县/区」后互相包含即视为一致（destSame）；表格目的地为空不约束
//   · 差异类型：车牌不一致 / 用车人多出·缺少 / 目的地不一致（matched）；表格有系统无（sheetOnly）；系统有表格无（entryOnly，仅出车卡片）
const XLSX = require('xlsx');
const { pool } = require('../db');

const DISPATCH_TYPE = '用车申请调度';
const DATE_RE = /^\d{4}-\d{2}-\d{2}$/;

// 必需表头列（按表头名定位列索引，兼容列序变化；「目的地」列名出现两次，取首个）
const REQUIRED = ['车牌号码', '驾驶员', '用车人', '预计用车时间', '目的地', '用车事由', '派车单类型', '派车单号'];

// 解析单份派车单 → { rows, skipped }；表头缺列抛错（消息带文件名）
function parseFile(buffer, fileName) {
  let rows;
  try {
    const wb = XLSX.read(buffer, { type: 'buffer' });
    rows = XLSX.utils.sheet_to_json(wb.Sheets[wb.SheetNames[0]], { header: 1, raw: false, defval: '' });
  } catch (e) {
    throw new Error(`「${fileName}」解析失败，请使用派车系统原始导出表`);
  }
  if (!rows.length) throw new Error(`「${fileName}」为空表`);

  const header = (rows[0] || []).map((c) => String(c).trim());
  const idx = {};
  for (const name of REQUIRED) {
    idx[name] = header.indexOf(name);
    if (idx[name] < 0) throw new Error(`「${fileName}」缺少必需列「${name}」，请使用派车系统原始导出表`);
  }

  const out = [];
  let skipped = 0;
  for (let i = 1; i < rows.length; i += 1) {
    const cells = (Array.isArray(rows[i]) ? rows[i] : []).map((c) => String(c).trim());
    if (cells.every((c) => !c)) continue; // 空行
    if (cells[idx['派车单类型']] !== DISPATCH_TYPE) continue; // 维保调度等非出工行忽略
    const date = (cells[idx['预计用车时间']] || '').slice(0, 10);
    if (!DATE_RE.test(date)) {
      skipped += 1; // 预计用车时间无法识别
      continue;
    }
    out.push({
      file: fileName,
      date,
      plate: cells[idx['车牌号码']],
      driver: cells[idx['驾驶员']],
      members: cells[idx['用车人']].split(/\s+/).filter(Boolean),
      from: '', // 出发地不参与对齐（设计稿仅展示目的地）
      to: cells[idx['目的地']],
      reason: cells[idx['用车事由']],
      orderNo: cells[idx['派车单号']],
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
            e.vehicle_id, v.plate_no, e.destination_id, d.name AS destination_name
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

// 对齐主流程：多文件合并解析 → 范围内出车卡片 → 按日配对 → 差异清单（按日期倒序）
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

  const dates = allRows.map((r) => r.date).sort();
  const entries = await loadEntries(team.id, dates[0], dates[dates.length - 1]);

  const byDate = {};
  allRows.forEach((r) => {
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
  for (const date of Object.keys(byDate)) {
    const { rows, entries: ents } = byDate[date];
    const { matched, sheetOnly, entryOnly } = pairOneDay(rows, ents);
    for (const { row, entry } of matched) {
      const entryNames = entry.memberList.map((m) => m.name);
      const missing = row.members.filter((n) => !entryNames.includes(n)); // 表格有、卡片缺
      const extra = entryNames.filter((n) => !row.members.includes(n)); // 卡片有、表格无
      const plateDiff = row.plate !== (entry.plate_no || '');
      const destDiff = !destSame(entry.destination_name, row.to);
      if (!plateDiff && !missing.length && !extra.length && !destDiff) {
        consistent += 1;
        continue;
      }
      if (plateDiff) plateCnt += 1;
      if (missing.length || extra.length) memberCnt += 1;
      if (destDiff) destCnt += 1;
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
          patrol_content: entry.patrol_content || '', // PUT 整卡更新需带回，避免误清巡视内容
          members: entry.memberList,
        },
        diffs: { plate: plateDiff, missing, extra, dest: destDiff },
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
      imported: allRows.length, // 参与对齐的有效行（已过滤非用车申请调度）
      skipped, // 预计用车时间无法识别而被跳过的行
      files: files.length,
      consistent,
      plate: plateCnt,
      members: memberCnt,
      destination: destCnt,
      sheetOnly: items.filter((i) => i.kind === 'sheetOnly').length,
      entryOnly: items.filter((i) => i.kind === 'entryOnly').length,
    },
    items,
  };
}

module.exports = { align, destSame };
