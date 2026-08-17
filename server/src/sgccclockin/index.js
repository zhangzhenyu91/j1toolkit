// 商旅打卡路由：出工日志扩展（后端中继商旅平台 + 同事务双写本地表）
// 全部接口需登录 + sgcc-clockin 应用权限 + 生效班组（req.team）
// 协议细节全部在 protocol.js（移植自已实测的逆向客户端，勿改口径）；设计见 design/sgcc-clockin.html
const express = require('express');
const axios = require('axios');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const teamUtil = require('../utils/team');
const config = require('../config');
const sgcc = require('./protocol');

const router = express.Router();
router.use(auth, requireApp('sgcc-clockin'));

// 班组上下文（与 worklog 同口径：超管 ?team_id= 指定，其余角色固定本班）
router.use(async (req, res, next) => {
  try {
    req.team = await teamUtil.resolveReqTeam(req);
    return next();
  } catch (err) {
    return next(err);
  }
});

const DATE_RE = /^\d{4}-\d{2}-\d{2}$/;
// 老数据口径：绑定成功后一次性回填 8 月数据；8 月之前不同步、本地已同步的删除
const BACKFILL_FROM = '2026-08-01';

function today() {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

// ---------- 账号工具 ----------

// 本人绑定行（绑定 = 本人短信登录自己的商旅账号）
async function myAccount(req) {
  const [rows] = await pool.query(
    'SELECT * FROM worklog_sgcc_account WHERE team_id = ? AND user_id = ?',
    [req.team.id, req.user.id]
  );
  return rows[0] || null;
}

// 出工成员对应的绑定行（打卡/照片以成员为口径，代打卡也用被打卡人的账号与机型）
async function accountByMember(teamId, memberId) {
  const [rows] = await pool.query(
    'SELECT * FROM worklog_sgcc_account WHERE team_id = ? AND member_id = ?',
    [teamId, memberId]
  );
  return rows[0] || null;
}

// 协议调用设备口径：一律用被打卡人绑定的机型/系统版本
function devOpt(account) {
  return { deviceType: account.device_type || 'Pixel 7', systemVersion: account.system_version || 'Android 13' };
}

// 登录态探测：dayNew 调通即有效；失效则标记 token_status=0（照片选人层据此置灰）
async function probeAuth(account) {
  let valid = false;
  try {
    const d = await sgcc.dayNew(account.token, today(), devOpt(account));
    valid = !!(d && Number(d.statusCode) === 200);
  } catch (e) { valid = false; }
  await pool.query(
    'UPDATE worklog_sgcc_account SET token_status = ?, last_check_at = NOW() WHERE id = ?',
    [valid ? 1 : 0, account.id]
  );
  return valid;
}

// 打卡/费用操作前置：取成员绑定行 + 校验登录态（失效实时探测一次兜底）
async function requireMemberAccount(req, res, memberId) {
  const account = await accountByMember(req.team.id, memberId);
  if (!account) {
    fail(res, 400, 40020, '该成员未绑定商旅账号，请其本人在「我的 → 商旅打卡」绑定');
    return null;
  }
  if (account.token_status !== 1) {
    const valid = await probeAuth(account); // 实时兜底探测，避免误标
    if (!valid) {
      fail(res, 400, 40021, '该成员商旅登录已过期，请其本人重新登录');
      return null;
    }
    account.token_status = 1;
  }
  return account;
}

// 打卡成功后用 dayNew 全量刷新该日打卡流水（开始/结束按时间排序定 seq，工时一并回写）
async function refreshClockins(account, date) {
  const d = await sgcc.dayNew(account.token, date, devOpt(account));
  const body = d && Number(d.statusCode) === 200 && d.data && d.data.body;
  if (!body) return null;
  const list = Array.isArray(body.clockInDetailList) ? body.clockInDetailList.slice() : [];
  list.sort((a, b) => String(a.clockInTime || a.createTime || '').localeCompare(String(b.clockInTime || b.createTime || '')));
  for (let i = 0; i < Math.min(list.length, 2); i += 1) {
    const it = list[i];
    await pool.query(
      `INSERT INTO worklog_clockin (team_id, member_id, clock_date, seq, detail_id, clock_time, position, longitude, latitude, work_hours, source)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
       ON DUPLICATE KEY UPDATE detail_id = VALUES(detail_id), clock_time = VALUES(clock_time),
         position = VALUES(position), longitude = VALUES(longitude), latitude = VALUES(latitude),
         work_hours = VALUES(work_hours)`,
      [account.team_id, account.member_id, date, i + 1,
        String(it.detailId ?? it.id ?? ''), it.clockInTime || null,
        String(it.position || ''), String(it.longitude ?? ''), String(it.latitude ?? ''),
        String(body.workHours ?? '')]
    );
  }
  return body;
}

// ---------- 打卡定位（腾讯逆编码完整地址串；打卡弹层预填与 /clockin 兜底共用）----------

// GET /geo?lng=&lat=：返回 {position, cityCode, cityName}；未配置 TENCENT_MAP_KEY 或失败时返回空串由前端手填
// position 口径：「中国」+ 腾讯 address 完整地址串（与商旅打卡 position 一致，如 中国山西省吕梁市汾阳市西河街道英雄北路）
router.get('/geo', async (req, res, next) => {
  try {
    const lng = Number(req.query.lng);
    const lat = Number(req.query.lat);
    if (!Number.isFinite(lng) || !Number.isFinite(lat) || Math.abs(lng) > 180 || Math.abs(lat) > 90) {
      return fail(res, 400, 40040, '经纬度参数无效');
    }
    const empty = { position: '', cityCode: '', cityName: '' };
    if (!config.worklog.tencentMapKey) return ok(res, empty);
    const resp = await axios.get('https://apis.map.qq.com/ws/geocoder/v1/', {
      params: { location: `${lat.toFixed(6)},${lng.toFixed(6)}`, key: config.worklog.tencentMapKey },
      timeout: 8000,
    });
    const r = resp.data && resp.data.status === 0 && resp.data.result;
    if (!r) return ok(res, empty);
    const ac = r.address_component || {};
    const address = String(r.address || '').trim();
    return ok(res, {
      position: address ? `中国${address}` : '',
      cityCode: String(ac.adcode || ''),
      cityName: String(ac.city || ac.district || ''),
    });
  } catch (err) { return next(err); }
});

// 打卡定位兜底：前端未带 position 时按经纬度服务端逆编码
async function reverseGeocode(lng, lat) {
  if (!config.worklog.tencentMapKey) return { position: '', cityCode: '', cityName: '' };
  try {
    const resp = await axios.get('https://apis.map.qq.com/ws/geocoder/v1/', {
      params: { location: `${Number(lat).toFixed(6)},${Number(lng).toFixed(6)}`, key: config.worklog.tencentMapKey },
      timeout: 8000,
    });
    const r = resp.data && resp.data.status === 0 && resp.data.result;
    if (!r) return { position: '', cityCode: '', cityName: '' };
    const ac = r.address_component || {};
    const address = String(r.address || '').trim();
    return {
      position: address ? `中国${address}` : '',
      cityCode: String(ac.adcode || ''),
      cityName: String(ac.city || ac.district || ''),
    };
  } catch (err) {
    console.error('[商旅打卡] 腾讯逆编码失败：', err.message);
    return { position: '', cityCode: '', cityName: '' };
  }
}

// ---------- 绑定（短信登录三步；密码登录有顶象滑块风控，不做）----------

// POST /login/captcha：取图形验证码（统一补 dataURL 前缀，小程序 <image> 可直接贴）
router.post('/login/captcha', async (req, res, next) => {
  try {
    const mobile = String((req.body && req.body.mobile) || '').trim();
    if (!/^1\d{10}$/.test(mobile)) return fail(res, 400, 40030, '手机号格式不正确');
    let image = await sgcc.loginCaptcha(mobile);
    // 商旅返回裸 base64（无 dataURL 前缀），统一补全；PNG 魔数 89504e47
    if (image && !image.startsWith('data:')) image = `data:image/png;base64,${image}`;
    return ok(res, { image });
  } catch (err) { return next(err); }
});

// POST /login/sms：发短信验证码
router.post('/login/sms', async (req, res, next) => {
  try {
    const { mobile, checkImgCode } = req.body || {};
    if (!/^1\d{10}$/.test(String(mobile || ''))) return fail(res, 400, 40030, '手机号格式不正确');
    const d = await sgcc.loginSendSms(String(mobile), String(checkImgCode || '').trim());
    if (!d || Number(d.statusCode) !== 200) {
      return fail(res, 400, 40031, (d && d.msg) || '短信发送失败，请重试');
    }
    return ok(res, null);
  } catch (err) { return next(err); }
});

// POST /login/bind：短信换 token 完成绑定（同事写入设备口径；成功后异步做 8 月回填）
router.post('/login/bind', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const { mobile, checkCode } = req.body || {};
    const deviceType = String(req.body.deviceType || '').trim().slice(0, 64);
    const systemVersion = String(req.body.systemVersion || '').trim().slice(0, 64);
    if (!/^1\d{10}$/.test(String(mobile || ''))) return fail(res, 400, 40030, '手机号格式不正确');
    if (!checkCode) return fail(res, 400, 40032, '请填写短信验证码');

    const { token } = await sgcc.loginBySms(String(mobile), String(checkCode).trim());

    // 认领出工成员：班组内按昵称匹配（与 worklog member-sync 同口径）
    const [members] = await pool.query(
      'SELECT id FROM worklog_member WHERE team_id = ? AND name = ?',
      [req.team.id, req.user.nickname]
    );
    const memberId = members.length ? members[0].id : null;

    await pool.query(
      `INSERT INTO worklog_sgcc_account (team_id, user_id, member_id, mobile, token, device_type, system_version, token_status, last_check_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, 1, NOW())
       ON DUPLICATE KEY UPDATE mobile = VALUES(mobile), token = VALUES(token),
         device_type = IF(VALUES(device_type) = '', device_type, VALUES(device_type)),
         system_version = IF(VALUES(system_version) = '', system_version, VALUES(system_version)),
         member_id = VALUES(member_id), token_status = 1, last_check_at = NOW()`,
      [req.team.id, req.user.id, memberId, String(mobile), token,
        deviceType || 'Xiaomi 2509FPN0BC', systemVersion || 'Android 16']
    );
    const account = await myAccount(req);

    // 异步回填 8 月数据（仅首次绑定做；不阻塞响应）
    if (account && !account.backfill_done) {
      backfillAccount(account).catch((err) => console.error('[商旅打卡] 8 月回填失败：', err.message));
    }
    return ok(res, { bound: true });
  } catch (err) { return next(err); }
});

