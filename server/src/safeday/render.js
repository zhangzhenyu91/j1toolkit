// 安全日活动记录：后端模板渲染（Dify 只产出三段文字，文档由本模块生成，替代旧 DOCX-MCP 链路）
// 模板：server/assets/safeday/activity-record-template.docx（docxtemplater 渲染，兼容占位符被 Word 拆散到多个 run）
//   - 普通字段：纯文本 {占位符}，linebreaks 使文本内换行落入文档
//   - 活动内容/结合本次内容复盘分析/结合实际岗位剖析内容：{@占位符} 原生 XML 注入——按换行拆为独立段落，
//     每段首行缩进两字符（firstLineChars=200），段落/字体口径沿用模板占位符段落
// 占位符口径见《开发指南》8.1
const fs = require('fs');
const path = require('path');
const PizZip = require('pizzip');
const Docxtemplater = require('docxtemplater');

const TEMPLATE_PATH = path.join(__dirname, '../../assets/safeday/activity-record-template.docx');

// 模板中以 {@xxx} 形式存在的三段正文占位符（渲染时注入段落 XML，其余字段仍走纯文本替换）
const RAW_XML_KEYS = ['活动内容', '结合本次内容复盘分析', '结合实际岗位剖析内容'];

// 段落属性沿用模板占位符段落口径（行距 240 自动、左对齐），仅 ind 改为首行缩进两字符：
// firstLineChars=200 为 Word 按字符缩进口径；firstLine=420（字号 21 半磅 × 2 字符）作不支持 chars 口径渲染器的回退
const PARA_PR = '<w:pPr><w:spacing w:line="240" w:lineRule="auto"/>' +
  '<w:ind w:firstLine="420" w:firstLineChars="200"/><w:jc w:val="left"/></w:pPr>';
// 字符属性沿用模板占位符 run 口径（默认宋体系字体，hint=eastAsia 保证中文走中文字体）
const RUN_PR = '<w:rPr><w:rFonts w:hint="eastAsia"/><w:lang w:val="en-US" w:eastAsia="zh-CN"/></w:rPr>';

function escapeXml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&apos;');
}

// 多行文本 → 多个 w:p（每段首行缩进两字符；段间空行保留为空段落）；空值也给一个带缩进的空段落保持版式
function toIndentedParagraphs(text) {
  const lines = String(text == null ? '' : text).trim().split(/\r?\n/);
  return lines.map((line) =>
    `<w:p>${PARA_PR}<w:r>${RUN_PR}<w:t xml:space="preserve">${escapeXml(line)}</w:t></w:r></w:p>`
  ).join('');
}

// 渲染一条活动记录为 docx buffer；fields 各值先转字符串（undefined/null 一律空串）
function renderRecord(fields) {
  const zip = new PizZip(fs.readFileSync(TEMPLATE_PATH));
  const doc = new Docxtemplater(zip, { paragraphLoop: true, linebreaks: true });
  const data = Object.assign({}, fields);
  for (const key of RAW_XML_KEYS) {
    data[key] = toIndentedParagraphs(fields[key]);
  }
  doc.render(data);
  return doc.getZip().generate({ type: 'nodebuffer', compression: 'DEFLATE' });
}

module.exports = { renderRecord };
