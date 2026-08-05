// 安全日活动记录：多 PDF 合并（自 SafeDayLogs 独立服务原样移植，纯 npm 方案）
const { PDFDocument } = require('pdf-lib');

// 按顺序合并多个 PDF buffer，返回合并后的 Buffer
async function mergePdfs(buffers) {
  const merged = await PDFDocument.create();
  for (const buf of buffers) {
    const doc = await PDFDocument.load(buf);
    const pages = await merged.copyPages(doc, doc.getPageIndices());
    for (const page of pages) {
      merged.addPage(page);
    }
  }
  const bytes = await merged.save();
  return Buffer.from(bytes);
}

module.exports = { mergePdfs };