// GET /account：本人绑定状态 + 今日打卡/费用摘要（「我的 → 商旅打卡」页数据源）
router.get('/account', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const account = await myAccount(req);
    if (!account) return ok(res, { bound: false });
    const date = today();
    const [clockins] = await pool.query(
      'SELECT seq, clock_time, position, work_hours FROM worklog_clockin WHERE member_id = ? AND clock_date = ? ORDER BY seq',
      [account.member_id, date]
    );
    const [fees] = await pool.query(
      'SELECT food_fee, transit_fee, cost_center_code, cost_center_name FROM worklog_fee WHERE member_id = ? AND fee_date = ?',
      [account.member_id, date]
    );
    return ok(res, {
      bound: true,
      mobile: String(account.mobile).replace(/^(\d{3})\d{4}(\d{4})$/, '$1****$2'),
      tokenStatus: account.token_status,
      deviceType: account.device_type,
      systemVersion: account.system_version,
      lastCheckAt: account.last_check_at,
      clockins,
      fee: fees[0] || null,
    });
  } catch (err) { return next(err); }
});

// PUT /account/device：本人编辑打卡设备信息（「厂商 型号」格式）
router.put('/account/device', async (req, res, next) => {
  try {
    const account = await myAccount(req);
    if (!account) return fail(res, 400, 40020, '请先绑定商旅账号');
    const deviceType = String(req.body.deviceType || '').trim().slice(0, 64);
    const systemVersion = String(req.body.systemVersion || '').trim().slice(0, 64);
    if (!deviceType) return fail(res, 400, 40033, '设备型号不能为空');
    await pool.query(
      'UPDATE worklog_sgcc_account SET device_type = ?, system_version = ? WHERE id = ?',
      [deviceType, systemVersion, account.id]
    );
    return ok(res, null);
  } catch (err) { return next(err); }
});

