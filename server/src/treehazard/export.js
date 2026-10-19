// 树障隐患记录：统计表导出（exceljs）与排序/命名/线路顺序工具
// buildWorkbook 供路由导出与内网同步模块共用（同步模块据此产出内网表，记录ID 隐藏列是行级比对的锚点）；
// split=true 按线路分 sheet（仅人工导出用；内网表始终单 sheet，diff.js parseIntranetSheet 只读首个 sheet）
const ExcelJS = require('exceljs');

// 表头（第 2 行；第 1 行为「{班组}树木统计表」合并标题）；第 17 列（Q 列）为隐藏的记录ID 锚点列
const HEADERS = ['序号', '电压等级', '线路名称', '区段', '树种', '数量', '距小号侧距离（m）', '距大号侧距离（m）',
  '直径（cm）', '水平距离（m）', '垂直距离（m）', '发现人', '发现时间', '处理人', '处理情况', '处理时间'];
// 列宽（2026-10 定稿口径）
const COL_WIDTHS = [5, 10, 15, 15, 15, 10, 10, 10, 10, 10, 10, 15, 15, 15, 10, 15];
const ID_COL = 17; // Q 列

// 垂直距离解析值：取文本中首个数字（可带小数）；「3-4」→3、「超高2」→2；空/无数字 → null（ingest / diff 共用）
function parseVerticalNum(text) {
  const m = /(\d+(?:\.\d+)?)/.exec(String(text ?? ''));
  return m ? Number(m[1]) : null;
}

// 导出日期单元格文本：Date / 'YYYY-MM-DD' → 'yyyy.MM.dd'，空 → ''
function fmtExportDate(v) {
  if (!v) return '';
  if (v instanceof Date) {
    if (Number.isNaN(v.getTime())) return '';
    const y = v.getFullYear();
    const m = String(v.getMonth() + 1).padStart(2, '0');
    const d = String(v.getDate()).padStart(2, '0');
    return `${y}.${m}.${d}`;
  }
  const s = String(v).trim();
  const m = /^(\d{4})[-./](\d{1,2})[-./](\d{1,2})/.exec(s);
  if (!m) return s;
  return `${m[1]}.${m[2].padStart(2, '0')}.${m[3].padStart(2, '0')}`;
}

