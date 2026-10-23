// 交跨记录：crane-answer 直推入库
// mode=direct：按 (team_id, session_id, crane_record_id) 幂等 upsert 进正式表（业务列全量更新）；
// mode=staging：进暂存区（同 (team_id, session_id) 批次整批替换），随批 xlsx 落盘 data/crossing/inbox/ 供事后核对。
// 可选 deletes（[{ crane_record_id, msg_id? }]）：crane-answer 实时删除推送，按 (team_id, crane_record_id) 定位
// （不限 session_id）命中即整条删除（与 DELETE /records/:id 同款），与 records 允许同批并存。
// 交跨为纯文字台账：无辅证照片、无 COS 逻辑；direct 逐条实时推送不发站内通知，仅 staging 进暂存区通知（同树障口径）。
const fs = require('fs');
const path = require('path');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const notice = require('../notice');
const { safeFileName } = require('./export');
const { normDateStr } = require('./diff');
const { deleteRecord } = require('./remove'); // 删除与 DELETE /records/:id 共用同款逻辑

function toIntOrNull(v) {
  const n = parseInt(String(v ?? '').trim(), 10);
  return Number.isInteger(n) ? n : null;
}

// 直推记录清洗：缺 line_name 且缺 line_name_raw 的条目返回 null（跳过计数）；line_name 空时回退 line_name_raw
function sanitizeRecord(r) {
  if (!r || typeof r !== 'object') return null;
  const lineName = (String(r.line_name ?? '').trim() || String(r.line_name_raw ?? '').trim()).slice(
    0,
    64
  );
  if (!lineName) return null;
  return {
    crane_record_id:
      String(r.crane_record_id ?? '')
        .trim()
        .slice(0, 64) || null, // 空存 NULL（uk_crane 不约束 NULL）
    voltage_level: String(r.voltage_level ?? '')
      .trim()
      .slice(0, 16),
    line_name: lineName,
    section: String(r.section ?? '')
      .trim()
      .slice(0, 32),
    pole_from: toIntOrNull(r.pole_from),
    pole_to: toIntOrNull(r.pole_to),
    crossing_type: String(r.crossing_type ?? '')
      .trim()
      .slice(0, 128),
    dist_small: String(r.dist_small ?? '')
      .trim()
      .slice(0, 16),
    dist_big: String(r.dist_big ?? '')
      .trim()
      .slice(0, 16),
    cross_horizontal: String(r.cross_horizontal ?? '')
      .trim()
      .slice(0, 16),
    cross_vertical: String(r.cross_vertical ?? '')
      .trim()
      .slice(0, 16),
    cross_angle: String(r.cross_angle ?? '')
      .trim()
      .slice(0, 16),
    has_hazard: String(r.has_hazard ?? '')
      .trim()
      .slice(0, 16),
    finder_name: String(r.finder_name ?? '')
      .trim()
      .slice(0, 32),
    inspect_at: normDateStr(r.inspect_at) || null,
    remark: String(r.remark ?? '')
      .trim()
      .slice(0, 255),
  };
}

// 业务列全量更新集（direct 模式已存在记录按直推权威值覆盖全部业务列；交跨无处理列）
const BUSINESS_SET = `voltage_level = ?, line_name = ?, section = ?, pole_from = ?, pole_to = ?, crossing_type = ?,
  dist_small = ?, dist_big = ?, cross_horizontal = ?, cross_vertical = ?, cross_angle = ?, has_hazard = ?,
  finder_name = ?, inspect_at = ?, remark = ?`;

function businessVals(rec) {
  return [
    rec.voltage_level,
    rec.line_name,
    rec.section,
    rec.pole_from,
    rec.pole_to,
    rec.crossing_type,
    rec.dist_small,
    rec.dist_big,
    rec.cross_horizontal,
    rec.cross_vertical,
    rec.cross_angle,
    rec.has_hazard,
    rec.finder_name,
    rec.inspect_at,
    rec.remark,
  ];
}