// DELETE /account：解除绑定（仅删本地 token 与打卡/费用同步数据，商旅 App 侧不受影响）
router.delete('/account', async (req, res, next) => {
  try {
    const account = await myAccount(req);
    if (!account) return ok(res, null);
    await pool.query('DELETE FROM worklog_sgcc_account WHERE id = ?', [account.id]);
    if (account.member_id) {
      await pool.query('DELETE FROM worklog_clockin WHERE member_id = ?', [account.member_id]);
      await pool.query('DELETE FROM worklog_fee WHERE member_id = ?', [account.member_id]);
    }
    return ok(res, null);
  } catch (err) { return next(err); }
});

// ---------- 打卡区数据与打卡操作 ----------

// GET /day?date=YYYY-MM-DD：本班组当日全部成员的 绑定/登录态 + 两次打卡 + 费用（按 member_id 索引）
// 小程序卡片打卡区据此渲染（本地优先，只查本地表）
router.get('/day', async (req, res, next) => {
  try {
    const date = String(req.query.date || '');
    if (!DATE_RE.test(date)) return fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD');
    if (!req.team) return ok(res, { members: {} });
    const [accounts] = await pool.query(
      'SELECT member_id, token_status, device_type FROM worklog_sgcc_account WHERE team_id = ? AND member_id IS NOT NULL',
      [req.team.id]
    );
    const [clockins] = await pool.query(
      'SELECT member_id, seq, detail_id, clock_time, position, work_hours FROM worklog_clockin WHERE team_id = ? AND clock_date = ?',
      [req.team.id, date]
    );
    const [fees] = await pool.query(
      'SELECT member_id, food_fee, transit_fee, cost_center_code, cost_center_name FROM worklog_fee WHERE team_id = ? AND fee_date = ?',
      [req.team.id, date]
    );
    const members = {};
    accounts.forEach((a) => {
      members[a.member_id] = { bound: true, tokenStatus: a.token_status };
    });
    clockins.forEach((c) => {
      const m = (members[c.member_id] = members[c.member_id] || {});
      m[`seq${c.seq}`] = { detailId: c.detail_id, time: c.clock_time, position: c.position };
      if (c.work_hours) m.workHours = c.work_hours;
    });
    fees.forEach((f) => {
      const m = (members[f.member_id] = members[f.member_id] || {});
      m.fee = {
        foodFee: Number(f.food_fee), transitFee: Number(f.transit_fee),
        costCenterCode: f.cost_center_code, costCenterName: f.cost_center_name,
      };
    });
    return ok(res, { members });
  } catch (err) { return next(err); }
});

