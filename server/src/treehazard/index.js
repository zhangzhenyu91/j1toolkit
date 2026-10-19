// 树障隐患记录路由：除 POST /ingest（crane-answer 直推，凭 X-Ingest-Token 免登录，挂在 auth 门控之前）外，
// 全部接口需登录 + tree-hazard 应用权限 + 生效班组（req.team，见 utils/team.js）；
// 暂存区/冲突/设置修改/删记录为 超管 或 本班 team_admin（requireDictAdmin，同出工日志字典门控口径）
const express = require('express');
const fs = require('fs');
const path = require('path');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const teamUtil = require('../utils/team');
const tokenQuery = require('../utils/tokenQuery');
const { buildPreviewUrl } = require('../utils/preview');
const { todayCn, CN_OFFSET_MS } = require('../utils/cndate');
const config = require('../config');
const cos = require('../worklog/cos'); // 与 notice/sgccclockin 同口径跨模块复用 COS 封装（key 前缀本模块自拼）
const exporter = require('./export');
const { intranetFields, normDateStr, parseSectionPoles } = require('./diff');
const ingest = require('./ingest');
const sync = require('./sync');

const router = express.Router();

// 文件预览服务器回源拉取下载地址时无法附带请求头：无 Authorization 头且 query 带 token 时映射为 Authorization（同出工日志口径）
router.use(tokenQuery);

// crane-answer 直推：不走 JWT，凭请求头 X-Ingest-Token 与配置比对（未配置 → 503；不等 → 401）
router.post('/ingest', (req, res, next) => {
  const token = config.treehazard.ingestToken;
  if (!token) return fail(res, 503, 50301, '未配置对接密钥');
  if (String(req.get('X-Ingest-Token') || '') !== token) return fail(res, 401, 40104, '对接密钥错误');
  return ingest.handleIngest(req, res, next);
});

router.use(auth, requireApp('tree-hazard'));

// 班组上下文：解析生效班组（超管可用 ?team_id= 指定；其余角色固定本班，未分配 → null）
router.use(async (req, res, next) => {
  try {
    req.team = await teamUtil.resolveReqTeam(req);
    return next();
  } catch (err) {
    return next(err);
  }
});

// 管理门控：超管任意班组（req.team 由 ?team_id= 解析），班组管理员仅本班
function requireDictAdmin(req, res, next) {
  if (req.user.role === 'admin') return next();
  if (req.user.role === 'team_admin' && req.team && req.user.team_id === req.team.id) return next();
  return fail(res, 403, 40304, '仅管理员可执行此操作');
}

const DATE_RE = /^\d{4}-\d{2}-\d{2}$/;

// JSON 列防御解析（mysql2 可能返回字符串或已解析对象，与 quiz options 同口径）
function parseJson(raw) {
  if (!raw) return null;
  if (typeof raw === 'string') {
    try {
      return JSON.parse(raw);
    } catch {
      return null;
    }
  }
  return raw;
}

// 记录全列查询（日期列格式化为 'YYYY-MM-DD' 文本）
const RECORD_COLS = `id, team_id, voltage_level, line_name, section, pole_from, pole_to, position_note,
  tree_species, tree_count, dist_small, dist_big, diameter, horizontal, vertical, vertical_num,
  finder_name, DATE_FORMAT(found_at, '%Y-%m-%d') AS found_at,
  handler_name, handle_note, DATE_FORMAT(handled_at, '%Y-%m-%d') AS handled_at,
  status, source, crane_session_id, crane_record_id, synced_at, created_by, created_at, updated_at`;

async function loadTeamRecords(teamId) {
  const [rows] = await pool.query(`SELECT ${RECORD_COLS} FROM treehazard_record WHERE team_id = ?`, [teamId]);
  return rows;
}

// 批量装配照片（一次 IN 查询）
async function attachPhotos(records) {
  if (!records.length) return records;
  const ids = records.map((r) => r.id);
  const [photos] = await pool.query(
    'SELECT id, record_id, kind, cos_key, url, uploaded_by, created_at FROM treehazard_photo WHERE record_id IN (?) ORDER BY id',
    [ids]
  );
  const byRecord = new Map();
  for (const p of photos) {
    if (!byRecord.has(p.record_id)) byRecord.set(p.record_id, []);
    byRecord.get(p.record_id).push(p);
  }
  for (const r of records) r.photos = byRecord.get(r.id) || [];
  return records;
}

