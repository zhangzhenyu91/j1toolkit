// 班组数据路由：车牌 / 目的地 / 出工成员 / 线路杆塔四类班组字典的统一管理（数据复用 worklog_dict_* 与
// worklog_tower 表，表结构不动；端点镜像 worklog 管理端点，请求/响应形态 1:1 一致，仅基路径为 /api/v1/teamdata）
// 全部接口需登录 + team-data 应用权限 + 生效班组（req.team），且全路由限 超管 或 本班 team_admin
const express = require('express');
const multer = require('multer');
const XLSX = require('xlsx');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const teamUtil = require('../utils/team');
const { fixLatin1Name } = require('../utils/file');
const towers = require('../worklog/towers'); // 杆塔缓存（导入后失效重载，与 worklog 共用）

const router = express.Router();
router.use(auth, requireApp('team-data'));

// 班组上下文：解析生效班组（超管可用 ?team_id= 指定；其余角色固定本班，未分配 → null）
router.use(async (req, res, next) => {
  try {
    req.team = await teamUtil.resolveReqTeam(req);
    return next();
  } catch (err) {
    return next(err);
  }
});

// 全路由管理门控：超管任意班组（req.team 由 ?team_id= 解析），班组管理员仅本班
router.use((req, res, next) => {
  if (req.user.role === 'admin') return next();
  if (req.user.role === 'team_admin' && req.team && req.user.team_id === req.team.id) return next();
  return fail(res, 403, 40304, '仅管理员可执行此操作');
});

// ===== 车牌 / 目的地 / 成员字典（实现照抄 worklog/index.js dictRoutes，仅去掉 /admin 前缀）=====

