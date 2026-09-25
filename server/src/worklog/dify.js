// 出工日志：Dify 照片验证工作流封装（接口细节见《开发指南》7.2）
// POST {DIFY_API_URL}/v1/workflows/run（blocking，/v1 由代码拼接）；inputs：date / destination / picture(COS 图片地址)；
// 输出仅识别的水印信息：title（施工内容）、time（拍摄时间）、weather（天气）、location（地点）、lng / lat（经纬度）；
// date_verify / destination_verify 已由 Dify 侧移除，日期/地点比对由后端 verify.js checkWatermark 完成
const axios = require('axios');
const config = require('../config');
const { workflowRunUrl, createEnsureConfigured } = require('../utils/dify');

const ensureConfigured = createEnsureConfigured({
  apiUrl: config.dify.apiUrl,
  apiKey: config.worklog.difyKey,
  keyEnvName: 'DIFY_WORKLOG_API_KEY',
});

// 识别单张照片水印信息，返回 { ok: true, workContent, time, weather, location, lng, lat }；
// 任何异常一律归 { ok: false }（不抛出），日期/地点核验由调用方据识别结果在后端完成

// 视觉模型偶发把 ℃ 输出为 LaTeX（如 晴 $17^{\circ} \mathrm{C}$ 东南风1级），归一恢复为 17℃
// 分「$ 包裹」与裸形式两组匹配，均不吃表达式之外的空白（防止 18^{\circ}C 两侧空格丢失）
function normalizeWeather(s) {
  return String(s)
    .replace(/\$\s*(-?\d+(?:\.\d+)?)\s*\\mathrm\s*\{\s*\^\{?\\circ\}?\s*C\s*\}\s*\$/g, '$1℃')
    .replace(/\$\s*(-?\d+(?:\.\d+)?)\s*\^\s*\{?\s*\\circ\s*\}?\s*\{?\s*\\(?:mathrm|text)\s*\{\s*C\s*\}\s*\}?\s*\$/g, '$1℃')
    .replace(/\$\s*(-?\d+(?:\.\d+)?)\s*\^\s*\{?\s*\\circ\s*\}?\s*C\s*\$/g, '$1℃')
    .replace(/(-?\d+(?:\.\d+)?)\s*\\mathrm\s*\{\s*\^\{?\\circ\}?\s*C\s*\}/g, '$1℃')
    .replace(/(-?\d+(?:\.\d+)?)\s*\^\s*\{?\s*\\circ\s*\}?\s*\{?\s*\\(?:mathrm|text)\s*\{\s*C\s*\}\s*\}?/g, '$1℃')
    .replace(/(-?\d+(?:\.\d+)?)\s*\^\s*\{?\s*\\circ\s*\}?\s*C\b/g, '$1℃')
    .replace(/\s{2,}/g, ' ')
    .trim();
}
async function verifyPhoto({ username, date, destination, url }) {
  try {
    ensureConfigured();
    const res = await axios.post(
      workflowRunUrl(config.dify.apiUrl),
      {
        inputs: {
          date,
          destination,
          // 单文件变量传对象（数组会报 invalid_param: must be a file）
          picture: { transfer_method: 'remote_url', url, type: 'image' },
        },
        response_mode: 'blocking',
        user: username,
      },
      {
        headers: {
          Authorization: `Bearer ${config.worklog.difyKey}`,
          'Content-Type': 'application/json',
        },
        timeout: 90000,
      }
    );
    const outputs = (res.data && res.data.data && res.data.data.outputs) || {};
    return {
      ok: true,
      workContent: outputs.title == null ? '' : String(outputs.title),
      time: outputs.time == null ? '' : String(outputs.time),
      weather: outputs.weather == null ? '' : normalizeWeather(outputs.weather),
      location: outputs.location == null ? '' : String(outputs.location),
      lng: outputs.lng == null ? '' : String(outputs.lng),
      lat: outputs.lat == null ? '' : String(outputs.lat),
    };
  } catch (err) {
    // Dify 的 4xx 响应体含具体原因（invalid_param 等），连同入参一并打出便于排查
    const detail = err.response && err.response.data ? JSON.stringify(err.response.data) : err.message;
    console.error('[出工日志] Dify 照片验证失败：', detail, '｜入参:', JSON.stringify({ date, destination, url }));
    return { ok: false };
  }
}

module.exports = { verifyPhoto };
