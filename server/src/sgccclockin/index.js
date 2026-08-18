// 商旅打卡路由：出工日志的扩展能力（后端中继商旅平台 + 双写本地表），归属出工日志
// 全部接口需登录 + work-log 应用权限 + 生效班组（req.team）
// 协议细节全部在 protocol.js（移植自已实测的逆向客户端，勿改口径）；设计见 design/sgcc-clockin.html
const express = require('express');
const axios = require('axios');
const crypto = require('crypto');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { pool } = require('../db');
const { ok, fail } = require('../utils/resp');
const teamUtil = require('../utils/team');
const config = require('../config');
const sgcc = require('./protocol');
const cos = require('../worklog/cos');
const dify = require('../worklog/dify');
const { checkWatermark } = require('../worklog/verify');

const router = express.Router();
router.use(auth, requireApp('work-log'));

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

function today() {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

// 日期串转点分格式（YYYY-MM-DD → YYYY.MM.DD，COS key 与 checkWatermark 的 logDate 口径，同 worklog）
function dots(dateStr) {
  return dateStr.replace(/-/g, '.');
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
    fail(res, 400, 40020, '该成员未绑定商旅账号，请其本人在「我的 → 绑定商旅」绑定');
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

// 商旅 clockInTime 口径混杂（毫秒时间戳 / YYYY-MM-DD HH:mm:ss / ISO），统一转北京时间 DATETIME 字符串
function fmtCnDateTime(v) {
  if (v === null || v === undefined || v === '') return null;
  let d = null;
  const s = String(v).trim();
  if (/^\d{13}$/.test(s)) d = new Date(Number(s)); // 毫秒时间戳
  else if (/^\d{10}$/.test(s)) d = new Date(Number(s) * 1000); // 秒时间戳
  else {
    const t = new Date(s.includes('T') ? s : s.replace(/-/g, '/'));
    if (!Number.isNaN(t.getTime())) d = t;
  }
  if (!d) return null;
  const parts = new Intl.DateTimeFormat('zh-CN', {
    timeZone: 'Asia/Shanghai', hour12: false,
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
  }).formatToParts(d).reduce((o, p) => { o[p.type] = p.value; return o; }, {});
  return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}:${parts.second}`;
}

// 打卡成功后用 dayNew 全量刷新该日打卡流水（开始/结束按时间排序定 seq，工时一并回写）
async function refreshClockins(account, date) {
  const d = await sgcc.dayNew(account.token, date, devOpt(account));
  const body = d && Number(d.statusCode) === 200 && d.data && d.data.body;
  if (!body) return null;
  const list = (Array.isArray(body.clockInDetailList) ? body.clockInDetailList.slice() : [])
    .map((it) => ({ ...it, _t: fmtCnDateTime(it.clockInTime ?? it.createTime) }))
    .sort((a, b) => String(a._t || '').localeCompare(String(b._t || '')));
  for (let i = 0; i < Math.min(list.length, 2); i += 1) {
    const it = list[i];
    await pool.query(
      `INSERT INTO worklog_clockin (team_id, member_id, clock_date, seq, detail_id, clock_time, position, longitude, latitude, work_hours, source)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
       ON DUPLICATE KEY UPDATE detail_id = VALUES(detail_id), clock_time = VALUES(clock_time),
         position = VALUES(position), longitude = VALUES(longitude), latitude = VALUES(latitude),
         work_hours = VALUES(work_hours)`,
      [account.team_id, account.member_id, date, i + 1,
        String(it.detailId ?? it.id ?? ''), it._t,
        String(it.position || ''), String(it.longitude ?? ''), String(it.latitude ?? ''),
        String(body.workHours ?? '')]
    );
  }
  return body;
}

// ---------- 打卡定位（腾讯逆编码完整地址串；打卡弹层预填与 /clockin 兜底共用）----------

// GET /geo：打卡定位解析（腾讯，复用 TENCENT_MAP_KEY；未配置或失败返回空串由前端手填/兜底）
//   ?lng=&lat=      逆编码：坐标 → {position, cityCode, cityName}（position =「中国」+ 完整地址串，与商旅打卡 position 同口径）
//   ?address=&region=  正向解析（手动输入地址用）：地址文字 → 坐标+城市；region 可传本机城市名缩小范围，
//                      返回 {position(规范地址串), cityCode, cityName, longitude, latitude}，解析失败全空
router.get('/geo', async (req, res, next) => {
  try {
    const empty = { position: '', cityCode: '', cityName: '', longitude: '', latitude: '' };
    const address = String(req.query.address || '').trim();
    if (address) {
      // 正向解析：先 address → 坐标，再走逆编码拿规范地址串与城市名（与逆编码口径一致）
      if (!config.worklog.tencentMapKey) return ok(res, empty);
      const params = { address: address.slice(0, 120), key: config.worklog.tencentMapKey };
      const region = String(req.query.region || '').trim();
      if (region) params.region = region.slice(0, 32);
      const resp = await axios.get('https://apis.map.qq.com/ws/geocoder/v1/', { params, timeout: 8000 });
      const r = resp.data && resp.data.status === 0 && resp.data.result;
      const fLng = r && r.location && Number(r.location.lng);
      const fLat = r && r.location && Number(r.location.lat);
      if (!Number.isFinite(fLng) || !Number.isFinite(fLat)) return ok(res, empty);
      const rev = await reverseGeocode(fLng, fLat);
      return ok(res, {
        position: rev.position,
        cityCode: String(r.adcode || rev.cityCode || ''),
        cityName: rev.cityName,
        longitude: fLng.toFixed(6),
        latitude: fLat.toFixed(6),
      });
    }
    const lng = Number(req.query.lng);
    const lat = Number(req.query.lat);
    if (!Number.isFinite(lng) || !Number.isFinite(lat) || Math.abs(lng) > 180 || Math.abs(lat) > 90) {
      return fail(res, 400, 40040, '经纬度参数无效');
    }
    if (!config.worklog.tencentMapKey) return ok(res, empty);
    const resp = await axios.get('https://apis.map.qq.com/ws/geocoder/v1/', {
      params: { location: `${lat.toFixed(6)},${lng.toFixed(6)}`, key: config.worklog.tencentMapKey },
      timeout: 8000,
    });
    const r = resp.data && resp.data.status === 0 && resp.data.result;
    if (!r) return ok(res, empty);
    const ac = r.address_component || {};
    const addr = String(r.address || '').trim();
    return ok(res, {
      position: addr ? `中国${addr}` : '',
      cityCode: String(ac.adcode || ''),
      cityName: String(ac.city || ac.district || ''),
      longitude: lng.toFixed(6),
      latitude: lat.toFixed(6),
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

// POST /login/bind：短信换 token 完成绑定（同事写入设备口径）
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

    // 重新登录成功：补核此前因登录态过期而核查失败的日期（近 62 天、仅当日用车人，同定时核查口径）；
    // 异步执行不阻塞响应，日期间按 SGCC_SYNC_INTERVAL_MS 间隔防风控；补核完成的日期清掉对应 auth 失败记录
    if (memberId) {
      (async () => {
        const [accRows] = await pool.query(
          'SELECT * FROM worklog_sgcc_account WHERE team_id = ? AND user_id = ?',
          [req.team.id, req.user.id]
        );
        const account = accRows[0];
        if (!account) return;
        if (!(await probeAuth(account))) return; // 新 token 探测不过（异常）则不补核，避免误清失败记录
        const [fails] = await pool.query(
          `SELECT DISTINCT DATE_FORMAT(sync_date, '%Y-%m-%d') AS d FROM worklog_sync_log
           WHERE team_id = ? AND member_id = ? AND type = 'auth' AND result = 'fail'
             AND sync_date >= DATE_SUB(CURDATE(), INTERVAL 62 DAY) ORDER BY d`,
          [account.team_id, account.member_id]
        );
        for (const { d } of fails) {
          const [m] = await pool.query(
            `SELECT 1 FROM worklog_entry e JOIN worklog_entry_member em ON em.entry_id = e.id
             WHERE e.team_id = ? AND e.log_date = ? AND em.member_id = ? LIMIT 1`,
            [account.team_id, d, account.member_id]
          );
          if (!m.length) continue; // 非当日用车人不补核
          try {
            await syncOne(account, d, 'daily');
            await pool.query(
              `DELETE FROM worklog_sync_log WHERE team_id = ? AND member_id = ? AND sync_date = ? AND type = 'auth' AND result = 'fail'`,
              [account.team_id, account.member_id, d]
            );
          } catch (e) {
            console.error(`[商旅打卡] 登录后补核失败（成员 ${account.member_id} ${d}）：`, e.message);
          }
          await new Promise((r) => setTimeout(r, config.sgcc.syncIntervalMs));
        }
      })().catch((err) => console.error('[商旅打卡] 登录后补核失败：', err.message));
    }
    return ok(res, { bound: true });
  } catch (err) { return next(err); }
});

// GET /account：本人绑定状态 + 今日打卡/费用摘要（「我的 → 绑定商旅」页数据源）
router.get('/account', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const account = await myAccount(req);
    if (!account) return ok(res, { bound: false });
    const date = today();
    const [clockins] = await pool.query(
      `SELECT seq, DATE_FORMAT(clock_time, '%Y-%m-%d %H:%i:%s') AS clock_time, position, work_hours
       FROM worklog_clockin WHERE member_id = ? AND clock_date = ? ORDER BY seq`,
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
      'SELECT member_id, token_status FROM worklog_sgcc_account WHERE team_id = ? AND member_id IS NOT NULL',
      [req.team.id]
    );
    const [clockins] = await pool.query(
      `SELECT member_id, seq, detail_id, DATE_FORMAT(clock_time, '%Y-%m-%d %H:%i:%s') AS clock_time, position, work_hours
       FROM worklog_clockin WHERE team_id = ? AND clock_date = ?`,
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
    // 打卡仅对当日开放（含更新）；费用修改不受此限
    if (date !== today()) return fail(res, 400, 40041, '仅当日日期可打卡');

    // 定位兜底：未带地址时按经纬度服务端逆编码（完整地址串 + 城市编码）
    let geo = { position, cityCode: String(req.body.cityCode || ''), cityName: String(req.body.cityName || '') };
    if (!geo.position) {
      geo = await reverseGeocode(longitude, latitude);
      if (!geo.position) return fail(res, 400, 40034, '定位逆编码失败，请重新定位或选择杆塔');
    } else if (!geo.cityCode || !geo.cityName) {
      // 带入他人打卡地址串的场景前端只带地址、不带城市信息：按经纬度补齐城市编码/城市名
      // （不覆盖已带入的地址串），否则上游报「打卡城市为空」
      const filled = await reverseGeocode(longitude, latitude);
      if (!geo.cityCode) geo.cityCode = filled.cityCode;
      if (!geo.cityName) geo.cityName = filled.cityName;
    }

    const account = await requireMemberAccount(req, res, memberId);
    if (!account) return;

    const opt = devOpt(account);
    const seqNum = Number(seq) === 2 ? 2 : 1;
    if (action === 'update') {
      // 更新：按本地存的 detailId 调用 updateMark（仅改地点）
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
        // 失败时探测登录态：已过期则置灰（token_status=0）并提示重新登录，区别于一般失败
        if (!(await probeAuth(account))) return fail(res, 400, 40021, '该成员商旅登录已过期，请其本人重新登录');
        return fail(res, 400, 40036, (d && d.msg) || '商旅更新打卡失败，请重试');
      }
    } else {
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
        // 失败时探测登录态：已过期则置灰（token_status=0）并提示重新登录，区别于一般失败
        if (!(await probeAuth(account))) return fail(res, 400, 40021, '该成员商旅登录已过期，请其本人重新登录');
        return fail(res, 400, 40036, (d && d.msg) || '商旅打卡失败，请重试');
      }
    }

    // 双写：从商旅全量刷新该日打卡流水（含工时）；
    // 随后补写城市信息（dayNew 明细不带城市编码，落库供后续打卡带入全套定位信息）
    await refreshClockins(account, date);
    await pool.query(
      'UPDATE worklog_clockin SET city_code = ?, city_name = ? WHERE member_id = ? AND clock_date = ? AND seq = ?',
      [geo.cityCode, geo.cityName, memberId, date, seqNum]
    );
    return ok(res, null, '打卡成功');
  } catch (err) { return next(err); }
});

// ---------- 费用 ----------

// 费用模板请求参数补全：城市/位置取本成员当日首条打卡（worklog_clockin 本地口径），
// 与逆向联调请求形状一致——城市参数缺失时模板「成本分配」可能不带默认成本中心，导致保存后成本分配为空
async function feeTplParams(memberId, date) {
  const [rows] = await pool.query(
    'SELECT position, city_code, city_name FROM worklog_clockin WHERE member_id = ? AND clock_date = ? ORDER BY seq LIMIT 1',
    [memberId, date]
  );
  const r = rows[0] || {};
  return { clockInDate: date, cityName: r.city_name || '', cityCode: r.city_code || '', position: r.position || '' };
}

// 模板「成本分配」（id=4）兜底：商旅默认带出则沿用并回报当前值；空值时用本成员最近一次本地成本中心回填。
// 注意 value 与 data 两字段不对称（value.value='1' 选项码 / data.value='成本中心' 选项文案），勿用同一串覆盖；
// 返回 { code, name }（当前生效值，供本地摘要回写）或 null（无来源可填）
async function ensureCostCenter(tpl, account) {
  const comp = (tpl.dtComponentList || []).find((c) => c.id === 4);
  if (!comp) return null;
  let v = {};
  try { v = comp.value ? JSON.parse(comp.value) : {}; } catch (e) { v = {}; }
  if (v && v.costCenterCode) return { code: String(v.costCenterCode), name: String(v.costCenterName || '') };
  const [rows] = await pool.query(
    `SELECT cost_center_code, cost_center_name FROM worklog_fee
     WHERE member_id = ? AND cost_center_code <> '' ORDER BY fee_date DESC LIMIT 1`,
    [account.member_id]
  );
  if (!rows.length) return null;
  const code = String(rows[0].cost_center_code);
  const name = String(rows[0].cost_center_name || '');
  comp.value = JSON.stringify({ costCenterName: name, value: '1', projectType: '', projectTypeName: '', costCenterCode: code });
  comp.data = JSON.stringify({ costCenterName: name, value: '成本中心', projectType: '', projectTypeName: '', costCenterCode: code });
  return { code, name };
}

// 费用模板组件提取成本分配（id=4）当前值；无则空串对
function extractCostCenter(tpl) {
  const comp = (tpl.dtComponentList || []).find((c) => c.id === 4);
  if (!comp || !comp.value) return { code: '', name: '' };
  try {
    const v = JSON.parse(comp.value);
    return { code: String(v.costCenterCode || ''), name: String(v.costCenterName || '') };
  } catch (e) {
    return { code: '', name: '' };
  }
}

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
    // 商旅实时模板（成本分配选项/上传图片组件现状都在模板里；城市参数补全，缺省模板可能不带默认成本中心）
    const d = await sgcc.getFeeInfoNew(account.token, await feeTplParams(memberId, date), devOpt(account));
    const body = d && Number(d.statusCode) === 200 && d.data && d.data.body;
    if (!body || !body.clockTemplate) return fail(res, 400, 40038, '获取商旅费用模板失败，请重试');
    // 成本分配（id=4）解析供弹层展示：选项文案按 optionsJsonObject 的 value 码反查；模板无值时回退本地摘要
    let costAlloc = null;
    const comp4 = (body.clockTemplate.dtComponentList || []).find((c) => c.id === 4);
    if (comp4 && comp4.value) {
      try {
        const v = JSON.parse(comp4.value);
        if (v && (v.costCenterCode || v.value)) {
          const opts = Array.isArray(comp4.optionsJsonObject) ? comp4.optionsJsonObject : [];
          const hit = opts.find((o) => String(o.value) === String(v.value));
          costAlloc = {
            label: hit ? String(hit.label || '') : '',
            costCenterName: String(v.costCenterName || ''),
            costCenterCode: String(v.costCenterCode || ''),
          };
        }
      } catch (e) { /* 模板值异常按无值处理 */ }
    }
    if (!costAlloc && fees[0] && fees[0].cost_center_code) {
      costAlloc = { label: '成本中心', costCenterName: fees[0].cost_center_name || '', costCenterCode: fees[0].cost_center_code };
    }
    return ok(res, { local: fees[0] || null, clockTemplate: body.clockTemplate, costAlloc });
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

    const d = await sgcc.getFeeInfoNew(account.token, await feeTplParams(memberId, date), devOpt(account));
    const body = d && Number(d.statusCode) === 200 && d.data && d.data.body;
    if (!body || !body.clockTemplate) return fail(res, 400, 40038, '获取商旅费用模板失败，请重试');

    // 成本分配（id=4）兜底：模板未带出时用本成员最近成本中心回填（勿用 overrides 同串覆盖，value/data 不对称）
    const cc = await ensureCostCenter(body.clockTemplate, account);

    // 补助明细（id=10）：伙食/交通；其余组件经 overrides 原样透传
    const foodFee = Number(req.body.foodFee) || 0;
    const transitFee = Number(req.body.transitFee) || 0;
    const overrides = { ...(req.body.overrides || {}) };
    overrides[10] = JSON.stringify({ foodFee: String(foodFee), arrive: String(transitFee) });

    const d2 = await sgcc.saveFeeInfoNew(account.token, date, body.clockTemplate, overrides, devOpt(account));
    if (!d2 || Number(d2.statusCode) !== 200) {
      return fail(res, 400, 40039, (d2 && d2.msg) || '商旅费用保存失败，请重试');
    }

    // 双写本地费用摘要（成本中心随本次生效值一并回写；未取到则保留旧值）
    await pool.query(
      `INSERT INTO worklog_fee (team_id, member_id, fee_date, food_fee, transit_fee, cost_center_code, cost_center_name, synced_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, NOW())
       ON DUPLICATE KEY UPDATE food_fee = VALUES(food_fee), transit_fee = VALUES(transit_fee),
         cost_center_code = IF(VALUES(cost_center_code) <> '', VALUES(cost_center_code), cost_center_code),
         cost_center_name = IF(VALUES(cost_center_name) <> '', VALUES(cost_center_name), cost_center_name),
         synced_at = NOW()`,
      [req.team.id, memberId, date, foodFee, transitFee, cc ? cc.code : '', cc ? cc.name : '']
    );
    return ok(res, null, '保存成功');
  } catch (err) { return next(err); }
});

// ---------- 照片同步（worklog 上传钩子调用 + 手动重试）----------

// 单张照片同步进每个所属人名的当日商旅费用照片（reimbEnclosure/add → saveFeeInfoNew 关联上传图片组件）
// 返回 { done:[memberId], skipped:[{memberId,reason}] }；任何一步失败不抛出（同步失败红标重试由 resync 兜底）
async function syncPhotoToSgcc(photoId) {
  const [rows] = await pool.query(
    `SELECT p.id, p.url, p.members, p.sgcc_img_id,
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

      // 关联进当日费用：上传图片组件（id=5）追加该图，整树重存；成本分配兜底同费用保存口径
      const fi = await sgcc.getFeeInfoNew(account.token, await feeTplParams(memberId, photo.log_date), devOpt(account));
      const tpl = fi && fi.data && fi.data.body && fi.data.body.clockTemplate;
      if (!tpl) throw new Error('获取费用模板失败');
      await ensureCostCenter(tpl, account);
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

// 从某成员当日商旅费用照片组件移除指定图片（同步；成本分配兜底同费用保存口径）
// 返回 { ok: true }（含商旅侧本就没有该图）或 { ok: false, error }
async function removeFeeImageRemote(account, date, imgId) {
  const fi = await sgcc.getFeeInfoNew(account.token, await feeTplParams(account.member_id, date), devOpt(account));
  const tpl = fi && fi.data && fi.data.body && fi.data.body.clockTemplate;
  if (!tpl) return { ok: false, error: '获取费用模板失败' };
  await ensureCostCenter(tpl, account);
  const comp = (tpl.dtComponentList || []).find((c) => c.id === 5);
  let imgs = [];
  if (comp && comp.value) { try { imgs = JSON.parse(comp.value); } catch (e) { imgs = []; } }
  if (!Array.isArray(imgs)) imgs = [];
  const kept = imgs.filter((it) => String(it.id) !== String(imgId));
  if (kept.length === imgs.length) return { ok: true }; // 商旅侧本就没有该图，视为已删除
  const sv = await sgcc.saveFeeInfoNew(account.token, date, tpl, { 5: JSON.stringify(kept) }, devOpt(account));
  if (!sv || Number(sv.statusCode) !== 200) return { ok: false, error: (sv && sv.msg) || '商旅费用保存失败' };
  return { ok: true };
}

// 照片人名剔除（同步远端先行，改人名接口在更新本地人名前调用）：逐个从被剔除成员的商旅费用照片移除该图。
// 一切以商旅平台为准：任一成员远端删除失败即中止并返回失败（本地人名不变更；未删链接全部保留，防核查回拉人名复活；
// 已成功成员的链接即时断开，重试编辑时由补传链路恢复其图片）；未绑定/登录过期视为失败（其商旅侧图片确实存在且无法操作）
async function removePhotoMembersRemote(photoId, removedNames) {
  const names = Array.isArray(removedNames) ? removedNames : [];
  if (!names.length) return { ok: true };
  const [rows] = await pool.query(
    `SELECT p.sgcc_img_id, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, e.team_id
     FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id WHERE p.id = ?`,
    [photoId]
  );
  if (!rows.length || !rows[0].sgcc_img_id) return { ok: true };
  let links = {};
  try { links = JSON.parse(rows[0].sgcc_img_id); } catch (e) { links = {}; }
  const logDate = rows[0].log_date;
  const teamId = rows[0].team_id;
  for (const name of names) {
    const [mrows] = await pool.query('SELECT id FROM worklog_member WHERE team_id = ? AND name = ?', [teamId, name]);
    if (!mrows.length) continue;
    const memberId = mrows[0].id;
    const imgId = links[memberId];
    if (!imgId) continue; // 该成员名下本就没同步成功过，无需远端删除
    const account = await accountByMember(teamId, memberId);
    let err = '';
    if (!account || account.token_status !== 1) {
      err = account && account.token_status === 0 ? '商旅登录已过期' : '未绑定商旅账号';
    } else {
      try {
        const r = await removeFeeImageRemote(account, logDate, imgId);
        if (!r.ok) err = r.error || '商旅侧删除失败';
      } catch (e) {
        err = e && e.message ? e.message : String(e);
      }
    }
    if (err) {
      await pool.query(
        `INSERT INTO worklog_sync_log (team_id, member_id, sync_date, scope, type, result, detail)
         VALUES (?, ?, ?, 'daily', 'photo', 'fail', ?)`,
        [teamId, memberId, logDate,
          `照片 ${photoId} 剔除人名「${name}」：商旅侧图片删除失败（${String(err).slice(0, 120)}），本地未变更，请重试`]
      );
      return { ok: false, failedName: name, error: err };
    }
    // 成功：即时断开链接并落库（后续成员中止也不回退；重试编辑时 syncPhotoToSgcc 会为缺链接成员补传恢复）
    delete links[memberId];
    await pool.query('UPDATE worklog_photo SET sgcc_img_id = ? WHERE id = ?', [JSON.stringify(links), photoId]);
  }
  return { ok: true };
}

// 删除本地照片前解除全部所属人名的商旅费用照片关联（同步远端先行，删除接口在删本地前调用）。
// 一切以商旅平台为准：任一成员解除失败即中止并返回失败（本地照片不删除；已成功成员的链接即时断开，重试仅处理剩余链接）
async function unlinkPhotoFromSgcc(photoId) {
  const [rows] = await pool.query(
    `SELECT p.sgcc_img_id, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, e.team_id
     FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id WHERE p.id = ?`,
    [photoId]
  );
  if (!rows.length || !rows[0].sgcc_img_id) return { ok: true };
  let links = {};
  try { links = JSON.parse(rows[0].sgcc_img_id); } catch (e) { links = {}; }
  const logDate = rows[0].log_date;
  const teamId = rows[0].team_id;
  for (const memberIdStr of Object.keys(links)) {
    const imgId = links[memberIdStr];
    const memberId = Number(memberIdStr);
    const account = await accountByMember(teamId, memberId);
    const [mrows] = await pool.query('SELECT name FROM worklog_member WHERE id = ?', [memberId]);
    const name = mrows.length ? mrows[0].name : String(memberId);
    let err = '';
    if (!account || account.token_status !== 1) {
      err = account && account.token_status === 0 ? '商旅登录已过期' : '未绑定商旅账号';
    } else {
      try {
        const r = await removeFeeImageRemote(account, logDate, imgId);
        if (!r.ok) err = r.error || '商旅侧删除失败';
      } catch (e) {
        err = e && e.message ? e.message : String(e);
      }
    }
    if (err) {
      await pool.query(
        `INSERT INTO worklog_sync_log (team_id, member_id, sync_date, scope, type, result, detail)
         VALUES (?, ?, ?, 'daily', 'photo', 'fail', ?)`,
        [teamId, memberId, logDate,
          `照片 ${photoId} 删除：解除成员「${name}」商旅费用照片失败（${String(err).slice(0, 120)}），本地未删除，请重试`]
      );
      return { ok: false, failedName: name, error: err };
    }
    delete links[memberIdStr];
    await pool.query('UPDATE worklog_photo SET sgcc_img_id = ? WHERE id = ?', [JSON.stringify(links), photoId]);
  }
  return { ok: true };
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

// ---------- 核查（每晚定时 + 手动拉取，一律以商旅为准覆盖本地）----------

// Dify 识别结果回写（worklog 的 writeBackVerify 未导出，在此内联等价实现）：
// 写库时重查记录日期与派车目的地，日期/地点核验由 checkWatermark 完成（logDate 用 dots 点分格式）
async function writeBackPullVerify(photoId, vr) {
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

// 费用照片双向对账（以商旅为准）：数据源为 getFeeInfoNew 模板 id=5「上传图片」组件的 value
// （JSON 数组，元素含 id/url，结构已实测，见 esgcc/sgcc/tools/fee_probe3.js 联调口径）；比对键 = 商旅图片 id
// 规则：商旅有本地无 → 下载存 COS 入库（members=[成员名]、source=1、is_watermark=1、
//       sgcc_img_id={memberId:图片id}、verify_status='pending'，异步 Dify 验证回写沿用 writeBackPullVerify）；
//       本地 source=1（商旅同步来的）有而商旅无 → 删除（COS 对象 + worklog_photo 行）；
//       本地 source=0（壹匣上传）商旅无 → 不删不动（可能正在同步途中），记「待商旅侧确认」
async function syncFeePhotos(account, date, remoteImgs, log) {
  // 商旅侧集合：商旅图片 id → url（只采信带非空 id 的元素）
  const remote = new Map();
  for (const it of remoteImgs) {
    if (it && it.id !== undefined && it.id !== null && String(it.id) !== '') {
      remote.set(String(it.id), typeof it.url === 'string' ? it.url.trim() : '');
    }
  }

  // 本地侧：本班组当日照片中与本成员相关的行（sgcc_img_id JSON map 含本成员 key，值即该成员商旅图片 id）；
  // source=1 行参与删除对账，source=0（壹匣上传）行只比对不删除
  const [photos] = await pool.query(
    `SELECT p.id, p.cos_key, p.source, p.sgcc_img_id
     FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
     WHERE e.team_id = ? AND e.log_date = ?`,
    [account.team_id, date]
  );
  const memberKey = String(account.member_id);
  const known = new Set(); // 本地已知的本成员商旅图片 id
  const localPulled = []; // source=1 且含本成员商旅图片 id 的本地照片
  const localUploaded = []; // source=0（壹匣上传）且已推送过商旅（含本成员图片 id）的本地照片
  for (const p of photos) {
    let map = {};
    if (p.sgcc_img_id) {
      try { map = typeof p.sgcc_img_id === 'string' ? JSON.parse(p.sgcc_img_id) : p.sgcc_img_id; } catch (e) { map = {}; }
    }
    if (!map || typeof map !== 'object') map = {};
    const imgId = map[memberKey] ? String(map[memberKey]) : '';
    if (!imgId) continue; // 与本成员无关（该成员名下无商旅图片 id）
    known.add(imgId);
    if (p.source === 1) localPulled.push({ id: p.id, cosKey: p.cos_key, imgId });
    else localUploaded.push({ id: p.id, imgId });
  }

  // 方向一：商旅有本地无 → 下载入库（挂到该成员当日首个出工记录下）；
  // 相同照片（内容 MD5 一致，如同一张图被传到多人费用）合并为一张：所属人名追加本成员，不再新建
  const toPull = [...remote.entries()].filter(([imgId]) => !known.has(imgId));
  let pulled = 0;
  let merged = 0;
  if (toPull.length) {
    // 成员名与绑定人 username（照片 members 口径 / Dify user 入参）
    const [mrows] = await pool.query('SELECT name FROM worklog_member WHERE id = ?', [account.member_id]);
    const memberName = mrows.length ? mrows[0].name : '';
    const [urows] = await pool.query('SELECT username FROM sys_user WHERE id = ?', [account.user_id]);
    const username = urows.length ? urows[0].username : '';
    // 该成员当日所在首个出工记录（照片须挂在记录下；找不到则记核查日志跳过）
    const [erows] = await pool.query(
      `SELECT e.id, d.name AS destination_name
       FROM worklog_entry e
       JOIN worklog_entry_member em ON em.entry_id = e.id
       LEFT JOIN worklog_destination d ON d.id = e.destination_id
       WHERE e.team_id = ? AND em.member_id = ? AND e.log_date = ?
       ORDER BY e.id LIMIT 1`,
      [account.team_id, account.member_id, date]
    );
    if (!erows.length) {
      await log('photo', 'fail', `费用照片：商旅侧 ${toPull.length} 张本地无，但成员当日无出工记录，已跳过入库`);
    } else if (!memberName) {
      await log('photo', 'fail', `费用照片：成员 ${account.member_id} 不存在，已跳过入库`);
    } else {
      const entry = erows[0];
      // COS key 规则同 worklog 照片：{prefix}{班组名}/{YYYY.MM.DD}/{entryId}-{ts}-{图片id}.jpg（带图片 id 防同毫秒撞键）
      const [trows] = await pool.query('SELECT name FROM sys_team WHERE id = ?', [account.team_id]);
      const teamName = trows.length ? trows[0].name : String(account.team_id);
      const prefix = config.worklog.cosPrefix.endsWith('/') ? config.worklog.cosPrefix : `${config.worklog.cosPrefix}/`;
      for (const [imgId, imgUrl] of toPull) {
        try {
          if (!/^https?:\/\//.test(imgUrl)) throw new Error('商旅侧未返回有效图片地址');
          const resp = await fetch(imgUrl, { signal: AbortSignal.timeout(60000) });
          if (!resp.ok) throw new Error(`照片下载失败（HTTP ${resp.status}）`);
          const buf = Buffer.from(await resp.arrayBuffer());
          const imgMd5 = crypto.createHash('md5').update(buf).digest('hex');
          // 相同照片内容合并（一图多人标注）：本班组当日已有同 MD5 照片 → 人名/链接并入，不再新建
          const [dup] = await pool.query(
            `SELECT p.id, p.members, p.sgcc_img_id FROM worklog_photo p
             JOIN worklog_entry e ON e.id = p.entry_id
             WHERE e.team_id = ? AND e.log_date = ? AND p.md5 = ? LIMIT 1`,
            [account.team_id, date, imgMd5]
          );
          if (dup.length) {
            const d = dup[0];
            const oldNames = typeof d.members === 'string' ? JSON.parse(d.members) : (d.members || []);
            let oldLinks = {};
            if (d.sgcc_img_id) { try { oldLinks = typeof d.sgcc_img_id === 'string' ? JSON.parse(d.sgcc_img_id) : d.sgcc_img_id; } catch (e) { oldLinks = {}; } }
            const newNames = oldNames.includes(memberName) ? oldNames : [...oldNames, memberName];
            oldLinks[memberKey] = imgId;
            await pool.query(
              'UPDATE worklog_photo SET members = ?, sgcc_img_id = ? WHERE id = ?',
              [JSON.stringify(newNames), JSON.stringify(oldLinks), d.id]
            );
            merged += 1;
            continue;
          }
          const key = `${prefix}${teamName}/${dots(date)}/${entry.id}-${Date.now()}-${imgId}.jpg`;
          await cos.putBuffer(key, buf, 'image/jpeg');
          const localUrl = cos.publicUrl(key);
          const [r] = await pool.query(
            `INSERT INTO worklog_photo (entry_id, cos_key, url, members, is_watermark, source, sgcc_img_id, verify_status, md5)
             VALUES (?, ?, ?, ?, 1, 1, ?, 'pending', ?)`,
            [entry.id, key, localUrl, JSON.stringify([memberName]), JSON.stringify({ [memberKey]: imgId }), imgMd5]
          );
          // 异步 Dify 验证并回写（不阻塞对账；写法同 worklog 上传照片）
          dify
            .verifyPhoto({ username, date: dots(date), destination: entry.destination_name || '', url: localUrl })
            .then((vr) => writeBackPullVerify(r.insertId, vr))
            .catch((err) => console.error('[商旅打卡] 补拉照片验证回写失败：', err.message));
          pulled += 1;
        } catch (err) {
          // 单张失败记核查日志继续，不抛出
          console.error(`[商旅打卡] 费用照片入库失败（成员 ${memberName} ${date} 图片 ${imgId}）：`, err.message);
          await log('photo', 'fail', `费用照片：商旅图片 ${imgId} 入库失败：${String(err.message).slice(0, 200)}`);
        }
      }
    }
  }

  // 方向二：本地 source=1 有而商旅无 → 删除（COS 对象 + worklog_photo 行）
  let deleted = 0;
  for (const p of localPulled.filter((x) => !remote.has(x.imgId))) {
    try {
      if (p.cosKey) await cos.deleteObject(p.cosKey);
      await pool.query('DELETE FROM worklog_photo WHERE id = ?', [p.id]);
      deleted += 1;
    } catch (err) {
      console.error(`[商旅打卡] 费用照片删除失败（照片 ${p.id}）：`, err.message);
      await log('photo', 'fail', `费用照片：本地照片 ${p.id}（商旅图片 ${p.imgId}）删除失败：${String(err.message).slice(0, 200)}`);
    }
  }

  // 本地 source=0（壹匣上传）商旅无 → 不删不动（照片可能正在同步途中，由上传钩子/手动重试兜底）
  const pendingConfirm = localUploaded.filter((x) => !remote.has(x.imgId)).length;
  if (pendingConfirm) {
    await log('photo', 'ok', `费用照片：本地壹匣上传 ${pendingConfirm} 张商旅侧暂未见到，不删不动，待商旅侧确认`);
  }

  await log('photo', pulled || deleted || merged ? 'diff' : 'ok', `费用照片：拉下 ${pulled} 张 / 合并 ${merged} 张 / 删除 ${deleted} 张`);
}

// 单日单人对账：登录态 → 打卡 → 费用 → 费用照片（一律以商旅为准覆盖本地；照片双向对账：拉新入库并触发验证，商旅侧已删的同步照片本地同步删除）
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

  // 打卡对账：先全量刷新，再删除本地 seq 不在商旅返回集合内的行（商旅 0 条则全删，一律以商旅为准）
  const [before] = await pool.query(
    'SELECT COUNT(*) AS cnt FROM worklog_clockin WHERE member_id = ? AND clock_date = ?',
    [account.member_id, date]
  );
  const body = await refreshClockins(account, date);
  if (body) {
    const remote = Array.isArray(body.clockInDetailList) ? body.clockInDetailList.length : 0;
    const keep = Math.min(remote, 2); // 本地仅落 seq 1/2（开始/结束）
    if (keep === 0) {
      await pool.query('DELETE FROM worklog_clockin WHERE member_id = ? AND clock_date = ?', [account.member_id, date]);
    } else {
      await pool.query(
        'DELETE FROM worklog_clockin WHERE member_id = ? AND clock_date = ? AND seq > ?',
        [account.member_id, date, keep]
      );
    }
    await log('clockin', before[0].cnt === keep ? 'ok' : 'diff',
      `打卡：商旅 ${remote} 条覆盖本地（本地原 ${before[0].cnt} 条）`);
  }

  // 费用对账：以商旅为准回写本地摘要（含成本分配；城市参数补全取模板，缺省模板可能不带默认成本中心）
  const fi = await sgcc.getFeeInfoNew(account.token, await feeTplParams(account.member_id, date), devOpt(account));
  const tpl = fi && fi.data && fi.data.body && fi.data.body.clockTemplate;
  if (tpl) {
    let food = 0; let transit = 0;
    const comp10 = (tpl.dtComponentList || []).find((c) => c.id === 10);
    if (comp10 && comp10.value) {
      try { const v = JSON.parse(comp10.value); food = Number(v.foodFee) || 0; transit = Number(v.arrive) || 0; } catch (e) { /* 忽略 */ }
    }
    const cc = extractCostCenter(tpl);
    const [old] = await pool.query(
      'SELECT food_fee, transit_fee, cost_center_code FROM worklog_fee WHERE member_id = ? AND fee_date = ?',
      [account.member_id, date]
    );
    const changed = !old.length || Number(old[0].food_fee) !== food || Number(old[0].transit_fee) !== transit
      || String(old[0].cost_center_code || '') !== cc.code;
    await pool.query(
      `INSERT INTO worklog_fee (team_id, member_id, fee_date, food_fee, transit_fee, cost_center_code, cost_center_name, synced_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, NOW())
       ON DUPLICATE KEY UPDATE food_fee = VALUES(food_fee), transit_fee = VALUES(transit_fee),
         cost_center_code = VALUES(cost_center_code), cost_center_name = VALUES(cost_center_name), synced_at = NOW()`,
      [account.team_id, account.member_id, date, food, transit, cc.code, cc.name]
    );
    await log('fee', changed ? 'diff' : 'ok',
      `费用：伙食 ${food} / 交通 ${transit} / 成本中心 ${cc.code || '无'}${changed ? '，已按商旅覆盖' : '，一致'}`);
  }

  // 费用照片双向对账（以商旅为准）：数据源为费用模板 id=5「上传图片」组件 value（复用费用对账已取的 tpl）
  if (tpl) {
    const comp5 = (tpl.dtComponentList || []).find((c) => c.id === 5);
    let imgs = [];
    if (comp5 && comp5.value) {
      try { const v = JSON.parse(comp5.value); if (Array.isArray(v)) imgs = v; } catch (e) { /* 忽略脏数据 */ }
    }
    await syncFeePhotos(account, date, imgs, log);
  }
}

// 整班单日核查（仅当日用车人：非用车人数据界面不展示，跳过可省一轮商旅 API，
// 也避免照片对账记「成员当日无出工记录」噪音日志）
async function syncTeamDay(teamId, date, scope) {
  const [accounts] = await pool.query(
    `SELECT DISTINCT a.* FROM worklog_sgcc_account a
     JOIN worklog_entry e ON e.team_id = a.team_id AND e.log_date = ?
     JOIN worklog_entry_member em ON em.entry_id = e.id AND em.member_id = a.member_id
     WHERE a.team_id = ? AND a.member_id IS NOT NULL`,
    [date, teamId]
  );
  for (const account of accounts) {
    try {
      await syncOne(account, date, scope);
    } catch (err) {
      console.error(`[商旅打卡] 核查失败（成员 ${account.member_id} ${date}）：`, err.message);
    }
    // 成员间间隔，防商旅侧风控（SGCC_SYNC_INTERVAL_MS，默认 1500ms）
    await new Promise((r) => setTimeout(r, config.sgcc.syncIntervalMs));
  }
}

// POST /sync/pull：手动从商旅拉取 {date: 'YYYY-MM-DD'} 或 {from: 'YYYY-MM-DD', to: 'YYYY-MM-DD'}（日期区段，最多跨 62 天），team_id?
// 所有 work-log 权限用户可用：触发本班组指定日期（区段）内当日用车人中的绑定成员从商旅拉取（一律以商旅为准覆盖本地）；
// team_id 仅超管生效（沿用 resolveReqTeam 口径：班组管理员/普通用户传了也被收敛到本班）；
// 区段逐日串行异步执行，结果见 GET /sync/logs
router.post('/sync/pull', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const from = String((req.body && (req.body.from || req.body.date)) || today());
    const to = String((req.body && req.body.to) || from);
    if (!DATE_RE.test(from) || !DATE_RE.test(to)) return fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD');
    if (from > to) return fail(res, 400, 40000, '日期区段不正确');
    // 逐日展开（按 UTC 毫秒步进，避免本地时区影响）；最多跨 62 天
    const DAY_MS = 24 * 60 * 60 * 1000;
    const fromMs = Date.parse(`${from}T00:00:00Z`);
    const days = Math.round((Date.parse(`${to}T00:00:00Z`) - fromMs) / DAY_MS) + 1;
    if (days > 62) return fail(res, 400, 40000, '日期区段最多跨 62 天');
    (async () => {
      for (let i = 0; i < days; i += 1) {
        const d = new Date(fromMs + i * DAY_MS).toISOString().slice(0, 10);
        await syncTeamDay(req.team.id, d, 'daily');
      }
    })().catch((err) => console.error('[商旅打卡] 手动拉取失败：', err.message));
    // 响应文案口径：M月D日（不补零）
    const md = (s) => `${Number(s.slice(5, 7))}月${Number(s.slice(8, 10))}日`;
    const msg = from === to ? `已发起从商旅拉取（${md(from)}）` : `已发起从商旅拉取（${md(from)} 至 ${md(to)}）`;
    return ok(res, null, msg);
  } catch (err) { return next(err); }
});

// GET /sync/logs?month=YYYY-MM：核查记录（近 100 条）
router.get('/sync/logs', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const month = /^\d{4}-\d{2}$/.test(String(req.query.month || '')) ? req.query.month : today().slice(0, 7);
    const [rows] = await pool.query(
      `SELECT l.id, DATE_FORMAT(l.sync_date, '%Y-%m-%d') AS sync_date, l.scope, l.type, l.result, l.detail,
              DATE_FORMAT(l.created_at, '%Y-%m-%d %H:%i') AS created_at, m.name AS member_name
       FROM worklog_sync_log l LEFT JOIN worklog_member m ON m.id = l.member_id
       WHERE l.team_id = ? AND DATE_FORMAT(l.sync_date, '%Y-%m') = ?
       ORDER BY l.id DESC LIMIT 100`,
      [req.team.id, month]
    );
    return ok(res, { list: rows });
  } catch (err) { return next(err); }
});