// POST /clockin：打卡（开始/结束/更新）
// {entry_id, member_id, seq:1|2, action:'mark'|'update', position, longitude, latitude, cityCode?, cityName?, remarks?}
// 备注默认「110kV及220kV输电线路巡视」（商旅 remarks，与记录卡片备注无关）；设备口径用被打卡人绑定机型
router.post('/clockin', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const { entry_id: entryId, member_id: memberId, seq, action } = req.body || {};
    const position = String(req.body.position || '').trim().slice(0, 255);
    const longitude = String(req.body.longitude ?? '').trim();
    const latitude = String(req.body.latitude ?? '').trim();
    const remarks = String(req.body.remarks || '110kV及220kV输电线路巡视').trim().slice(0, 255);
    if (!entryId || !memberId) return fail(res, 400, 40000, '参数不完整');
    if (!longitude || !latitude) return fail(res, 400, 40034, '缺少打卡位置信息');

    // 记录与成员归属校验
    const [entries] = await pool.query(
      `SELECT id, DATE_FORMAT(log_date, '%Y-%m-%d') AS log_date FROM worklog_entry WHERE id = ? AND team_id = ?`,
      [entryId, req.team.id]
    );
    if (!entries.length) return fail(res, 404, 40400, '日志不存在');
    const [mrows] = await pool.query(
      'SELECT id FROM worklog_entry_member WHERE entry_id = ? AND member_id = ?',
      [entryId, memberId]
    );
    if (!mrows.length) return fail(res, 400, 40007, '该成员不是本卡用车人');
    const date = entries[0].log_date;

    // 定位兜底：未带地址时按经纬度服务端逆编码（完整地址串 + 城市编码）
    let geo = { position, cityCode: String(req.body.cityCode || ''), cityName: String(req.body.cityName || '') };
    if (!geo.position) {
      geo = await reverseGeocode(longitude, latitude);
      if (!geo.position) return fail(res, 400, 40034, '定位逆编码失败，请重新定位或选择杆塔');
    }

    const account = await requireMemberAccount(req, res, memberId);
    if (!account) return;

    const opt = devOpt(account);
    if (action === 'update') {
      // 更新：按本地存的 detailId 调用 updateMark（仅改地点）
      const seqNum = Number(seq) === 2 ? 2 : 1;
      const [rows] = await pool.query(
        'SELECT detail_id FROM worklog_clockin WHERE member_id = ? AND clock_date = ? AND seq = ?',
        [memberId, date, seqNum]
      );
      const detailId = rows.length ? rows[0].detail_id : '';
      if (!detailId) return fail(res, 400, 40035, '未找到该次打卡记录，无法更新');
      const d = await sgcc.updateMark(account.token, {
        detailId, cityCode: geo.cityCode, cityName: geo.cityName,
        position: geo.position, longitude, latitude,
      }, opt);
      if (!d || Number(d.statusCode) !== 200) {
        return fail(res, 400, 40036, (d && d.msg) || '商旅更新打卡失败，请重试');
      }
    } else {
      const seqNum = Number(seq) === 2 ? 2 : 1;
      const [exists] = await pool.query(
        'SELECT id FROM worklog_clockin WHERE member_id = ? AND clock_date = ? AND seq = ?',
        [memberId, date, seqNum]
      );
      if (exists.length) return fail(res, 400, 40037, seqNum === 1 ? '已开始打卡' : '已结束打卡');
      const d = await sgcc.markNew(account.token, {
        clockInDate: date, cityCode: geo.cityCode, cityName: geo.cityName,
        position: geo.position, longitude, latitude, remarks,
      }, seqNum === 2, opt);
      if (!d || Number(d.statusCode) !== 200) {
        return fail(res, 400, 40036, (d && d.msg) || '商旅打卡失败，请重试');
      }
    }

    // 双写：从商旅全量刷新该日打卡流水（含工时）
    await refreshClockins(account, date);
    return ok(res, null, '打卡成功');
  } catch (err) { return next(err); }
});