// direct：逐条 upsert，返回 { inserted, updated }
async function importDirect(team, sessionId, records) {
  let inserted = 0;
  let updated = 0;
  for (const rec of records) {
    let existingId = null;
    if (rec.crane_record_id) {
      const [ex] = await pool.query(
        'SELECT id FROM crossing_record WHERE team_id = ? AND crane_session_id = ? AND crane_record_id = ?',
        [team.id, sessionId, rec.crane_record_id]
      );
      existingId = ex.length ? ex[0].id : null;
    }
    if (existingId) {
      await pool.query(`UPDATE crossing_record SET ${BUSINESS_SET} WHERE id = ?`, [
        ...businessVals(rec),
        existingId,
      ]);
      updated += 1;
    } else {
      await pool.query(
        `INSERT INTO crossing_record (team_id, voltage_level, line_name, section, pole_from, pole_to, crossing_type,
          dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
          finder_name, inspect_at, remark, source, crane_session_id, crane_record_id)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'crane', ?, ?)`,
        [
          team.id,
          rec.voltage_level,
          rec.line_name,
          rec.section,
          rec.pole_from,
          rec.pole_to,
          rec.crossing_type,
          rec.dist_small,
          rec.dist_big,
          rec.cross_horizontal,
          rec.cross_vertical,
          rec.cross_angle,
          rec.has_hazard,
          rec.finder_name,
          rec.inspect_at,
          rec.remark,
          sessionId,
          rec.crane_record_id,
        ]
      );
      inserted += 1;
    }
  }
  return { inserted, updated };
}

// 随批 xlsx 落盘：data/crossing/inbox/{team_id}/{session_id}.xlsx（路径段过滤非法字符），返回相对 server 目录的存储路径
function saveBatchXlsx(teamId, sessionId, xlsxBase64) {
  const buf = Buffer.from(String(xlsxBase64), 'base64');
  if (!buf.length) return '';
  const dir = path.join(__dirname, '..', '..', 'data', 'crossing', 'inbox', String(teamId));
  fs.mkdirSync(dir, { recursive: true });
  const file = `${safeFileName(sessionId)}.xlsx`;
  fs.writeFileSync(path.join(dir, file), buf);
  return `data/crossing/inbox/${teamId}/${file}`;
}

