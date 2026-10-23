// 交跨记录路由：除 POST /ingest（crane-answer 直推，凭 X-Ingest-Token 免登录，挂在 auth 门控之前）外，
// 全部接口需登录 + crossing 应用权限 + 生效班组（req.team，见 utils/team.js）；
// 暂存区/冲突/删记录/手动同步为 超管 或 本班 team_admin（requireDictAdmin，同出工日志字典门控口径）。
// 交跨无处理流程：无 handle 端点、无辅证照片、无阈值设置（数据全部来自群捕获/暂存导入/内网回写）
const express = require('express');
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const teamUtil = require('../utils/team');
const tokenQuery = require('../utils/tokenQuery');
const { buildPreviewUrl } = require('../utils/preview');
const { CN_OFFSET_MS } = require('../utils/cndate');
const config = require('../config');
const exporter = require('./export');
const { intranetFields, normDateStr, parseSectionPoles } = require('./diff');
const ingest = require('./ingest');
const remove = require('./remove'); // 删除记录共用逻辑（DELETE /records/:id 与直推 deletes 同款）
const sync = require('./sync');
const weknoraSync = require('./weknorasync');

const router = express.Router();

// 文件预览服务器回源拉取下载地址时无法附带请求头：无 Authorization 头且 query 带 token 时映射为 Authorization（同出工日志口径）
router.use(tokenQuery);

// crane-answer 直推：不走 JWT，凭请求头 X-Ingest-Token 与配置比对（未配置 → 503；不等 → 401；
// 先比长度再恒定时间比较，防时序侧信道）
router.post('/ingest', (req, res, next) => {
  const token = config.crossing.ingestToken;
  if (!token) return fail(res, 503, 50301, '未配置对接密钥');
  const provided = Buffer.from(String(req.get('X-Ingest-Token') || ''), 'utf8');
  const expected = Buffer.from(token, 'utf8');
  if (provided.length !== expected.length || !crypto.timingSafeEqual(provided, expected)) {
    return fail(res, 401, 40104, '对接密钥错误');
  }
  return ingest.handleIngest(req, res, next);
});

router.use(auth, requireApp('crossing'));

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

// JSON 列防御解析（mysql2 可能返回字符串或已解析对象，与 treehazard options 同口径）
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
const RECORD_COLS = `id, team_id, voltage_level, line_name, section, pole_from, pole_to, crossing_type,
  dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
  finder_name, DATE_FORMAT(inspect_at, '%Y-%m-%d') AS inspect_at, remark,
  source, crane_session_id, crane_record_id, synced_at, created_by, created_at, updated_at`;

async function loadTeamRecords(teamId) {
  const [rows] = await pool.query(`SELECT ${RECORD_COLS} FROM crossing_record WHERE team_id = ?`, [
    teamId,
  ]);
  return rows;
}

