// 出工日志：Dify 照片验证工作流封装（接口细节见《开发指南》7.2）
// POST {DIFY_API_URL}/v1/workflows/run（blocking，/v1 由代码拼接）；inputs：date / destination / picture(COS 图片地址)；
// 输出 date_verify / destination_verify（'true'/'false'）、title（施工内容）、time（拍摄时间）、
// weather（天气）、location（地点）、lng / lat（经纬度）
const axios = require('axios');
const config = require('../config');

function ensureConfigured() {
  if (!config.dify.apiUrl || !config.worklog.difyKey) {
    const err = new Error('Dify 未配置（DIFY_API_URL / DIFY_WORKLOG_API_KEY）');
    err.expose = true;
    throw err;
  }
}

function isFalse(v) {
  return v === false || String(v).toLowerCase() === 'false';
}

// 工作流地址：DIFY_API_URL 只填域名即可（/v1 由代码拼接；配置已带 /v1 也不会重复）
function workflowUrl() {
  const base = config.dify.apiUrl.replace(/\/+$/, '');
  return `${base}${base.endsWith('/v1') ? '' : '/v1'}/workflows/run`;
}

// 验证单张照片，返回 { status, workContent, lng, lat }；任何异常一律归为 failed（不抛出）
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
    // 未通过项逐项记录：date_verify/destination_verify 任一 'false' 即该项不符
    const dateOk = !isFalse(outputs.date_verify);
    const destOk = !isFalse(outputs.destination_verify);
    const status = dateOk && destOk ? 'passed' : 'mismatch';
    return {
      status,
      dateOk,
      destOk,
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
    return { status: 'failed', dateOk: null, destOk: null, workContent: '', time: '', weather: '', location: '', lng: '', lat: '' };
  }
}

module.exports = { verifyPhoto, ensureConfigured };
