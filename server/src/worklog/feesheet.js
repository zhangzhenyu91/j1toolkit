// 出差费用汇总表生成：按日期范围把出车卡片渲染为「人 × 日」费用矩阵 docx（一张卡片一行、一个用车人一列、末尾合计行）
// 模板：server/assets/worklog/fee-summary-template.docx（A4 横向单表，含 3 个样板行：表头 / 数据 / 合计；
// 表头首格为 WPS 斜线单元格「出差任务单日期 / 金额 / 姓名」，克隆时只改列宽不动文字；
// 表格固定定位不居中：tblpX=1600 / tblpY=1800（距页面左缘 / 页顶，2026-08 与管理员确认）
// 生成方式：pizzip 读模板 document.xml，克隆样板单元格拼行——列数随人数动态，docxtemplater 不适用，直接拼 XML
// 列宽口径（2026-08 与管理员确认）：人名 ≤7 时人名列统一 1631 dxa（首列 2220 固定）——
// 该值使 7 人时表格右缘距页面右缘最接近 1600（页宽 16838 − 距左 1600 − 日期列 2220 − 7×1631 = 1601）；
// 人名 >7 时人名列总宽保持 7 人时的 11417 dxa（表格总宽 13637 不变），人名列均分（余数并入末列）
const fs = require('fs');
const path = require('path');
const PizZip = require('pizzip');
const { pool } = require('../db');

const TEMPLATE_PATH = path.join(__dirname, '../../assets/worklog/fee-summary-template.docx');

// 费用倍率：单元格文案 {费用}×{倍率}={计算值}；当前业务恒为 1，未来出现非 ×1 场景改此处即可
const FEE_MULTIPLIER = 1;

const DATE_COL_W = 2220; // 首列（日期列）宽，dxa
const PAGE_W = 16838; // A4 横向页宽，dxa
const TABLE_X = 1600; // 表格距页面左缘（与模板 tblpX 一致）
const EDGE_GAP = 1600; // 7 人时表格右缘距页面右缘的目标间距
const MAX_FIXED_PERSONS = 7; // 人名 ≤7 用统一宽；>7 保持 7 人总宽均分
// 7 人时人名列统一宽：使表格右缘距页面右缘最接近 1600 的整数解（1631 → 实际间距 1601）
const PERSON_COL_W = Math.round((PAGE_W - TABLE_X - EDGE_GAP - DATE_COL_W) / MAX_FIXED_PERSONS);
// 7 人（8 列）时的表格总宽：13637 dxa；>7 人时人名列总宽不变、均分
const TOTAL_W_MAX_FIXED = DATE_COL_W + PERSON_COL_W * MAX_FIXED_PERSONS;

// 人名列宽：≤7 人统一 1631；>7 人保持 7 人总宽、人名列均分（余数并入末列）
function personColWidths(n) {
  if (n <= MAX_FIXED_PERSONS) return new Array(n).fill(PERSON_COL_W);
  const share = TOTAL_W_MAX_FIXED - DATE_COL_W;
  const base = Math.floor(share / n);
  const widths = new Array(n).fill(base);
  widths[n - 1] += share - base * n;
  return widths;
}

function escXml(s) {
  return String(s).replace(/[&<>"']/g, (ch) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&apos;',
  }[ch]));
}

// 克隆样板单元格并重写列宽与文本；text 传 null 保留样板原文（斜线表头格 / 合计格），传 '' 置空
function fillCell(tpl, width, text) {
  let tc = tpl.replace(/(<w:tcW w:w=")\d+(")/, `$1${width}$2`);
  if (text !== null) {
    tc = tc.replace(/<w:t(?:\s[^>]*)?>[\s\S]*?<\/w:t>/, text ? `<w:t>${escXml(text)}</w:t>` : '<w:t></w:t>');
  }
  return tc;
}

// 取范围内出车卡片（行）、出现过的用车人（列，按成员字典点亮顺序）与每人每日费用（伙食补助+交通费）
async function loadData(teamId, from, to) {
  const [entries] = await pool.query(
    `SELECT e.id, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date
     FROM worklog_entry e
     WHERE e.log_date BETWEEN ? AND ? AND e.team_id = ? AND e.vehicle_id IS NOT NULL
     ORDER BY e.log_date, e.created_at, e.id`,
    [from, to, teamId]
  );
  if (!entries.length) return null;

  // 列 = 范围内当过用车人的成员（未出现者不参与排列），按字典点亮顺序 sort
  const [persons] = await pool.query(
    `SELECT DISTINCT m.id, m.name, m.sort
     FROM worklog_entry_member em
     JOIN worklog_member m ON m.id = em.member_id
     JOIN worklog_entry e ON e.id = em.entry_id
     WHERE e.log_date BETWEEN ? AND ? AND e.team_id = ? AND e.vehicle_id IS NOT NULL
     ORDER BY m.sort, m.id`,
    [from, to, teamId]
  );

  const ids = entries.map((e) => e.id);
  const [members] = await pool.query(
    'SELECT entry_id, member_id FROM worklog_entry_member WHERE entry_id IN (?)',
    [ids]
  );
  const memberSet = {};
  members.forEach((m) => {
    (memberSet[m.entry_id] = memberSet[m.entry_id] || new Set()).add(m.member_id);
  });

  // 费用：每人每日一条（worklog_fee，商旅同步）；键 = member_id|fee_date
  const [fees] = await pool.query(
    `SELECT member_id, DATE_FORMAT(fee_date, '%Y-%m-%d') AS fee_date, food_fee, transit_fee
     FROM worklog_fee WHERE team_id = ? AND fee_date BETWEEN ? AND ?`,
    [teamId, from, to]
  );
  const feeMap = {};
  fees.forEach((f) => {
    feeMap[`${f.member_id}|${f.fee_date}`] = Number(f.food_fee) + Number(f.transit_fee);
  });

  return { entries, persons, memberSet, feeMap };
}

