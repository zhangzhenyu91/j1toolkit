// 出工日志路由：全部接口需登录 + work-log 应用权限 + 生效班组（req.team，见 utils/team.js）；
// /admin/* 字典接口超管可管任意班组（?team_id= 指定），班组管理员仅本班
// 业务规则与设计稿见《开发指南》第四、七章与 design/worklog.html
const express = require('express');
const archiver = require('archiver');
const crypto = require('crypto');
const multer = require('multer');
const XLSX = require('xlsx');
const { Readable } = require('stream');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const teamUtil = require('../utils/team');
const config = require('../config');
const cos = require('./cos');
const dify = require('./dify');
const geo = require('./geo');
const towers = require('./towers');
const Watermark = require('./watermark');
const { renderWatermarkedPhoto } = require('./render-photo');
const { computeVerifyPassed, computeFailReasons, myReportReasons, checkWatermark } = require('./verify');
const tasksheet = require('./tasksheet');
const feesheet = require('./feesheet');
const dispatch = require('./dispatch');

const router = express.Router();

// 文件预览服务器回源拉取下载地址时无法附带请求头：
// 无 Authorization 头且 query 带 token 时，映射为 Authorization: Bearer 再走统一鉴权（同安全日记录口径）
router.use((req, res, next) => {
  if (!req.headers.authorization && typeof req.query.token === 'string' && req.query.token) {
    req.headers.authorization = `Bearer ${req.query.token}`;
  }
  next();
});

router.use(auth, requireApp('work-log'));

// 班组上下文：解析生效班组（超管可用 ?team_id= 指定；其余角色固定本班，未分配 → null）
router.use(async (req, res, next) => {
  try {
    req.team = await teamUtil.resolveReqTeam(req);
    return next();
  } catch (err) {
    return next(err);
  }
});

// 字典管理权限：超管任意班组（req.team 由 ?team_id= 解析），班组管理员仅本班
function requireDictAdmin(req, res, next) {
  if (req.user.role === 'admin') return next();
  if (req.user.role === 'team_admin' && req.team && req.user.team_id === req.team.id) return next();
  return fail(res, 403, 40304, '仅管理员可执行此操作');
}

const DATE_RE = /^\d{4}-\d{2}-\d{2}$/;
const MONTH_RE = /^\d{4}-\d{2}$/;

// 日期串工具：log_date 以 DATE_FORMAT 取出为 'YYYY-MM-DD' 字符串，避免时区换算
function dots(dateStr) {
  return dateStr.replace(/-/g, '.');
}

// 备注附件 JSON 解析（mysql2 对 JSON 列可能返回字符串或已解析对象，与 photo.members 同口径防御）
function parseRemarkFiles(raw) {
  if (!raw) return [];
  const arr = typeof raw === 'string' ? JSON.parse(raw) : raw;
  return Array.isArray(arr) ? arr : [];
}

