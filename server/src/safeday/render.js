// 安全日活动记录：后端模板渲染（Dify 只产出三段文字，文档由本模块生成，替代旧 DOCX-MCP 链路）
// 模板：server/assets/safeday/activity-record-template.docx（纯文本 {占位符}，docxtemplater 渲染，
// 兼容占位符被 Word 拆散到多个 run；linebreaks 使 Dify 文本内换行正确落入文档）
// 占位符口径见《开发指南》8.1
const fs = require('fs');
const path = require('path');
const PizZip = require('pizzip');
const Docxtemplater = require('docxtemplater');

const TEMPLATE_PATH = path.join(__dirname, '../../assets/safeday/activity-record-template.docx');

// 渲染一条活动记录为 docx buffer；fields 各值先转字符串（undefined/null 一律空串）
function renderRecord(fields) {
  const zip = new PizZip(fs.readFileSync(TEMPLATE_PATH));
  const doc = new Docxtemplater(zip, { paragraphLoop: true, linebreaks: true });
  doc.render(fields);
  return doc.getZip().generate({ type: 'nodebuffer', compression: 'DEFLATE' });
}

module.exports = { renderRecord };
