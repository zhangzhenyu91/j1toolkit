// 树障隐患记录：crane-answer 直推入库
// mode=direct：按 (team_id, session_id, crane_record_id) 幂等 upsert 进正式表（已存在仅更新发现侧列，不动处理列/状态）；
// mode=staging：进暂存区（同 (team_id, session_id) 批次整批替换），随批 xlsx 落盘 data/treehazard/inbox/ 供事后核对
const fs = require('fs');
const path = require('path');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const notice = require('../notice');
const { parseVerticalNum, safeFileName } = require('./export');
const { normDateStr } = require('./diff');

function toIntOrNull(v) {
  const n = parseInt(String(v ?? '').trim(), 10);
  return Number.isInteger(n) ? n : null;
}

// 直推记录清洗：缺 line_name 且缺 line_name_raw 的条目返回 null（跳过计数）；line_name 空时回退 line_name_raw
function sanitizeRecord(r) {
  if (!r || typeof r !== 'object') return null;
  const lineName = (String(r.line_name ?? '').trim() || String(r.line_name_raw ?? '').trim()).slice(0, 64);
  if (!lineName) return null;
  return {
    crane_record_id: String(r.crane_record_id ?? '').trim().slice(0, 64) || null, // 空存 NULL（uk_crane 不约束 NULL）
    voltage_level: String(r.voltage_level ?? '').trim().slice(0, 16),
    line_name: lineName,
    section: String(r.section ?? '').trim().slice(0, 32),
    pole_from: toIntOrNull(r.pole_from),
    pole_to: toIntOrNull(r.pole_to),
    position_note: String(r.position_note ?? '').trim().slice(0, 32),
    tree_species: String(r.tree_species ?? '').trim().slice(0, 32),
    tree_count: String(r.tree_count ?? '').trim().slice(0, 16),
    dist_small: String(r.dist_small ?? '').trim().slice(0, 16),
    dist_big: String(r.dist_big ?? '').trim().slice(0, 16),
    diameter: String(r.diameter ?? '').trim().slice(0, 16),
    horizontal: String(r.horizontal ?? '').trim().slice(0, 16),
    vertical: String(r.vertical ?? '').trim().slice(0, 16),
    vertical_num: parseVerticalNum(r.vertical),
    finder_name: String(r.finder_name ?? '').trim().slice(0, 32),
    found_at: normDateStr(r.found_at) || null,
  };
}

// 发现侧列（direct 模式已存在记录仅更新这些列，处理三列与状态不动）
const DISCOVERY_SET = `voltage_level = ?, line_name = ?, section = ?, pole_from = ?, pole_to = ?, position_note = ?,
  tree_species = ?, tree_count = ?, dist_small = ?, dist_big = ?, diameter = ?, horizontal = ?, vertical = ?,
  vertical_num = ?, finder_name = ?, found_at = ?`;

function discoveryVals(rec) {
  return [rec.voltage_level, rec.line_name, rec.section, rec.pole_from, rec.pole_to, rec.position_note,
    rec.tree_species, rec.tree_count, rec.dist_small, rec.dist_big, rec.diameter, rec.horizontal, rec.vertical,
    rec.vertical_num, rec.finder_name, rec.found_at];
}

// direct：逐条 upsert，返回 { inserted, updated }
async function importDirect(teamId, sessionId, records) {
  let inserted = 0;
  let updated = 0;
  for (const rec of records) {
    let existingId = null;
    if (rec.crane_record_id) {
      const [ex] = await pool.query(
        'SELECT id FROM treehazard_record WHERE team_id = ? AND crane_session_id = ? AND crane_record_id = ?',
        [teamId, sessionId, rec.crane_record_id]
      );
      existingId = ex.length ? ex[0].id : null;
    }
    if (existingId) {
      await pool.query(`UPDATE treehazard_record SET ${DISCOVERY_SET} WHERE id = ?`, [...discoveryVals(rec), existingId]);
      updated += 1;
    } else {
      await pool.query(
        `INSERT INTO treehazard_record (team_id, voltage_level, line_name, section, pole_from, pole_to, position_note,
          tree_species, tree_count, dist_small, dist_big, diameter, horizontal, vertical, vertical_num,
          finder_name, found_at, status, source, crane_session_id, crane_record_id)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'unhandled', 'crane', ?, ?)`,
        [teamId, rec.voltage_level, rec.line_name, rec.section, rec.pole_from, rec.pole_to, rec.position_note,
          rec.tree_species, rec.tree_count, rec.dist_small, rec.dist_big, rec.diameter, rec.horizontal, rec.vertical,
          rec.vertical_num, rec.finder_name, rec.found_at, sessionId, rec.crane_record_id]
      );
      inserted += 1;
    }
  }
  return { inserted, updated };
}

// 随批 xlsx 落盘：data/treehazard/inbox/{team_id}/{session_id}.xlsx（路径段过滤非法字符），返回相对 server 目录的存储路径
function saveBatchXlsx(teamId, sessionId, xlsxBase64) {
  const buf = Buffer.from(String(xlsxBase64), 'base64');
  if (!buf.length) return '';
  const dir = path.join(__dirname, '..', '..', 'data', 'treehazard', 'inbox', String(teamId));
  fs.mkdirSync(dir, { recursive: true });
  const file = `${safeFileName(sessionId)}.xlsx`;
  fs.writeFileSync(path.join(dir, file), buf);
  return `data/treehazard/inbox/${teamId}/${file}`;
}