// 备注附件入库前清洗：字段形状校验 + 长度截断；不合法返回 null（cos_key 仅作留存，删除集合只取库内旧值）
function sanitizeRemarkFile(f) {
  if (!f || typeof f !== 'object') return null;
  const name = String(f.name || '').trim().slice(0, 128);
  const url = String(f.url || '').trim().slice(0, 512);
  const cosKey = String(f.cos_key || '').trim().slice(0, 255);
  const type = String(f.type || '');
  const size = Number(f.size) || 0;
  if (!name || !/^https?:\/\//i.test(url) || !cosKey || cosKey.includes('..')) return null;
  if (!['image', 'video', 'doc'].includes(type)) return null;
  return { name, url, cos_key: cosKey, type, size };
}

// 装配某日期范围内的卡片全量（含用车人、照片、verify_passed）
async function loadEntries(where, params) {
  // 超时兜底：pending 超 10 分钟视为验证失败（如服务端在 Dify 回调途中重启导致回写丢失；
  // 置 failed 后卡片出现「重新验证」按钮，用户可一键重试；若 Dify 结果随后到达仍以真实结果覆盖）
  await pool.query(
    `UPDATE worklog_photo SET verify_status = 'failed'
     WHERE verify_status = 'pending' AND created_at < DATE_SUB(NOW(), INTERVAL 10 MINUTE)`
  );
  const [entries] = await pool.query(
    `SELECT e.id, e.team_id, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, e.patrol_content,
            e.remark, e.remark_files,
            e.vehicle_id, v.plate_no, e.destination_id, d.name AS destination_name,
            e.created_by, e.created_at
     FROM worklog_entry e
     LEFT JOIN worklog_vehicle v ON v.id = e.vehicle_id
     LEFT JOIN worklog_destination d ON d.id = e.destination_id
     WHERE ${where} ORDER BY e.created_at, e.id`,
    params
  );
  if (!entries.length) return [];

  const ids = entries.map((e) => e.id);
  const [members] = await pool.query(
    `SELECT em.id, em.entry_id, em.member_id, m.name, em.checked, em.sort
     FROM worklog_entry_member em JOIN worklog_member m ON m.id = em.member_id
     WHERE em.entry_id IN (?) ORDER BY em.sort, em.id`,
    [ids]
  );
  const [photos] = await pool.query(
    `SELECT id, entry_id, cos_key, url, members, verify_status, work_content,
            shot_time, weather, location, lng, lat, date_ok, dest_ok, created_at
       ${config.sgcc && config.sgcc.enabled ? ', is_watermark, source, sgcc_synced' : ''}
     FROM worklog_photo WHERE entry_id IN (?) ORDER BY id`,
    [ids]
  );

  const memberMap = {};
  members.forEach((m) => {
    (memberMap[m.entry_id] = memberMap[m.entry_id] || []).push({
      id: m.id, member_id: m.member_id, name: m.name, checked: m.checked, sort: m.sort,
    });
  });
  const photoMap = {};
  photos.forEach((p) => {
    (photoMap[p.entry_id] = photoMap[p.entry_id] || []).push({
      id: p.id,
      url: p.url,
      members: typeof p.members === 'string' ? JSON.parse(p.members) : p.members,
      verify_status: p.verify_status,
      work_content: p.work_content,
      shot_time: p.shot_time,
      weather: p.weather,
      location: p.location,
      lng: p.lng,
      lat: p.lat,
      date_ok: p.date_ok,
      dest_ok: p.dest_ok,
      ...(config.sgcc && config.sgcc.enabled
        ? { is_watermark: p.is_watermark, source: p.source, sgcc_synced: p.sgcc_synced } : {}),
    });
  });

  // 商旅打卡开启时：装配 绑定/登录态（成员级）、当日两次打卡（clockinMap）与 当日费用（feeMap），供新 7 条规则与前端打卡区
  let sgccByMember = {};
  const clockinByDate = {}; // clockinByDate[log_date][member_id][seq]
  const feeByDate = {}; // feeByDate[log_date][member_id] = { foodFee, transitFee }（规则 f 判定用）
  if (config.sgcc && config.sgcc.enabled) {
    const teamIds = [...new Set(entries.map((e) => e.team_id).filter(Boolean))];
    if (teamIds.length) {
      const [accounts] = await pool.query(
        'SELECT member_id, token_status FROM worklog_sgcc_account WHERE team_id IN (?) AND member_id IS NOT NULL',
        [teamIds]
      );
      accounts.forEach((a) => { sgccByMember[a.member_id] = a; });
      const dates = [...new Set(entries.map((e) => e.log_date))];
      const [clockins] = await pool.query(
        `SELECT member_id, DATE_FORMAT(clock_date, '%Y-%m-%d') AS clock_date, seq, detail_id,
                DATE_FORMAT(clock_time, '%Y-%m-%d %H:%i:%s') AS clock_time, position, longitude, latitude,
                city_code, city_name, work_hours
         FROM worklog_clockin WHERE team_id IN (?) AND clock_date IN (?)`,
        [teamIds, dates]
      );
      clockins.forEach((c) => {
        const d = (clockinByDate[c.clock_date] = clockinByDate[c.clock_date] || {});
        const m = (d[c.member_id] = d[c.member_id] || {});
        // 带全套定位信息（坐标+城市编码/城市名）：打卡弹层带入他人打卡时整套带入，避免地址与城市不对应
        m[c.seq] = {
          detailId: c.detail_id, time: c.clock_time, position: c.position, workHours: c.work_hours,
          lng: c.longitude, lat: c.latitude, cityCode: c.city_code, cityName: c.city_name,
        };
      });
      // 当日费用（规则 f 用）：与 clockins 同口径按 team_id + 日期集合批量查
      const [fees] = await pool.query(
        `SELECT member_id, DATE_FORMAT(fee_date, '%Y-%m-%d') AS fee_date, food_fee, transit_fee
         FROM worklog_fee WHERE team_id IN (?) AND fee_date IN (?)`,
        [teamIds, dates]
      );
      fees.forEach((f) => {
        const d = (feeByDate[f.fee_date] = feeByDate[f.fee_date] || {});
        d[f.member_id] = { foodFee: Number(f.food_fee), transitFee: Number(f.transit_fee) };
      });
    }
  }

  return entries.map((e) => {
    const members = (memberMap[e.id] || []).map((m) => ({
      ...m,
      ...(config.sgcc && config.sgcc.enabled
        ? { sgccBound: !!sgccByMember[m.member_id], sgccTokenStatus: sgccByMember[m.member_id] ? sgccByMember[m.member_id].token_status : null }
        : {}),
    }));
    const entry = {
      ...e,
      remark: e.remark || '',
      remark_files: parseRemarkFiles(e.remark_files),
      members,
      photos: photoMap[e.id] || [],
      clockinMap: (clockinByDate[e.log_date] || {}),
      feeMap: (feeByDate[e.log_date] || {}),
    };
    entry.verify_passed = computeVerifyPassed(entry);
    entry.verify_reasons = computeFailReasons(entry);
    return entry;
  });
}

// 校验车牌/目的地/成员 id 有效且归属当前生效班组（车牌、目的地需启用）
async function validDictId(table, id, needEnabled, teamId) {
  if (!id) return true;
  const [rows] = await pool.query(`SELECT id, status FROM ${table} WHERE id = ? AND team_id = ?`, [id, teamId]);
  if (!rows.length) return false;
  return needEnabled ? rows[0].status === 1 : true;
}

// 当前登录用户对应的出工成员（按 sys_user.nickname == worklog_member.name 班组内匹配，仅查看个人视图用）
async function myMember(req) {
  const nickname = req.user.nickname;
  if (!nickname || !req.team) return null;
  const [members] = await pool.query(
    'SELECT id, name FROM worklog_member WHERE name = ? AND team_id = ?',
    [nickname, req.team.id]
  );
  return members.length ? members[0] : null;
}

// GET /meta：下拉/点亮数据源（前端自行补「未出车」固定项）；按生效班组出数
router.get('/meta', async (req, res, next) => {
  try {
    if (!req.team) return ok(res, { vehicles: [], destinations: [], members: [] });
    const [vehicles] = await pool.query(
      'SELECT id, plate_no FROM worklog_vehicle WHERE status = 1 AND team_id = ? ORDER BY sort, id',
      [req.team.id]
    );
    const [destinations] = await pool.query(
      'SELECT id, name FROM worklog_destination WHERE status = 1 AND team_id = ? ORDER BY sort, id',
      [req.team.id]
    );
    const [members] = await pool.query(
      'SELECT id, name, sort FROM worklog_member WHERE status = 1 AND team_id = ? ORDER BY sort, id',
      [req.team.id]
    );
    return ok(res, { vehicles, destinations, members });
  } catch (err) {
    return next(err);
  }
});

// GET /logs?date=YYYY-MM-DD&scope=all|mine：某日卡片全量；scope=mine 仅含用车人包含自己的卡片
router.get('/logs', async (req, res, next) => {
  try {
    const { date } = req.query;
    if (!DATE_RE.test(date || '')) return fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD');
    if (!req.team) return ok(res, { list: [] });
    let where = 'e.log_date = ? AND e.team_id = ?';
    const params = [date, req.team.id];
    if (req.query.scope === 'mine') {
      const me = await myMember(req);
      if (!me) return ok(res, { list: [] });
      where += ' AND e.id IN (SELECT entry_id FROM worklog_entry_member WHERE member_id = ?)';
      params.push(me.id);
    }
    const list = await loadEntries(where, params);
    return ok(res, { list });
  } catch (err) {
    return next(err);
  }
});

// GET /day-status?month=YYYY-MM&scope=all|mine：当月每日验证状态映射，供日历着色
// 状态优先级：failed 红 > remark 黄（验证通过但有备注）> passed 绿；免验证不参与着色
// scope=mine 个人视图：仅统计用车人包含自己的卡片；红色 = 我未打卡 / 我的费用不达标 / 没有含我名字的水印照片 / 含我照片未通过（含验证中/失败），口径同 /report scope=mine（verify.js myReportReasons）
router.get('/day-status', async (req, res, next) => {
  try {
    const { month } = req.query;
    if (!MONTH_RE.test(month || '')) return fail(res, 400, 40000, '月份格式应为 YYYY-MM');
    if (!req.team) return ok(res, { map: {} });

    if (req.query.scope === 'mine') {
      const me = await myMember(req);
      if (!me) return ok(res, { map: {} });
      const list = await loadEntries(
        `DATE_FORMAT(e.log_date, '%Y-%m') = ? AND e.team_id = ? AND e.id IN (SELECT entry_id FROM worklog_entry_member WHERE member_id = ?)`,
        [month, req.team.id, me.id]
      );
      const map = {};
      list.forEach((e) => {
        if (map[e.log_date] === 'failed') return; // 有未通过即锁定红
        // 个人口径与 /report scope=mine 同源（verify.js myReportReasons）：
        // 剔除非水印照片；商旅开启时按两次打卡 + 规则 f 费用判定
        let st = myReportReasons(e, me).length ? 'failed' : 'passed';
        // 通过但有备注 → 黄（不覆盖红；已有黄不被绿覆盖）
        if (st === 'passed' && (e.remark || e.remark_files.length)) st = 'remark';
        if (st === 'passed' && map[e.log_date] === 'remark') return;
        map[e.log_date] = st;
      });
      return ok(res, { map });
    }

    const list = await loadEntries(`DATE_FORMAT(e.log_date, '%Y-%m') = ? AND e.team_id = ?`, [month, req.team.id]);
    const map = {};
    list.forEach((e) => {
      if (e.verify_passed === 'exempt') return; // 免验证不参与着色
      if (map[e.log_date] === 'failed') return; // 有未通过即锁定红
      let st = e.verify_passed === 'failed' ? 'failed' : 'passed';
      // 通过但有备注 → 黄（不覆盖红；已有黄不被绿覆盖）
      if (st === 'passed' && (e.remark || e.remark_files.length)) st = 'remark';
      if (st === 'passed' && map[e.log_date] === 'remark') return;
      map[e.log_date] = st;
    });
    return ok(res, { map });
  } catch (err) {
    return next(err);
  }
});

// POST /logs：新建卡片（一卡片一派车；vehicle_id 空=未出车）；归入当前生效班组
router.post('/logs', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const { log_date, patrol_content = '', vehicle_id = null, destination_id = null } = req.body || {};
    let { member_ids = [] } = req.body || {};
    if (!DATE_RE.test(log_date || '')) return fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD');
    member_ids = Array.isArray(member_ids) ? member_ids.map(Number).filter(Number.isInteger) : [];

    if (!vehicle_id) {
      if (destination_id || member_ids.length) {
        return fail(res, 400, 40001, '未出车时不可填写目的地与用车人');
      }
    } else {
      if (!(await validDictId('worklog_vehicle', vehicle_id, true, req.team.id))) {
        return fail(res, 400, 40002, '车牌号无效或已停用');
      }
      if (destination_id && !(await validDictId('worklog_destination', destination_id, true, req.team.id))) {
        return fail(res, 400, 40003, '目的地无效或已停用');
      }
    }

    // 成员 id 校验并取 sort（限本班组启用成员）
    let memberRows = [];
    if (member_ids.length) {
      const [rows] = await pool.query(
        'SELECT id, sort FROM worklog_member WHERE id IN (?) AND status = 1 AND team_id = ?',
        [member_ids, req.team.id]
      );
      if (rows.length !== new Set(member_ids).size) {
        return fail(res, 400, 40004, '存在无效或已停用的成员');
      }
      memberRows = rows;
    }

    const [r] = await pool.query(
      'INSERT INTO worklog_entry (team_id, log_date, patrol_content, vehicle_id, destination_id, created_by) VALUES (?, ?, ?, ?, ?, ?)',
      [req.team.id, log_date, patrol_content, vehicle_id, destination_id, req.user.id]
    );
    const entryId = r.insertId;
    for (const m of memberRows) {
      await pool.query(
        'INSERT INTO worklog_entry_member (entry_id, member_id, sort) VALUES (?, ?, ?)',
        [entryId, m.id, m.sort]
      );
    }
    return ok(res, { id: entryId });
  } catch (err) {
    return next(err);
  }
});

