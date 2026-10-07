// 文件在线预览地址拼接（安全日记录 / 出工日志任务单与费用汇总共用）：
// 微软官方 Office 查看器（view.officeapps.live.com），凭地址内 ?token= 回源拉取本站下载接口
//（query token 映射见 utils/tokenQuery.js；回源要求本站公网可达，本地 127.0.0.1 预览不可用属正常）；
// 仅支持 Office 格式（doc/docx/xls/xlsx/ppt/pptx），其余格式返回空串由调用方降级为下载
const config = require('../config');

const OFFICE_EXTS = new Set(['doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx']);

// downloadPath：本站下载接口路径（含自身 query，不含 token——token 由本函数统一附加）；
// fileName 供识别格式（扩展名决定能否预览），displayName 保留兼容旧调用签名（微软查看器用不到）；
// 非 Office 格式返回空串（调用方按「该格式不支持在线预览，请下载查看」提示）
function buildPreviewUrl(req, downloadPath, fileName, displayName) {
  const ext = String(fileName || '').split('.').pop().toLowerCase();
  if (!OFFICE_EXTS.has(ext)) return '';
  // 反代后 req.protocol 恒为 http（未开 trust proxy）：优先取 X-Forwarded-Proto 头回退
  const proto = req.headers['x-forwarded-proto'] || req.protocol;
  // 文件名缀在下载路径尾（查看器按 URL 路径扩展名识别格式，缺扩展名会报「无法打开」），query 原样保留
  const qIdx = downloadPath.indexOf('?');
  const basePath = qIdx === -1 ? downloadPath : downloadPath.slice(0, qIdx);
  const query = qIdx === -1 ? '' : downloadPath.slice(qIdx + 1);
  const srcPath = `${basePath}/${encodeURIComponent(fileName)}`;
  const downloadUrl = `${proto}://${req.get('host')}${srcPath}?${query ? `${query}&` : ''}token=${encodeURIComponent(req.token)}`;
  return `${config.preview.viewerUrl}?src=${encodeURIComponent(downloadUrl)}`;
}

module.exports = { buildPreviewUrl };