function dictRoutes(path, table, field, label, countRefs) {
  // 名称列宽：plate_no VARCHAR(32)，目的地/成员 name VARCHAR(64)（入库前截断防 ER_DATA_TOO_LONG）
  const nameLen = field === 'plate_no' ? 32 : 64;
  router.get(`/${path}`, async (req, res, next) => {
    try {
      if (!req.team) return ok(res, { list: [] });
      const extra = table === 'worklog_member' ? ', user_id' : (table === 'worklog_destination' ? ', out_of_city, stationed' : '');
      const [rows] = await pool.query(
        `SELECT id, ${field} AS name, sort, status${extra} FROM ${table} WHERE team_id = ? ORDER BY sort, id`,
        [req.team.id]
      );
      return ok(res, { list: rows });
    } catch (err) {
      return next(err);
    }
  });

  router.post(`/${path}`, async (req, res, next) => {
    try {
      if (!req.team) return fail(res, 400, 40010, '无可用班组');
      const name = String((req.body && req.body.name) || '').trim().slice(0, nameLen);
      if (!name) return fail(res, 400, 40012, `请输入${label}名称`);
      const [maxRows] = await pool.query(
        `SELECT COALESCE(MAX(sort), 0) AS maxSort FROM ${table} WHERE team_id = ?`,
        [req.team.id]
      );
      try {
        const [r] = await pool.query(
          `INSERT INTO ${table} (team_id, ${field}, sort) VALUES (?, ?, ?)`,
          [req.team.id, name, maxRows[0].maxSort + 1]
        );
        return ok(res, { id: r.insertId });
      } catch (err) {
        if (err.code === 'ER_DUP_ENTRY') return fail(res, 409, 40900, `「${name}」已存在`);
        throw err;
      }
    } catch (err) {
      return next(err);
    }
  });

  router.put(`/${path}/:id`, async (req, res, next) => {
    try {
      if (!req.team) return fail(res, 400, 40010, '无可用班组');
      const id = Number(req.params.id);
      const [exist] = await pool.query(`SELECT id FROM ${table} WHERE id = ? AND team_id = ?`, [id, req.team.id]);
      if (!exist.length) return fail(res, 404, 40400, `${label}不存在`);
      const { name, sort, status } = req.body || {};
      if (name !== undefined) {
        const trimmed = String(name).trim().slice(0, nameLen);
        if (!trimmed) return fail(res, 400, 40012, `请输入${label}名称`);
        try {
          await pool.query(`UPDATE ${table} SET ${field} = ? WHERE id = ?`, [trimmed, id]);
        } catch (err) {
          if (err.code === 'ER_DUP_ENTRY') return fail(res, 409, 40900, `「${trimmed}」已存在`);
          throw err;
        }
      }
      if (sort !== undefined) {
        await pool.query(`UPDATE ${table} SET sort = ? WHERE id = ?`, [Number(sort) || 0, id]);
      }
      if (status !== undefined) {
        await pool.query(`UPDATE ${table} SET status = ? WHERE id = ?`, [Number(status) ? 1 : 0, id]);
      }
      // 市外/驻地标记（仅目的地字典，互斥——费用口径同出工日志）：标一个即清另一个；同置 1 拒绝
      if (table === 'worklog_destination' && req.body
        && (req.body.out_of_city !== undefined || req.body.stationed !== undefined)) {
        const ooc = req.body.out_of_city === undefined ? null : (Number(req.body.out_of_city) ? 1 : 0);
        const sta = req.body.stationed === undefined ? null : (Number(req.body.stationed) ? 1 : 0);
        if (ooc === 1 && sta === 1) return fail(res, 400, 40012, '市外与驻地只能标记其一');
        const sets = [];
        const vals = [];
        if (ooc !== null) { sets.push('out_of_city = ?'); vals.push(ooc); }
        if (sta !== null) { sets.push('stationed = ?'); vals.push(sta); }
        if (ooc === 1) sets.push('stationed = 0');
        if (sta === 1) sets.push('out_of_city = 0');
        await pool.query(`UPDATE ${table} SET ${sets.join(', ')} WHERE id = ?`, [...vals, id]);
      }
      return ok(res, null);
    } catch (err) {
      return next(err);
    }
  });

  router.delete(`/${path}/:id`, async (req, res, next) => {
    try {
      if (!req.team) return fail(res, 400, 40010, '无可用班组');
      const id = Number(req.params.id);
      const refs = await countRefs(id);
      if (refs > 0) return fail(res, 409, 40901, `该${label}已被 ${refs} 条日志引用，请改为停用`);
      const [r] = await pool.query(`DELETE FROM ${table} WHERE id = ? AND team_id = ?`, [id, req.team.id]);
      if (!r.affectedRows) return fail(res, 404, 40400, `${label}不存在`);
      return ok(res, null);
    } catch (err) {
      return next(err);
    }
  });
}

dictRoutes('vehicles', 'worklog_vehicle', 'plate_no', '车牌', async (id) => {
  const [rows] = await pool.query('SELECT COUNT(*) AS cnt FROM worklog_entry WHERE vehicle_id = ?', [id]);
  return rows[0].cnt;
});
dictRoutes('destinations', 'worklog_destination', 'name', '目的地', async (id) => {
  const [rows] = await pool.query('SELECT COUNT(*) AS cnt FROM worklog_entry WHERE destination_id = ?', [id]);
  return rows[0].cnt;
});
dictRoutes('members', 'worklog_member', 'name', '成员', async (id) => {
  const [rows] = await pool.query('SELECT COUNT(*) AS cnt FROM worklog_entry_member WHERE member_id = ?', [id]);
  return rows[0].cnt;
});

