// 树障隐患记录：crane-answer 直推入库
// mode=direct：按 (team_id, session_id, crane_record_id) 幂等 upsert 进正式表（已存在仅更新发现侧列，不动处理列/状态）；
// mode=staging：进暂存区（同 (team_id, session_id) 批次整批替换），随批 xlsx 落盘 data/treehazard/inbox/ 供事后核对。
// 可选 deletes（[{ crane_record_id, msg_id? }]）：crane-answer 实时删除推送，按 (team_id, crane_record_id) 定位
// （不限 session_id）命中即整条删除（与 DELETE /records/:id 同款），与 records 允许同批并存。
// 辅证照片（records[i].evidence_photos，base64 dataURL）：随直推以壹匣名义写入自家 COS（treehazard 前缀，
// 与处理前后照片同桶同权，班管/超管可删）；direct 整组替换该记录的 evidence 照片（字段缺省=旧版直推不动既有辅证），
// staging 入暂存照片表、转入正式表时归挂、废弃/整批替换时连带清理。照片失败仅记日志计数，不阻断记录入库。
const fs = require('fs');
const path = require('path');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const notice = require('../notice');
const config = require('../config');
const cos = require('../worklog/cos'); // 与照片上传接口同口径跨模块复用 COS 封装（key 前缀本模块自拼）
const { parseVerticalNum, safeFileName } = require('./export');
const { normDateStr } = require('./diff');
const { deleteRecord } = require('./remove'); // 删除与 DELETE /records/:id 共用同款逻辑
const { parseDataUrlPhoto } = require('../utils/photo'); // dataURL 解析（格式/解码统一在此，大小限值本模块自校）

function toIntOrNull(v) {
  const n = parseInt(String(v ?? '').trim(), 10);
  return Number.isInteger(n) ? n : null;
}

// ===== 辅证照片（evidence_photos）=====

const PHOTO_MAX_BYTES = 15 * 1024 * 1024;
const PHOTO_MAX_PER_RECORD = 9;

// 解析直推记录附带的辅证照片：字段缺省（旧版直推）返回 null（不动既有辅证）；数组为权威值（可为空=清除）；
// 单张非法（非 jpeg/png dataURL、超 15MB）跳过，每条记录最多 9 张
function parseEvidencePhotos(raw) {
  if (!raw || typeof raw !== 'object' || !Array.isArray(raw.evidence_photos)) return null;
  const list = [];
  for (const item of raw.evidence_photos.slice(0, PHOTO_MAX_PER_RECORD)) {
    const photo = parseDataUrlPhoto(String((item && item.image) || ''));
    if (!photo || !photo.buf.length || photo.buf.length > PHOTO_MAX_BYTES) continue;
    list.push({
      msgId: String((item && item.msg_id) ?? '').trim().slice(0, 64),
      buf: photo.buf,
      ext: photo.ext,
      contentType: photo.contentType,
    });
  }
  return list;
}

// direct 模式整组替换某记录的 evidence 照片（先删旧 COS 对象与表行再逐张上传），返回 { stored, failed }
async function replaceEvidencePhotos(teamName, recordId, photos) {
  const [olds] = await pool.query("SELECT id, cos_key FROM treehazard_photo WHERE record_id = ? AND kind = 'evidence'", [recordId]);
  for (const p of olds) {
    try {
      await cos.deleteObject(p.cos_key);
    } catch (e) {
      console.error('[树障] 重推清理旧辅证 COS 对象失败（继续）：', e.message);
    }
  }
  if (olds.length) await pool.query("DELETE FROM treehazard_photo WHERE record_id = ? AND kind = 'evidence'", [recordId]);
  let stored = 0;
  let failed = 0;
  for (let i = 0; i < photos.length; i += 1) {
    const photo = photos[i];
    const key = `${config.treehazard.cosPrefix}${safeFileName(teamName)}/${recordId}/evidence-${Date.now()}-${i}.${photo.ext}`;
    try {
      await cos.putBuffer(key, photo.buf, photo.contentType);
      await pool.query(
        "INSERT INTO treehazard_photo (record_id, kind, cos_key, url, uploaded_by) VALUES (?, 'evidence', ?, ?, NULL)",
        [recordId, key, cos.publicUrl(key)]
      );
      stored += 1;
    } catch (e) {
      failed += 1;
      console.error('[树障] 直推辅证照片入 COS 失败（记录已入库，照片缺失）：', e.message);
    }
  }
  return { stored, failed };
}