// PUT /logs/:id：修改卡片（用车人全量替换，保留仍在名单者的打卡状态）
router.put('/logs/:id', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const entryId = Number(req.params.id);
    const [exist] = await pool.query('SELECT id, destination_id FROM worklog_entry WHERE id = ? AND team_id = ?', [entryId, req.team.id]);
    if (!exist.length) return fail(res, 404, 40400, '日志不存在');

    const { patrol_content = '', vehicle_id = null, destination_id = null } = req.body || {};
    let { member_ids } = req.body || {};

    if (!vehicle_id) {
      member_ids = [];
      if (destination_id) return fail(res, 400, 40001, '未出车时不可填写目的地');
      // 未出车时若已有照片（历史改派车为未出车），拒绝，需先删除照片
      const [photoRows] = await pool.query('SELECT COUNT(*) AS cnt FROM worklog_photo WHERE entry_id = ?', [entryId]);
      if (photoRows[0].cnt) return fail(res, 400, 40005, '存在水印照片，不可改为未出车，请先删除照片');
    } else {
      if (!(await validDictId('worklog_vehicle', vehicle_id, true, req.team.id))) {
        return fail(res, 400, 40002, '车牌号无效或已停用');
      }
      if (destination_id && !(await validDictId('worklog_destination', destination_id, true, req.team.id))) {
        return fail(res, 400, 40003, '目的地无效或已停用');
      }
    }
    member_ids = Array.isArray(member_ids) ? member_ids.map(Number).filter(Number.isInteger) : [];
    let memberRows = [];
    if (member_ids.length) {
      const [rows] = await pool.query(
        'SELECT id, sort FROM worklog_member WHERE id IN (?) AND status = 1 AND team_id = ?',
        [member_ids, req.team.id]
      );
      if (rows.length !== new Set(member_ids).size) {
        return fail(res, 400, 40004, '存在无效或已停用的成员');
      }
      memberRows = rows;
    }

    // 被移出名单的成员若已有照片，拒绝（需先调整照片人名）
    const [photoRows] = await pool.query('SELECT members FROM worklog_photo WHERE entry_id = ?', [entryId]);
    const [currentRows] = await pool.query(
      'SELECT em.member_id, m.name, em.checked FROM worklog_entry_member em JOIN worklog_member m ON m.id = em.member_id WHERE em.entry_id = ?',
      [entryId]
    );
    const keptNames = new Map();
    if (memberRows.length) {
      const [nameRows] = await pool.query('SELECT id, name FROM worklog_member WHERE id IN (?)', [memberRows.map((m) => m.id)]);
      nameRows.forEach((n) => keptNames.set(n.name, n.id));
    }
    for (const p of photoRows) {
      const names = typeof p.members === 'string' ? JSON.parse(p.members) : p.members;
      for (const n of names || []) {
        if (!keptNames.has(n)) {
          return fail(res, 400, 40006, `成员「${n}」已有水印照片，不可移出用车人，请先调整照片人名`);
        }
      }
    }

    await pool.query(
      'UPDATE worklog_entry SET patrol_content = ?, vehicle_id = ?, destination_id = ? WHERE id = ?',
      [patrol_content, vehicle_id, vehicle_id ? destination_id : null, entryId]
    );

    // 派车目的地变更：已出结果（passed/mismatch）的照片按库内识别地点重新核验地点一致性并联动状态
    // （仅库内重算，不重调 Dify；pending 照片由 writeBackVerify 写库时按最新目的地比对，failed 不动）
    const newDestId = vehicle_id ? destination_id : null;
    if (Number(exist[0].destination_id || 0) !== Number(newDestId || 0)) {
      let destName = '';
      if (newDestId) {
        const [destRows] = await pool.query('SELECT name FROM worklog_destination WHERE id = ?', [newDestId]);
        destName = destRows.length ? destRows[0].name : '';
      }
      const [photos] = await pool.query(
        `SELECT id, date_ok, location FROM worklog_photo WHERE entry_id = ? AND verify_status IN ('passed', 'mismatch')`,
        [entryId]
      );
      for (const p of photos) {
        const destOk = !destName || String(p.location || '').includes(destName); // 与 checkWatermark 地点口径一致
        const dateOk = p.date_ok !== 0; // 日期结果不受目的地变更影响（NULL=历史数据按相符保留）
        await pool.query('UPDATE worklog_photo SET dest_ok = ?, verify_status = ? WHERE id = ?', [
          destOk ? 1 : 0,
          dateOk && destOk ? 'passed' : 'mismatch',
          p.id,
        ]);
      }
    }

    const checkedMap = new Map(currentRows.map((r) => [r.member_id, r.checked]));
    await pool.query('DELETE FROM worklog_entry_member WHERE entry_id = ?', [entryId]);
    for (const m of memberRows) {
      await pool.query(
        'INSERT INTO worklog_entry_member (entry_id, member_id, checked, sort) VALUES (?, ?, ?, ?)',
        [entryId, m.id, checkedMap.get(m.id) || 0, m.sort]
      );
    }

    // 备注与附件：仅在请求显式携带对应字段时更新（巡视内容/派车等保存不带备注字段，避免误清）
    const hasRemark = Object.prototype.hasOwnProperty.call(req.body || {}, 'remark');
    const hasFiles = Object.prototype.hasOwnProperty.call(req.body || {}, 'remark_files');
    if (hasRemark || hasFiles) {
      const [oldRows] = await pool.query('SELECT remark, remark_files FROM worklog_entry WHERE id = ?', [entryId]);
      const oldFiles = parseRemarkFiles(oldRows[0] && oldRows[0].remark_files);
      const newRemark = hasRemark
        ? String(req.body.remark || '').trim().slice(0, 500)
        : (oldRows[0].remark || '');
      let newFiles = oldFiles;
      if (hasFiles) {
        const rawFiles = req.body.remark_files;
        if (!Array.isArray(rawFiles) || rawFiles.length > 9) {
          return fail(res, 400, 40020, '备注附件最多 9 个');
        }
        newFiles = [];
        for (const f of rawFiles) {
          const clean = sanitizeRemarkFile(f);
          if (!clean) return fail(res, 400, 40018, '备注附件数据不完整或格式不支持');
          newFiles.push(clean);
        }
      }
      await pool.query('UPDATE worklog_entry SET remark = ?, remark_files = ? WHERE id = ?', [
        newRemark, JSON.stringify(newFiles), entryId,
      ]);
      // 被移除的附件同步删除 COS 对象（删除集合只来自库内旧值，客户端传值不会触发删除）
      const keptKeys = new Set(newFiles.map((f) => f.cos_key));
      for (const f of oldFiles) {
        if (!keptKeys.has(f.cos_key)) {
          try {
            await cos.deleteObject(f.cos_key);
          } catch (err) {
            console.error('[出工日志] 删除备注附件 COS 对象失败（继续保存）：', f.cos_key, err.message);
          }
        }
      }
    }
    return ok(res, { id: entryId });
  } catch (err) {
    return next(err);
  }
});

// DELETE /logs/:id：删除卡片（先删 COS 对象（水印照片 + 备注附件），再删行）
router.delete('/logs/:id', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const entryId = Number(req.params.id);
    const [entryRows] = await pool.query(
      'SELECT remark_files FROM worklog_entry WHERE id = ? AND team_id = ?',
      [entryId, req.team.id]
    );
    if (!entryRows.length) return fail(res, 404, 40400, '日志不存在');
    const [photos] = await pool.query('SELECT cos_key FROM worklog_photo WHERE entry_id = ?', [entryId]);
    const cosKeys = photos.map((p) => p.cos_key);
    parseRemarkFiles(entryRows[0].remark_files).forEach((f) => cosKeys.push(f.cos_key));
    for (const key of cosKeys) {
      try {
        await cos.deleteObject(key);
      } catch (err) {
        console.error('[出工日志] 删除 COS 对象失败（继续删库记录）：', key, err.message);
      }
    }
    await pool.query('DELETE FROM worklog_photo WHERE entry_id = ?', [entryId]);
    await pool.query('DELETE FROM worklog_entry_member WHERE entry_id = ?', [entryId]);
    const [r] = await pool.query('DELETE FROM worklog_entry WHERE id = ? AND team_id = ?', [entryId, req.team.id]);
    if (!r.affectedRows) return fail(res, 404, 40400, '日志不存在');
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// PUT /logs/:id/members/:mid/check：打卡切换
router.put('/logs/:id/members/:mid/check', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const [r] = await pool.query(
      `UPDATE worklog_entry_member em JOIN worklog_entry e ON e.id = em.entry_id
       SET em.checked = 1 - em.checked WHERE em.id = ? AND em.entry_id = ? AND e.team_id = ?`,
      [Number(req.params.mid), Number(req.params.id), req.team.id]
    );
    if (!r.affectedRows) return fail(res, 404, 40400, '打卡记录不存在');
    const [rows] = await pool.query('SELECT checked FROM worklog_entry_member WHERE id = ?', [Number(req.params.mid)]);
    return ok(res, { checked: rows[0].checked });
  } catch (err) {
    return next(err);
  }
});