// 本班垂直距离阈值（未配置 → null）
async function fetchThreshold(teamId) {
  const [rows] = await pool.query('SELECT vertical_threshold FROM treehazard_team_setting WHERE team_id = ?', [teamId]);
  return rows.length && rows[0].vertical_threshold !== null ? Number(rows[0].vertical_threshold) : null;
}

// 北京时间本周一 00:00 的库内墙钟文本（库内 created_at 随容器为 UTC 墙钟，北京周界减 8h 后比较）
function weekStartText() {
  const cnNow = new Date(Date.now() + CN_OFFSET_MS); // 其 UTC 字段即北京墙钟
  const dow = (cnNow.getUTCDay() + 6) % 7; // 周一 = 0
  const utcMs = Date.UTC(cnNow.getUTCFullYear(), cnNow.getUTCMonth(), cnNow.getUTCDate() - dow, 0, 0, 0) - CN_OFFSET_MS;
  const d = new Date(utcMs);
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${p(d.getUTCHours())}:${p(d.getUTCMinutes())}:${p(d.getUTCSeconds())}`;
}

// ===== 看板 =====

// GET /dashboard：本班 未处理数 / 本周新增（created_at 在本周一至今）/ 超阈值（未处理且 vertical_num ≤ 阈值，无阈值配置则 0）/
// 已处理数，顺带返回 threshold 与 pending_conflicts / staging_batches 计数
router.get('/dashboard', async (req, res, next) => {
  try {
    if (!req.team) {
      return ok(res, { unhandled: 0, week_new: 0, over_threshold: 0, handled: 0, threshold: null, pending_conflicts: 0, staging_batches: 0 });
    }
    const teamId = req.team.id;
    const threshold = await fetchThreshold(teamId);
    const [[unhandled]] = await pool.query("SELECT COUNT(*) AS n FROM treehazard_record WHERE team_id = ? AND status = 'unhandled'", [teamId]);
    const [[weekNew]] = await pool.query('SELECT COUNT(*) AS n FROM treehazard_record WHERE team_id = ? AND created_at >= ?', [teamId, weekStartText()]);
    const [[overThreshold]] = threshold === null
      ? [[{ n: 0 }]]
      : await pool.query(
        "SELECT COUNT(*) AS n FROM treehazard_record WHERE team_id = ? AND status = 'unhandled' AND vertical_num IS NOT NULL AND vertical_num <= ?",
        [teamId, threshold]
      );
    const [[handled]] = await pool.query("SELECT COUNT(*) AS n FROM treehazard_record WHERE team_id = ? AND status = 'handled'", [teamId]);
    const [[conflicts]] = await pool.query("SELECT COUNT(*) AS n FROM treehazard_conflict WHERE team_id = ? AND status = 'pending'", [teamId]);
    const [[staging]] = await pool.query("SELECT COUNT(*) AS n FROM treehazard_staging_batch WHERE team_id = ? AND status = 'pending'", [teamId]);
    return ok(res, {
      unhandled: unhandled.n,
      week_new: weekNew.n,
      over_threshold: overThreshold.n,
      handled: handled.n,
      threshold,
      pending_conflicts: conflicts.n,
      staging_batches: staging.n,
    });
  } catch (err) {
    return next(err);
  }
});

// ===== 记录列表与处理 =====

// GET /records：本班全部记录，按 线路顺序（worklog_tower 导入序，未匹配排尾）分组返回；每条记录带 photos 数组
router.get('/records', async (req, res, next) => {
  try {
    if (!req.team) return ok(res, { threshold: null, lines: [], unmatched: [] });
    const teamId = req.team.id;
    const [threshold, lineRows, records] = await Promise.all([
      fetchThreshold(teamId),
      exporter.fetchLineOrder(pool, teamId),
      loadTeamRecords(teamId),
    ]);
    await attachPhotos(records);
    const lineOrder = new Map(lineRows.map((l, i) => [l.line_name, i]));
    const sorted = exporter.sortRecords(records, lineOrder);
    const byLine = new Map();
    const unmatched = [];
    for (const r of sorted) {
      if (lineOrder.has(r.line_name)) {
        if (!byLine.has(r.line_name)) byLine.set(r.line_name, []);
        byLine.get(r.line_name).push(r);
      } else {
        unmatched.push(r);
      }
    }
    const lines = lineRows
      .filter((l) => byLine.has(l.line_name))
      .map((l) => ({ voltage_level: l.voltage_level, line_name: l.line_name, records: byLine.get(l.line_name) }));
    return ok(res, { threshold, lines, unmatched });
  } catch (err) {
    return next(err);
  }
});

// GET /records/:id：单条记录（含 photos 数组），仅本班（备用：详情/单条刷新）
router.get('/records/:id', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const recordId = Number(req.params.id);
    const [rows] = await pool.query(`SELECT ${RECORD_COLS} FROM treehazard_record WHERE id = ? AND team_id = ?`, [recordId, req.team.id]);
    if (!rows.length) return fail(res, 404, 40400, '记录不存在');
    await attachPhotos(rows);
    return ok(res, rows[0]);
  } catch (err) {
    return next(err);
  }
});

// 处理入参校验与取值：handle_note 必填 trim 非空；handler_name 默认当前用户昵称；handled_at 默认当日（北京时间）
function parseHandleBody(req, res) {
  const note = String((req.body && req.body.handle_note) ?? '').trim();
  if (!note) {
    fail(res, 400, 40001, '处理情况不能为空');
    return null;
  }
  const handlerName = (String((req.body && req.body.handler_name) ?? '').trim() || req.user.nickname || '').slice(0, 32);
  let handledAt = String((req.body && req.body.handled_at) ?? '').trim();
  if (!handledAt) handledAt = todayCn();
  if (!DATE_RE.test(handledAt)) {
    fail(res, 400, 40001, '处理时间格式应为 YYYY-MM-DD');
    return null;
  }
  return { handlerName, note: note.slice(0, 64), handledAt };
}

// POST /records/:id/handle：单条处理（仅本班记录）
router.post('/records/:id/handle', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const parsed = parseHandleBody(req, res);
    if (!parsed) return;
    const recordId = Number(req.params.id);
    const [rows] = await pool.query('SELECT id FROM treehazard_record WHERE id = ? AND team_id = ?', [recordId, req.team.id]);
    if (!rows.length) return fail(res, 404, 40400, '记录不存在');
    await pool.query(
      "UPDATE treehazard_record SET status = 'handled', handler_name = ?, handle_note = ?, handled_at = ? WHERE id = ?",
      [parsed.handlerName, parsed.note, parsed.handledAt, recordId]
    );
    sync.touch(); // 登记服务端变更，空闲后自动同步内网
    return ok(res, { id: recordId });
  } catch (err) {
    return next(err);
  }
});

// POST /records/handle-batch：批量处理（同事务逐条更新，仅本班 + 仅 unhandled），body { ids, handler_name?, handle_note, handled_at? }
router.post('/records/handle-batch', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const parsed = parseHandleBody(req, res);
    if (!parsed) return;
    const ids = Array.isArray(req.body && req.body.ids)
      ? req.body.ids.map(Number).filter((n) => Number.isInteger(n) && n > 0)
      : [];
    if (!ids.length) return fail(res, 400, 40001, 'ids 应为非空记录 id 数组');
    const conn = await pool.getConnection();
    let updated = 0;
    try {
      await conn.beginTransaction();
      for (const id of ids) {
        const [r] = await conn.query(
          "UPDATE treehazard_record SET status = 'handled', handler_name = ?, handle_note = ?, handled_at = ? WHERE id = ? AND team_id = ? AND status = 'unhandled'",
          [parsed.handlerName, parsed.note, parsed.handledAt, id, req.team.id]
        );
        updated += r.affectedRows;
      }
      await conn.commit();
    } catch (e) {
      await conn.rollback().catch(() => {});
      throw e;
    } finally {
      conn.release();
    }
    sync.touch(); // 登记服务端变更，空闲后自动同步内网
    return ok(res, { updated });
  } catch (err) {
    return next(err);
  }
});

// ===== 照片（处理前/后；evidence 证据照预留不开放上传）=====

// POST /records/:id/photos?kind=before|after：body { image: 'data:image/jpeg;base64,...' }（dataURL 解析同出工日志）
router.post('/records/:id/photos', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const kind = String(req.query.kind ?? '').trim();
    if (!['before', 'after'].includes(kind)) return fail(res, 400, 40001, 'kind 应为 before 或 after');
    const recordId = Number(req.params.id);
    const [rows] = await pool.query('SELECT id FROM treehazard_record WHERE id = ? AND team_id = ?', [recordId, req.team.id]);
    if (!rows.length) return fail(res, 404, 40400, '记录不存在');

    const match = /^data:image\/(jpeg|jpg|png);base64,(.+)$/.exec((req.body && req.body.image) || '');
    if (!match) return fail(res, 400, 40010, '照片格式应为 jpeg/png（base64 dataURL）');
    const buf = Buffer.from(match[2], 'base64');
    if (!buf.length || buf.length > 15 * 1024 * 1024) return fail(res, 400, 40011, '照片大小应在 15MB 以内');
    const contentType = `image/${match[1] === 'png' ? 'png' : 'jpeg'}`;

    const key = `${config.treehazard.cosPrefix}${exporter.safeFileName(req.team.name)}/${recordId}/${kind}-${Date.now()}.jpg`;
    await cos.putBuffer(key, buf, contentType);
    const url = cos.publicUrl(key);
    const [r] = await pool.query(
      'INSERT INTO treehazard_photo (record_id, kind, cos_key, url, uploaded_by) VALUES (?, ?, ?, ?, ?)',
      [recordId, kind, key, url, req.user.id]
    );
    const [created] = await pool.query(
      'SELECT id, record_id, kind, cos_key, url, uploaded_by, created_at FROM treehazard_photo WHERE id = ?',
      [r.insertId]
    );
    sync.touch(); // 登记服务端变更，空闲后自动同步内网
    return ok(res, created[0]);
  } catch (err) {
    return next(err);
  }
});

// DELETE /records/:id/photos/:photoId：上传者本人或班管/超管；删 COS 对象 + 表行
router.delete('/records/:id/photos/:photoId', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const [rows] = await pool.query(
      `SELECT p.id, p.cos_key, p.uploaded_by FROM treehazard_photo p
       JOIN treehazard_record r ON r.id = p.record_id
       WHERE p.id = ? AND p.record_id = ? AND r.team_id = ?`,
      [Number(req.params.photoId), Number(req.params.id), req.team.id]
    );
    if (!rows.length) return fail(res, 404, 40400, '照片不存在');
    const photo = rows[0];
    const isManager = req.user.role === 'admin' || (req.user.role === 'team_admin' && req.user.team_id === req.team.id);
    if (photo.uploaded_by !== req.user.id && !isManager) return fail(res, 403, 40304, '仅上传者本人或管理员可删除');
    try {
      await cos.deleteObject(photo.cos_key);
    } catch (e) {
      console.error('[树障] 删除 COS 对象失败（继续删表行）：', e.message);
    }
    await pool.query('DELETE FROM treehazard_photo WHERE id = ?', [photo.id]);
    sync.touch(); // 登记服务端变更，空闲后自动同步内网
    return ok(res, { id: photo.id });
  } catch (err) {
    return next(err);
  }
});

// DELETE /records/:id：班管/超管；删照片 COS 对象与表行、删记录、写 treehazard_deleted_id 墓碑
router.delete('/records/:id', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const recordId = Number(req.params.id);
    const [rows] = await pool.query('SELECT id FROM treehazard_record WHERE id = ? AND team_id = ?', [recordId, req.team.id]);
    if (!rows.length) return fail(res, 404, 40400, '记录不存在');
    const [photos] = await pool.query('SELECT cos_key FROM treehazard_photo WHERE record_id = ?', [recordId]);
    for (const p of photos) {
      try {
        await cos.deleteObject(p.cos_key);
      } catch (e) {
        console.error('[树障] 删除 COS 对象失败（继续删记录）：', e.message);
      }
    }
    const conn = await pool.getConnection();
    try {
      await conn.beginTransaction();
      await conn.query('DELETE FROM treehazard_photo WHERE record_id = ?', [recordId]);
      await conn.query('DELETE FROM treehazard_record WHERE id = ?', [recordId]);
      await conn.query('INSERT IGNORE INTO treehazard_deleted_id (id, team_id) VALUES (?, ?)', [recordId, req.team.id]);
      await conn.commit();
    } catch (e) {
      await conn.rollback().catch(() => {});
      throw e;
    } finally {
      conn.release();
    }
    sync.touch(); // 登记服务端变更（含墓碑），空闲后自动同步内网
    return ok(res, { id: recordId });
  } catch (err) {
    return next(err);
  }
});

// ===== 班组设置 =====

// GET /settings：{ vertical_threshold }（未配置 → null）
router.get('/settings', async (req, res, next) => {
  try {
    return ok(res, { vertical_threshold: req.team ? await fetchThreshold(req.team.id) : null });
  } catch (err) {
    return next(err);
  }
});

// PUT /settings：班管/超管；body { vertical_threshold }（空 = 清除）
router.put('/settings', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const raw = req.body && req.body.vertical_threshold;
    let val = null;
    if (raw !== null && raw !== undefined && String(raw).trim() !== '') {
      const n = Number(String(raw).trim());
      if (!Number.isFinite(n) || n < 0) return fail(res, 400, 40001, '阈值应为非负数字');
      val = n;
    }
    await pool.query(
      `INSERT INTO treehazard_team_setting (team_id, vertical_threshold, updated_by) VALUES (?, ?, ?)
       ON DUPLICATE KEY UPDATE vertical_threshold = VALUES(vertical_threshold), updated_by = VALUES(updated_by)`,
      [req.team.id, val, req.user.id]
    );
    return ok(res, { vertical_threshold: val });
  } catch (err) {
    return next(err);
  }
});

// ===== 导出与在线预览 =====

// 生成统计表 buffer（team 参数化：路由传 req.team，office 在线预览回源复用；line 可选分线路；记录经 sortRecords 排序）
async function buildTeamExport(team, line) {
  let records = await loadTeamRecords(team.id);
  const lineRows = await exporter.fetchLineOrder(pool, team.id);
  const lineOrder = new Map(lineRows.map((l, i) => [l.line_name, i]));
  if (line) records = records.filter((r) => r.line_name === line);
  const sorted = exporter.sortRecords(records, lineOrder);
  const wb = await exporter.buildWorkbook({ teamName: team.name, records: sorted });
  const buf = Buffer.from(await wb.xlsx.writeBuffer());
  return { buf, fileName: exporter.exportFileName(team.name, line || undefined) };
}

// GET /export/preview?line=：OO 启用（OO_PUBLIC_URL）→ 返回 /office.html 承载页地址（office 模块 src=treehazard 签发，
// thd 票据回源即时生成）；否则微软 Office 查看器（凭地址内 ?token= 回源下方下载接口，同出工日志 /task-sheet/preview 口径）
router.get('/export/preview', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const line = String(req.query.line ?? '').trim();
    if (config.office.publicUrl) {
      const q = ['src=treehazard'];
      if (line) q.push(`line=${encodeURIComponent(line)}`);
      if (req.query.team_id) q.push(`team_id=${encodeURIComponent(req.query.team_id)}`);
      q.push('back=/treehazard.html');
      return ok(res, { url: `/office.html?${q.join('&')}` });
    }
    const dq = [];
    if (line) dq.push(`line=${encodeURIComponent(line)}`);
    if (req.query.team_id) dq.push(`team_id=${encodeURIComponent(req.query.team_id)}`);
    const fileName = exporter.exportFileName(req.team.name, line || undefined);
    const url = buildPreviewUrl(req, `/api/v1/treehazard/export${dq.length ? `?${dq.join('&')}` : ''}`, fileName);
    if (!url) return fail(res, 400, 40011, '未配置文件预览服务');
    return ok(res, { url });
  } catch (err) {
    return next(err);
  }
});

// GET /export?line=：统计表 xlsx 下载（:name 文件名后缀变体供查看器按扩展名识别格式；二进制响应不走 {code,data} 信封）
router.get(['/export', '/export/:name([^/]+\\.xlsx)'], async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const { buf, fileName } = await buildTeamExport(req.team, String(req.query.line ?? '').trim());
    res.setHeader('Content-Type', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet');
    res.setHeader('Content-Disposition', `attachment; filename="treehazard.xlsx"; filename*=UTF-8''${encodeURIComponent(fileName)}`);
    return res.send(buf);
  } catch (err) {
    return next(err);
  }
});

// ===== 暂存区（班管/超管）=====

// GET /staging/batches：本班 pending 批次列表（含记录数）
router.get('/staging/batches', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const [rows] = await pool.query(
      `SELECT b.id, b.team_id, b.crane_session_id, b.group_name, b.pusher_name, b.review_note, b.xlsx_path, b.status, b.created_at,
         (SELECT COUNT(*) FROM treehazard_staging_record s WHERE s.batch_id = b.id) AS record_count
       FROM treehazard_staging_batch b WHERE b.team_id = ? AND b.status = 'pending' ORDER BY b.id DESC`,
      [req.team.id]
    );
    return ok(res, rows);
  } catch (err) {
    return next(err);
  }
});

// GET /staging/batches/:id：批次 + 全部记录行
router.get('/staging/batches/:id', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const [batches] = await pool.query(
      'SELECT id, team_id, crane_session_id, group_name, pusher_name, review_note, xlsx_path, status, created_at FROM treehazard_staging_batch WHERE id = ? AND team_id = ?',
      [Number(req.params.id), req.team.id]
    );
    if (!batches.length) return fail(res, 404, 40400, '批次不存在');
    const [rows] = await pool.query(
      `SELECT id, batch_id, crane_record_id, voltage_level, line_name, section, tree_species, tree_count,
         dist_small, dist_big, diameter, horizontal, vertical, vertical_num, finder_name,
         DATE_FORMAT(found_at, '%Y-%m-%d') AS found_at, handler_name, handle_note,
         DATE_FORMAT(handled_at, '%Y-%m-%d') AS handled_at, created_at
       FROM treehazard_staging_record WHERE batch_id = ? ORDER BY id`,
      [batches[0].id]
    );
    return ok(res, { batch: batches[0], records: rows });
  } catch (err) {
    return next(err);
  }
});

// 暂存行可编辑列与长度（白名单；日期列单独归一）
const STAGING_EDITABLE = {
  voltage_level: 16, line_name: 64, section: 32, tree_species: 32, tree_count: 16,
  dist_small: 16, dist_big: 16, diameter: 16, horizontal: 16, vertical: 16, finder_name: 32,
  handler_name: 32, handle_note: 64,
};

// PUT /staging/records/:id：更新暂存行业务列（白名单字段；vertical 变更时重算 vertical_num）
router.put('/staging/records/:id', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const recordId = Number(req.params.id);
    const [rows] = await pool.query(
      `SELECT s.id FROM treehazard_staging_record s JOIN treehazard_staging_batch b ON b.id = s.batch_id
       WHERE s.id = ? AND b.team_id = ?`,
      [recordId, req.team.id]
    );
    if (!rows.length) return fail(res, 404, 40400, '记录不存在');
    const sets = [];
    const vals = [];
    const body = req.body || {};
    for (const [col, len] of Object.entries(STAGING_EDITABLE)) {
      if (Object.prototype.hasOwnProperty.call(body, col)) {
        sets.push(`${col} = ?`);
        vals.push(String(body[col] ?? '').trim().slice(0, len));
      }
    }
    for (const col of ['found_at', 'handled_at']) {
      if (Object.prototype.hasOwnProperty.call(body, col)) {
        sets.push(`${col} = ?`);
        vals.push(normDateStr(body[col]) || null);
      }
    }
    if (Object.prototype.hasOwnProperty.call(body, 'vertical')) {
      sets.push('vertical_num = ?');
      vals.push(exporter.parseVerticalNum(body.vertical));
    }
    if (!sets.length) return fail(res, 400, 40001, '没有可更新的字段');
    vals.push(recordId);
    await pool.query(`UPDATE treehazard_staging_record SET ${sets.join(', ')} WHERE id = ?`, vals);
    return ok(res, { id: recordId });
  } catch (err) {
    return next(err);
  }
});

// POST /staging/batches/:id/import：导入正式表（body { record_ids? } 缺省全部；事务）：
// 暂存行按契约不含 pole_from/pole_to/position_note：pole 区间由 diff.parseSectionPoles 从区段文本解析（便于按杆号排序）
// 逐行写入（source='staging'，crane_session_id/crane_record_id 从批次与行带过来；撞 uk_crane 改更新发现侧列），
// 删除已导入行；批次行空 → status=imported
router.post('/staging/batches/:id/import', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const batchId = Number(req.params.id);
    const [batches] = await pool.query(
      "SELECT * FROM treehazard_staging_batch WHERE id = ? AND team_id = ? AND status = 'pending'",
      [batchId, req.team.id]
    );
    if (!batches.length) return fail(res, 404, 40400, '批次不存在或已处理');
    const batch = batches[0];
    let ids = null;
    if (req.body && Array.isArray(req.body.record_ids)) {
      ids = req.body.record_ids.map(Number).filter((n) => Number.isInteger(n) && n > 0);
      if (!ids.length) return fail(res, 400, 40001, 'record_ids 无效');
    }
    const conn = await pool.getConnection();
    let imported = 0;
    try {
      await conn.beginTransaction();
      const [rows] = ids
        ? await conn.query('SELECT * FROM treehazard_staging_record WHERE batch_id = ? AND id IN (?) ORDER BY id', [batchId, ids])
        : await conn.query('SELECT * FROM treehazard_staging_record WHERE batch_id = ? ORDER BY id', [batchId]);
      for (const s of rows) {
        const [poleFrom, poleTo] = parseSectionPoles(s.section);
        let existingId = null;
        if (s.crane_record_id) {
          const [ex] = await conn.query(
            'SELECT id FROM treehazard_record WHERE team_id = ? AND crane_session_id = ? AND crane_record_id = ?',
            [req.team.id, batch.crane_session_id, s.crane_record_id]
          );
          existingId = ex.length ? ex[0].id : null;
        }
        if (existingId) {
          await conn.query(
            `UPDATE treehazard_record SET voltage_level = ?, line_name = ?, section = ?, pole_from = ?, pole_to = ?,
               tree_species = ?, tree_count = ?, dist_small = ?, dist_big = ?, diameter = ?, horizontal = ?, vertical = ?,
               vertical_num = ?, finder_name = ?, found_at = ? WHERE id = ?`,
            [s.voltage_level, s.line_name, s.section, poleFrom, poleTo, s.tree_species, s.tree_count,
              s.dist_small, s.dist_big, s.diameter, s.horizontal, s.vertical, s.vertical_num, s.finder_name, s.found_at, existingId]
          );
        } else {
          await conn.query(
            `INSERT INTO treehazard_record (team_id, voltage_level, line_name, section, pole_from, pole_to,
               tree_species, tree_count, dist_small, dist_big, diameter, horizontal, vertical, vertical_num,
               finder_name, found_at, handler_name, handle_note, handled_at, status, source, crane_session_id, crane_record_id)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'staging', ?, ?)`,
            [req.team.id, s.voltage_level, s.line_name, s.section, poleFrom, poleTo, s.tree_species, s.tree_count,
              s.dist_small, s.dist_big, s.diameter, s.horizontal, s.vertical, s.vertical_num, s.finder_name, s.found_at,
              s.handler_name, s.handle_note, s.handled_at, s.handled_at ? 'handled' : 'unhandled',
              batch.crane_session_id, s.crane_record_id]
          );
        }
        await conn.query('DELETE FROM treehazard_staging_record WHERE id = ?', [s.id]);
        imported += 1;
      }
      const [left] = await conn.query('SELECT COUNT(*) AS cnt FROM treehazard_staging_record WHERE batch_id = ?', [batchId]);
      if (!left[0].cnt) await conn.query("UPDATE treehazard_staging_batch SET status = 'imported' WHERE id = ?", [batchId]);
      await conn.commit();
    } catch (e) {
      await conn.rollback().catch(() => {});
      throw e;
    } finally {
      conn.release();
    }
    sync.touch(); // 登记服务端变更，空闲后自动同步内网
    return ok(res, { imported });
  } catch (err) {
    return next(err);
  }
});

// DELETE /staging/batches/:id：物理删除批次与全部记录行（随批 xlsx 一并清理，失败仅记日志）
router.delete('/staging/batches/:id', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const batchId = Number(req.params.id);
    const [batches] = await pool.query(
      'SELECT id, xlsx_path FROM treehazard_staging_batch WHERE id = ? AND team_id = ?',
      [batchId, req.team.id]
    );
    if (!batches.length) return fail(res, 404, 40400, '批次不存在');
    await pool.query('DELETE FROM treehazard_staging_record WHERE batch_id = ?', [batchId]);
    await pool.query('DELETE FROM treehazard_staging_batch WHERE id = ?', [batchId]);
    if (batches[0].xlsx_path) {
      const abs = path.join(__dirname, '..', '..', batches[0].xlsx_path);
      fs.unlink(abs, (e) => {
        if (e && e.code !== 'ENOENT') console.error('[树障] 清理暂存 xlsx 失败：', e.message);
      });
    }
    return ok(res, { id: batchId });
  } catch (err) {
    return next(err);
  }
});

// ===== 冲突（班管/超管；内网同步产生，人工二选一解决）=====

// GET /conflicts：本班 pending 冲突列表（带 record 当前值）
router.get('/conflicts', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const [rows] = await pool.query(
      `SELECT c.id, c.team_id, c.record_id, c.server_snapshot, c.intranet_snapshot, c.status, c.created_at,
         r.voltage_level, r.line_name, r.section, r.tree_species, r.tree_count, r.dist_small, r.dist_big, r.diameter,
         r.horizontal, r.vertical, r.vertical_num, r.finder_name, DATE_FORMAT(r.found_at, '%Y-%m-%d') AS found_at,
         r.handler_name, r.handle_note, DATE_FORMAT(r.handled_at, '%Y-%m-%d') AS handled_at, r.status AS record_status
       FROM treehazard_conflict c LEFT JOIN treehazard_record r ON r.id = c.record_id
       WHERE c.team_id = ? AND c.status = 'pending' ORDER BY c.id`,
      [req.team.id]
    );
    const data = rows.map((row) => ({
      ...row,
      server_snapshot: parseJson(row.server_snapshot),
      intranet_snapshot: parseJson(row.intranet_snapshot),
    }));
    return ok(res, data);
  } catch (err) {
    return next(err);
  }
});

// POST /conflicts/:id/resolve：body { keep: 'server'|'intranet' }
// keep=intranet → 用 intranet_snapshot 业务列覆盖记录并落 synced_at=NOW；keep=server → 仅落 synced_at=NOW
router.post('/conflicts/:id/resolve', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const keep = String((req.body && req.body.keep) ?? '').trim();
    if (!['server', 'intranet'].includes(keep)) return fail(res, 400, 40001, 'keep 应为 server 或 intranet');
    const conflictId = Number(req.params.id);
    const [rows] = await pool.query(
      "SELECT * FROM treehazard_conflict WHERE id = ? AND team_id = ? AND status = 'pending'",
      [conflictId, req.team.id]
    );
    if (!rows.length) return fail(res, 404, 40400, '冲突不存在或已解决');
    const conflict = rows[0];
    const conn = await pool.getConnection();
    try {
      await conn.beginTransaction();
      if (conflict.record_id) {
        if (keep === 'intranet') {
          // 快照列名与内网行对象同形（diff.parseIntranetSheet 产物），复用 diff 的字段集归一
          const fields = intranetFields(parseJson(conflict.intranet_snapshot) || {});
          await conn.query(
            `UPDATE treehazard_record SET voltage_level = ?, line_name = ?, section = ?, tree_species = ?, tree_count = ?,
               dist_small = ?, dist_big = ?, diameter = ?, horizontal = ?, vertical = ?, finder_name = ?, found_at = ?,
               handler_name = ?, handle_note = ?, handled_at = ?, vertical_num = ?, status = ?, synced_at = NOW()
             WHERE id = ?`,
            [fields.voltage_level, fields.line_name, fields.section, fields.tree_species, fields.tree_count,
              fields.dist_small, fields.dist_big, fields.diameter, fields.horizontal, fields.vertical,
              fields.finder_name, fields.found_at, fields.handler_name, fields.handle_note, fields.handled_at,
              fields.vertical_num, fields.status, conflict.record_id]
          );
        } else {
          await conn.query('UPDATE treehazard_record SET synced_at = NOW() WHERE id = ?', [conflict.record_id]);
        }
      }
      await conn.query(
        "UPDATE treehazard_conflict SET status = 'resolved', resolution = ?, resolved_by = ?, resolved_at = NOW() WHERE id = ?",
        [keep, req.user.id, conflictId]
      );
      await conn.commit();
    } catch (e) {
      await conn.rollback().catch(() => {});
      throw e;
    } finally {
      conn.release();
    }
    sync.touch(); // 裁决落库也是服务端变更，空闲后自动同步内网
    return ok(res, { id: conflictId });
  } catch (err) {
    return next(err);
  }
});

// ===== 内网 U 盘同步（编排见 ./sync.js）=====

// POST /sync/push-now：手动触发一轮完整同步（异步执行：含指令窗口等待，不宜挂 HTTP 等待；结果走通知与 /sync/status）
router.post('/sync/push-now', requireDictAdmin, (req, res) => {
  if (!config.treehazard.syncDeviceDdns) return fail(res, 503, 50301, '未配置同步设备');
  if (sync.isRunning()) return fail(res, 423, 42301, '同步进行中，请稍后再试');
  sync.runCycle('manual').catch(() => {}); // runCycle 内部已全 catch + 通知，此处双保险
  return ok(res, { started: true }, '同步已开始');
});

// GET /sync/status：{ enabled, running, last, pending }（last 为本班最近一条 sync_log；pending 为空闲触发剩余分钟数）
router.get('/sync/status', async (req, res, next) => {
  try {
    return ok(res, await sync.status());
  } catch (err) {
    return next(err);
  }
});

// 内网 U 盘同步排程启动（未配置 TREEHAZARD_SYNC_DEVICE_DDNS 时停用不排程）
sync.start();

module.exports = router;
// 供 office 模块签发 src=treehazard 配置时回源复用（router 本体不变，追加命名导出）
module.exports.buildTeamExport = buildTeamExport;