// staging：照片随批入暂存照片表（COS 键带 staging-批次 目录），按暂存记录 insertId 归挂（staging_record_id），
// crane_record_id 同步冗余落列供旧库回退；返回 { stored, failed }
async function storeStagingPhotos(teamName, batchId, stagingRecordId, craneRecordId, photos) {
  let stored = 0;
  let failed = 0;
  for (let i = 0; i < photos.length; i += 1) {
    const photo = photos[i];
    const key = `${config.treehazard.cosPrefix}${safeFileName(teamName)}/staging-${batchId}/${safeFileName(craneRecordId) || 'rec'}-${Date.now()}-${i}.${photo.ext}`;
    try {
      await cos.putBuffer(key, photo.buf, photo.contentType);
      await pool.query(
        'INSERT INTO treehazard_staging_photo (batch_id, staging_record_id, crane_record_id, cos_key, url) VALUES (?, ?, ?, ?, ?)',
        [batchId, stagingRecordId, craneRecordId, key, cos.publicUrl(key)]
      );
      stored += 1;
    } catch (e) {
      failed += 1;
      console.error('[树障] 暂存辅证照片入 COS 失败（不影响批次入库）：', e.message);
    }
  }
  return { stored, failed };
}

// 批次废弃/整批替换时连带清理暂存照片（COS 对象 + 表行；导入转入走 index.js 的归挂逻辑）
async function clearStagingPhotos(batchId) {
  const [rows] = await pool.query('SELECT id, cos_key FROM treehazard_staging_photo WHERE batch_id = ?', [batchId]);
  for (const p of rows) {
    try {
      await cos.deleteObject(p.cos_key);
    } catch (e) {
      console.error('[树障] 清理暂存辅证 COS 对象失败（继续）：', e.message);
    }
  }
  if (rows.length) await pool.query('DELETE FROM treehazard_staging_photo WHERE batch_id = ?', [batchId]);
  return rows.length;
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

// direct：逐条 upsert（附带辅证照片时整组替换 evidence 组），返回 { inserted, updated, photos, photos_failed }
async function importDirect(team, sessionId, records) {
  let inserted = 0;
  let updated = 0;
  let photos = 0;
  let photosFailed = 0;
  for (const rec of records) {
    let existingId = null;
    if (rec.crane_record_id) {
      const [ex] = await pool.query(
        'SELECT id FROM treehazard_record WHERE team_id = ? AND crane_session_id = ? AND crane_record_id = ?',
        [team.id, sessionId, rec.crane_record_id]
      );
      existingId = ex.length ? ex[0].id : null;
    }
    let recordId;
    if (existingId) {
      await pool.query(`UPDATE treehazard_record SET ${DISCOVERY_SET} WHERE id = ?`, [...discoveryVals(rec), existingId]);
      recordId = existingId;
      updated += 1;
    } else {
      const [r] = await pool.query(
        `INSERT INTO treehazard_record (team_id, voltage_level, line_name, section, pole_from, pole_to, position_note,
          tree_species, tree_count, dist_small, dist_big, diameter, horizontal, vertical, vertical_num,
          finder_name, found_at, status, source, crane_session_id, crane_record_id)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'unhandled', 'crane', ?, ?)`,
        [team.id, rec.voltage_level, rec.line_name, rec.section, rec.pole_from, rec.pole_to, rec.position_note,
          rec.tree_species, rec.tree_count, rec.dist_small, rec.dist_big, rec.diameter, rec.horizontal, rec.vertical,
          rec.vertical_num, rec.finder_name, rec.found_at, sessionId, rec.crane_record_id]
      );
      recordId = r.insertId;
      inserted += 1;
    }
    // photos 为 null（旧版直推未上报）不动既有辅证；数组（含空）为权威值整组替换
    if (Array.isArray(rec.photos)) {
      const pr = await replaceEvidencePhotos(team.name, recordId, rec.photos);
      photos += pr.stored;
      photosFailed += pr.failed;
    }
  }
  return { inserted, updated, photos, photos_failed: photosFailed };
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

// staging：同 (team_id, session_id) 批次整批替换（含已导入批次被重推时复位为 pending；暂存照片连带先清后存），
// 返回 { batch_id, count, photos, photos_failed }
// reviewNote：审核意见（可空，空串存 NULL），整批替换时随批次行一并更新
async function importStaging(team, sessionId, groupName, pusherName, reviewNote, xlsxBase64, records) {
  const xlsxPath = xlsxBase64 ? saveBatchXlsx(team.id, sessionId, xlsxBase64) : '';
  const [batches] = await pool.query(
    'SELECT id FROM treehazard_staging_batch WHERE team_id = ? AND crane_session_id = ?',
    [team.id, sessionId]
  );
  let batchId;
  if (batches.length) {
    batchId = batches[0].id;
    await pool.query('DELETE FROM treehazard_staging_record WHERE batch_id = ?', [batchId]);
    await clearStagingPhotos(batchId); // 整批替换：旧暂存照片（COS 对象 + 表行）连带清理
    await pool.query(
      `UPDATE treehazard_staging_batch SET group_name = ?, pusher_name = ?, review_note = ?, xlsx_path = ?, status = 'pending', created_at = NOW()
       WHERE id = ?`,
      [groupName, pusherName, reviewNote, xlsxPath, batchId]
    );
  } else {
    const [r] = await pool.query(
      'INSERT INTO treehazard_staging_batch (team_id, crane_session_id, group_name, pusher_name, review_note, xlsx_path) VALUES (?, ?, ?, ?, ?, ?)',
      [team.id, sessionId, groupName, pusherName, reviewNote, xlsxPath]
    );
    batchId = r.insertId;
  }
  let photos = 0;
  let photosFailed = 0;
  for (const rec of records) {
    const [r] = await pool.query(
      `INSERT INTO treehazard_staging_record (batch_id, crane_record_id, voltage_level, line_name, section,
        tree_species, tree_count, dist_small, dist_big, diameter, horizontal, vertical, vertical_num,
        finder_name, found_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      [batchId, rec.crane_record_id, rec.voltage_level, rec.line_name, rec.section,
        rec.tree_species, rec.tree_count, rec.dist_small, rec.dist_big, rec.diameter, rec.horizontal, rec.vertical,
        rec.vertical_num, rec.finder_name, rec.found_at]
    );
    if (Array.isArray(rec.photos) && rec.photos.length) {
      // 照片按本条暂存行的 insertId 归挂（同批多条空 crane_record_id 时不再互相误挂）
      const pr = await storeStagingPhotos(team.name, batchId, r.insertId, rec.crane_record_id || '', rec.photos);
      photos += pr.stored;
      photosFailed += pr.failed;
    }
  }
  return { batch_id: batchId, count: records.length, photos, photos_failed: photosFailed };
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
    // 清洗 + 跳过计数（缺线路名的条目不入库）；辅证照片随记录解析（字段缺省=null 表示旧版直推）
    const records = [];
    let skipped = 0;
    for (const r of rawRecords) {
      const rec = sanitizeRecord(r);
      if (rec) {
        rec.photos = parseEvidencePhotos(r);
        records.push(rec);
      } else {
        skipped += 1;
      }
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
        'SELECT id FROM treehazard_record WHERE team_id = ? AND crane_record_id = ?',
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
      const { inserted, updated, photos, photos_failed: photosFailed } = await importDirect(team, sessionId, records);
      const photoLine = photos || photosFailed ? `\n辅证照片：${photos} 张${photosFailed ? `（失败 ${photosFailed} 张）` : ''}` : '';
      notify(team, '树障记录入库', `班组：${team.name}\n入库：新增 ${inserted} 条 / 更新 ${updated} 条（跳过 ${skipped} 条）${photoLine}\n推送人：${pusherName || '未知'}`);
      return ok(res, { inserted, updated, skipped, photos, photos_failed: photosFailed, deleted });
    }

    const {
      batch_id: batchId, count, photos, photos_failed: photosFailed,
    } = await importStaging(team, sessionId, groupName, pusherName, reviewNote, body.xlsx_base64, records);
    // 有审核意见时通知附备注摘要（超长截断，全文见暂存区批次）
    const noteLine = reviewNote ? `\n备注：${reviewNote.length > 80 ? `${reviewNote.slice(0, 80)}…` : reviewNote}` : '';
    const photoLine = photos || photosFailed ? `\n辅证照片：${photos} 张${photosFailed ? `（失败 ${photosFailed} 张）` : ''}` : '';
    notify(team, '树障记录进暂存区', `班组：${team.name}\n暂存：${count} 条（跳过 ${skipped} 条），待确认导入${photoLine}\n推送人：${pusherName || '未知'}${noteLine}`);
    return ok(res, { batch_id: batchId, count, photos, photos_failed: photosFailed, deleted });
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

module.exports = { handleIngest, clearStagingPhotos };