// GET /photos?from=&to=：日期范围内的全部水印照片（批量下载用，按日期+上传序排列）
router.get('/photos', async (req, res, next) => {
  try {
    const { from, to } = req.query;
    if (!DATE_RE.test(from || '') || !DATE_RE.test(to || '')) {
      return fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD');
    }
    if (from > to) return fail(res, 400, 40013, '开始日期不能晚于结束日期');
    if (!req.team) return ok(res, { list: [] });
    const [rows] = await pool.query(
      `SELECT p.id, p.url, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date,
              DATE_FORMAT(e.log_date, '%Y-%m') AS month, DAYOFMONTH(e.log_date) AS day, p.members
       FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
       WHERE e.log_date BETWEEN ? AND ? AND e.team_id = ? ORDER BY e.log_date, p.id`,
      [from, to, req.team.id]
    );
    rows.forEach((r) => {
      r.members = typeof r.members === 'string' ? JSON.parse(r.members) : r.members;
    });
    return ok(res, { list: rows });
  } catch (err) {
    return next(err);
  }
});

// GET /report?from=&to=&scope=all|mine：验证报告（原「验证不通过报告」）——范围为「不通过记录 ∪ 有备注的记录」
// 不通过记录带全部原因（scope=mine 个人口径仅列个人相关原因）；备注不论通过与否均带出（remark / remark_has_files），
// 通过/免验证记录仅备注时 reasons 为空，前端按 reasons 有无 + verify 区分角标
// scope=mine 个人口径：仅含「我未打卡 / 我未上传水印照片 / 我的水印照片未通过」的卡片，或我是用车人且有备注的卡片
router.get('/report', async (req, res, next) => {
  try {
    const { from, to } = req.query;
    if (!DATE_RE.test(from || '') || !DATE_RE.test(to || '')) {
      return fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD');
    }
    if (from > to) return fail(res, 400, 40013, '开始日期不能晚于结束日期');
    if (!req.team) return ok(res, { list: [] });

    let me = null;
    if (req.query.scope === 'mine') {
      me = await myMember(req);
      if (!me) return ok(res, { list: [] });
    }
    const list = await loadEntries('e.log_date BETWEEN ? AND ? AND e.team_id = ?', [from, to, req.team.id]);
    const items = [];
    list.forEach((e) => {
      const hasRemark = !!(e.remark || e.remark_files.length);
      let reasons;
      if (me) {
        reasons = myReportReasons(e, me);
        const amMember = e.members.some((m) => m.member_id === me.id);
        if (!reasons.length && !(amMember && hasRemark)) return;
      } else {
        reasons = e.verify_passed === 'failed' ? e.verify_reasons : [];
        if (!reasons.length && !hasRemark) return;
      }
      items.push({
        id: e.id,
        log_date: e.log_date,
        plate_no: e.plate_no || '未出车',
        members: e.members.map((m) => m.name),
        verify: e.verify_passed, // passed / failed / exempt（角标以 reasons 有无优先判定未通过）
        reasons,
        remark: e.remark,
        remark_has_files: e.remark_files.length > 0,
      });
    });
    items.sort((a, b) => (a.log_date < b.log_date ? -1 : a.log_date > b.log_date ? 1 : a.id - b.id));
    return ok(res, { list: items });
  } catch (err) {
    return next(err);
  }
});

// ===== 备注附件（图片 / 视频 / Office 文档，传 COS；格式白名单见 REMARK_EXTS） =====
const remarkUpload = multer({
  storage: multer.memoryStorage(),
  limits: { fileSize: 50 * 1024 * 1024, files: 1 },
});

const REMARK_EXTS = {
  image: ['jpg', 'jpeg', 'png', 'gif', 'webp', 'bmp'],
  video: ['mp4', 'mov', 'm4v'],
  doc: ['doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'pdf'],
};

const REMARK_MIME = {
  jpg: 'image/jpeg', jpeg: 'image/jpeg', png: 'image/png', gif: 'image/gif', webp: 'image/webp', bmp: 'image/bmp',
  mp4: 'video/mp4', mov: 'video/quicktime', m4v: 'video/x-m4v',
  doc: 'application/msword',
  docx: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  xls: 'application/vnd.ms-excel',
  xlsx: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  ppt: 'application/vnd.ms-powerpoint',
  pptx: 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  pdf: 'application/pdf',
};

function getFileExt(name) {
  const idx = String(name || '').lastIndexOf('.');
  return idx === -1 ? '' : String(name).slice(idx + 1).toLowerCase();
}

// 按 entryId + cos_key 定位备注附件（预览/下载共用；key 必须确属该卡且该卡归属当前班组，防止拿任意外地拉扯）
async function findRemarkFile(entryId, key, teamId) {
  const [rows] = await pool.query(
    'SELECT remark_files FROM worklog_entry WHERE id = ? AND team_id = ?',
    [entryId, teamId]
  );
  if (!rows.length) return { entry: false };
  const file = parseRemarkFiles(rows[0].remark_files).find((f) => f.cos_key === key);
  return { entry: true, file };
}

// POST /logs/:id/remark-files：上传单个备注附件（multipart 字段 file；表单 name 可覆盖文件名）
router.post(
  '/logs/:id/remark-files',
  // multer 错误（超限等）转成业务响应，避免落入全局 500
  (req, res, next) => {
    remarkUpload.single('file')(req, res, (err) => {
      if (err) {
        if (err.code === 'LIMIT_FILE_SIZE') return fail(res, 400, 40019, '附件大小应在 50MB 以内');
        return next(err);
      }
      return next();
    });
  },
  async (req, res, next) => {
    try {
      // multipart 表单体在路由级 multer 之后才可读：此处重新解析生效班组（兼容 formData 携带 team_id）
      const team = await teamUtil.resolveTeam(req.user, req.body && req.body.team_id !== undefined ? req.body.team_id : req.query.team_id);
      if (!team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
      const entryId = Number(req.params.id);
      const [entries] = await pool.query(
        `SELECT id, DATE_FORMAT(log_date, '%Y-%m-%d') AS log_date FROM worklog_entry WHERE id = ? AND team_id = ?`,
        [entryId, team.id]
      );
      if (!entries.length) return fail(res, 404, 40400, '日志不存在');
      if (!req.file || !req.file.buffer || !req.file.buffer.length) {
        return fail(res, 400, 40018, '请选择要上传的附件');
      }
      // 文件名：表单 name 优先（小程序 chooseMedia 临时文件名无意义）；回退原始名并修正 latin1 乱码
      const fallback = Buffer.from(req.file.originalname || '', 'latin1').toString('utf8').trim();
      const name = (String((req.body && req.body.name) || '').trim() || fallback || '附件').slice(0, 128);
      const ext = getFileExt(name);
      const type = Object.keys(REMARK_EXTS).find((t) => REMARK_EXTS[t].includes(ext));
      if (!type) {
        return fail(res, 400, 40018, '仅支持图片、视频或 Office 文档（doc/docx/xls/xlsx/ppt/pptx/pdf）');
      }
      const prefix = config.worklog.cosPrefix.endsWith('/') ? config.worklog.cosPrefix : `${config.worklog.cosPrefix}/`;
      const key = `${prefix}remark/${team.name}/${dots(entries[0].log_date)}/${entryId}-${Date.now()}.${ext}`;
      await cos.putBuffer(key, req.file.buffer, REMARK_MIME[ext] || 'application/octet-stream');
      return ok(res, { name, url: cos.publicUrl(key), cos_key: key, type, size: req.file.buffer.length });
    } catch (err) {
      return next(err);
    }
  }
);

// GET /logs/:id/remark-preview?key=：拼接 basemetas 预览地址（同安全日记录口径；COS 公共读，预览服务直接回源 COS）
router.get('/logs/:id/remark-preview', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const { entry, file } = await findRemarkFile(Number(req.params.id), String(req.query.key || ''), req.team.id);
    if (!entry) return fail(res, 404, 40400, '日志不存在');
    if (!file) return fail(res, 404, 40400, '附件不存在');
    if (file.type !== 'doc') return fail(res, 400, 40021, '仅 Office 文档支持在线预览');
    const base = (config.basemetas.url || '').replace(/\/+$/, '');
    if (!base) return fail(res, 400, 40021, '未配置文件预览服务');
    const url = `${base}/preview/view?url=${encodeURIComponent(cos.publicUrl(file.cos_key))}`
      + `&fileName=${encodeURIComponent(file.name)}&displayName=${encodeURIComponent(file.name)}`;
    return ok(res, { url });
  } catch (err) {
    return next(err);
  }
});