// ---------- 费用 ----------

// GET /fee?member_id=&date=：费用弹层数据源（本地摘要 + 商旅实时模板原文，供成本分配等选项渲染）
router.get('/fee', async (req, res, next) => {
  try {
    const memberId = Number(req.query.member_id);
    const date = String(req.query.date || '');
    if (!memberId || !DATE_RE.test(date)) return fail(res, 400, 40000, '参数不完整或日期格式错误');
    const account = await requireMemberAccount(req, res, memberId);
    if (!account) return;

    const [fees] = await pool.query(
      'SELECT food_fee, transit_fee, cost_center_code, cost_center_name, extra FROM worklog_fee WHERE member_id = ? AND fee_date = ?',
      [memberId, date]
    );
    // 商旅实时模板（成本分配选项/上传图片组件现状都在模板里）
    const d = await sgcc.getFeeInfoNew(account.token, { clockInDate: date }, devOpt(account));
    const body = d && Number(d.statusCode) === 200 && d.data && d.data.body;
    if (!body || !body.clockTemplate) return fail(res, 400, 40038, '获取商旅费用模板失败，请重试');
    return ok(res, { local: fees[0] || null, clockTemplate: body.clockTemplate });
  } catch (err) { return next(err); }
});

// POST /fee：保存费用 {member_id, date, foodFee, transitFee, overrides?}
// overrides 为组件级透传：{ 组件id: value字符串 }（如成本分配选项整段 value），原样进 saveFeeInfoNew
router.post('/fee', async (req, res, next) => {
  try {
    const memberId = Number(req.body && req.body.member_id);
    const date = String((req.body && req.body.date) || '');
    if (!memberId || !DATE_RE.test(date)) return fail(res, 400, 40000, '参数不完整或日期格式错误');
    const account = await requireMemberAccount(req, res, memberId);
    if (!account) return;

    const d = await sgcc.getFeeInfoNew(account.token, { clockInDate: date }, devOpt(account));
    const body = d && Number(d.statusCode) === 200 && d.data && d.data.body;
    if (!body || !body.clockTemplate) return fail(res, 400, 40038, '获取商旅费用模板失败，请重试');

    // 补助明细（id=10）：伙食/交通；其余组件经 overrides 原样透传
    const foodFee = Number(req.body.foodFee) || 0;
    const transitFee = Number(req.body.transitFee) || 0;
    const overrides = { ...(req.body.overrides || {}) };
    overrides[10] = JSON.stringify({ foodFee: String(foodFee), arrive: String(transitFee) });

    const d2 = await sgcc.saveFeeInfoNew(account.token, date, body.clockTemplate, overrides, devOpt(account));
    if (!d2 || Number(d2.statusCode) !== 200) {
      return fail(res, 400, 40039, (d2 && d2.msg) || '商旅费用保存失败，请重试');
    }

    // 双写本地费用摘要（成本中心取模板现状值，由核查对账兜底）
    await pool.query(
      `INSERT INTO worklog_fee (team_id, member_id, fee_date, food_fee, transit_fee, synced_at)
       VALUES (?, ?, ?, ?, ?, NOW())
       ON DUPLICATE KEY UPDATE food_fee = VALUES(food_fee), transit_fee = VALUES(transit_fee), synced_at = NOW()`,
      [req.team.id, memberId, date, foodFee, transitFee]
    );
    return ok(res, null, '保存成功');
  } catch (err) { return next(err); }
});

// ---------- 照片同步（worklog 上传钩子调用 + 手动重试）----------