// 北京时间本周一 00:00 的库内墙钟文本（库内 created_at 随容器为 UTC 墙钟，北京周界减 8h 后比较）
function weekStartText() {
  const cnNow = new Date(Date.now() + CN_OFFSET_MS); // 其 UTC 字段即北京墙钟
  const dow = (cnNow.getUTCDay() + 6) % 7; // 周一 = 0
  const utcMs =
    Date.UTC(cnNow.getUTCFullYear(), cnNow.getUTCMonth(), cnNow.getUTCDate() - dow, 0, 0, 0) -
    CN_OFFSET_MS;
  const d = new Date(utcMs);
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${p(d.getUTCHours())}:${p(d.getUTCMinutes())}:${p(d.getUTCSeconds())}`;
}

// ===== 看板 =====

// GET /dashboard：本班 记录总数 / 本周新增（created_at 在本周一至今）/ 存在隐患数（has_hazard 非空）/
// 待确认线路数（待导入暂存批次涉及的去重线路数），顺带返回 pending_conflicts / staging_batches 计数
router.get('/dashboard', async (req, res, next) => {
  try {
    if (!req.team) {
      return ok(res, {
        total: 0,
        week_new: 0,
        hazard: 0,
        pending_lines: 0,
        pending_conflicts: 0,
        staging_batches: 0,
      });
    }
    const teamId = req.team.id;
    const [[total]] = await pool.query(
      'SELECT COUNT(*) AS n FROM crossing_record WHERE team_id = ?',
      [teamId]
    );
    const [[weekNew]] = await pool.query(
      'SELECT COUNT(*) AS n FROM crossing_record WHERE team_id = ? AND created_at >= ?',
      [teamId, weekStartText()]
    );
    const [[hazard]] = await pool.query(
      "SELECT COUNT(*) AS n FROM crossing_record WHERE team_id = ? AND has_hazard <> ''",
      [teamId]
    );
    const [[pendingLines]] = await pool.query(
      `SELECT COUNT(DISTINCT s.line_name) AS n FROM crossing_staging_record s
       JOIN crossing_staging_batch b ON b.id = s.batch_id
       WHERE b.team_id = ? AND b.status = 'pending'`,
      [teamId]
    );
    const [[conflicts]] = await pool.query(
      "SELECT COUNT(*) AS n FROM crossing_conflict WHERE team_id = ? AND status = 'pending'",
      [teamId]
    );
    const [[staging]] = await pool.query(
      "SELECT COUNT(*) AS n FROM crossing_staging_batch WHERE team_id = ? AND status = 'pending'",
      [teamId]
    );
    return ok(res, {
      total: total.n,
      week_new: weekNew.n,
      hazard: hazard.n,
      pending_lines: pendingLines.n,
      pending_conflicts: conflicts.n,
      staging_batches: staging.n,
    });
  } catch (err) {
    return next(err);
  }
});

// ===== 记录列表 =====

// GET /records：本班全部记录，按 线路分组（team_line 运维线路导入序，未匹配排尾）返回，组内
// 按 区段升序（同级 pole_from 升序 → created_at）；运维线路全部成组下发（含 0 记录线路）
router.get('/records', async (req, res, next) => {
  try {
    if (!req.team) return ok(res, { lines: [], unmatched: [] });
    const teamId = req.team.id;
    const [lineRows, records] = await Promise.all([
      exporter.fetchLineOrder(pool, teamId),
      loadTeamRecords(teamId),
    ]);
    const lineOrder = new Map(lineRows.map((l, i) => [l.line_name, i]));
    const sorted = exporter.sortRecords(records, exporter.makeLineGroupComparator(lineOrder));
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
    // 运维线路全部成组下发（含 0 记录线路），线路大卡需展示空线路
    const lines = lineRows.map((l) => ({
      voltage_level: l.voltage_level,
      line_name: l.line_name,
      records: byLine.get(l.line_name) || [],
    }));
    return ok(res, { lines, unmatched });
  } catch (err) {
    return next(err);
  }
});

// GET /records/:id：单条记录（仅本班；备用：详情/单条刷新）
router.get('/records/:id', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const recordId = Number(req.params.id);
    const [rows] = await pool.query(
      `SELECT ${RECORD_COLS} FROM crossing_record WHERE id = ? AND team_id = ?`,
      [recordId, req.team.id]
    );
    if (!rows.length) return fail(res, 404, 40400, '记录不存在');
    return ok(res, rows[0]);
  } catch (err) {
    return next(err);
  }
});

// DELETE /records/:id：班管/超管；删记录、写 crossing_deleted_id 墓碑
// （删除本体见 ./remove.js，与直推 deletes 共用同款逻辑）
router.delete('/records/:id', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const recordId = Number(req.params.id);
    const [rows] = await pool.query('SELECT id FROM crossing_record WHERE id = ? AND team_id = ?', [
      recordId,
      req.team.id,
    ]);
    if (!rows.length) return fail(res, 404, 40400, '记录不存在');
    await remove.deleteRecord(recordId, req.team.id);
    return ok(res, { id: recordId });
  } catch (err) {
    return next(err);
  }
});

// ===== 导出与在线预览 =====

// query/body 的 split 开关归一（'1'/'true'/1/true 为开）：按线路分 sheet 导出
const splitOn = (v) => v === '1' || v === 'true' || v === 1 || v === true;

// 生成统计表 buffer（team 参数化：路由传 req.team，office 在线预览回源复用；line 可选分线路过滤；
// split=true 按线路分 sheet；记录由 buildWorkbook 按出口口径内部排序：单表 = 电压等级降序>线路名升序
// >区段升序，分表 = 导入序分 sheet + sheet 内区段升序）
async function buildTeamExport(team, line, split) {
  let records = await loadTeamRecords(team.id);
  const lineRows = await exporter.fetchLineOrder(pool, team.id);
  const lineOrder = new Map(lineRows.map((l, i) => [l.line_name, i]));
  if (line) records = records.filter((r) => r.line_name === line);
  const wb = await exporter.buildWorkbook({
    teamName: team.name,
    records,
    split: !!split,
    lineOrder,
  });
  const buf = Buffer.from(await wb.xlsx.writeBuffer());
  return { buf, fileName: exporter.exportFileName(team.name, line || undefined) };
}

// GET /export/preview?line=：OO 启用（OO_PUBLIC_URL）→ 返回 /office.html 承载页地址（office 模块 src=crossing 签发，
// crs 票据回源即时生成）；否则微软 Office 查看器（凭地址内 ?token= 回源下方下载接口，同出工日志 /task-sheet/preview 口径）
router.get('/export/preview', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const line = String(req.query.line ?? '').trim();
    if (config.office.publicUrl) {
      const q = ['src=crossing'];
      if (line) q.push(`line=${encodeURIComponent(line)}`);
      if (req.query.team_id) q.push(`team_id=${encodeURIComponent(req.query.team_id)}`);
      if (splitOn(req.query.split)) q.push('split=1');
      q.push('back=/crossing.html');
      return ok(res, { url: `/office.html?${q.join('&')}` });
    }
    const dq = [];
    if (line) dq.push(`line=${encodeURIComponent(line)}`);
    if (req.query.team_id) dq.push(`team_id=${encodeURIComponent(req.query.team_id)}`);
    if (splitOn(req.query.split)) dq.push('split=1');
    const fileName = exporter.exportFileName(req.team.name, line || undefined);
    const url = buildPreviewUrl(
      req,
      `/api/v1/crossing/export${dq.length ? `?${dq.join('&')}` : ''}`,
      fileName
    );
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
    const { buf, fileName } = await buildTeamExport(
      req.team,
      String(req.query.line ?? '').trim(),
      splitOn(req.query.split)
    );
    res.setHeader(
      'Content-Type',
      'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'
    );
    res.setHeader(
      'Content-Disposition',
      `attachment; filename="crossing.xlsx"; filename*=UTF-8''${encodeURIComponent(fileName)}`
    );
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
         (SELECT COUNT(*) FROM crossing_staging_record s WHERE s.batch_id = b.id) AS record_count
       FROM crossing_staging_batch b WHERE b.team_id = ? AND b.status = 'pending' ORDER BY b.id DESC`,
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
      'SELECT id, team_id, crane_session_id, group_name, pusher_name, review_note, xlsx_path, status, created_at FROM crossing_staging_batch WHERE id = ? AND team_id = ?',
      [Number(req.params.id), req.team.id]
    );
    if (!batches.length) return fail(res, 404, 40400, '批次不存在');
    const [rows] = await pool.query(
      `SELECT id, batch_id, crane_record_id, voltage_level, line_name, section, crossing_type,
         dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard, finder_name,
         DATE_FORMAT(inspect_at, '%Y-%m-%d') AS inspect_at, remark, created_at
       FROM crossing_staging_record WHERE batch_id = ? ORDER BY id`,
      [batches[0].id]
    );
    return ok(res, { batch: batches[0], records: rows });
  } catch (err) {
    return next(err);
  }
});