// GET /logs/:id/remark-download?key=：附件下载代理（COS 跨域无 CORS，网页端经本接口回源并附下载文件名）
router.get('/logs/:id/remark-download', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const { entry, file } = await findRemarkFile(Number(req.params.id), String(req.query.key || ''), req.team.id);
    if (!entry) return fail(res, 404, 40400, '日志不存在');
    if (!file) return fail(res, 404, 40400, '附件不存在');
    const resp = await fetch(cos.publicUrl(file.cos_key), { signal: AbortSignal.timeout(60000) });
    if (!resp.ok) return fail(res, 502, 50201, `附件回源失败（HTTP ${resp.status}）`);
    res.setHeader('Content-Type', resp.headers.get('content-type') || 'application/octet-stream');
    const len = Number(resp.headers.get('content-length') || 0);
    if (len) res.setHeader('Content-Length', len);
    // RFC5987 编码中文文件名，附 ASCII fallback（同 ZIP 下载口径）
    res.setHeader(
      'Content-Disposition',
      `attachment; filename="file"; filename*=UTF-8''${encodeURIComponent(file.name || '附件')}`
    );
    Readable.fromWeb(resp.body).pipe(res);
  } catch (err) {
    return next(err);
  }
});

// 照片人名校验：⊆ 本卡用车人，且不与本卡其他照片冲突（每人限一张）
async function checkPhotoMembers(entryId, names, excludePhotoId, skipUsed) {
  const [memberRows] = await pool.query(
    'SELECT m.name FROM worklog_entry_member em JOIN worklog_member m ON m.id = em.member_id WHERE em.entry_id = ?',
    [entryId]
  );
  const allowed = new Set(memberRows.map((r) => r.name));
  for (const n of names) {
    if (!allowed.has(n)) return { code: 40007, message: `「${n}」不是本卡用车人` };
  }
  // 非水印照片（skipUsed）不占「每人限一张」名额，也不参与占用判定
  if (skipUsed) return null;
  const [photos] = await pool.query(
    `SELECT id, members FROM worklog_photo WHERE entry_id = ?
       ${config.sgcc && config.sgcc.enabled ? 'AND is_watermark = 1' : ''}`,
    [entryId]
  );
  const used = new Set();
  photos.forEach((p) => {
    if (excludePhotoId && p.id === excludePhotoId) return;
    (typeof p.members === 'string' ? JSON.parse(p.members) : p.members || []).forEach((n) => used.add(n));
  });
  for (const n of names) {
    if (used.has(n)) return { code: 40008, message: `「${n}」已有水印照片，每人限一张` };
  }
  return null;
}

// GET /geo?lng=&lat=：按经纬度取当前「地点 + 天气」（腾讯），供「选照片并添加水印」无历史照片时预填；
// 未配置 TENCENT_MAP_KEY 或调用失败时返回空串，前端留空手填
router.get('/geo', async (req, res, next) => {
  try {
    const lng = Number(req.query.lng);
    const lat = Number(req.query.lat);
    if (!Number.isFinite(lng) || !Number.isFinite(lat) || Math.abs(lng) > 180 || Math.abs(lat) > 90) {
      return fail(res, 400, 40016, '经纬度参数无效');
    }
    const r = await geo.fetchLocationWeather(lng, lat);
    return ok(res, r);
  } catch (err) {
    return next(err);
  }
});

// GET /towers：当前生效班组杆塔坐标全量（行 = [电压等级, 线路名称, 杆塔号, 经度, 纬度]），数据读写见 towers.js
router.get('/towers', async (req, res, next) => {
  try {
    if (!req.team) return ok(res, []);
    return ok(res, await towers.getTowers(req.team.id));
  } catch (err) {
    return next(err);
  }
});

// GET /towers/template：杆塔坐标导入模板（xlsx，表头 + 示例行）
router.get('/towers/template', requireDictAdmin, async (req, res, next) => {
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

// POST /towers/import：导入杆塔坐标 Excel（.xlsx），全量替换本班组坐标（事务）
const towerUpload = multer({ storage: multer.memoryStorage(), limits: { fileSize: 10 * 1024 * 1024, files: 1 } });
router.post(
  '/towers/import',
  requireDictAdmin,
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
      const fname = Buffer.from(req.file.originalname || '', 'latin1').toString('utf8');
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
          req.team.id,
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
      towers.invalidate(team.id);
      return ok(res, { imported: data.length, skipped }, `已导入 ${data.length} 条坐标`);
    } catch (err) {
      return next(err);
    }
  }
);

// 水印字段清洗：字符串、去首尾空格、按库列宽截断（work_content 500 / shot_time 32 / weather 64 / location 250）
function sanitizeWm(wm) {
  const cut = (v, n) => (typeof v === 'string' ? v.trim().slice(0, n) : '');
  const fields = {
    content: cut(wm.content, 500),
    time: cut(wm.time, 32),
    weather: cut(wm.weather, 64),
    location: cut(wm.location, 250),
    longitude: cut(wm.longitude, 32),
    latitude: cut(wm.latitude, 32),
  };
  // 防伪码：14 位字符集内才采信前端值，否则服务端重新生成（不由用户输入）
  const code = cut(wm.antiCode, 14);
  fields.antiCode = /^[A-HJ-NP-Z2-9]{14}$/.test(code) ? code : Watermark.randomCode(14);
  return fields;
}

// Dify 识别结果统一回写：写库时重新查询卡片当前的记录日期与派车目的地（而非发起验证时的快照），
// 规避「验证途中改派车目的地」的口径过期竞态；日期/地点核验由 checkWatermark 在后端完成（Dify 仅返回识别信息）
async function writeBackVerify(photoId, vr) {
  const [rows] = await pool.query(
    `SELECT DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, d.name AS destination_name
     FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
     LEFT JOIN worklog_destination d ON d.id = e.destination_id
     WHERE p.id = ?`,
    [photoId]
  );
  if (!rows.length) return; // 照片已删除
  if (!vr.ok) {
    await pool.query(
      `UPDATE worklog_photo SET verify_status = 'failed', work_content = '', shot_time = '', weather = '', location = '', lng = '', lat = '', date_ok = NULL, dest_ok = NULL WHERE id = ?`,
      [photoId]
    );
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
}

// POST /logs/:id/photos：上传照片（base64 → COS）。body.wm 可选：「选照片并添加水印」时携带
// { content/time/weather/location/longitude/latitude/antiCode/orientation }，服务端先渲染水印再传 COS、异步触发 Dify 验证；
// body.plain=true 为非水印照片（商旅打卡开启时可用）：原图直传、不渲染、不验证、不占「每人限一张」
// 三类照片（已有水印直传 / 服务端加水印 / 非水印）上传成功后均异步同步进所属人名的当日商旅费用照片
router.post('/logs/:id/photos', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const entryId = Number(req.params.id);
    const [entries] = await pool.query(
      `SELECT e.id, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, e.vehicle_id, d.name AS destination_name
       FROM worklog_entry e LEFT JOIN worklog_destination d ON d.id = e.destination_id
       WHERE e.id = ? AND e.team_id = ?`,
      [entryId, req.team.id]
    );
    const entry = entries[0];
    if (!entry) return fail(res, 404, 40400, '日志不存在');
    if (!entry.vehicle_id) return fail(res, 400, 40001, '未出车不可上传照片');

    const { image, members, wm, plain } = req.body || {};
    const isPlain = !!(plain && config.sgcc && config.sgcc.enabled);
    const names = Array.isArray(members) ? members.filter((n) => typeof n === 'string' && n.trim()) : [];
    if (!names.length) return fail(res, 400, 40009, '请选择照片所属人名');
    const memberErr = await checkPhotoMembers(entryId, names, null, isPlain);
    if (memberErr) return fail(res, 400, memberErr.code, memberErr.message);

    const match = /^data:image\/(jpeg|jpg|png);base64,(.+)$/.exec(image || '');
    if (!match) return fail(res, 400, 40010, '照片格式应为 jpeg/png（base64 dataURL）');
    let buf = Buffer.from(match[2], 'base64');
    if (!buf.length || buf.length > 15 * 1024 * 1024) {
      return fail(res, 400, 40011, '照片大小应在 15MB 以内');
    }

    // 需要加水印时：服务端渲染（EXIF 方向矫正 + 防伪码校验），产物统一为 JPEG；非水印跳过渲染
    let contentType = `image/${match[1] === 'png' ? 'png' : 'jpeg'}`;
    if (!isPlain && wm && typeof wm === 'object') {
      try {
        buf = await renderWatermarkedPhoto(buf, sanitizeWm(wm), wm.orientation);
        contentType = 'image/jpeg';
      } catch (err) {
        console.error('[出工日志] 水印渲染失败：', err.message);
        return fail(res, 400, 40015, '水印渲染失败，请重试');
      }
    }

    const prefix = config.worklog.cosPrefix.endsWith('/') ? config.worklog.cosPrefix : `${config.worklog.cosPrefix}/`;
    const key = `${prefix}${req.team.name}/${dots(entry.log_date)}/${entryId}-${Date.now()}.${contentType === 'image/png' ? 'png' : 'jpg'}`;
    await cos.putBuffer(key, buf, contentType);
    const url = cos.publicUrl(key);

    // 图片内容 MD5：商旅拉取按内容合并相同照片（一图多人标注）；列由商旅打卡模块补建，未开启时不写
    let photoId;
    if (config.sgcc && config.sgcc.enabled) {
      const imgMd5 = crypto.createHash('md5').update(buf).digest('hex');
      const [r] = await pool.query(
        `INSERT INTO worklog_photo (entry_id, cos_key, url, members, md5)
         VALUES (?, ?, ?, ?, ?)`,
        [entryId, key, url, JSON.stringify(names), imgMd5]
      );
      photoId = r.insertId;
    } else {
      const [r] = await pool.query(
        `INSERT INTO worklog_photo (entry_id, cos_key, url, members)
         VALUES (?, ?, ?, ?)`,
        [entryId, key, url, JSON.stringify(names)]
      );
      photoId = r.insertId;
    }
    if (isPlain) {
      // 非水印照片：免验证标记（不参与卡片验证规则）
      await pool.query(
        `UPDATE worklog_photo SET is_watermark = 0, verify_status = 'skipped' WHERE id = ?`,
        [photoId]
      );
    }

    if (!isPlain) {
      // 异步执行 Dify 识别并回写（含后端日期/地点核验），不阻塞响应（前端轮询 verify_status）
      dify
        .verifyPhoto({
          username: req.user.username,
          date: dots(entry.log_date),
          destination: entry.destination_name || '',
          url,
        })
        .then((vr) => writeBackVerify(photoId, vr))
        .catch((err) => console.error('[出工日志] 验证结果回写失败：', err.message));
    }

    // 商旅打卡开启时：异步同步进所属人名的当日商旅费用照片（失败由 sgcc_synced=2 + resync 兜底）
    if (config.sgcc && config.sgcc.enabled) {
      require('../sgccclockin').syncPhotoToSgcc(photoId)
        .catch((err) => console.error('[商旅打卡] 照片同步失败：', err.message));
    }

    return ok(res, {
      id: photoId, url,
      verify_status: isPlain ? 'skipped' : 'pending',
      ...(isPlain ? { is_watermark: 0 } : {}),
    });
  } catch (err) {
    return next(err);
  }
});