// 单张照片同步进每个所属人名的当日商旅费用照片（reimbEnclosure/add → saveFeeInfoNew 关联上传图片组件）
// 返回 { done:[memberId], skipped:[{memberId,reason}] }；任何一步失败不抛出（同步失败红标重试由 resync 兜底）
async function syncPhotoToSgcc(photoId) {
  const [rows] = await pool.query(
    `SELECT p.id, p.url, p.members, p.is_watermark, p.sgcc_img_id,
            DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, e.team_id
     FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id WHERE p.id = ?`,
    [photoId]
  );
  const photo = rows[0];
  if (!photo) return;
  const names = typeof photo.members === 'string' ? JSON.parse(photo.members) : (photo.members || []);
  const links = photo.sgcc_img_id ? JSON.parse(photo.sgcc_img_id) : {};

  // 图片原文（COS 回源）
  const resp = await fetch(photo.url, { signal: AbortSignal.timeout(60000) });
  if (!resp.ok) throw new Error(`照片回源失败（HTTP ${resp.status}）`);
  const buf = Buffer.from(await resp.arrayBuffer());
  const imgBase64Str = buf.toString('base64');

  let changed = false;
  for (const name of names) {
    const [mrows] = await pool.query(
      'SELECT id FROM worklog_member WHERE team_id = ? AND name = ?', [photo.team_id, name]
    );
    if (!mrows.length) continue;
    const memberId = mrows[0].id;
    if (links[memberId]) continue; // 该成员名下已同步过
    const account = await accountByMember(photo.team_id, memberId);
    if (!account || account.token_status !== 1) {
      await pool.query(
        `INSERT INTO worklog_sync_log (team_id, member_id, sync_date, scope, type, result, detail)
         VALUES (?, ?, ?, 'daily', 'photo', 'fail', ?)`,
        [photo.team_id, memberId, photo.log_date, `照片 ${photoId} 同步跳过：未绑定或登录已过期`]
      );
      continue;
    }
    try {
      const up = await sgcc.reimbEnclosureAdd(account.token, {
        imgBase64Str, fileName: `photo-${photoId}.jpg`, fileSize: buf.length, ext: '.jpg',
      }, devOpt(account));
      const imgId = up && up.data && (up.data.id || (up.data.body && up.data.body.id));
      const imgUrl = up && up.data && (up.data.imageUrl || (up.data.body && up.data.body.imageUrl));
      if (!imgId) throw new Error('商旅图片上传未返回 id');

      // 关联进当日费用：上传图片组件（id=5）追加该图，整树重存
      const fi = await sgcc.getFeeInfoNew(account.token, { clockInDate: photo.log_date }, devOpt(account));
      const tpl = fi && fi.data && fi.data.body && fi.data.body.clockTemplate;
      if (!tpl) throw new Error('获取费用模板失败');
      const comp = (tpl.dtComponentList || []).find((c) => c.id === 5);
      let imgs = [];
      if (comp && comp.value) { try { imgs = JSON.parse(comp.value); } catch (e) { imgs = []; } }
      if (!Array.isArray(imgs)) imgs = [];
      imgs.push({ id: imgId, url: imgUrl || '' });
      const sv = await sgcc.saveFeeInfoNew(account.token, photo.log_date, tpl, { 5: JSON.stringify(imgs) }, devOpt(account));
      if (!sv || Number(sv.statusCode) !== 200) throw new Error('费用照片关联保存失败');

      links[memberId] = imgId;
      changed = true;
    } catch (err) {
      console.error(`[商旅打卡] 照片 ${photoId} 同步到成员 ${name} 失败：`, err.message);
      await pool.query(
        `INSERT INTO worklog_sync_log (team_id, member_id, sync_date, scope, type, result, detail)
         VALUES (?, ?, ?, 'daily', 'photo', 'fail', ?)`,
        [photo.team_id, memberId, photo.log_date, `照片 ${photoId} 同步失败：${String(err.message).slice(0, 200)}`]
      );
    }
  }

  const doneCount = Object.keys(links).length;
  await pool.query(
    'UPDATE worklog_photo SET sgcc_img_id = ?, sgcc_synced = ? WHERE id = ?',
    [JSON.stringify(links), doneCount >= names.length && names.length ? 1 : 2, photoId]
  );
}