// 暂存行可编辑列与长度（白名单；日期列单独归一）
const STAGING_EDITABLE = {
  voltage_level: 16,
  line_name: 64,
  section: 32,
  crossing_type: 128,
  dist_small: 16,
  dist_big: 16,
  cross_horizontal: 16,
  cross_vertical: 16,
  cross_angle: 16,
  has_hazard: 16,
  finder_name: 32,
  remark: 255,
};

// PUT /staging/records/:id：更新暂存行业务列（白名单字段）
router.put('/staging/records/:id', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const recordId = Number(req.params.id);
    const [rows] = await pool.query(
      `SELECT s.id FROM crossing_staging_record s JOIN crossing_staging_batch b ON b.id = s.batch_id
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
        vals.push(
          String(body[col] ?? '')
            .trim()
            .slice(0, len)
        );
      }
    }
    if (Object.prototype.hasOwnProperty.call(body, 'inspect_at')) {
      sets.push('inspect_at = ?');
      vals.push(normDateStr(body.inspect_at) || null);
    }
    if (!sets.length) return fail(res, 400, 40001, '没有可更新的字段');
    vals.push(recordId);
    await pool.query(`UPDATE crossing_staging_record SET ${sets.join(', ')} WHERE id = ?`, vals);
    return ok(res, { id: recordId });
  } catch (err) {
    return next(err);
  }
});

// POST /staging/batches/:id/import：导入正式表（body { record_ids? } 缺省全部；事务）：
// 暂存行按契约不含 pole_from/pole_to：pole 区间由 diff.parseSectionPoles 从区段文本解析（便于按杆号排序）
// 逐行写入（source='staging'，crane_session_id/crane_record_id 从批次与行带过来；撞 uk_crane 改更新业务列），
// 删除已导入行；批次行空 → status=imported
router.post('/staging/batches/:id/import', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const batchId = Number(req.params.id);
    const [batches] = await pool.query(
      "SELECT * FROM crossing_staging_batch WHERE id = ? AND team_id = ? AND status = 'pending'",
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
        ? await conn.query(
            'SELECT * FROM crossing_staging_record WHERE batch_id = ? AND id IN (?) ORDER BY id',
            [batchId, ids]
          )
        : await conn.query('SELECT * FROM crossing_staging_record WHERE batch_id = ? ORDER BY id', [
            batchId,
          ]);
      for (const s of rows) {
        const [poleFrom, poleTo] = parseSectionPoles(s.section);
        let existingId = null;
        if (s.crane_record_id) {
          const [ex] = await conn.query(
            'SELECT id FROM crossing_record WHERE team_id = ? AND crane_session_id = ? AND crane_record_id = ?',
            [req.team.id, batch.crane_session_id, s.crane_record_id]
          );
          existingId = ex.length ? ex[0].id : null;
        }
        if (existingId) {
          await conn.query(
            `UPDATE crossing_record SET voltage_level = ?, line_name = ?, section = ?, pole_from = ?, pole_to = ?,
               crossing_type = ?, dist_small = ?, dist_big = ?, cross_horizontal = ?, cross_vertical = ?, cross_angle = ?,
               has_hazard = ?, finder_name = ?, inspect_at = ?, remark = ?
             WHERE id = ?`,
            [
              s.voltage_level,
              s.line_name,
              s.section,
              poleFrom,
              poleTo,
              s.crossing_type,
              s.dist_small,
              s.dist_big,
              s.cross_horizontal,
              s.cross_vertical,
              s.cross_angle,
              s.has_hazard,
              s.finder_name,
              s.inspect_at,
              s.remark,
              existingId,
            ]
          );
        } else {
          await conn.query(
            `INSERT INTO crossing_record (team_id, voltage_level, line_name, section, pole_from, pole_to, crossing_type,
               dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
               finder_name, inspect_at, remark, source, crane_session_id, crane_record_id)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'staging', ?, ?)`,
            [
              req.team.id,
              s.voltage_level,
              s.line_name,
              s.section,
              poleFrom,
              poleTo,
              s.crossing_type,
              s.dist_small,
              s.dist_big,
              s.cross_horizontal,
              s.cross_vertical,
              s.cross_angle,
              s.has_hazard,
              s.finder_name,
              s.inspect_at,
              s.remark,
              batch.crane_session_id,
              s.crane_record_id,
            ]
          );
        }
        await conn.query('DELETE FROM crossing_staging_record WHERE id = ?', [s.id]);
        imported += 1;
      }
      const [left] = await conn.query(
        'SELECT COUNT(*) AS cnt FROM crossing_staging_record WHERE batch_id = ?',
        [batchId]
      );
      if (!left[0].cnt)
        await conn.query("UPDATE crossing_staging_batch SET status = 'imported' WHERE id = ?", [
          batchId,
        ]);
      await conn.commit();
    } catch (e) {
      await conn.rollback().catch(() => {});
      throw e;
    } finally {
      conn.release();
    }
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
      'SELECT id, xlsx_path FROM crossing_staging_batch WHERE id = ? AND team_id = ?',
      [batchId, req.team.id]
    );
    if (!batches.length) return fail(res, 404, 40400, '批次不存在');
    await pool.query('DELETE FROM crossing_staging_record WHERE batch_id = ?', [batchId]);
    await pool.query('DELETE FROM crossing_staging_batch WHERE id = ?', [batchId]);
    if (batches[0].xlsx_path) {
      const abs = path.join(__dirname, '..', '..', batches[0].xlsx_path);
      fs.unlink(abs, (e) => {
        if (e && e.code !== 'ENOENT') console.error('[交跨] 清理暂存 xlsx 失败：', e.message);
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
         r.voltage_level, r.line_name, r.section, r.crossing_type, r.dist_small, r.dist_big, r.cross_horizontal,
         r.cross_vertical, r.cross_angle, r.has_hazard, r.finder_name,
         DATE_FORMAT(r.inspect_at, '%Y-%m-%d') AS inspect_at, r.remark
       FROM crossing_conflict c LEFT JOIN crossing_record r ON r.id = c.record_id
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
    if (!['server', 'intranet'].includes(keep))
      return fail(res, 400, 40001, 'keep 应为 server 或 intranet');
    const conflictId = Number(req.params.id);
    const [rows] = await pool.query(
      "SELECT * FROM crossing_conflict WHERE id = ? AND team_id = ? AND status = 'pending'",
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
            `UPDATE crossing_record SET voltage_level = ?, line_name = ?, section = ?, crossing_type = ?,
               dist_small = ?, dist_big = ?, cross_horizontal = ?, cross_vertical = ?, cross_angle = ?, has_hazard = ?,
               finder_name = ?, inspect_at = ?, remark = ?, synced_at = NOW()
             WHERE id = ?`,
            [
              fields.voltage_level,
              fields.line_name,
              fields.section,
              fields.crossing_type,
              fields.dist_small,
              fields.dist_big,
              fields.cross_horizontal,
              fields.cross_vertical,
              fields.cross_angle,
              fields.has_hazard,
              fields.finder_name,
              fields.inspect_at,
              fields.remark,
              conflict.record_id,
            ]
          );
        } else {
          await conn.query('UPDATE crossing_record SET synced_at = NOW() WHERE id = ?', [
            conflict.record_id,
          ]);
        }
      }
      await conn.query(
        "UPDATE crossing_conflict SET status = 'resolved', resolution = ?, resolved_by = ?, resolved_at = NOW() WHERE id = ?",
        [keep, req.user.id, conflictId]
      );
      await conn.commit();
    } catch (e) {
      await conn.rollback().catch(() => {});
      throw e;
    } finally {
      conn.release();
    }
    return ok(res, { id: conflictId });
  } catch (err) {
    return next(err);
  }
});

// ===== 内网 U 盘同步（编排见 ./sync.js）=====

// POST /sync/push-now：手动触发一轮完整同步（异步执行：含指令窗口等待，不宜挂 HTTP 等待；结果走通知与 /sync/status）
router.post('/sync/push-now', requireDictAdmin, (req, res) => {
  if (!config.crossing.syncDeviceDdns) return fail(res, 503, 50301, '未配置同步设备');
  if (sync.isRunning()) return fail(res, 423, 42301, '同步进行中，请稍后再试');
  sync.runCycle('manual').catch(() => {}); // runCycle 内部已全 catch + 通知，此处双保险
  return ok(res, { started: true }, '同步已开始');
});

// GET /sync/status：{ enabled, running, last, nextNight }（last 为本班最近一条 sync_log；nextNight 为下次夜间取件时间，北京时间 ISO，未启用为 null）
router.get('/sync/status', async (req, res, next) => {
  try {
    return ok(res, await sync.status());
  } catch (err) {
    return next(err);
  }
});

// 内网 U 盘同步排程启动（未配置 CROSSING_SYNC_DEVICE_DDNS 时停用不排程）
sync.start();

// ===== WeKnora 知识库同步（编排见 ./weknorasync.js）=====

// POST /weknora-sync/push-now：手动触发一轮知识库快照同步（班管/超管；交跨同步量级为数个 HTTP 调用，
// await 同步结果返回 {ok, created, updated, deleted, errors[]} 供按钮 toast 明细）
router.post('/weknora-sync/push-now', requireDictAdmin, async (req, res, next) => {
  try {
    if (!config.weknora.crossingKbId || !config.weknora.apiKey)
      return fail(res, 503, 50301, '未配置知识库同步');
    if (weknoraSync.isRunning()) return fail(res, 423, 42301, '同步进行中，请稍后再试');
    const r = await weknoraSync.runSync();
    return ok(res, r, r.ok ? '已同步至知识库' : '同步完成，部分失败');
  } catch (err) {
    return next(err);
  }
});

// WeKnora 知识库同步排程启动（在 sync.start() 之后注册：与 crossing-fetch 同时刻串行，取件回写完再同步；
// 未配置知识库 ID 或 WEKNORA_API_KEY 时停用不排程）
weknoraSync.start();

module.exports = router;
// 供 office 模块签发 src=crossing 配置时回源复用（router 本体不变，追加命名导出）
module.exports.buildTeamExport = buildTeamExport;