// staging：同 (team_id, session_id) 批次整批替换（含已导入批次被重推时复位为 pending），返回 { batch_id, count }
// reviewNote：审核意见（可空，空串存 NULL），整批替换时随批次行一并更新
async function importStaging(teamId, sessionId, groupName, pusherName, reviewNote, xlsxBase64, records) {
  const xlsxPath = xlsxBase64 ? saveBatchXlsx(teamId, sessionId, xlsxBase64) : '';
  const [batches] = await pool.query(
    'SELECT id FROM treehazard_staging_batch WHERE team_id = ? AND crane_session_id = ?',
    [teamId, sessionId]
  );
  let batchId;
  if (batches.length) {
    batchId = batches[0].id;
    await pool.query('DELETE FROM treehazard_staging_record WHERE batch_id = ?', [batchId]);
    await pool.query(
      `UPDATE treehazard_staging_batch SET group_name = ?, pusher_name = ?, review_note = ?, xlsx_path = ?, status = 'pending', created_at = NOW()
       WHERE id = ?`,
      [groupName, pusherName, reviewNote, xlsxPath, batchId]
    );
  } else {
    const [r] = await pool.query(
      'INSERT INTO treehazard_staging_batch (team_id, crane_session_id, group_name, pusher_name, review_note, xlsx_path) VALUES (?, ?, ?, ?, ?, ?)',
      [teamId, sessionId, groupName, pusherName, reviewNote, xlsxPath]
    );
    batchId = r.insertId;
  }
  for (const rec of records) {
    await pool.query(
      `INSERT INTO treehazard_staging_record (batch_id, crane_record_id, voltage_level, line_name, section,
        tree_species, tree_count, dist_small, dist_big, diameter, horizontal, vertical, vertical_num,
        finder_name, found_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      [batchId, rec.crane_record_id, rec.voltage_level, rec.line_name, rec.section,
        rec.tree_species, rec.tree_count, rec.dist_small, rec.dist_big, rec.diameter, rec.horizontal, rec.vertical,
        rec.vertical_num, rec.finder_name, rec.found_at]
    );
  }
  return { batch_id: batchId, count: records.length };
}

// POST /ingest 主流程（鉴权在路由处凭 X-Ingest-Token 完成，本函数假定已放行）
async function handleIngest(req, res, next) {
  try {
    const body = req.body || {};
    const sessionId = String(body.session_id ?? '').trim().slice(0, 64);
    const teamName = String(body.team_name ?? '').trim();
    const mode = String(body.mode ?? '').trim();
    if (!sessionId) return fail(res, 400, 40001, '参数错误：session_id 不能为空');
    if (!teamName) return fail(res, 400, 40001, '参数错误：team_name 不能为空');
    if (!['direct', 'staging'].includes(mode)) return fail(res, 400, 40001, "参数错误：mode 应为 direct 或 staging");

    // 按班组名精确匹配（仅启用班组）
    const [teams] = await pool.query('SELECT id, name FROM sys_team WHERE name = ? AND status = 1', [teamName]);
    if (!teams.length) return fail(res, 404, 40400, '班组不存在');
    const team = teams[0];

    const pusherName = String((body.pusher && body.pusher.name) ?? '').trim().slice(0, 32);
    const groupName = String(body.group_name ?? '').trim().slice(0, 64);
    // 审核意见（仅 staging 有意义；空串存 NULL）
    const reviewNote = String(body.review_note ?? '').trim().slice(0, 255) || null;
    const rawRecords = Array.isArray(body.records) ? body.records : [];
    // 清洗 + 跳过计数（缺线路名的条目不入库）
    const records = [];
    let skipped = 0;
    for (const r of rawRecords) {
      const rec = sanitizeRecord(r);
      if (rec) records.push(rec);
      else skipped += 1;
    }

    if (mode === 'direct') {
      const { inserted, updated } = await importDirect(team.id, sessionId, records);
      require('./sync').touch(); // 直推入库即服务端变更，登记后空闲自动同步内网（惰性加载与上方 notice 同型）
      notify(team, '树障记录入库', `班组：${team.name}\n入库：新增 ${inserted} 条 / 更新 ${updated} 条（跳过 ${skipped} 条）\n推送人：${pusherName || '未知'}`);
      return ok(res, { inserted, updated, skipped });
    }

    const { batch_id: batchId, count } = await importStaging(team.id, sessionId, groupName, pusherName, reviewNote, body.xlsx_base64, records);
    // 有审核意见时通知附备注摘要（超长截断，全文见暂存区批次）
    const noteLine = reviewNote ? `\n备注：${reviewNote.length > 80 ? `${reviewNote.slice(0, 80)}…` : reviewNote}` : '';
    notify(team, '树障记录进暂存区', `班组：${team.name}\n暂存：${count} 条（跳过 ${skipped} 条），待确认导入\n推送人：${pusherName || '未知'}${noteLine}`);
    return ok(res, { batch_id: batchId, count });
  } catch (err) {
    return next(err);
  }
}

// 站内 + 班组群通知（失败仅记日志，不影响直推应答）
function notify(team, title, content) {
  notice
    .push({ targets: ['admin', 'team_admin'], title, content, wxTeamIds: [team.id] })
    .catch((err) => console.error('[树障] 直推结果通知失败：', err.message));
}

module.exports = { handleIngest };