// 删除本地照片前解除商旅费用照片关联（重存费用剔除该图）；失败仅记日志，不阻塞删除
async function unlinkPhotoFromSgcc(photoId) {
  const [rows] = await pool.query(
    `SELECT p.sgcc_img_id, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, e.team_id
     FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id WHERE p.id = ?`,
    [photoId]
  );
  if (!rows.length || !rows[0].sgcc_img_id) return;
  const links = JSON.parse(rows[0].sgcc_img_id);
  for (const [memberId, imgId] of Object.entries(links)) {
    const account = await accountByMember(rows[0].team_id, Number(memberId));
    if (!account || account.token_status !== 1) continue;
    try {
      const fi = await sgcc.getFeeInfoNew(account.token, { clockInDate: rows[0].log_date }, devOpt(account));
      const tpl = fi && fi.data && fi.data.body && fi.data.body.clockTemplate;
      if (!tpl) continue;
      const comp = (tpl.dtComponentList || []).find((c) => c.id === 5);
      let imgs = [];
      if (comp && comp.value) { try { imgs = JSON.parse(comp.value); } catch (e) { imgs = []; } }
      if (!Array.isArray(imgs)) continue;
      const kept = imgs.filter((it) => String(it.id) !== String(imgId));
      if (kept.length !== imgs.length) {
        await sgcc.saveFeeInfoNew(account.token, rows[0].log_date, tpl, { 5: JSON.stringify(kept) }, devOpt(account));
      }
    } catch (err) {
      console.error(`[商旅打卡] 解除照片 ${photoId} 商旅关联失败（成员 ${memberId}）：`, err.message);
    }
  }
}

// POST /photos/:id/resync：同步失败的照片手动重试
router.post('/photos/:id/resync', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const photoId = Number(req.params.id);
    const [rows] = await pool.query(
      'SELECT p.id FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id WHERE p.id = ? AND e.team_id = ?',
      [photoId, req.team.id]
    );
    if (!rows.length) return fail(res, 404, 40400, '照片不存在');
    await pool.query('UPDATE worklog_photo SET sgcc_synced = 0 WHERE id = ?', [photoId]);
    syncPhotoToSgcc(photoId).catch((err) => console.error('[商旅打卡] 照片重试同步失败：', err.message));
    return ok(res, null, '已重新提交同步');
  } catch (err) { return next(err); }
});

// ---------- 核查（每日定时 + 手动）----------

// 单日单人对账：登录态 → 打卡 → 费用 → 照片（商旅侧新照片入库并触发验证）
async function syncOne(account, date, scope) {
  const log = (type, result, detail) => pool.query(
    `INSERT INTO worklog_sync_log (team_id, member_id, sync_date, scope, type, result, detail)
     VALUES (?, ?, ?, ?, ?, ?, ?)`,
    [account.team_id, account.member_id, date, scope, type, result, String(detail).slice(0, 500)]
  );

  // 登录态
  const valid = await probeAuth(account);
  if (!valid) {
    await log('auth', 'fail', '登录已失效，已标记置灰');
    return;
  }

  // 打卡对账：商旅 detailId 集合 vs 本地
  const body = await refreshClockins(account, date);
  if (body) {
    const remote = Array.isArray(body.clockInDetailList) ? body.clockInDetailList.length : 0;
    const [local] = await pool.query(
      'SELECT COUNT(*) AS cnt FROM worklog_clockin WHERE member_id = ? AND clock_date = ?',
      [account.member_id, date]
    );
    await log('clockin', local[0].cnt >= remote ? 'ok' : 'diff',
      `商旅 ${remote} 条 / 本地 ${local[0].cnt} 条${local[0].cnt >= remote ? '，一致' : '，已按商旅回填'}`);
  }

  // 费用对账：以商旅为准回写本地摘要
  const fi = await sgcc.getFeeInfoNew(account.token, { clockInDate: date }, devOpt(account));
  const tpl = fi && fi.data && fi.data.body && fi.data.body.clockTemplate;
  if (tpl) {
    let food = 0; let transit = 0;
    const comp10 = (tpl.dtComponentList || []).find((c) => c.id === 10);
    if (comp10 && comp10.value) {
      try { const v = JSON.parse(comp10.value); food = Number(v.foodFee) || 0; transit = Number(v.arrive) || 0; } catch (e) { /* 忽略 */ }
    }
    const [old] = await pool.query(
      'SELECT food_fee, transit_fee FROM worklog_fee WHERE member_id = ? AND fee_date = ?',
      [account.member_id, date]
    );
    const changed = !old.length || Number(old[0].food_fee) !== food || Number(old[0].transit_fee) !== transit;
    await pool.query(
      `INSERT INTO worklog_fee (team_id, member_id, fee_date, food_fee, transit_fee, synced_at)
       VALUES (?, ?, ?, ?, ?, NOW())
       ON DUPLICATE KEY UPDATE food_fee = VALUES(food_fee), transit_fee = VALUES(transit_fee), synced_at = NOW()`,
      [account.team_id, account.member_id, date, food, transit]
    );
    await log('fee', changed ? 'diff' : 'ok', `伙食 ${food} / 交通 ${transit}${changed ? '，已按商旅回写' : '，一致'}`);
  }
}

