// 文件小工具：扩展名提取、multer 中文文件名修正、XML 转义（出工日志 / 安全日记录等共用）

// 小写扩展名（不含点；无扩展名返回空串）
function getFileExt(name) {
  const idx = String(name || '').lastIndexOf('.');
  return idx === -1 ? '' : String(name).slice(idx + 1).toLowerCase();
}

// multer 1.x 默认按 latin1 解析文件名，中文名需转回 UTF-8
function fixLatin1Name(name) {
  return Buffer.from(String(name || ''), 'latin1').toString('utf8');
}

// XML 特殊字符转义（docx 模板拼接用）
function escapeXml(s) {
  return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&apos;');
}

module.exports = { getFileExt, fixLatin1Name, escapeXml };