// ---------- 每日定时核查（SGCC_SYNC_TIME，默认 23:00，按北京时间排程）----------
// 时区口径：容器本地时区不固定（云端 Docker 通常为 UTC），若按本地时间排程，
// SGCC_SYNC_TIME=23:00 会被排到北京时间次日 07:00；中国无夏令时，统一按 UTC+8 固定偏移换算：
// 「当前 UTC 时间戳 + 8h」取北京年月日，再以 Date.UTC(北京年月日, 配置时分) − 8h 得触发的 UTC 时间戳
const CN_OFFSET_MS = 8 * 60 * 60 * 1000; // 北京时间固定偏移（UTC+8）
function nextDailyRunUtc(hh, mm) {
  const now = Date.now();
  const cn = new Date(now + CN_OFFSET_MS); // 其 UTC 年月日即北京日历日
  let nextUtc = Date.UTC(cn.getUTCFullYear(), cn.getUTCMonth(), cn.getUTCDate(), hh, mm, 0) - CN_OFFSET_MS;
  if (nextUtc <= now) { // 今日北京时点已过 → 顺延次日（Date.UTC 自动处理跨月进位）
    nextUtc = Date.UTC(cn.getUTCFullYear(), cn.getUTCMonth(), cn.getUTCDate() + 1, hh, mm, 0) - CN_OFFSET_MS;
  }
  return { nextUtc, now };
}
function scheduleDaily() {
  const [hh, mm] = String(config.sgcc.syncTime || '23:00').split(':').map((s) => parseInt(s, 10));
  const { nextUtc, now } = nextDailyRunUtc(hh || 23, mm || 0);
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
  }, nextUtc - now);
  timer.unref(); // 不阻塞进程退出（服务常驻时正常生效）
  // 日志按北京时间打印（与排程口径一致，避免容器 UTC 时误读）
  console.log(`[商旅打卡] 每日核查已排程：${new Date(nextUtc).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' })}（北京时间）`);
}
scheduleDaily();

module.exports = router;
// 供 worklog 照片上传/删除钩子调用（config.sgcc.enabled 守卫在调用方）
module.exports.syncPhotoToSgcc = syncPhotoToSgcc;
module.exports.unlinkPhotoFromSgcc = unlinkPhotoFromSgcc;
module.exports.removePhotoMembersRemote = removePhotoMembersRemote;