// PUT /members/:id/move {direction:'up'|'down'}：成员排序（与班内相邻成员换位；事务内整体重写 sort 为连续 1..N，
// 兼容历史同值 sort；direction 为准，兼容 worklog 旧口径 dir）
router.put('/members/:id/move', async (req, res, next) => {
  const conn = await pool.getConnection();
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const id = Number(req.params.id);
    const rawDir = req.body && (req.body.direction || req.body.dir);
    const dir = rawDir === 'up' ? 'up' : 'down';
    await conn.beginTransaction();
    const [rows] = await conn.query(
      'SELECT id FROM worklog_member WHERE team_id = ? ORDER BY sort, id',
      [req.team.id]
    );
    const idx = rows.findIndex((r) => r.id === id);
    if (idx < 0) {
      await conn.rollback();
      return fail(res, 404, 40400, '成员不存在');
    }
    const swapIdx = dir === 'up' ? idx - 1 : idx + 1;
    if (swapIdx < 0 || swapIdx >= rows.length) {
      await conn.rollback();
      return ok(res, null); // 已在顶端 / 底端，次序不变
    }
    const order = rows.map((r) => r.id);
    order.splice(swapIdx, 0, order.splice(idx, 1)[0]);
    for (let i = 0; i < order.length; i += 1) {
      await conn.query('UPDATE worklog_member SET sort = ? WHERE id = ?', [i + 1, order[i]]);
    }
    await conn.commit();
    return ok(res, null);
  } catch (err) {
    await conn.rollback().catch(() => {});
    return next(err);
  } finally {
    conn.release();
  }
});

// ===== 线路与杆塔 =====

// GET /lines：本班线路聚合（按 worklog_tower 导入序），[{ voltage_level, line_name, tower_count }]
router.get('/lines', async (req, res, next) => {
  try {
    if (!req.team) return ok(res, []);
    const [rows] = await pool.query(
      `SELECT voltage_level, line_name, COUNT(*) AS tower_count
       FROM worklog_tower WHERE team_id = ? GROUP BY voltage_level, line_name ORDER BY MIN(sort)`,
      [req.team.id]
    );
    return ok(res, rows);
  } catch (err) {
    return next(err);
  }
});

// GET /towers：本班全部杆塔行（管理视图，含 id 与 sort）
router.get('/towers', async (req, res, next) => {
  try {
    if (!req.team) return ok(res, []);
    const [rows] = await pool.query(
      'SELECT id, voltage_level, line_name, tower_no, lng, lat, sort FROM worklog_tower WHERE team_id = ? ORDER BY sort, id',
      [req.team.id]
    );
    return ok(res, rows);
  } catch (err) {
    return next(err);
  }
});