// 整班单日核查
async function syncTeamDay(teamId, date, scope) {
  const [accounts] = await pool.query(
    'SELECT * FROM worklog_sgcc_account WHERE team_id = ? AND member_id IS NOT NULL',
    [teamId]
  );
  for (const account of accounts) {
    try {
      await syncOne(account, date, scope);
    } catch (err) {
      console.error(`[商旅打卡] 核查失败（成员 ${account.member_id} ${date}）：`, err.message);
    }
  }
}

// 绑定后一次性回填：8/1 → 今天逐日对账；并清除 8 月之前的本地同步数据
async function backfillAccount(account) {
  const from = new Date(`${BACKFILL_FROM}T00:00:00`);
  const end = new Date(`${today()}T00:00:00`);
  for (let d = new Date(from); d <= end; d.setDate(d.getDate() + 1)) {
    const date = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
    try {
      await syncOne(account, date, 'backfill');
    } catch (err) {
      console.error(`[商旅打卡] 回填失败（成员 ${account.member_id} ${date}）：`, err.message);
    }
  }
  await pool.query('DELETE FROM worklog_clockin WHERE member_id = ? AND clock_date < ?', [account.member_id, BACKFILL_FROM]);
  await pool.query('DELETE FROM worklog_fee WHERE member_id = ? AND fee_date < ?', [account.member_id, BACKFILL_FROM]);
  await pool.query('UPDATE worklog_sgcc_account SET backfill_done = 1 WHERE id = ?', [account.id]);
  console.log(`[商旅打卡] 成员 ${account.member_id} 8 月回填完成`);
}

// POST /sync/check：手动同步核查 {date?}（默认今天）
router.post('/sync/check', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const date = String((req.body && req.body.date) || today());
    if (!DATE_RE.test(date)) return fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD');
    syncTeamDay(req.team.id, date, 'daily').catch((err) => console.error('[商旅打卡] 手动核查失败：', err.message));
    return ok(res, null, '已发起核查，结果请稍后在核查记录中查看');
  } catch (err) { return next(err); }
});

// GET /sync/logs?month=YYYY-MM：核查记录（近 100 条）
router.get('/sync/logs', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const month = /^\d{4}-\d{2}$/.test(String(req.query.month || '')) ? req.query.month : today().slice(0, 7);
    const [rows] = await pool.query(
      `SELECT l.id, DATE_FORMAT(l.sync_date, '%Y-%m-%d') AS sync_date, l.scope, l.type, l.result, l.detail,
              l.created_at, m.name AS member_name
       FROM worklog_sync_log l LEFT JOIN worklog_member m ON m.id = l.member_id
       WHERE l.team_id = ? AND DATE_FORMAT(l.sync_date, '%Y-%m') = ?
       ORDER BY l.id DESC LIMIT 100`,
      [req.team.id, month]
    );
    return ok(res, { list: rows });
  } catch (err) { return next(err); }
});

// ---------- 每日定时核查（SGCC_SYNC_TIME，默认 23:00）----------
function scheduleDaily() {
  const [hh, mm] = String(config.sgcc.syncTime || '23:00').split(':').map((s) => parseInt(s, 10));
  const now = new Date();
  const nextRun = new Date(now.getFullYear(), now.getMonth(), now.getDate(), hh || 23, mm || 0, 0);
  if (nextRun <= now) nextRun.setDate(nextRun.getDate() + 1);
  const timer = setTimeout(async () => {
    try {
      const [teams] = await pool.query('SELECT DISTINCT team_id FROM worklog_sgcc_account WHERE team_id IS NOT NULL');
      for (const t of teams) {
        await syncTeamDay(t.team_id, today(), 'daily');
      }
      console.log('[商旅打卡] 每日定时核查完成');
    } catch (err) {
      console.error('[商旅打卡] 每日定时核查失败：', err.message);
    }
    scheduleDaily(); // 排次日
  }, nextRun - now);
  timer.unref(); // 不阻塞进程退出（服务常驻时正常生效）
  console.log(`[商旅打卡] 每日核查已排程：${nextRun.toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' })}`);
}
scheduleDaily();

module.exports = router;
// 供 worklog 照片上传/删除钩子调用（config.sgcc.enabled 守卫在调用方）
module.exports.syncPhotoToSgcc = syncPhotoToSgcc;
module.exports.unlinkPhotoFromSgcc = unlinkPhotoFromSgcc;
