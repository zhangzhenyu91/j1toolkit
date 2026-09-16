// 微信消息推送：Dify「发送微信消息」工作流封装（通知的微信外发渠道，站内通知之外的附加能力）
// POST {DIFY_API_URL}/v1/workflows/run（streaming，/v1 由代码拼接）；一次调用仅发一个接收方（多人/多群逐个循环调用）；
// inputs 三变量：wxid 接收方（单个 wxid 原文，不再双引号包裹；个人 wxid 发个人、班组 wxid 实为微信群发群）/
// text 文本（支持 \n 换行；群消息可在内容中以「@昵称 」提及成员——昵称取 sys_user.nickname，末尾须带一个空格）/
// at 被@人 wxid（多个以 , 分隔，仅群消息有效，个人发送传空串）；
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

// 解析被@人：用户 id → { wxid, nickname }（限正常且 wxid/昵称均已设置者，按 wxid 去重）；群消息 @ 用
async function resolveMentions(userIds = []) {
  const uids = [...new Set(userIds.map((v) => Number(v)).filter((v) => Number.isInteger(v) && v > 0))];
  if (!uids.length) return [];
  const [rows] = await pool.query(
    "SELECT wxid, nickname FROM sys_user WHERE id IN (?) AND status = 1 AND wxid != '' AND nickname != ''", [uids]
  );
  const seen = new Set();
  return rows
    .filter((r) => !seen.has(r.wxid) && seen.add(r.wxid))
    .map((r) => ({ wxid: r.wxid, nickname: r.nickname }));
}

// 发送微信消息：解析 wxid 后逐个接收方各调一次 Dify 工作流（一次调用仅支持一个 wxid）；
// atUserIds 可选：群消息 @ 这些用户（正文前自动拼「@昵称 」并传 at 输入，个人发送忽略）；
// 返回 { ok, sent, reason?, error? }（ok=全部接收方均发送成功，sent=成功数），不抛错
async function sendTo({ userIds = [], teamIds = [], text, atUserIds = [], user = 'wxpush' }) {
  if (!isConfigured()) return { ok: false, sent: 0, reason: '未配置 DIFY_WXPUSH_API_KEY' };
  let wxids;
  let mentions;
  try {
    [wxids, mentions] = await Promise.all([resolveWxids({ userIds, teamIds }), resolveMentions(atUserIds)]);
  } catch (err) {
    console.error('[微信推送] wxid 解析失败：', err.message);
    return { ok: false, sent: 0, error: err.message };
  }
  if (!wxids.length) return { ok: false, sent: 0, reason: '接收方均未设置 wxid' };
  const baseText = String(text || '');
  // 群消息 @ 前缀：「@昵称 」逐个拼接（昵称后须带一个空格）；at 为被@人 wxid 逗号分隔
  const atPrefix = mentions.map((m) => `@${m.nickname} `).join('');
  const atStr = mentions.map((m) => m.wxid).join(',');
  let sent = 0;
  const errors = [];
  for (const wxid of wxids) {
    const isRoom = wxid.includes('@chatroom');
    try {
      await axios.post(
        workflowRunUrl(config.dify.apiUrl),
        {
          inputs: {
            // 工作流现要求：单个 wxid 原文（不再双引号包裹、不支持多个），逐个接收方循环发送
            wxid,
            text: isRoom && atPrefix ? `${atPrefix}\n${baseText}` : baseText,
            at: isRoom ? atStr : '',
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
      sent += 1;
    } catch (err) {
      console.error(`[微信推送] Dify 工作流调用失败（${wxid}）：`, err.message);
      errors.push(`${wxid}: ${err.message}`);
    }
  }
  if (!errors.length) return { ok: true, sent };
  return { ok: false, sent, error: errors.join('；') };
}

module.exports = { isConfigured, resolveWxids, resolveMentions, sendTo };