// GET /towers/template：杆塔坐标导入模板（xlsx，表头 + 示例行；实现照抄 worklog /towers/template）
router.get('/towers/template', async (req, res, next) => {
  try {
    const ws = XLSX.utils.aoa_to_sheet([
      ['电压等级', '线路名称', '杆塔号', '经度', '纬度'],
      ['110kV', '示例线路', 'N1', '111.123456', '37.123456'],
    ]);
    ws['!cols'] = [{ wch: 12 }, { wch: 20 }, { wch: 12 }, { wch: 14 }, { wch: 14 }];
    const wb = XLSX.utils.book_new();
    XLSX.utils.book_append_sheet(wb, ws, '杆塔坐标');
    const buf = XLSX.write(wb, { type: 'buffer', bookType: 'xlsx' });
    res.setHeader('Content-Type', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet');
    res.setHeader(
      'Content-Disposition',
      `attachment; filename="tower-template.xlsx"; filename*=UTF-8''${encodeURIComponent('杆塔坐标导入模板.xlsx')}`
    );
    return res.send(buf);
  } catch (err) {
    return next(err);
  }
});

// POST /towers/import：导入杆塔坐标 Excel（.xlsx），全量替换本班组坐标（事务；实现照抄 worklog /towers/import）
const towerUpload = multer({ storage: multer.memoryStorage(), limits: { fileSize: 10 * 1024 * 1024, files: 1 } });
router.post(
  '/towers/import',
  (req, res, next) => {
    towerUpload.single('file')(req, res, (err) => {
      if (err) {
        if (err.code === 'LIMIT_FILE_SIZE') return fail(res, 400, 40022, '文件大小应在 10MB 以内');
        return next(err);
      }
      return next();
    });
  },
  async (req, res, next) => {
    try {
      // multipart 表单体在路由级 multer 之后才可读：此处重新解析生效班组（兼容 formData 携带 team_id）
      const team = await teamUtil.resolveTeam(req.user, req.body && req.body.team_id !== undefined ? req.body.team_id : req.query.team_id);
      if (!team) return fail(res, 400, 40022, '无可用班组');
      if (req.user.role === 'team_admin' && req.user.team_id !== team.id) {
        return fail(res, 403, 40304, '仅管理员可执行此操作');
      }
      if (!req.file || !req.file.buffer || !req.file.buffer.length) {
        return fail(res, 400, 40022, '请选择要上传的 Excel 文件');
      }
      const fname = fixLatin1Name(req.file.originalname);
      if (!/\.xlsx$/i.test(fname)) return fail(res, 400, 40022, '仅支持 .xlsx 文件，请使用模板填写');

      let rows;
      try {
        const wb = XLSX.read(req.file.buffer, { type: 'buffer' });
        rows = XLSX.utils.sheet_to_json(wb.Sheets[wb.SheetNames[0]], { header: 1, raw: false, defval: '' });
      } catch (e) {
        return fail(res, 400, 40022, 'Excel 解析失败，请使用模板文件填写');
      }

      // 逐行校验：空行跳过；表头行跳过；缺列或经纬度非数字计为跳过
      const data = [];
      let skipped = 0;
      for (const r of rows) {
        const cells = (Array.isArray(r) ? r : []).map((c) => String(c).trim());
        if (cells.every((c) => !c)) continue;
        if (cells[0].includes('电压') || cells[2].includes('杆塔')) continue;
        const [voltage, line, towerNo, lng, lat] = cells;
        if (!voltage || !line || !towerNo || !lng || !lat
          || Number.isNaN(Number(lng)) || Number.isNaN(Number(lat))) {
          skipped++;
          continue;
        }
        data.push([
          team.id,
          voltage.slice(0, 32), line.slice(0, 64), towerNo.slice(0, 64),
          lng.slice(0, 32), lat.slice(0, 32),
        ]);
      }
      if (!data.length) return fail(res, 400, 40022, '未识别到有效坐标行，请按模板列填写');

      const conn = await pool.getConnection();
      try {
        await conn.beginTransaction();
        await conn.query('DELETE FROM worklog_tower WHERE team_id = ?', [team.id]);
        for (let i = 0; i < data.length; i += 500) {
          const chunk = data.slice(i, i + 500).map((r, j) => [...r, i + j + 1]);
          await conn.query(
            'INSERT INTO worklog_tower (team_id, voltage_level, line_name, tower_no, lng, lat, sort) VALUES ?',
            [chunk]
          );
        }
        await conn.commit();
      } catch (e) {
        await conn.rollback().catch(() => {});
        throw e;
      } finally {
        conn.release();
      }
      towers.invalidate(team.id); // 与 worklog 共用的杆塔缓存失效重载
      return ok(res, { imported: data.length, skipped }, `已导入 ${data.length} 条坐标`);
    } catch (err) {
      return next(err);
    }
  }
);

// ===== 运维线路（team_line，建表见 ./schema.js；树障隐患分组/导出共用此线路源）=====

// GET /oplines：本班运维线路（按 sort），{ list: [{ id, voltage_level, line_name, sort, created_at, updated_at }] }
router.get('/oplines', async (req, res, next) => {
  try {
    if (!req.team) return ok(res, { list: [] });
    const [rows] = await pool.query(
      'SELECT id, voltage_level, line_name, sort, created_at, updated_at FROM team_line WHERE team_id = ? ORDER BY sort, id',
      [req.team.id]
    );
    return ok(res, { list: rows });
  } catch (err) {
    return next(err);
  }
});

// POST /oplines {voltage_level, line_name}：新增线路（sort 排尾；班内 line_name 唯一）
router.post('/oplines', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const voltage = String((req.body && req.body.voltage_level) || '').trim().slice(0, 16);
    const lineName = String((req.body && req.body.line_name) || '').trim().slice(0, 64);
    if (!voltage) return fail(res, 400, 40012, '请输入电压等级');
    if (!lineName) return fail(res, 400, 40012, '请输入线路名称');
    const [maxRows] = await pool.query('SELECT COALESCE(MAX(sort), 0) AS maxSort FROM team_line WHERE team_id = ?', [req.team.id]);
    try {
      const [r] = await pool.query(
        'INSERT INTO team_line (team_id, voltage_level, line_name, sort) VALUES (?, ?, ?, ?)',
        [req.team.id, voltage, lineName, maxRows[0].maxSort + 1]
      );
      return ok(res, { id: r.insertId });
    } catch (err) {
      if (err.code === 'ER_DUP_ENTRY') return fail(res, 409, 40900, `「${lineName}」已存在`);
      throw err;
    }
  } catch (err) {
    return next(err);
  }
});

