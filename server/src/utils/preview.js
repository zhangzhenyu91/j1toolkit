// basemetas 文件预览地址拼接（安全日记录 / 出工日志任务单与费用汇总共用）：
// 预览服务器凭地址内 ?token= 回源拉取下载接口（query token 映射见 utils/tokenQuery.js）
const config = require('../config');

// downloadPath：本站下载接口路径（含自身 query，不含 token——token 由本函数统一附加）；
// fileName 供预览服务识别格式，displayName 为展示名（缺省同 fileName）；
// 未配置 BASEMETAS_URL 返回空串（调用方按「未配置文件预览服务」报错）
function buildPreviewUrl(req, downloadPath, fileName, displayName) {
  const base = config.basemetas.url;
  if (!base) return '';
  // 反代后 req.protocol 恒为 http（未开 trust proxy）：优先取 X-Forwarded-Proto 头回退
  const proto = req.headers['x-forwarded-proto'] || req.protocol;
  const sep = downloadPath.includes('?') ? '&' : '?';
  const downloadUrl = `${proto}://${req.get('host')}${downloadPath}${sep}token=${encodeURIComponent(req.token)}`;
  return `${base}/preview/view?url=${encodeURIComponent(downloadUrl)}`
    + `&fileName=${encodeURIComponent(fileName)}`
    + `&displayName=${encodeURIComponent(displayName || fileName)}`;
}

module.exports = { buildPreviewUrl };
