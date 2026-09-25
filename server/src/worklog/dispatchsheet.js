// 出工日志 · 派车汇总（超管 / 班组管理员）：范围内出车卡片按「派车单号非空」导出为极简 xlsx
// 两列「派车单号 / 用车日期」（用车日期=卡片日志日期；不取预计用车时间——同步只落当日单，且排序已统一为卡片顺序）；
// 数据源 worklog_entry.dispatch_order_no（每日同步带入，改派车可改）；
// 排序与工作任务单/费用汇总完全同口径：ORDER BY log_date, created_at, id（日期 + 卡片创建序）
const XLSX = require('xlsx');
const { pool } = require('../db');

// 范围内已填派车单号的出车卡片（未出车卡无派车单，天然排除）
async function loadRows(teamId, from, to) {
  const [rows] = await pool.query(
    `SELECT e.dispatch_order_no AS order_no, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date
     FROM worklog_entry e
     WHERE e.team_id = ? AND e.log_date BETWEEN ? AND ?
       AND e.vehicle_id IS NOT NULL
       AND e.dispatch_order_no IS NOT NULL AND e.dispatch_order_no <> ''
     ORDER BY e.log_date, e.created_at, e.id`,
    [teamId, from, to]
  );
  return rows;
}

// build(teamId, from, to) → null（范围内无派车单记录）| { buffer }
async function build(teamId, from, to) {
  const rows = await loadRows(teamId, from, to);
  if (!rows.length) return null;
  const ws = XLSX.utils.aoa_to_sheet([
    ['派车单号', '用车日期'],
    ...rows.map((r) => [r.order_no, r.log_date]),
  ]);
  ws['!cols'] = [{ wch: 24 }, { wch: 14 }];
  const wb = XLSX.utils.book_new();
  XLSX.utils.book_append_sheet(wb, ws, '派车汇总');
  return { buffer: XLSX.write(wb, { type: 'buffer', bookType: 'xlsx' }) };
}

module.exports = { build };