// PUT /oplines/:id {voltage_level?, line_name?}：改电压等级/线路名（仅更新出现的字段）
router.put('/oplines/:id', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const id = Number(req.params.id);
    const [exist] = await pool.query('SELECT id FROM team_line WHERE id = ? AND team_id = ?', [id, req.team.id]);
    if (!exist.length) return fail(res, 404, 40400, '线路不存在');
    const { voltage_level: voltage, line_name: lineName } = req.body || {};
    if (voltage !== undefined) {
      const trimmed = String(voltage).trim().slice(0, 16);
      if (!trimmed) return fail(res, 400, 40012, '请输入电压等级');
      await pool.query('UPDATE team_line SET voltage_level = ? WHERE id = ?', [trimmed, id]);
    }
    if (lineName !== undefined) {
      const trimmed = String(lineName).trim().slice(0, 64);
      if (!trimmed) return fail(res, 400, 40012, '请输入线路名称');
      try {
        await pool.query('UPDATE team_line SET line_name = ? WHERE id = ?', [trimmed, id]);
      } catch (err) {
        if (err.code === 'ER_DUP_ENTRY') return fail(res, 409, 40900, `「${trimmed}」已存在`);
        throw err;
      }
    }
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// DELETE /oplines/:id：删除线路（无外键引用；树障记录按 line_name 文本关联，删线后其记录落 unmatched，不拦截）
router.delete('/oplines/:id', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const id = Number(req.params.id);
    const [r] = await pool.query('DELETE FROM team_line WHERE id = ? AND team_id = ?', [id, req.team.id]);
    if (!r.affectedRows) return fail(res, 404, 40400, '线路不存在');
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// PUT /oplines/:id/move {direction:'up'|'down'}：线路排序（与班内相邻线路换位；事务内整体重写 sort 为连续 1..N，
// 兼容历史同值 sort；direction 为准，兼容旧口径 dir）——与 members move 同型
router.put('/oplines/:id/move', async (req, res, next) => {
  const conn = await pool.getConnection();
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const id = Number(req.params.id);
    const rawDir = req.body && (req.body.direction || req.body.dir);
    const dir = rawDir === 'up' ? 'up' : 'down';
    await conn.beginTransaction();
    const [rows] = await conn.query('SELECT id FROM team_line WHERE team_id = ? ORDER BY sort, id', [req.team.id]);
    const idx = rows.findIndex((r) => r.id === id);
    if (idx < 0) {
      await conn.rollback();
      return fail(res, 404, 40400, '线路不存在');
    }
    const swapIdx = dir === 'up' ? idx - 1 : idx + 1;
    if (swapIdx < 0 || swapIdx >= rows.length) {
      await conn.rollback();
      return ok(res, null); // 已在顶端 / 底端，次序不变
    }
    const order = rows.map((r) => r.id);
    order.splice(swapIdx, 0, order.splice(idx, 1)[0]);
    for (let i = 0; i < order.length; i += 1) {
      await conn.query('UPDATE team_line SET sort = ? WHERE id = ?', [i + 1, order[i]]);
    }
    await conn.commit();
    return ok(res, null);
  } catch (err) {
    await conn.rollback().catch(() => {});
    return next(err);
  } finally {
    conn.release();
  }
});

// GET /oplines/template：运维线路导入模板（xlsx，仅首行表头；列宽 12/20）
router.get('/oplines/template', async (req, res, next) => {
  try {
    const ws = XLSX.utils.aoa_to_sheet([['电压等级', '线路名称']]);
    ws['!cols'] = [{ wch: 12 }, { wch: 20 }];
    const wb = XLSX.utils.book_new();
    XLSX.utils.book_append_sheet(wb, ws, '运维线路');
    const buf = XLSX.write(wb, { type: 'buffer', bookType: 'xlsx' });
    res.setHeader('Content-Type', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet');
    res.setHeader(
      'Content-Disposition',
      `attachment; filename="opline-template.xlsx"; filename*=UTF-8''${encodeURIComponent('运维线路导入模板.xlsx')}`
    );
    return res.send(buf);
  } catch (err) {
    return next(err);
  }
});

// POST /oplines/import：导入运维线路 Excel（.xlsx），全量替换本班组线路（事务；校验口径照 towers/import：
// 空行/表头行跳过、缺电压或线路名计跳过；文件内 line_name 重复计跳过防撞 uk_team_line）
router.post(
  '/oplines/import',
  (req, res, next) => {
    towerUpload.single('file')(req, res, (err) => { // 与杆塔导入共用同一 multer 口径（10MB/单文件）
      if (err) {
        if (err.code === 'LIMIT_FILE_SIZE') return fail(res, 400, 40022, '文件大小应在 10MB 以内');
        return next(err);
      }
      return next();
    });
  },
  async (req, res, next) => {
    try {
      // multipart 表单体在路由级 multer 之后才可读：此处重新解析生效班组（兼容 formData 携带 team_id）
      const team = await teamUtil.resolveTeam(req.user, req.body && req.body.team_id !== undefined ? req.body.team_id : req.query.team_id);
      if (!team) return fail(res, 400, 40022, '无可用班组');
      if (req.user.role === 'team_admin' && req.user.team_id !== team.id) {
        return fail(res, 403, 40304, '仅管理员可执行此操作');
      }
      if (!req.file || !req.file.buffer || !req.file.buffer.length) {
        return fail(res, 400, 40022, '请选择要上传的 Excel 文件');
      }
      const fname = fixLatin1Name(req.file.originalname);
      if (!/\.xlsx$/i.test(fname)) return fail(res, 400, 40022, '仅支持 .xlsx 文件，请使用模板填写');

      let rows;
      try {
        const wb = XLSX.read(req.file.buffer, { type: 'buffer' });
        rows = XLSX.utils.sheet_to_json(wb.Sheets[wb.SheetNames[0]], { header: 1, raw: false, defval: '' });
      } catch (e) {
        return fail(res, 400, 40022, 'Excel 解析失败，请使用模板文件填写');
      }

      // 逐行校验：空行跳过；表头行跳过；缺电压等级或线路名、班内重复线路名计为跳过
      const data = [];
      const seen = new Set();
      let skipped = 0;
      for (const r of rows) {
        const cells = (Array.isArray(r) ? r : []).map((c) => String(c).trim());
        if (cells.every((c) => !c)) continue;
        if (cells[0].includes('电压') || cells[1].includes('线路')) continue;
        const [voltage, line] = cells;
        if (!voltage || !line || seen.has(line)) {
          skipped++;
          continue;
        }
        seen.add(line);
        data.push([team.id, voltage.slice(0, 16), line.slice(0, 64)]);
      }
      if (!data.length) return fail(res, 400, 40022, '未识别到有效线路行，请按模板列填写');

      const conn = await pool.getConnection();
      try {
        await conn.beginTransaction();
        await conn.query('DELETE FROM team_line WHERE team_id = ?', [team.id]);
        for (let i = 0; i < data.length; i += 500) {
          const chunk = data.slice(i, i + 500).map((r, j) => [...r, i + j + 1]);
          await conn.query('INSERT INTO team_line (team_id, voltage_level, line_name, sort) VALUES ?', [chunk]);
        }
        await conn.commit();
      } catch (e) {
        await conn.rollback().catch(() => {});
        throw e;
      } finally {
        conn.release();
      }
      return ok(res, { imported: data.length, skipped }, `已导入 ${data.length} 条线路`);
    } catch (err) {
      return next(err);
    }
  }
);

module.exports = router;
