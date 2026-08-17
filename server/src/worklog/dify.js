// 出工日志：Dify 照片验证工作流封装（接口细节见《开发指南》7.2）
// POST {DIFY_API_URL}/v1/workflows/run（blocking，/v1 由代码拼接）；inputs：date / destination / picture(COS 图片地址)；
// 输出仅识别的水印信息：title（施工内容）、time（拍摄时间）、weather（天气）、location（地点）、lng / lat（经纬度）；
// date_verify / destination_verify 已由 Dify 侧移除，日期/地点比对由后端 verify.js checkWatermark 完成
const axios = require('axios');
const config = require('../config');

function ensureConfigured() {
  if (!config.dify.apiUrl || !config.worklog.difyKey) {
    const err = new Error('Dify 未配置（DIFY_API_URL / DIFY_WORKLOG_API_KEY）');
    err.expose = true;
    throw err;
  }
}

// 工作流地址：DIFY_API_URL 只填域名即可（/v1 由代码拼接；配置已带 /v1 也不会重复）
function workflowUrl() {
  const base = config.dify.apiUrl.replace(/\/+$/, '');
  return `${base}${base.endsWith('/v1') ? '' : '/v1'}/workflows/run`;
}

// 识别单张照片水印信息，返回 { ok: true, workContent, time, weather, location, lng, lat }；
// 任何异常一律归 { ok: false }（不抛出），日期/地点核验由调用方据识别结果在后端完成
async function verifyPhoto({ username, date, destination, url }) {
  try {
    ensureConfigured();
    const res = await axios.post(
      workflowUrl(),
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
      weather: outputs.weather == null ? '' : String(outputs.weather),
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
