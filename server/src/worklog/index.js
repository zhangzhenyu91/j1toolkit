// 出工日志路由：全部接口需登录 + work-log 应用权限；/admin/* 再叠加管理员角色校验
// 业务规则与设计稿见《开发指南》第四、七章与 design/worklog.html
const express = require('express');
const archiver = require('archiver');
const multer = require('multer');
const { Readable } = require('stream');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const requireAdmin = require('../middleware/requireAdmin');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const config = require('../config');
const cos = require('./cos');
const dify = require('./dify');
const geo = require('./geo');
const Watermark = require('./watermark');
const { renderWatermarkedPhoto } = require('./render-photo');
const { computeVerifyPassed, computeFailReasons, myReportReasons } = require('./verify');

const router = express.Router();
router.use(auth, requireApp('work-log'));

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
    `SELECT e.id, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, e.patrol_content,
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
    });
  });

  return entries.map((e) => {
    const entry = {
      ...e,
      remark: e.remark || '',
      remark_files: parseRemarkFiles(e.remark_files),
      members: memberMap[e.id] || [],
      photos: photoMap[e.id] || [],
    };
    entry.verify_passed = computeVerifyPassed(entry);
    entry.verify_reasons = computeFailReasons(entry);
    return entry;
  });
}

// 校验车牌/目的地/成员 id 有效（车牌、目的地需启用）
async function validDictId(table, id, needEnabled) {
  if (!id) return true;
  const [rows] = await pool.query(`SELECT id, status FROM ${table} WHERE id = ?`, [id]);
  if (!rows.length) return false;
  return needEnabled ? rows[0].status === 1 : true;
}

// 当前登录用户对应的出工成员（按 sys_user.nickname == worklog_member.name 匹配，仅查看个人视图用）
async function myMember(userId) {
  const [users] = await pool.query('SELECT nickname FROM sys_user WHERE id = ?', [userId]);
  if (!users.length || !users[0].nickname) return null;
  const [members] = await pool.query('SELECT id, name FROM worklog_member WHERE name = ?', [users[0].nickname]);
  return members.length ? members[0] : null;
}

// GET /meta：下拉/点亮数据源（前端自行补「未出车」固定项）
router.get('/meta', async (req, res, next) => {
  try {
    const [vehicles] = await pool.query(
      'SELECT id, plate_no FROM worklog_vehicle WHERE status = 1 ORDER BY sort, id'
    );
    const [destinations] = await pool.query(
      'SELECT id, name FROM worklog_destination WHERE status = 1 ORDER BY sort, id'
    );
    const [members] = await pool.query(
      'SELECT id, name, sort FROM worklog_member WHERE status = 1 ORDER BY sort, id'
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
    let where = 'e.log_date = ?';
    const params = [date];
    if (req.query.scope === 'mine') {
      const me = await myMember(req.user.id);
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
// scope=mine 个人视图：仅统计用车人包含自己的卡片；红色 = 我未打卡 / 没有含我名字的水印照片 / 含我照片未通过（含验证中/失败）
router.get('/day-status', async (req, res, next) => {
  try {
    const { month } = req.query;
    if (!MONTH_RE.test(month || '')) return fail(res, 400, 40000, '月份格式应为 YYYY-MM');

    if (req.query.scope === 'mine') {
      const me = await myMember(req.user.id);
      if (!me) return ok(res, { map: {} });
      const list = await loadEntries(
        `DATE_FORMAT(e.log_date, '%Y-%m') = ? AND e.id IN (SELECT entry_id FROM worklog_entry_member WHERE member_id = ?)`,
        [month, me.id]
      );
      const map = {};
      list.forEach((e) => {
        if (map[e.log_date] === 'failed') return; // 有未通过即锁定红
        const myRow = e.members.find((m) => m.member_id === me.id);
        const myPhotos = e.photos.filter((p) => (p.members || []).includes(me.name));
        let st = 'passed';
        if (!myRow || !myRow.checked) st = 'failed';
        else if (!myPhotos.length) st = 'failed';
        else if (myPhotos.some((p) => p.verify_status !== 'passed')) st = 'failed';
        // 通过但有备注 → 黄（不覆盖红；已有黄不被绿覆盖）
        if (st === 'passed' && (e.remark || e.remark_files.length)) st = 'remark';
        if (st === 'passed' && map[e.log_date] === 'remark') return;
        map[e.log_date] = st;
      });
      return ok(res, { map });
    }

    const list = await loadEntries(`DATE_FORMAT(e.log_date, '%Y-%m') = ?`, [month]);
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

// POST /logs：新建卡片（一卡片一派车；vehicle_id 空=未出车）
router.post('/logs', async (req, res, next) => {
  try {
    const { log_date, patrol_content = '', vehicle_id = null, destination_id = null } = req.body || {};
    let { member_ids = [] } = req.body || {};
    if (!DATE_RE.test(log_date || '')) return fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD');
    member_ids = Array.isArray(member_ids) ? member_ids.map(Number).filter(Number.isInteger) : [];

    if (!vehicle_id) {
      if (destination_id || member_ids.length) {
        return fail(res, 400, 40001, '未出车时不可填写目的地与用车人');
      }
    } else {
      if (!(await validDictId('worklog_vehicle', vehicle_id, true))) {
        return fail(res, 400, 40002, '车牌号无效或已停用');
      }
      if (destination_id && !(await validDictId('worklog_destination', destination_id, true))) {
        return fail(res, 400, 40003, '目的地无效或已停用');
      }
    }

    // 成员 id 校验并取 sort
    let memberRows = [];
    if (member_ids.length) {
      const [rows] = await pool.query(
        'SELECT id, sort FROM worklog_member WHERE id IN (?) AND status = 1',
        [member_ids]
      );
      if (rows.length !== new Set(member_ids).size) {
        return fail(res, 400, 40004, '存在无效或已停用的成员');
      }
      memberRows = rows;
    }

    const [r] = await pool.query(
      'INSERT INTO worklog_entry (log_date, patrol_content, vehicle_id, destination_id, created_by) VALUES (?, ?, ?, ?, ?)',
      [log_date, patrol_content, vehicle_id, destination_id, req.user.id]
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
    const entryId = Number(req.params.id);
    const [exist] = await pool.query('SELECT id FROM worklog_entry WHERE id = ?', [entryId]);
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
      if (!(await validDictId('worklog_vehicle', vehicle_id, true))) {
        return fail(res, 400, 40002, '车牌号无效或已停用');
      }
      if (destination_id && !(await validDictId('worklog_destination', destination_id, true))) {
        return fail(res, 400, 40003, '目的地无效或已停用');
      }
    }
    member_ids = Array.isArray(member_ids) ? member_ids.map(Number).filter(Number.isInteger) : [];
    let memberRows = [];
    if (member_ids.length) {
      const [rows] = await pool.query(
        'SELECT id, sort FROM worklog_member WHERE id IN (?) AND status = 1',
        [member_ids]
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
    const entryId = Number(req.params.id);
    const [photos] = await pool.query('SELECT cos_key FROM worklog_photo WHERE entry_id = ?', [entryId]);
    const [entryRows] = await pool.query('SELECT remark_files FROM worklog_entry WHERE id = ?', [entryId]);
    const cosKeys = photos.map((p) => p.cos_key);
    if (entryRows.length) {
      parseRemarkFiles(entryRows[0].remark_files).forEach((f) => cosKeys.push(f.cos_key));
    }
    for (const key of cosKeys) {
      try {
        await cos.deleteObject(key);
      } catch (err) {
        console.error('[出工日志] 删除 COS 对象失败（继续删库记录）：', key, err.message);
      }
    }
    await pool.query('DELETE FROM worklog_photo WHERE entry_id = ?', [entryId]);
    await pool.query('DELETE FROM worklog_entry_member WHERE entry_id = ?', [entryId]);
    const [r] = await pool.query('DELETE FROM worklog_entry WHERE id = ?', [entryId]);
    if (!r.affectedRows) return fail(res, 404, 40400, '日志不存在');
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// PUT /logs/:id/members/:mid/check：打卡切换
router.put('/logs/:id/members/:mid/check', async (req, res, next) => {
  try {
    const [r] = await pool.query(
      'UPDATE worklog_entry_member SET checked = 1 - checked WHERE id = ? AND entry_id = ?',
      [Number(req.params.mid), Number(req.params.id)]
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
    const [rows] = await pool.query(
      `SELECT p.id, p.url, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date,
              DATE_FORMAT(e.log_date, '%Y-%m') AS month, DAYOFMONTH(e.log_date) AS day, p.members
       FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
       WHERE e.log_date BETWEEN ? AND ? ORDER BY e.log_date, p.id`,
      [from, to]
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

    let me = null;
    if (req.query.scope === 'mine') {
      me = await myMember(req.user.id);
      if (!me) return ok(res, { list: [] });
    }
    const list = await loadEntries('e.log_date BETWEEN ? AND ?', [from, to]);
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

// 按 entryId + cos_key 定位备注附件（预览/下载共用；key 必须确属该卡，防止拿任意外地拉扯）
async function findRemarkFile(entryId, key) {
  const [rows] = await pool.query('SELECT remark_files FROM worklog_entry WHERE id = ?', [entryId]);
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
      const entryId = Number(req.params.id);
      const [entries] = await pool.query(
        `SELECT id, DATE_FORMAT(log_date, '%Y-%m-%d') AS log_date FROM worklog_entry WHERE id = ?`,
        [entryId]
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
      const key = `${prefix}remark/${dots(entries[0].log_date)}/${entryId}-${Date.now()}.${ext}`;
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
    const { entry, file } = await findRemarkFile(Number(req.params.id), String(req.query.key || ''));
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
    const { entry, file } = await findRemarkFile(Number(req.params.id), String(req.query.key || ''));
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
async function checkPhotoMembers(entryId, names, excludePhotoId) {
  const [memberRows] = await pool.query(
    'SELECT m.name FROM worklog_entry_member em JOIN worklog_member m ON m.id = em.member_id WHERE em.entry_id = ?',
    [entryId]
  );
  const allowed = new Set(memberRows.map((r) => r.name));
  for (const n of names) {
    if (!allowed.has(n)) return { code: 40007, message: `「${n}」不是本卡用车人` };
  }
  const [photos] = await pool.query('SELECT id, members FROM worklog_photo WHERE entry_id = ?', [entryId]);
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

// GET /geo?lng=&lat=：按经纬度取当前「地点 + 天气」（和风），供「选照片并添加水印」无历史照片时预填；
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

// 水印字段清洗：字符串、去首尾空格、按库列宽截断（work_content 512 / shot_time 32 / weather 64 / location 255）
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

// POST /logs/:id/photos：上传水印照片（base64 → COS），异步触发 Dify 验证
// body.wm 可选：「选照片并添加水印」时携带 { content/time/weather/location/longitude/latitude/antiCode/orientation }，
// 服务端先把水印渲染到原图上，再按同一流程传 COS、触发验证
router.post('/logs/:id/photos', async (req, res, next) => {
  try {
    const entryId = Number(req.params.id);
    const [entries] = await pool.query(
      `SELECT e.id, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, e.vehicle_id, d.name AS destination_name
       FROM worklog_entry e LEFT JOIN worklog_destination d ON d.id = e.destination_id WHERE e.id = ?`,
      [entryId]
    );
    const entry = entries[0];
    if (!entry) return fail(res, 404, 40400, '日志不存在');
    if (!entry.vehicle_id) return fail(res, 400, 40001, '未出车不可上传水印照片');

    const { image, members, wm } = req.body || {};
    const names = Array.isArray(members) ? members.filter((n) => typeof n === 'string' && n.trim()) : [];
    if (!names.length) return fail(res, 400, 40009, '请选择照片所属人名');
    const memberErr = await checkPhotoMembers(entryId, names, null);
    if (memberErr) return fail(res, 400, memberErr.code, memberErr.message);

    const match = /^data:image\/(jpeg|jpg|png);base64,(.+)$/.exec(image || '');
    if (!match) return fail(res, 400, 40010, '照片格式应为 jpeg/png（base64 dataURL）');
    let buf = Buffer.from(match[2], 'base64');
    if (!buf.length || buf.length > 15 * 1024 * 1024) {
      return fail(res, 400, 40011, '照片大小应在 15MB 以内');
    }

    // 需要加水印时：服务端渲染（EXIF 方向矫正 + 防伪码校验），产物统一为 JPEG
    let contentType = `image/${match[1] === 'png' ? 'png' : 'jpeg'}`;
    if (wm && typeof wm === 'object') {
      try {
        buf = await renderWatermarkedPhoto(buf, sanitizeWm(wm), wm.orientation);
        contentType = 'image/jpeg';
      } catch (err) {
        console.error('[出工日志] 水印渲染失败：', err.message);
        return fail(res, 400, 40015, '水印渲染失败，请重试');
      }
    }

    const prefix = config.worklog.cosPrefix.endsWith('/') ? config.worklog.cosPrefix : `${config.worklog.cosPrefix}/`;
    const key = `${prefix}${dots(entry.log_date)}/${entryId}-${Date.now()}.${contentType === 'image/png' ? 'png' : 'jpg'}`;
    await cos.putBuffer(key, buf, contentType);
    const url = cos.publicUrl(key);

    const [r] = await pool.query(
      'INSERT INTO worklog_photo (entry_id, cos_key, url, members) VALUES (?, ?, ?, ?)',
      [entryId, key, url, JSON.stringify(names)]
    );
    const photoId = r.insertId;

    // 异步执行 Dify 验证并回写，不阻塞响应（前端轮询 verify_status）
    dify
      .verifyPhoto({
        username: req.user.username,
        date: dots(entry.log_date),
        destination: entry.destination_name || '',
        url,
      })
      .then((vr) =>
        pool.query(
          `UPDATE worklog_photo SET verify_status = ?, work_content = ?, shot_time = ?, weather = ?, location = ?, lng = ?, lat = ?, date_ok = ?, dest_ok = ? WHERE id = ?`,
          [vr.status, vr.workContent, vr.time, vr.weather, vr.location, vr.lng, vr.lat,
            vr.dateOk == null ? null : vr.dateOk ? 1 : 0, vr.destOk == null ? null : vr.destOk ? 1 : 0, photoId]
        )
      )
      .catch((err) => console.error('[出工日志] 验证结果回写失败：', err.message));

    return ok(res, { id: photoId, url, verify_status: 'pending' });
  } catch (err) {
    return next(err);
  }
});

// POST /photos/:id/verify：验证失败（failed）后重新验证——重置为 pending 并异步重调 Dify
router.post('/photos/:id/verify', async (req, res, next) => {
  try {
    const photoId = Number(req.params.id);
    const [rows] = await pool.query(
      `SELECT p.id, p.url, p.verify_status,
              DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, d.name AS destination_name
       FROM worklog_photo p
       JOIN worklog_entry e ON e.id = p.entry_id
       LEFT JOIN worklog_destination d ON d.id = e.destination_id
       WHERE p.id = ?`,
      [photoId]
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
      .then((vr) =>
        pool.query(
          `UPDATE worklog_photo SET verify_status = ?, work_content = ?, shot_time = ?, weather = ?, location = ?, lng = ?, lat = ?, date_ok = ?, dest_ok = ? WHERE id = ?`,
          [vr.status, vr.workContent, vr.time, vr.weather, vr.location, vr.lng, vr.lat,
            vr.dateOk == null ? null : vr.dateOk ? 1 : 0, vr.destOk == null ? null : vr.destOk ? 1 : 0, photoId]
        )
      )
      .catch((err) => console.error('[出工日志] 验证结果回写失败：', err.message));
    return ok(res, { verify_status: 'pending' });
  } catch (err) {
    return next(err);
  }
});

// PUT /photos/:id/members：修改照片所属人名
router.put('/photos/:id/members', async (req, res, next) => {
  try {
    const photoId = Number(req.params.id);
    const [rows] = await pool.query('SELECT entry_id FROM worklog_photo WHERE id = ?', [photoId]);
    if (!rows.length) return fail(res, 404, 40400, '照片不存在');
    const { members } = req.body || {};
    const names = Array.isArray(members) ? members.filter((n) => typeof n === 'string' && n.trim()) : [];
    if (!names.length) return fail(res, 400, 40009, '请选择照片所属人名');
    const memberErr = await checkPhotoMembers(rows[0].entry_id, names, photoId);
    if (memberErr) return fail(res, 400, memberErr.code, memberErr.message);
    await pool.query('UPDATE worklog_photo SET members = ? WHERE id = ?', [JSON.stringify(names), photoId]);
    return ok(res, null);
  } catch (err) {
    return next(err);
  }
});

// DELETE /photos/:id：删除照片（同步删 COS 对象）
router.delete('/photos/:id', async (req, res, next) => {
  try {
    const photoId = Number(req.params.id);
    const [rows] = await pool.query('SELECT cos_key FROM worklog_photo WHERE id = ?', [photoId]);
    if (!rows.length) return fail(res, 404, 40400, '照片不存在');
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

// ===== 管理接口（admin）：车牌号 / 目的地 / 人员 三类字典同构维护 =====
function dictRoutes(path, table, field, label, countRefs) {
  router.get(`/admin/${path}`, requireAdmin, async (req, res, next) => {
    try {
      const [rows] = await pool.query(`SELECT id, ${field} AS name, sort, status FROM ${table} ORDER BY sort, id`);
      return ok(res, { list: rows });
    } catch (err) {
      return next(err);
    }
  });

  router.post(`/admin/${path}`, requireAdmin, async (req, res, next) => {
    try {
      const name = String((req.body && req.body.name) || '').trim();
      if (!name) return fail(res, 400, 40012, `请输入${label}名称`);
      const [maxRows] = await pool.query(`SELECT COALESCE(MAX(sort), 0) AS maxSort FROM ${table}`);
      try {
        const [r] = await pool.query(`INSERT INTO ${table} (${field}, sort) VALUES (?, ?)`, [name, maxRows[0].maxSort + 1]);
        return ok(res, { id: r.insertId });
      } catch (err) {
        if (err.code === 'ER_DUP_ENTRY') return fail(res, 409, 40900, `「${name}」已存在`);
        throw err;
      }
    } catch (err) {
      return next(err);
    }
  });

  router.put(`/admin/${path}/:id`, requireAdmin, async (req, res, next) => {
    try {
      const id = Number(req.params.id);
      const [exist] = await pool.query(`SELECT id FROM ${table} WHERE id = ?`, [id]);
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

  router.delete(`/admin/${path}/:id`, requireAdmin, async (req, res, next) => {
    try {
      const id = Number(req.params.id);
      const refs = await countRefs(id);
      if (refs > 0) return fail(res, 409, 40901, `该${label}已被 ${refs} 条日志引用，请改为停用`);
      const [r] = await pool.query(`DELETE FROM ${table} WHERE id = ?`, [id]);
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

module.exports = router;