// 拼表格 XML：tblGrid + 表头行 + 数据行 ×N + 合计行
function renderXml(xml, data) {
  const tbl = xml.match(/<w:tbl>[\s\S]*?<\/w:tbl>/);
  if (!tbl) throw new Error('模板缺少 w:tbl');
  const tblPr = tbl[0].match(/<w:tblPr>[\s\S]*?<\/w:tblPr>/);
  const trs = tbl[0].match(/<w:tr[ >][\s\S]*?<\/w:tr>/g);
  if (!tblPr || !trs || trs.length !== 3) throw new Error('费用汇总模板应为 3 个样板行（表头/数据/合计）');
  const trPrefix = (tr) => tr.match(/^<w:tr[^>]*>(?:<w:trPr>[\s\S]*?<\/w:trPr>)?/)[0];
  const cellsOf = (tr) => tr.match(/<w:tc>[\s\S]*?<\/w:tc>/g);
  const [headCells, rowCells, sumCells] = trs.map(cellsOf);

  const { persons, entries, memberSet, feeMap } = data;
  const widths = personColWidths(persons.length);
  const totalW = DATE_COL_W + widths.reduce((a, b) => a + b, 0);
  const grid = `<w:tblGrid>${[DATE_COL_W, ...widths].map((w) => `<w:gridCol w:w="${w}"/>`).join('')}</w:tblGrid>`;
  const tblPrNew = tblPr[0].replace(/(<w:tblW w:w=")\d+(")/, `$1${totalW}$2`);

  // 表头：首格为斜线单元格（只改列宽保留原文），其后一个用车人一列
  const head = trPrefix(trs[0])
    + fillCell(headCells[0], DATE_COL_W, null)
    + persons.map((p, i) => fillCell(headCells[1], widths[i], p.name)).join('')
    + '</w:tr>';

  // 数据行：一卡一行，日期（M月D日）入第 1 列；用车人列填 {费用}×{倍率}={计算值}，非用车人列留空
  const totals = new Array(persons.length).fill(0);
  const rows = entries.map((e) => {
    const [, m, d] = e.log_date.split('-');
    const inEntry = memberSet[e.id] || new Set();
    const cells = persons.map((p, i) => {
      if (!inEntry.has(p.id)) return fillCell(rowCells[1], widths[i], '');
      const fee = feeMap[`${p.id}|${e.log_date}`];
      if (fee == null) return fillCell(rowCells[1], widths[i], ''); // 防御：生成前核验已拦截（规则 f）
      const calc = fee * FEE_MULTIPLIER;
      totals[i] += calc;
      return fillCell(rowCells[1], widths[i], `${fee}×${FEE_MULTIPLIER}=${calc}`);
    });
    return trPrefix(trs[1]) + fillCell(rowCells[0], DATE_COL_W, `${Number(m)}月${Number(d)}日`) + cells.join('') + '</w:tr>';
  });

  // 合计行：第 1 列「合计」保留样板原文，各列填本列计算值之和
  const sum = trPrefix(trs[2])
    + fillCell(sumCells[0], DATE_COL_W, null)
    + totals.map((t, i) => fillCell(sumCells[1], widths[i], String(t))).join('')
    + '</w:tr>';

  const newTbl = `<w:tbl>${tblPrNew}${grid}${head}${rows.join('')}${sum}</w:tbl>`;
  return xml.replace(tbl[0], newTbl);
}

// 生成范围内的出差费用汇总 docx；无出车卡片返回 null
async function build(team, from, to) {
  const data = await loadData(team.id, from, to);
  if (!data) return null;
  if (!data.persons.length) return null; // 出车卡片必有用车人才产生列；防御（正常已被生成前核验拦截）
  const zip = new PizZip(fs.readFileSync(TEMPLATE_PATH));
  zip.file('word/document.xml', renderXml(zip.file('word/document.xml').asText(), data));
  return { buffer: zip.generate({ type: 'nodebuffer', compression: 'DEFLATE' }), count: data.entries.length };
}

module.exports = { build };