// staging：同 (team_id, session_id) 批次整批替换（含已导入批次被重推时复位为 pending），
// 返回 { batch_id, count }
// reviewNote：审核意见（可空，空串存 NULL），整批替换时随批次行一并更新
async function importStaging(
  team,
  sessionId,
  groupName,
  pusherName,
  reviewNote,
  xlsxBase64,
  records
) {
  const xlsxPath = xlsxBase64 ? saveBatchXlsx(team.id, sessionId, xlsxBase64) : '';
  const [batches] = await pool.query(
    'SELECT id FROM crossing_staging_batch WHERE team_id = ? AND crane_session_id = ?',
    [team.id, sessionId]
  );
  let batchId;
  if (batches.length) {
    batchId = batches[0].id;
    await pool.query('DELETE FROM crossing_staging_record WHERE batch_id = ?', [batchId]);
    await pool.query(
      `UPDATE crossing_staging_batch SET group_name = ?, pusher_name = ?, review_note = ?, xlsx_path = ?, status = 'pending', created_at = NOW()
       WHERE id = ?`,
      [groupName, pusherName, reviewNote, xlsxPath, batchId]
    );
  } else {
    const [r] = await pool.query(
      'INSERT INTO crossing_staging_batch (team_id, crane_session_id, group_name, pusher_name, review_note, xlsx_path) VALUES (?, ?, ?, ?, ?, ?)',
      [team.id, sessionId, groupName, pusherName, reviewNote, xlsxPath]
    );
    batchId = r.insertId;
  }
  for (const rec of records) {
    await pool.query(
      `INSERT INTO crossing_staging_record (batch_id, crane_record_id, voltage_level, line_name, section, crossing_type,
        dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
        finder_name, inspect_at, remark)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      [
        batchId,
        rec.crane_record_id,
        rec.voltage_level,
        rec.line_name,
        rec.section,
        rec.crossing_type,
        rec.dist_small,
        rec.dist_big,
        rec.cross_horizontal,
        rec.cross_vertical,
        rec.cross_angle,
        rec.has_hazard,
        rec.finder_name,
        rec.inspect_at,
        rec.remark,
      ]
    );
  }
  return { batch_id: batchId, count: records.length };
}

// POST /ingest 主流程（鉴权在路由处凭 X-Ingest-Token 完成，本函数假定已放行）
async function handleIngest(req, res, next) {
  try {
    const body = req.body || {};
    const sessionId = String(body.session_id ?? '')
      .trim()
      .slice(0, 64);
    const teamName = String(body.team_name ?? '').trim();
    const mode = String(body.mode ?? '').trim();
    if (!sessionId) return fail(res, 400, 40001, '参数错误：session_id 不能为空');
    if (!teamName) return fail(res, 400, 40001, '参数错误：team_name 不能为空');
    if (!['direct', 'staging'].includes(mode))
      return fail(res, 400, 40001, '参数错误：mode 应为 direct 或 staging');

    // 按班组名精确匹配（仅启用班组）
    const [teams] = await pool.query(
      'SELECT id, name FROM sys_team WHERE name = ? AND status = 1',
      [teamName]
    );
    if (!teams.length) return fail(res, 404, 40400, '班组不存在');
    const team = teams[0];

    const pusherName = String((body.pusher && body.pusher.name) ?? '')
      .trim()
      .slice(0, 32);
    const groupName = String(body.group_name ?? '')
      .trim()
      .slice(0, 64);
    // 审核意见（仅 staging 有意义；空串存 NULL）
    const reviewNote =
      String(body.review_note ?? '')
        .trim()
        .slice(0, 255) || null;
    const rawRecords = Array.isArray(body.records) ? body.records : [];
    // 清洗 + 跳过计数（缺线路名的条目不入库）
    const records = [];
    let skipped = 0;
    for (const r of rawRecords) {
      const rec = sanitizeRecord(r);
      if (rec) records.push(rec);
      else skipped += 1;
    }

    // 可选 deletes：crane-answer 实时删除推送（[{ crane_record_id, msg_id? }]），按 (team_id, crane_record_id)
    // 定位（不限 session_id），命中则与 DELETE /records/:id 同款删除并计 deleted，未命中/缺 id 计 skipped 不报错；
    // 与 records 允许同批并存
    const rawDeletes = Array.isArray(body.deletes) ? body.deletes : [];
    let deleted = 0;
    for (const d of rawDeletes) {
      const craneId = String((d && d.crane_record_id) ?? '').trim();
      if (!craneId) {
        skipped += 1;
        continue;
      }
      const [hit] = await pool.query(
        'SELECT id FROM crossing_record WHERE team_id = ? AND crane_record_id = ?',
        [team.id, craneId]
      );
      if (!hit.length) {
        skipped += 1;
        continue;
      }
      for (const row of hit) await deleteRecord(row.id, team.id); // 同 id 跨会话桶可能多行，一并删除
      deleted += 1;
    }

    if (mode === 'direct') {
      const { inserted, updated } = await importDirect(team, sessionId, records);
      // direct 为 crane-answer 逐条实时推送（落库/更正各一次），不发站内通知避免刷屏；
      // 群内回执由 crane-answer 自行发送（「已记录」/「报送失败…」）
      return ok(res, { inserted, updated, skipped, deleted });
    }

    const { batch_id: batchId, count } = await importStaging(
      team,
      sessionId,
      groupName,
      pusherName,
      reviewNote,
      body.xlsx_base64,
      records
    );
    // 有审核意见时通知附备注摘要（超长截断，全文见暂存区批次）
    const noteLine = reviewNote
      ? `\n备注：${reviewNote.length > 80 ? `${reviewNote.slice(0, 80)}…` : reviewNote}`
      : '';
    notify(
      team,
      '交跨记录进暂存区',
      `班组：${team.name}\n暂存：${count} 条（跳过 ${skipped} 条），待确认导入\n推送人：${pusherName || '未知'}${noteLine}`
    );
    return ok(res, { batch_id: batchId, count, deleted });
  } catch (err) {
    return next(err);
  }
}

// 站内通知（超管 + 本班班组管理员；失败仅记日志，不影响直推应答）。
// 仅 staging 进暂存区时通知（人工整批报送、低频，提醒班管确认导入）；direct 逐条实时推送不通知（避免刷屏）。
// 群内反馈一律由 crane-answer 自行发送（成功「已记录」/失败「报送失败…」），壹匣通知系统不向微信群推送
function notify(team, title, content) {
  notice
    .push({ targets: ['admin', 'team_admin'], title, content })
    .catch((err) => console.error('[交跨] 直推结果通知失败：', err.message));
}

module.exports = { handleIngest };