// POST /photos/:id/verify：验证失败（failed）后重新验证——重置为 pending 并异步重调 Dify
router.post('/photos/:id/verify', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const photoId = Number(req.params.id);
    const [rows] = await pool.query(
      `SELECT p.id, p.url, p.verify_status,
              DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, d.name AS destination_name
       FROM worklog_photo p
       JOIN worklog_entry e ON e.id = p.entry_id
       LEFT JOIN worklog_destination d ON d.id = e.destination_id
       WHERE p.id = ? AND e.team_id = ?`,
      [photoId, req.team.id]
    );
    const photo = rows[0];
    if (!photo) return fail(res, 404, 40400, '照片不存在');
    if (photo.verify_status !== 'failed') {
      return fail(res, 400, 40014, '仅验证失败的照片可重新验证');
    }
    await pool.query(
      `UPDATE worklog_photo SET verify_status = 'pending', work_content = '', shot_time = '', weather = '', location = '', lng = '', lat = '', date_ok = NULL, dest_ok = NULL WHERE id = ?`,
      [photoId]
    );
    // 异步重调 Dify 并回写（不阻塞响应，前端轮询 verify_status）
    dify
      .verifyPhoto({
        username: req.user.username,
        date: dots(photo.log_date),
        destination: photo.destination_name || '',
        url: photo.url,
      })
      .then((vr) => writeBackVerify(photoId, vr))
      .catch((err) => console.error('[出工日志] 验证结果回写失败：', err.message));
    return ok(res, { verify_status: 'pending' });
  } catch (err) {
    return next(err);
  }
});

// PUT /photos/:id/members：修改照片所属人名（商旅打卡开启时：未绑定/登录过期成员的人名状态不可更改；
// 变更后差量同步——新增人名补传商旅费用照片，剔除人名从其商旅费用照片移除）
router.put('/photos/:id/members', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const photoId = Number(req.params.id);
    const [rows] = await pool.query(
      `SELECT p.entry_id, p.members FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
       WHERE p.id = ? AND e.team_id = ?`,
      [photoId, req.team.id]
    );
    if (!rows.length) return fail(res, 404, 40400, '照片不存在');
    const { members } = req.body || {};
    const names = Array.isArray(members) ? members.filter((n) => typeof n === 'string' && n.trim()) : [];
    if (!names.length) return fail(res, 400, 40009, '请选择照片所属人名');
    const memberErr = await checkPhotoMembers(rows[0].entry_id, names, photoId);
    if (memberErr) return fail(res, 400, memberErr.code, memberErr.message);

    const oldNames = typeof rows[0].members === 'string' ? JSON.parse(rows[0].members) : (rows[0].members || []);

    // 商旅打卡开启时：先校验未绑定/登录过期成员的人名状态不可更改（增减均拒绝；此类成员的商旅联动无法进行）
    if (config.sgcc && config.sgcc.enabled) {
      const [mems] = await pool.query(
        `SELECT m.name, a.token_status
         FROM worklog_entry_member em
         JOIN worklog_member m ON m.id = em.member_id
         LEFT JOIN worklog_sgcc_account a ON a.member_id = m.id
         WHERE em.entry_id = ?`,
        [rows[0].entry_id]
      );
      for (const m of mems) {
        if (m.token_status === 1) continue; // 绑定且登录态有效
        const was = oldNames.includes(m.name);
        const now = names.includes(m.name);
        if (was !== now) {
          return fail(res, 400, 40024,
            `成员「${m.name}」${m.token_status === 0 ? '商旅登录已过期' : '未绑定商旅账号'}，人名状态不可更改`);
        }
      }
      // 剔除人名：同步远端先删（一切以商旅平台为准——远端删除失败则本地不变更，全部中止）
      const removed = oldNames.filter((n) => !names.includes(n));
      if (removed.length) {
        const r = await require('../sgccclockin').removePhotoMembersRemote(photoId, removed);
        if (!r.ok) {
          return fail(res, 400, 40025,
            `成员「${r.failedName}」商旅费用照片删除失败（${r.error}），本地未修改，请重试`);
        }
      }
    }

    await pool.query('UPDATE worklog_photo SET members = ? WHERE id = ?', [JSON.stringify(names), photoId]);
    // 商旅打卡开启时：新增人名异步补传（已链接成员跳过；失败记核查日志由 resync 兜底；
    // 另覆盖一种自愈——上轮剔除部分成功后中止，缺链接的在名人随本次补传恢复商旅图片）
    if (config.sgcc && config.sgcc.enabled) {
      require('../sgccclockin').syncPhotoToSgcc(photoId)
        .catch((err) => console.error('[商旅打卡] 照片人名变更后补同步失败：', err.message));
    }
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// DELETE /photos/:id：删除照片（商旅打卡开启时先同步解除全部所属人名的商旅费用照片关联——
// 一切以商旅平台为准：解除失败则本地不删除；成功后才删 COS 对象与库记录）
router.delete('/photos/:id', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const photoId = Number(req.params.id);
    const [rows] = await pool.query(
      `SELECT p.cos_key FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
       WHERE p.id = ? AND e.team_id = ?`,
      [photoId, req.team.id]
    );
    if (!rows.length) return fail(res, 404, 40400, '照片不存在');
    if (config.sgcc && config.sgcc.enabled) {
      const r = await require('../sgccclockin').unlinkPhotoFromSgcc(photoId);
      if (!r.ok) {
        return fail(res, 400, 40026,
          `成员「${r.failedName}」商旅费用照片解除失败（${r.error}），本地未删除，请重试`);
      }
    }
    try {
      await cos.deleteObject(rows[0].cos_key);
    } catch (err) {
      console.error('[出工日志] 删除 COS 对象失败（继续删库记录）：', rows[0].cos_key, err.message);
    }
    await pool.query('DELETE FROM worklog_photo WHERE id = ?', [photoId]);
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// GET /photos/:id/download：单张照片下载代理（COS 跨域无 CORS，网页端经本接口回源并附下载文件名）
router.get('/photos/:id/download', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const photoId = Number(req.params.id);
    const [rows] = await pool.query(
      `SELECT p.cos_key, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date
       FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id WHERE p.id = ? AND e.team_id = ?`,
      [photoId, req.team.id]
    );
    if (!rows.length) return fail(res, 404, 40400, '照片不存在');
    const resp = await fetch(cos.publicUrl(rows[0].cos_key), { signal: AbortSignal.timeout(60000) });
    if (!resp.ok) return fail(res, 502, 50201, `照片回源失败（HTTP ${resp.status}）`);
    res.setHeader('Content-Type', resp.headers.get('content-type') || 'application/octet-stream');
    const len = Number(resp.headers.get('content-length') || 0);
    if (len) res.setHeader('Content-Length', len);
    // 文件名沿用打包下载规则：日期-照片id.jpg；RFC5987 编码，附 ASCII fallback
    const name = `${rows[0].log_date || '照片'}-${photoId}.jpg`;
    res.setHeader(
      'Content-Disposition',
      `attachment; filename="photo-${photoId}.jpg"; filename*=UTF-8''${encodeURIComponent(name)}`
    );
    Readable.fromWeb(resp.body).pipe(res);
  } catch (err) {
    return next(err);
  }
});

