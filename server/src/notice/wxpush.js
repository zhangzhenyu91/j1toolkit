// 微信消息推送：Dify「发送微信消息」工作流封装（通知的微信外发渠道，站内通知之外的附加能力）
// POST {DIFY_API_URL}/v1/workflows/run（streaming，/v1 由代码拼接）；
// inputs：wxid 接收方（个人/微信群 wxid，多个以 `", "` 分隔逐个双引号包裹）/ text 文本（支持 \n 换行）；
// 未配置 DIFY_WXPUSH_API_KEY 或接收方均未设 wxid 时静默跳过，全程不向调用方抛错
const axios = require('axios');
const config = require('../config');
const { pool } = require('../db');
const { workflowRunUrl } = require('../utils/dify');

// 是否已配置（未配置时微信推送整体停用，站内通知照常）
function isConfigured() {
  return Boolean(config.dify.apiUrl && config.dify.wxpushKey);
}

// 解析接收方 wxid：用户个人 wxid + 班组群 wxid（均限启用/正常且已设 wxid 者），去重后返回
async function resolveWxids({ userIds = [], teamIds = [] }) {
  const wxids = [];
  const uids = [...new Set(userIds.map((v) => Number(v)).filter((v) => Number.isInteger(v) && v > 0))];
  const tids = [...new Set(teamIds.map((v) => Number(v)).filter((v) => Number.isInteger(v) && v > 0))];
  if (uids.length) {
    const [rows] = await pool.query(
      "SELECT wxid FROM sys_user WHERE id IN (?) AND status = 1 AND wxid != ''", [uids]
    );
    rows.forEach((r) => wxids.push(r.wxid));
  }
  if (tids.length) {
    const [rows] = await pool.query(
      "SELECT wxid FROM sys_team WHERE id IN (?) AND status = 1 AND wxid != ''", [tids]
    );
    rows.forEach((r) => wxids.push(r.wxid));
  }
  return [...new Set(wxids)];
}

// 发送微信消息：解析 wxid 并调 Dify 工作流；返回 { ok, sent, reason?, error? }，不抛错
async function sendTo({ userIds = [], teamIds = [], text, user = 'wxpush' }) {
  if (!isConfigured()) return { ok: false, sent: 0, reason: '未配置 DIFY_WXPUSH_API_KEY' };
  let wxids = [];
  try {
    wxids = await resolveWxids({ userIds, teamIds });
  } catch (err) {
    console.error('[微信推送] wxid 解析失败：', err.message);
    return { ok: false, sent: 0, error: err.message };
  }
  if (!wxids.length) return { ok: false, sent: 0, reason: '接收方均未设置 wxid' };
  try {
    await axios.post(
      workflowRunUrl(config.dify.apiUrl),
      {
        inputs: {
          // 工作流要求：每个 wxid 用双引号包裹，多个以 ", " 分隔（如 "wxid_a", "wxid_b"）
          wxid: wxids.map((w) => `"${w}"`).join(', '),
          text: String(text || ''),
        },
        response_mode: 'streaming',
        user,
      },
      {
        headers: {
          Authorization: `Bearer ${config.dify.wxpushKey}`,
          'Content-Type': 'application/json',
        },
        timeout: 60000,
      }
    );
    return { ok: true, sent: wxids.length };
  } catch (err) {
    console.error('[微信推送] Dify 工作流调用失败：', err.message);
    return { ok: false, sent: 0, error: err.message };
  }
}

module.exports = { isConfigured, resolveWxids, sendTo };
