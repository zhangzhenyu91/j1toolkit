// 出工日志：水印照片验证流水线（上传/重验/商旅补拉共用）
// Dify 识别（调用失败或施工内容为空均自动重试，最多重试 3 次——重试期间照片保持 pending，双端显示「验证中」）→
// 结果回写（写库时重查卡片当前的记录日期与派车目的地做日期/地点核验，规避验证途中改目的地的口径过期竞态）→
// 重试耗尽仍无施工内容 → 置「验证失败」并通知班组人工审核（站内通知班管 + 班组微信群消息 @班管）
const { pool } = require('../db');
const dify = require('./dify');
const { checkWatermark } = require('./verify');
const { dots } = require('../utils/cndate');

// 调用失败或施工内容为空时的最大重试次数（不含首次调用；视觉模型偶发漏识别 title、调用偶发超时，直接重试即可恢复）
const MAX_RETRY = 3;

// 识别结果有效 = 调用成功且施工内容非空（空内容触发重试）
function usable(vr) {
  return vr && vr.ok && !!String(vr.workContent || '').trim();
}

// Dify 识别（含失败/空内容重试）：返回最终一次结果 vr；vr.ok 且 workContent 为空 = 重试耗尽
async function recognize(params) {
  let vr = null;
  for (let attempt = 0; attempt <= MAX_RETRY; attempt += 1) {
    vr = await dify.verifyPhoto(params);
    if (usable(vr)) return vr;
    if (attempt < MAX_RETRY) {
      console.warn(`[出工日志] Dify ${vr.ok ? '识别施工内容为空' : '调用失败'}，第 ${attempt + 1} 次重试（${params.url}）`);
    }
  }
  return vr;
}

// 重试耗尽仍无施工内容 → 通知班组人工审核：站内通知本班班组管理员 + 班组微信群消息 @班管；
// 通知失败仅记日志（照片已置「验证失败」，班管亦可在汇总前核验中看到）
async function notifyManualReview(photoId) {
  const [rows] = await pool.query(
    `SELECT e.id AS entry_id, e.team_id, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date,
            v.plate_no, p.members
     FROM worklog_photo p
     JOIN worklog_entry e ON e.id = p.entry_id
     LEFT JOIN worklog_vehicle v ON v.id = e.vehicle_id
     WHERE p.id = ?`,
    [photoId]
  );
  if (!rows.length) return; // 照片已删除
  const info = rows[0];
  const names = (() => {
    const arr = typeof info.members === 'string' ? JSON.parse(info.members) : info.members;
    return Array.isArray(arr) && arr.length ? arr.join('、') : '';
  })();
  const [teamAdmins] = await pool.query(
    "SELECT id FROM sys_user WHERE role = 'team_admin' AND team_id = ? AND status = 1",
    [info.team_id]
  );
  const atIds = teamAdmins.map((a) => a.id);
  await require('../notice').push({ // 与 dispatch-sync 同型惰性加载
    userIds: atIds,
    targets: [],
    title: '水印照片需人工审核',
    content: `${info.log_date} ${info.plate_no || '未出车'} 卡片${names ? `（${names}）` : ''}的水印照片`
      + `经 ${MAX_RETRY} 次重试仍未识别出施工内容，已标记为验证失败。`
      + '请在出工日志中找到当日该车辆卡片，点击照片「验证失败」手动修正施工内容。',
    wxTeamIds: [info.team_id],
    wxAtUserIds: atIds,
  });
}

// 统一入口：Dify 识别（含重试）→ 回写（含后端日期/地点核验）→ 必要时通知人工审核；全程不向调用方抛错
async function verifyAndWriteBack(photoId, params) {
  try {
    const vr = await recognize(params);
    const [rows] = await pool.query(
      `SELECT DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, d.name AS destination_name
       FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
       LEFT JOIN worklog_destination d ON d.id = e.destination_id
       WHERE p.id = ?`,
      [photoId]
    );
    if (!rows.length) return; // 照片已删除
    // Dify 调用失败或重试耗尽仍无施工内容 → 验证失败（后者追加班组人工审核通知）
    if (!usable(vr)) {
      await pool.query(
        `UPDATE worklog_photo SET verify_status = 'failed', work_content = '', shot_time = '', weather = '', location = '', lng = '', lat = '', date_ok = NULL, dest_ok = NULL WHERE id = ?`,
        [photoId]
      );
      if (vr.ok) {
        try {
          await notifyManualReview(photoId);
        } catch (err) {
          console.error('[出工日志] 照片人工审核通知发送失败：', err.message);
        }
      }
      return;
    }
    const chk = checkWatermark({
      time: vr.time,
      location: vr.location,
      logDate: dots(rows[0].log_date),
      destination: rows[0].destination_name,
    });
    await pool.query(
      `UPDATE worklog_photo SET verify_status = ?, work_content = ?, shot_time = ?, weather = ?, location = ?, lng = ?, lat = ?, date_ok = ?, dest_ok = ? WHERE id = ?`,
      [chk.status, vr.workContent, vr.time, vr.weather, vr.location, vr.lng, vr.lat,
        chk.dateOk ? 1 : 0, chk.destOk ? 1 : 0, photoId]
    );
  } catch (err) {
    console.error('[出工日志] 验证结果回写失败：', err.message);
  }
}

module.exports = { verifyAndWriteBack };