// ===== 照片 ZIP 打包下载（自 WorkLogs 独立服务移植，请求体形状不变：{ photos: [{ url, name }] }）=====
const ZIP_MAX_PHOTOS = 300;
const PHOTO_MAX_BYTES = 50 * 1024 * 1024;

// 文件名净化：去路径分隔符与非法字符，防止 zip 内路径穿越
function sanitizeFileName(name) {
  const cleaned = String(name || '')
    .replace(/[\\/:*?"<>|\x00-\x1f]/g, '_')
    .trim();
  return cleaned || 'photo.jpg';
}

// POST /zip：批量打包水印照片，逐张下载流式写入 zip；失败记录进清单继续，全部失败才报错
router.post('/zip', async (req, res, next) => {
  try {
    const photos = req.body && Array.isArray(req.body.photos) ? req.body.photos : [];
    if (!photos.length) {
      return fail(res, 400, 40017, '没有可下载的照片');
    }
    if (photos.length > ZIP_MAX_PHOTOS) {
      return fail(res, 400, 40017, `一次最多打包 ${ZIP_MAX_PHOTOS} 张照片`);
    }
    const list = [];
    for (const p of photos) {
      const url = String((p && p.url) || '');
      if (!/^https?:\/\//i.test(url)) {
        return fail(res, 400, 40017, '照片地址不合法（仅支持 http/https）');
      }
      list.push({ url, name: sanitizeFileName(p && p.name) });
    }

    // RFC5987 编码中文文件名，附 ASCII fallback
    res.setHeader('Content-Type', 'application/zip');
    res.setHeader(
      'Content-Disposition',
      `attachment; filename="photos.zip"; filename*=UTF-8''${encodeURIComponent('水印照片.zip')}`
    );

    // store 模式：图片本身已是压缩格式，不再二次压缩
    const archive = archiver('zip', { store: true });
    archive.on('warning', (e) => console.warn(`[出工日志] ZIP 警告：${e && e.message ? e.message : e}`));
    archive.on('error', (e) => console.error(`[出工日志] ZIP 错误：${e && e.message ? e.message : e}`));
    // 客户端中断时及时清理，停止后续下载
    res.on('close', () => {
      archive.destroy();
    });
    archive.pipe(res);

    const failed = [];
    let success = 0;
    for (const item of list) {
      if (archive.destroyed) return; // 客户端已断开
      try {
        const resp = await fetch(item.url, { signal: AbortSignal.timeout(30000) });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const declared = Number(resp.headers.get('content-length') || 0);
        if (declared > PHOTO_MAX_BYTES) throw new Error('照片超过 50MB，已跳过');
        const buf = Buffer.from(await resp.arrayBuffer());
        if (buf.length > PHOTO_MAX_BYTES) throw new Error('照片超过 50MB，已跳过');
        archive.append(buf, { name: item.name });
        success++;
      } catch (e) {
        console.warn(`[出工日志] 照片下载失败（${item.url}）：${e && e.message ? e.message : e}`);
        failed.push(item);
      }
    }

    if (success === 0) {
      // 尚无字节写出，可安全改回 JSON 错误响应
      archive.unpipe(res);
      archive.destroy();
      res.removeHeader('Content-Type');
      res.removeHeader('Content-Disposition');
      return fail(res, 500, 50001, '照片下载失败');
    }

    if (failed.length) {
      const content = failed.map((f) => `${f.name} ${f.url}`).join('\n');
      archive.append(content, { name: '下载失败清单.txt' });
    }

    try {
      await archive.finalize();
    } catch (e) {
      console.error(`[出工日志] ZIP 打包失败：${e && e.message ? e.message : e}`);
      archive.destroy();
    }
  } catch (err) {
    return next(err);
  }
});

// ===== 工作任务单（管理员）：范围内全部出车卡片渲染模板并合并为单个 docx，一卡一页，供打印 =====

// 解析日期范围入参：from/to 优先；仅传 date 时视为单日（兼容旧调用）；范围上限 31 天（打印场景按周/月）
function sheetRange(req) {
  let { from, to } = req.query || {};
  if (!from && !to && req.query.date) {
    from = req.query.date;
    to = req.query.date;
  }
  if (!DATE_RE.test(from || '') || !DATE_RE.test(to || '') || from > to) return null;
  const days = (new Date(`${to}T00:00:00Z`) - new Date(`${from}T00:00:00Z`)) / 86400000 + 1;
  if (days > 31) return 'tooLong';
  return { from, to };
}

// 范围文件名：单日 工作任务单-2026-08-18.docx；跨天 工作任务单-2026-08-18至2026-08-20.docx
function sheetFileName(from, to) {
  return from === to ? `工作任务单-${from}.docx` : `工作任务单-${from}至${to}.docx`;
}

// 费用汇总文件名：口径同任务单（单日带单日期，跨天带范围）
function feeFileName(from, to) {
  return from === to ? `费用汇总-${from}.docx` : `费用汇总-${from}至${to}.docx`;
}

// 生成前核验（工作任务单 / 费用汇总共用）：范围内存在未通过验证的记录时拦截，
// 随 data.failures 下发未通过清单（日期/车牌/用车人/未通过项明细），前端弹层展示「请处理后再操作」
async function sheetVerifyFailures(teamId, from, to) {
  const list = await loadEntries('e.log_date BETWEEN ? AND ? AND e.team_id = ?', [from, to, teamId]);
  const failed = list.filter((e) => e.verify_passed === 'failed');
  if (!failed.length) return null;
  return failed.map((e) => ({
    id: e.id,
    log_date: e.log_date,
    plate_no: e.plate_no || '未出车',
    members: e.members.map((m) => m.name),
    reasons: e.verify_reasons,
  }));
}

// 核验拦截：有未通过记录时按 40901 响应并返回 true（任务单 / 费用汇总的下载与预览四路由共用）
function failIfVerifyFailed(res, failures) {
  if (!failures) return false;
  fail(res, 409, 40901, `所选范围内有 ${failures.length} 条记录存在未通过项，请处理后再操作`, { failures });
  return true;
}

// 强制执行：管理员确认后带 force=1 跳过生成前核验（预览地址内嵌的下载地址需同步携带，否则 basemetas 回源仍会被拦）
function isForce(req) {
  return req.query.force === '1';
}

// GET /task-sheet?from=&to=（或 date= 单日）：二进制 docx 响应（不走 {code,data} 信封；
// 供小程序 downloadFile / 网页 fetch+blob / basemetas 回源三种消费方式）
router.get('/task-sheet', requireDictAdmin, async (req, res, next) => {
  try {
    const range = sheetRange(req);
    if (range === 'tooLong') return fail(res, 400, 40000, '日期范围不能超过 31 天');
    if (!range) return fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD，且 from 不晚于 to');
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    if (!isForce(req) && failIfVerifyFailed(res, await sheetVerifyFailures(req.team.id, range.from, range.to))) return;
    const result = await tasksheet.build(req.team, range.from, range.to);
    if (!result) return fail(res, 404, 40402, '该日期范围没有出车记录，无可生成的卡片');
    const fileName = sheetFileName(range.from, range.to);
    res.setHeader('Content-Type', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document');
    res.setHeader('Content-Disposition',
      `attachment; filename="task-sheet-${range.from}.docx"; filename*=UTF-8''${encodeURIComponent(fileName)}`);
    return res.send(result.buffer);
  } catch (err) {
    return next(err);
  }
});

// GET /task-sheet/preview?from=&to=（或 date= 单日）：拼 basemetas 预览地址（预览服务器凭地址内 ?token= 回源拉取上方下载接口；同安全日记录口径）
router.get('/task-sheet/preview', requireDictAdmin, async (req, res, next) => {
  try {
    const range = sheetRange(req);
    if (range === 'tooLong') return fail(res, 400, 40000, '日期范围不能超过 31 天');
    if (!range) return fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD，且 from 不晚于 to');
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    if (!isForce(req) && failIfVerifyFailed(res, await sheetVerifyFailures(req.team.id, range.from, range.to))) return;
    const base = (config.basemetas.url || '').replace(/\/+$/, '');
    if (!base) return fail(res, 400, 40011, '未配置文件预览服务');
    // 预检：无出车记录时直接报错，避免预览服务回源拉到错误响应
    if (!(await tasksheet.hasRows(req.team.id, range.from, range.to))) {
      return fail(res, 404, 40402, '该日期范围没有出车记录，无可生成的卡片');
    }
    // 反代后 req.protocol 恒为 http（未开 trust proxy）：优先取 X-Forwarded-Proto 头回退
    const proto = req.headers['x-forwarded-proto'] || req.protocol;
    const teamQ = req.query.team_id ? `&team_id=${encodeURIComponent(req.query.team_id)}` : '';
    const downloadUrl = `${proto}://${req.get('host')}/api/v1/worklog/task-sheet?from=${range.from}&to=${range.to}${teamQ}` +
      `&token=${encodeURIComponent(req.token)}${isForce(req) ? '&force=1' : ''}`;
    const fileName = sheetFileName(range.from, range.to);
    const url = `${base}/preview/view?url=${encodeURIComponent(downloadUrl)}` +
      `&fileName=${encodeURIComponent(fileName)}&displayName=${encodeURIComponent(fileName)}`;
    return ok(res, { url });
  } catch (err) {
    return next(err);
  }
});

// ===== 出差费用汇总（管理员）：范围内出车卡片渲染为「人 × 日」费用矩阵 docx（一卡一行、一人一列、末尾合计行）=====
// 列 = 范围内当过用车人的成员（按成员字典点亮顺序）；单元格 = {伙食补助+交通费}×1={计算值}；生成前核验同任务单

// GET /fee-sheet?from=&to=（或 date= 单日）：二进制 docx 响应（消费方式同 /task-sheet）
router.get('/fee-sheet', requireDictAdmin, async (req, res, next) => {
  try {
    const range = sheetRange(req);
    if (range === 'tooLong') return fail(res, 400, 40000, '日期范围不能超过 31 天');
    if (!range) return fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD，且 from 不晚于 to');
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    if (!isForce(req) && failIfVerifyFailed(res, await sheetVerifyFailures(req.team.id, range.from, range.to))) return;
    const result = await feesheet.build(req.team, range.from, range.to);
    if (!result) return fail(res, 404, 40402, '该日期范围没有出车记录，无可生成的汇总');
    const fileName = feeFileName(range.from, range.to);
    res.setHeader('Content-Type', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document');
    res.setHeader('Content-Disposition',
      `attachment; filename="fee-sheet-${range.from}.docx"; filename*=UTF-8''${encodeURIComponent(fileName)}`);
    return res.send(result.buffer);
  } catch (err) {
    return next(err);
  }
});

// GET /fee-sheet/preview?from=&to=（或 date= 单日）：拼 basemetas 预览地址（口径同 /task-sheet/preview）
router.get('/fee-sheet/preview', requireDictAdmin, async (req, res, next) => {
  try {
    const range = sheetRange(req);
    if (range === 'tooLong') return fail(res, 400, 40000, '日期范围不能超过 31 天');
    if (!range) return fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD，且 from 不晚于 to');
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    if (!isForce(req) && failIfVerifyFailed(res, await sheetVerifyFailures(req.team.id, range.from, range.to))) return;
    const base = (config.basemetas.url || '').replace(/\/+$/, '');
    if (!base) return fail(res, 400, 40011, '未配置文件预览服务');
    // 预检：无出车记录时直接报错，避免预览服务回源拉到错误响应（判定口径同任务单 hasRows）
    if (!(await tasksheet.hasRows(req.team.id, range.from, range.to))) {
      return fail(res, 404, 40402, '该日期范围没有出车记录，无可生成的汇总');
    }
    // 反代后 req.protocol 恒为 http（未开 trust proxy）：优先取 X-Forwarded-Proto 头回退
    const proto = req.headers['x-forwarded-proto'] || req.protocol;
    const teamQ = req.query.team_id ? `&team_id=${encodeURIComponent(req.query.team_id)}` : '';
    const downloadUrl = `${proto}://${req.get('host')}/api/v1/worklog/fee-sheet?from=${range.from}&to=${range.to}${teamQ}` +
      `&token=${encodeURIComponent(req.token)}${isForce(req) ? '&force=1' : ''}`;
    const fileName = feeFileName(range.from, range.to);
    const url = `${base}/preview/view?url=${encodeURIComponent(downloadUrl)}` +
      `&fileName=${encodeURIComponent(fileName)}&displayName=${encodeURIComponent(fileName)}`;
    return ok(res, { url });
  } catch (err) {
    return next(err);
  }
});

// POST /dispatch/align：导入派车单（multipart 字段 files，多文件合并解析）按「日期＋用车人」对齐当天出车卡片，
// 返回差异清单（只读对齐不写库；更正走 PUT /logs/{id}，车牌入字典走 /admin/vehicles）
const dispatchUpload = multer({ storage: multer.memoryStorage(), limits: { fileSize: 10 * 1024 * 1024, files: 20 } });
router.post(
  '/dispatch/align',
  requireDictAdmin,
  (req, res, next) => {
    dispatchUpload.array('files', 20)(req, res, (err) => {
      if (err) {
        if (err.code === 'LIMIT_FILE_SIZE') return fail(res, 400, 40023, '单个文件大小应在 10MB 以内');
        if (err.code === 'LIMIT_FILE_COUNT' || err.code === 'LIMIT_UNEXPECTED_FILE') {
          return fail(res, 400, 40023, '一次最多导入 20 个文件');
        }
        return next(err);
      }
      return next();
    });
  },
  async (req, res, next) => {
    try {
      // multipart 表单体在路由级 multer 之后才可读：此处重新解析生效班组（同 towers/import 口径）
      const team = await teamUtil.resolveTeam(req.user, req.body && req.body.team_id !== undefined ? req.body.team_id : req.query.team_id);
      if (!team) return fail(res, 400, 40023, '无可用班组');
      if (req.user.role === 'team_admin' && req.user.team_id !== team.id) {
        return fail(res, 403, 40304, '仅管理员可执行此操作');
      }
      const files = (req.files || []).map((f) => ({
        // multer 对中文文件名为 latin1，转回 utf8（同 towers/import 口径）
        name: Buffer.from(f.originalname || '', 'latin1').toString('utf8'),
        buffer: f.buffer,
      }));
      if (!files.length) return fail(res, 400, 40023, '请选择派车单文件');
      for (const f of files) {
        if (!/\.(xls|xlsx)$/i.test(f.name)) return fail(res, 400, 40023, `「${f.name}」不是 Excel 文件（仅支持 .xls / .xlsx）`);
      }
      try {
        const result = await dispatch.align(team, files);
        return ok(res, result, `对齐完成：${result.items.length} 条不一致`);
      } catch (e) {
        if (e.status === 400) return fail(res, 400, 40023, e.message);
        throw e;
      }
    } catch (err) {
      return next(err);
    }
  }
);

// ===== 管理接口（车牌号 / 目的地 / 人员 三类字典同构维护，按生效班组隔离）=====
// 权限：超管可管任意班组（?team_id= 指定），班组管理员仅本班（requireDictAdmin）
function dictRoutes(path, table, field, label, countRefs) {
  router.get(`/admin/${path}`, requireDictAdmin, async (req, res, next) => {
    try {
      if (!req.team) return ok(res, { list: [] });
      const extra = table === 'worklog_member' ? ', user_id' : '';
      const [rows] = await pool.query(
        `SELECT id, ${field} AS name, sort, status${extra} FROM ${table} WHERE team_id = ? ORDER BY sort, id`,
        [req.team.id]
      );
      return ok(res, { list: rows });
    } catch (err) {
      return next(err);
    }
  });

  router.post(`/admin/${path}`, requireDictAdmin, async (req, res, next) => {
    try {
      if (!req.team) return fail(res, 400, 40010, '无可用班组');
      const name = String((req.body && req.body.name) || '').trim();
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

  router.put(`/admin/${path}/:id`, requireDictAdmin, async (req, res, next) => {
    try {
      if (!req.team) return fail(res, 400, 40010, '无可用班组');
      const id = Number(req.params.id);
      const [exist] = await pool.query(`SELECT id FROM ${table} WHERE id = ? AND team_id = ?`, [id, req.team.id]);
      if (!exist.length) return fail(res, 404, 40400, `${label}不存在`);
      const { name, sort, status } = req.body || {};
      if (name !== undefined) {
        const trimmed = String(name).trim();
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
      return ok(res, null);
    } catch (err) {
      return next(err);
    }
  });

  router.delete(`/admin/${path}/:id`, requireDictAdmin, async (req, res, next) => {
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

// PUT /admin/members/:id/move {dir:'up'|'down'}：成员排序（点亮按钮顺序；工作任务单「工作负责人」取排序最前的用车人）
// 与班内相邻成员换位；事务内按当前顺序整体重写 sort 为连续 1..N（兼容历史同值 sort，避免交换后次序不变）
router.put('/admin/members/:id/move', requireDictAdmin, async (req, res, next) => {
  const conn = await pool.getConnection();
  try {
    if (!req.team) return fail(res, 400, 40010, '无可用班组');
    const id = Number(req.params.id);
    const dir = (req.body && req.body.dir) === 'up' ? 'up' : 'down';
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

module.exports = router;
