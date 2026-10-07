// 工作任务单生成：把日期范围内全部出车卡片渲染进 Word 模板并合并为单个 docx（一卡一页，按日期+创建序排列），供管理员打印
// 模板：server/assets/worklog/task-sheet-template.docx（A4 横向，纯文本 {占位符}，docxtemplater 渲染，
// 兼容占位符被 Word 拆散到多个 run 的情况）；占位符口径见《开发指南》出工日志章
// 合并口径：各卡片同模板渲染产物 styles/fontTable/footer 完全一致、body 自包含（无图片无新增关系），
// 故以首份为底、后续文档 body（去掉 sectPr）依次追加、首段加 pageBreakBefore 分页即可，无需重映射关系
const fs = require('fs');
const path = require('path');
const PizZip = require('pizzip');
const Docxtemplater = require('docxtemplater');
const { pool } = require('../db');

const TEMPLATE_PATH = path.join(__dirname, '../../assets/worklog/task-sheet-template.docx');

// 渲染单条卡片为完整 docx buffer；record 各字段先转成字符串（undefined/null 一律空串，避免模板漏填报错）
function renderOne(tplBuf, record) {
  const zip = new PizZip(tplBuf);
  const doc = new Docxtemplater(zip, { paragraphLoop: true, linebreaks: true });
  doc.render(record);
  return doc.getZip().generate({ type: 'nodebuffer', compression: 'DEFLATE' });
}

// 分页口径：不用独立分页符段落（其段落标记会占用新页首行，把整页高的卡片内容往下挤一行，
// 导致卡片末行溢出、两卡之间出现近乎空白的页——已踩坑）；改为给追加卡片的首段加
// pageBreakBefore（该段从新页页首开始；已在新页页首时幂等不再断页，且不占行高）
function withPageBreakBefore(body) {
  const pi = body.indexOf('<w:p');
  if (pi === -1) return body; // 首元素非段落（如直接是表格），不加断页（模板结构如此则分页由调用方自查）
  const gt = body.indexOf('>', pi);
  const openTag = body.slice(pi, gt + 1);
  const rest = body.slice(gt + 1);
  if (rest.startsWith('<w:pPr>')) {
    return body.slice(0, pi) + openTag + '<w:pPr><w:pageBreakBefore/>' + rest.slice(7);
  }
  return body.slice(0, pi) + openTag + '<w:pPr><w:pageBreakBefore/></w:pPr>' + rest;
}

// 提取 document.xml 的 body 内容（去掉结尾 sectPr，页面设置以首份为准）
function bodyContent(xml) {
  const m = xml.match(/<w:body>([\s\S]*)<\/w:body>/);
  if (!m) throw new Error('模板缺少 w:body');
  return m[1].replace(/<w:sectPr[\s\S]*?<\/w:sectPr>\s*$/, '');
}

// 合并多份同模板 docx：首份为底，其余 body 首段加 pageBreakBefore 后依次追加到结尾 sectPr 之前
function mergeDocx(buffers) {
  const base = new PizZip(buffers[0]);
  let xml = base.file('word/document.xml').asText();
  const injection = buffers
    .slice(1)
    .map((buf) => withPageBreakBefore(bodyContent(new PizZip(buf).file('word/document.xml').asText())))
    .join('');
  if (injection) {
    xml = xml.replace(/<w:sectPr[\s\S]*?<\/w:sectPr>/, (sect) => injection + sect);
    base.file('word/document.xml', xml);
  }
  return base.generate({ type: 'nodebuffer', compression: 'DEFLATE' });
}

// 取范围内出车卡片集合（未出车卡片无用车人/目的地，不生成任务单）：车牌 + 目的地 + 用车人 + 照片施工内容
async function loadSheetRows(teamId, from, to) {
  const [entries] = await pool.query(
    `SELECT e.id, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, v.plate_no, d.name AS destination_name
     FROM worklog_entry e
     LEFT JOIN worklog_vehicle v ON v.id = e.vehicle_id
     LEFT JOIN worklog_destination d ON d.id = e.destination_id
     WHERE e.log_date BETWEEN ? AND ? AND e.team_id = ? AND e.vehicle_id IS NOT NULL
     ORDER BY e.log_date, e.created_at, e.id`,
    [from, to, teamId]
  );
  if (!entries.length) return [];

  const ids = entries.map((e) => e.id);
  // 用车人顺序 = 成员字典「点亮按钮顺序」（em.sort 存的是 worklog_member.sort，非点击顺序）
  const [members] = await pool.query(
    `SELECT em.entry_id, m.name
     FROM worklog_entry_member em JOIN worklog_member m ON m.id = em.member_id
     WHERE em.entry_id IN (?) ORDER BY em.sort, em.id`,
    [ids]
  );
  // 施工内容来自水印照片（Dify 识别 title），同卡片多张照片去重、按上传先后拼接
  const [photos] = await pool.query(
    `SELECT entry_id, work_content FROM worklog_photo
     WHERE entry_id IN (?) AND work_content <> '' ORDER BY id`,
    [ids]
  );

  const memberMap = {};
  members.forEach((m) => {
    (memberMap[m.entry_id] = memberMap[m.entry_id] || []).push(m.name);
  });
  const contentMap = {};
  photos.forEach((p) => {
    const arr = (contentMap[p.entry_id] = contentMap[p.entry_id] || []);
    if (!arr.includes(p.work_content)) arr.push(p.work_content);
  });

  return entries.map((e) => ({
    date: e.log_date,
    names: memberMap[e.id] || [],
    plate: e.plate_no || '',
    destination: e.destination_name || '',
    contents: (contentMap[e.id] || []).join('、'),
  }));
}

// 生成范围内的工作任务单 docx（每张卡片按其自身日期填 {年}{月}{日}）；无出车卡片返回 null
async function build(team, from, to) {
  const rows = await loadSheetRows(team.id, from, to);
  if (!rows.length) return null;

  const tplBuf = fs.readFileSync(TEMPLATE_PATH);
  const buffers = rows.map((row) => {
    const [y, m, d] = row.date.split('-'); // log_date 已按 DATE_FORMAT 取 'YYYY-MM-DD'，月日即补零两位
    return renderOne(tplBuf, {
      线路杆塔号: row.contents,
      工作班组: String(team.name || ''),
      工作负责人: row.names[0] || '',
      年: y,
      月: m,
      日: d,
      工作地点: row.destination,
      工作班成员: row.names.join(' '),
      派车情况: row.plate,
    });
  });
  return { buffer: mergeDocx(buffers), count: rows.length };
}

// 轻量预检：预览前确认范围内有出车卡片（避免预览服务回源拉到错误响应）
async function hasRows(teamId, from, to) {
  const [rows] = await pool.query(
    'SELECT COUNT(*) AS cnt FROM worklog_entry WHERE log_date BETWEEN ? AND ? AND team_id = ? AND vehicle_id IS NOT NULL',
    [from, to, teamId]
  );
  return rows[0].cnt > 0;
}

module.exports = { build, hasRows };