// 文件名非法字符清洗（Windows/COS 通用：\ / : * ? " < > | 与控制字符一律替换为 _）
function safeFileName(name) {
  const s = String(name ?? '').replace(/[\\/:*?"<>|]/g, '_').trim();
  // 控制字符（0x00-0x1f）一并替换为 _
  return s.replace(/[\s\S]/g, (ch) => (ch.charCodeAt(0) < 32 ? '_' : ch)) || '未命名';
}

// 导出文件名：分线路「{线路名}树木统计表.xlsx」，全量「{班组名}树木统计表.xlsx」
function exportFileName(teamName, lineName) {
  const base = lineName ? safeFileName(lineName) : safeFileName(teamName);
  return `${base}树木统计表.xlsx`;
}

// 线路顺序表：team_line 本班运维线路（sort 序）→ (voltage_level, line_name) 序列（路由与同步模块共用；
// 2026-10 起由 worklog_tower 聚合换源 team_line，与杆塔坐标解耦；空表 → 空序列，记录全部落 unmatched）
async function fetchLineOrder(pool, teamId) {
  const [rows] = await pool.query(
    'SELECT voltage_level, line_name FROM team_line WHERE team_id = ? ORDER BY sort, id',
    [teamId]
  );
  return rows.map((r) => ({ voltage_level: r.voltage_level, line_name: r.line_name }));
}

// 数值前缀（电压等级等按数值排序用；无数字 → Infinity 排尾）
function numPrefix(v) {
  const m = /(\d+(?:\.\d+)?)/.exec(String(v ?? ''));
  return m ? Number(m[1]) : Infinity;
}

// 记录排序（纯函数；调用方先做 lineName 过滤）：
// 电压等级数值升序 → 线路顺序（lineOrder=Map<line_name,index>，未匹配排尾、未匹配间按 line_name 中文排序）
// → status（unhandled 在前）→ pole_from 数值升序（NULL 排尾）→ created_at
function sortRecords(records, lineOrder) {
  const order = lineOrder instanceof Map ? lineOrder : new Map();
  const lineRank = (name) => (order.has(name) ? order.get(name) : Infinity);
  return [...records].sort((a, b) => {
    const v = numPrefix(a.voltage_level) - numPrefix(b.voltage_level);
    if (v) return v;
    const la = lineRank(a.line_name);
    const lb = lineRank(b.line_name);
    if (la !== lb) return la - lb;
    if (la === Infinity) {
      const c = String(a.line_name || '').localeCompare(String(b.line_name || ''), 'zh-CN');
      if (c) return c;
    }
    const sa = a.status === 'unhandled' ? 0 : 1;
    const sb = b.status === 'unhandled' ? 0 : 1;
    if (sa !== sb) return sa - sb;
    const pa = a.pole_from === null || a.pole_from === undefined ? Infinity : Number(a.pole_from);
    const pb = b.pole_from === null || b.pole_from === undefined ? Infinity : Number(b.pole_from);
    if (pa !== pb) return pa - pb;
    return new Date(a.created_at) - new Date(b.created_at);
  });
}

// 电压等级单元格幂等格式化：值已含 kV 不重复追加，纯数字才拼 kV，其余原样（读回剥后缀口径见 diff.js parseIntranetSheet）
function fmtVoltageCell(v) {
  const s = String(v ?? '').trim();
  if (!s) return '';
  if (/kv/i.test(s)) return s;
  return /^\d+(?:\.\d+)?$/.test(s) ? `${s}kV` : s;
}

// 单元格样式常量：全部写入单元格居中（含隐藏 ID 列）；数据区 A2:P末行 thin 框线（标题行 A1:P1 合并不加框线）
const CELL_CENTER = { horizontal: 'center', vertical: 'middle' };
// 数据行（R3 起）在居中基础上自动换行（长文本如处理情况不截断）；表头/标题行不换行
const CELL_CENTER_WRAP = { horizontal: 'center', vertical: 'middle', wrapText: true };
const THIN_BORDER = {
  top: { style: 'thin' }, left: { style: 'thin' }, bottom: { style: 'thin' }, right: { style: 'thin' },
};

// 单个 worksheet 写入完整模板：R1 合并标题（行高 45 磅/字号 20/加粗居中，不加框线）、R2 表头（35 磅/12 加粗）、
// 数据行（25 磅/11），表头与数据区居中 + thin 框线，Q 列隐藏记录ID（行级比对锚点）
function writeSheet(wb, { name, title, records }) {
  const ws = wb.addWorksheet(name);
  ws.columns = [
    ...COL_WIDTHS.map((width) => ({ width })),
    { header: '记录ID', key: 'rid', width: 10, hidden: true }, // Q 列：隐藏
  ];

  // 第 1 行：合并标题
  ws.mergeCells(1, 1, 1, HEADERS.length);
  ws.getRow(1).height = 45;
  const titleCell = ws.getCell(1, 1);
  titleCell.value = title;
  titleCell.font = { bold: true, size: 20 };
  titleCell.alignment = CELL_CENTER;

  // 第 2 行：表头
  const headRow = ws.getRow(2);
  headRow.height = 35;
  HEADERS.forEach((h, i) => {
    const c = headRow.getCell(i + 1);
    c.value = h;
    c.alignment = CELL_CENTER;
    c.border = THIN_BORDER;
  });
  headRow.font = { bold: true, size: 12 };
  headRow.commit();

  // 数据自第 3 行：全部单元格按文本写入（numFmt '@'），居中且自动换行（长文本不截断），仅 Q 列记录ID 写数值
  records.forEach((r, i) => {
    const row = ws.getRow(i + 3);
    row.height = 25;
    const cells = [
      String(i + 1),
      fmtVoltageCell(r.voltage_level),
      String(r.line_name ?? ''),
      String(r.section ?? ''),
      String(r.tree_species ?? ''),
      String(r.tree_count ?? ''),
      String(r.dist_small ?? ''),
      String(r.dist_big ?? ''),
      String(r.diameter ?? ''),
      String(r.horizontal ?? ''),
      String(r.vertical ?? ''),
      String(r.finder_name ?? ''),
      fmtExportDate(r.found_at),
      String(r.handler_name ?? ''),
      String(r.handle_note ?? ''),
      fmtExportDate(r.handled_at),
    ];
    cells.forEach((v, j) => {
      const c = row.getCell(j + 1);
      c.value = v;
      c.numFmt = '@';
      c.alignment = CELL_CENTER_WRAP;
      c.border = THIN_BORDER;
    });
    const idCell = row.getCell(ID_COL);
    idCell.value = Number(r.id) || null;
    idCell.alignment = CELL_CENTER_WRAP;
    row.font = { size: 11 };
    row.commit();
  });
}

// 分表归组：records（已 sortRecords 排序）按 line_name 保序归组，组序=首现序（线路顺序 + 未匹配排尾）；
// lineOrder 未覆盖的记录归末尾「未匹配线路」组（unmatched 标记）
function groupByLine(records, lineOrder) {
  const order = lineOrder instanceof Map ? lineOrder : new Map();
  const groups = []; // [{ lineName, voltage, records, unmatched }]
  const byName = new Map();
  const unmatched = { lineName: '未匹配线路', voltage: '', records: [], unmatched: true };
  for (const r of records || []) {
    const name = String(r.line_name ?? '');
    if (order.has(name)) {
      if (!byName.has(name)) {
        const g = { lineName: name, voltage: r.voltage_level, records: [], unmatched: false };
        byName.set(name, g);
        groups.push(g);
      }
      byName.get(name).records.push(r);
    } else {
      unmatched.records.push(r);
    }
  }
  if (unmatched.records.length) groups.push(unmatched);
  return groups;
}

// sheet 名清洗（Excel 约束）：禁字符 [ ] : * ? / \ 换全角、≤31 字符、空名兜底「未命名线路」、重名追加序号（n）
const SHEET_FULLWIDTH = { '[': '【', ']': '】', ':': '：', '*': '＊', '?': '？', '/': '／', '\\': '＼' };
function sheetNameOf(base, used) {
  let name = String(base ?? '').replace(/[[\]:*?/\\]/g, (ch) => SHEET_FULLWIDTH[ch]).trim();
  if (!name) name = '未命名线路';
  name = name.slice(0, 31);
  if (!used.has(name)) {
    used.add(name);
    return name;
  }
  for (let i = 2; ; i += 1) {
    const suffix = `（${i}）`;
    const cand = `${name.slice(0, 31 - suffix.length)}${suffix}`;
    if (!used.has(cand)) {
      used.add(cand);
      return cand;
    }
  }
}

// 生成统计表 workbook（records 须已由 sortRecords 排好序）：
// split=false（默认）单 sheet「树障」，标题「{班组}树木统计表」；
// split=true 按线路分 sheet（lineOrder 必传）：sheet 名 = fmtVoltageCell(电压)+线路名（幂等 kV，Excel 名约束清洗），
// 每 sheet 完整模板、序号自 1 重排，标题「{电压等级}{线路名称}树木统计表」（与分线路导出文件名口径一致），
// 未匹配线路记录归末尾「未匹配线路」sheet（标题同为「未匹配线路」）
async function buildWorkbook({ teamName, records, split = false, lineOrder }) {
  const wb = new ExcelJS.Workbook();
  if (!split) {
    writeSheet(wb, { name: '树障', title: `${teamName}树木统计表`, records });
    return wb;
  }
  const groups = groupByLine(records, lineOrder);
  if (!groups.length) {
    writeSheet(wb, { name: '树障', title: `${teamName}树木统计表`, records: [] }); // 无记录兜底：空表保持单 sheet
    return wb;
  }
  const used = new Set();
  for (const g of groups) {
    const label = g.unmatched ? '未匹配线路' : `${fmtVoltageCell(g.voltage)}${g.lineName}`;
    writeSheet(wb, {
      name: sheetNameOf(label, used),
      title: g.unmatched ? '未匹配线路' : `${label}树木统计表`,
      records: g.records,
    });
  }
  return wb;
}

module.exports = { buildWorkbook, sortRecords, exportFileName, fetchLineOrder, parseVerticalNum, safeFileName };
