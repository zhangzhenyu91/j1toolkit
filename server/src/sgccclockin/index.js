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

// 本人绑定行（绑定 = 本人短信登录自己的商旅账号；token 跟人走，user_id 全局唯一，调班无需重绑）
async function myAccount(req) {
  const [rows] = await pool.query(
    'SELECT * FROM worklog_sgcc_account WHERE user_id = ?',
    [req.user.id]
  );
  return rows[0] || null;
}

// 出工成员对应的绑定行（人级解析：先按 member_id 直连，落空经 member.user_id 兜底——
// 调班重名回退路径的旧成员行也能解析到本人账号；代打卡用被打卡人的账号与机型）
async function accountByMember(memberId) {
  const [rows] = await pool.query(
    'SELECT * FROM worklog_sgcc_account WHERE member_id = ?',
    [memberId]
  );
  if (rows.length) return rows[0];
  const [fallback] = await pool.query(
    `SELECT a.* FROM worklog_sgcc_account a
     JOIN worklog_member m ON m.id = ?
     WHERE m.user_id IS NOT NULL AND a.user_id = m.user_id`,
    [memberId]
  );
  return fallback[0] || null;
}

// 协议调用设备口径：一律用被打卡人绑定的机型/系统版本
function devOpt(account) {
  return { deviceType: account.device_type || 'Pixel 7', systemVersion: account.system_version || 'Android 13' };
}

// 登录态探测：dayNew 调通即有效；失效则标记 token_status=0（照片选人层据此置灰）。
// 由有效探测为失效时自动通知本人与超管（见 16.3）：仅 1→0 跳变通知一次，
// token_status 已是 0 的后续探测不重复通知；通知发送失败不影响业务流（fire-and-forget）
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
  if (!valid && Number(account.token_status) === 1) {
    account.token_status = 0; // 同步内存态，防同一 account 对象被连续探测时重复通知
    notifyTokenExpired(account).catch((err) => console.error('[商旅打卡] 登录过期通知发送失败：', err.message));
  }
  return valid;
}

// 登录过期通知：按人投放给本人（绑定账号）与全部超管（fire-and-forget，异常由调用方 catch）
// 微信推送：本人个人 wxid + 所属班组群（user_id 缺省时经成员绑定账号兜底解析本人）
async function notifyTokenExpired(account) {
  let name = '';
  let wxUserId = account.user_id || null;
  if (account.member_id) {
    const [m] = await pool.query('SELECT name, user_id FROM worklog_member WHERE id = ?', [account.member_id]);
    name = m.length ? m[0].name : '';
    if (!wxUserId) wxUserId = m.length ? m[0].user_id : null;
  }
  if (!name && account.user_id) {
    const [u] = await pool.query('SELECT nickname FROM sys_user WHERE id = ?', [account.user_id]);
    name = u.length ? u[0].nickname : '';
  }
  let wxTeamId = null;
  if (wxUserId) {
    const [ut] = await pool.query('SELECT team_id FROM sys_user WHERE id = ?', [wxUserId]);
    wxTeamId = ut.length ? ut[0].team_id : null;
  }
  const [admins] = await pool.query("SELECT id FROM sys_user WHERE role = 'admin' AND status = 1");
  const userIds = [...new Set([account.user_id, ...admins.map((a) => a.id)].filter(Boolean))];
  if (!userIds.length) return;
  await require('../notice').push({
    userIds,
    targets: [],
    title: '商旅登录已过期',
    content: `成员「${name || '未知'}」商旅登录已过期，打卡/费用/照片同步已暂停，请尽快在「我的 → 商旅打卡」重新登录。`,
    wxUserIds: wxUserId ? [wxUserId] : [],
    wxTeamIds: wxTeamId ? [wxTeamId] : [],
  });
}

// 打卡/费用操作前置：成员有效性校验（本班成员，或当日/指定日期本班卡片上的他班成员——跨班卡、人已调班的旧卡均可代打卡；
// 账号人级解析全局化后防跨班操作与本班无关的他班成员）+ 取绑定行 + 校验登录态（失效实时探测一次兜底）
async function requireMemberAccount(req, res, memberId, date = '') {
  if (!req.team) {
    fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    return null;
  }
  const [mrows] = await pool.query(
    `SELECT m.id FROM worklog_member m
     WHERE m.id = ? AND (m.team_id = ? OR EXISTS (
       SELECT 1 FROM worklog_entry e JOIN worklog_entry_member em ON em.entry_id = e.id
       WHERE e.team_id = ? AND em.member_id = m.id AND e.log_date = ?))`,
    [memberId, req.team.id, req.team.id, date]
  );
  if (!mrows.length) {
    fail(res, 404, 40400, '成员不存在或不属于本班组');
    return null;
  }
  const account = await accountByMember(memberId);
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

// 打卡成功后用 dayNew 全量刷新该日打卡流水（工时一并回写）。
// seq 分配规则：按 detailId 匹配本地已有记录保留原 seq（防止排序重排导致开始/结束互换）；
// 新记录（本地无该 detailId）按时间排序分配到空余 seq 位。
// 本地行键 memberId/teamId = 卡片上下文（缺省账号自身口径）：打卡/费用行以 member_id 为人级稳定键，
// team_id 仅写入时所属班快照——调班后人级读取不受 team_id 影响，代打卡落在卡片成员名下
async function refreshClockins(account, date, memberId = account.member_id, teamId = account.team_id) {
  const d = await sgcc.dayNew(account.token, date, devOpt(account));
  const body = d && Number(d.statusCode) === 200 && d.data && d.data.body;
  if (!body) return null;
  const list = (Array.isArray(body.clockInDetailList) ? body.clockInDetailList.slice() : [])
    .map((it) => ({ ...it, _t: fmtCnDateTime(it.clockInTime ?? it.createTime), _detailId: String(it.detailId ?? it.id ?? '') }));

  // 查本地已有记录，建立 detailId → seq 映射
  const [existing] = await pool.query(
    'SELECT seq, detail_id FROM worklog_clockin WHERE member_id = ? AND clock_date = ?',
    [memberId, date]
  );
  const seqByDetailId = {};
  for (const row of existing) {
    if (row.detail_id) seqByDetailId[row.detail_id] = row.seq;
  }

  // 分离：已有记录（保持原 seq）与新记录（按时间排序分配空位）
  const known = [];
  const fresh = [];
  for (const it of list) {
    if (it._detailId && seqByDetailId[it._detailId]) known.push({ ...it, _seq: seqByDetailId[it._detailId] });
    else fresh.push(it);
  }
  fresh.sort((a, b) => String(a._t || '').localeCompare(String(b._t || '')));
  const usedSeqs = new Set(known.map((it) => it._seq));
  let nextSeq = 1;
  for (const it of fresh) {
    while (usedSeqs.has(nextSeq)) nextSeq += 1;
    it._seq = nextSeq;
    usedSeqs.add(nextSeq);
  }

  for (const it of [...known, ...fresh]) {
    if (it._seq > 2) continue; // 本地仅落 seq 1/2（开始/结束）
    await pool.query(
      `INSERT INTO worklog_clockin (team_id, member_id, clock_date, seq, detail_id, clock_time, position, longitude, latitude, work_hours, source)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
       ON DUPLICATE KEY UPDATE detail_id = VALUES(detail_id), clock_time = VALUES(clock_time),
         position = VALUES(position), longitude = VALUES(longitude), latitude = VALUES(latitude),
         work_hours = VALUES(work_hours)`,
      [teamId, memberId, date, it._seq,
        it._detailId, it._t,
        String(it.position || ''), String(it.longitude ?? ''), String(it.latitude ?? ''),
        String(body.workHours ?? '')]
    );
  }
  return body;
}

// ---------- 打卡定位（高德逆编码完整地址串；打卡弹层预填与 /clockin 兜底共用）----------

// GET /geo：打卡定位解析（高德，复用 AMAP_MAP_KEY；未配置或失败返回空串由前端手填/兜底）
//   ?lng=&lat=      逆编码：坐标 → {position, cityCode, cityName}（position = 部件拼接完整地址串，与商旅打卡 position 同口径）
//   ?address=&region=  正向解析（手动输入地址用）：地址文字 → 坐标+城市；region 可传本机城市名缩小范围（高德 city 参数），
//                      返回 {position(规范地址串), cityCode, cityName, longitude, latitude}，解析失败全空
router.get('/geo', async (req, res, next) => {
  try {
    const empty = { position: '', cityCode: '', cityName: '', longitude: '', latitude: '' };
    const address = String(req.query.address || '').trim();
    if (address) {
      // 正向解析：先 address → 坐标，再走逆编码拿规范地址串与城市名（与逆编码口径一致）
      if (!config.worklog.amapMapKey) return ok(res, empty);
      const params = { address: address.slice(0, 120), key: config.worklog.amapMapKey };
      const region = String(req.query.region || '').trim();
      if (region) params.city = region.slice(0, 32);
      const resp = await axios.get(`${config.worklog.amapBaseUrl}/v3/geocode/geo`, { params, timeout: 8000 });
      const d = resp.data || {};
      const g = d.status === '1' && d.geocodes && d.geocodes[0];
      const loc = g && typeof g.location === 'string' ? g.location.split(',') : []; // 高德 location：「经度,纬度」
      const fLng = Number(loc[0]);
      const fLat = Number(loc[1]);
      if (!Number.isFinite(fLng) || !Number.isFinite(fLat)) return ok(res, empty);
      const rev = await reverseGeocode(fLng, fLat);
      return ok(res, {
        position: rev.position,
        cityCode: gstr(g.adcode) || rev.cityCode,
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
    if (!config.worklog.amapMapKey) return ok(res, empty);
    const resp = await axios.get(`${config.worklog.amapBaseUrl}/v3/geocode/regeo`, {
      params: { location: `${lng.toFixed(6)},${lat.toFixed(6)}`, key: config.worklog.amapMapKey }, // 高德：经度在前
      timeout: 8000,
    });
    const d = resp.data || {};
    const ac = d.status === '1' && d.regeocode && d.regeocode.addressComponent;
    if (!ac) return ok(res, empty);
    return ok(res, {
      position: composePosition(ac),
      cityCode: gstr(ac.adcode),
      cityName: gstr(ac.city) || gstr(ac.district),
      longitude: lng.toFixed(6),
      latitude: lat.toFixed(6),
    });
  } catch (err) { return next(err); }
});

// 高德空值字段返回空数组（[]）而非空串，统一收敛为字符串
function gstr(v) {
  return typeof v === 'string' ? v.trim() : '';
}

// 组装打卡 position：高德 addressComponent 部件拼接（与商旅打卡 position 同口径）。
// 口径：国家+省+市+区+乡镇/街道+道路（拼到道路为止，不带门牌号），如「中国山西省临汾市尧都区辛寺街道解放东路」：
// ① 乡镇/街道取 township（社区街道，非道路），插区与道路之间（与道路同名不重复）；
// ② 道路取 streetNumber.street；
// ③ 直辖市与省直辖县级区划 city 返回为空（高德口径），city===province 与 district===city 同值去重为防御口径。
function composePosition(ac) {
  const nation = gstr(ac.country);
  const province = gstr(ac.province);
  const rawCity = gstr(ac.city);
  const rawDistrict = gstr(ac.district);
  const town = gstr(ac.township);
  const street = gstr(ac.streetNumber && ac.streetNumber.street);
  const city = rawCity === province ? '' : rawCity; // 直辖市去重（防御）
  const district = rawDistrict === rawCity ? '' : rawDistrict; // 省直辖县级区划去重（防御）
  return [nation, province, city, district, town === street ? '' : town, street].filter(Boolean).join('');
}

// 打卡定位兜底：前端未带 position 时按经纬度服务端逆编码
async function reverseGeocode(lng, lat) {
  if (!config.worklog.amapMapKey) return { position: '', cityCode: '', cityName: '' };
  try {
    const resp = await axios.get(`${config.worklog.amapBaseUrl}/v3/geocode/regeo`, {
      params: { location: `${Number(lng).toFixed(6)},${Number(lat).toFixed(6)}`, key: config.worklog.amapMapKey }, // 高德：经度在前
      timeout: 8000,
    });
    const d = resp.data || {};
    const ac = d.status === '1' && d.regeocode && d.regeocode.addressComponent;
    if (!ac) return { position: '', cityCode: '', cityName: '' };
    return {
      position: composePosition(ac),
      cityCode: gstr(ac.adcode),
      cityName: gstr(ac.city) || gstr(ac.district),
    };
  } catch (err) {
    console.error('[商旅打卡] 高德逆编码失败：', err.message);
    return { position: '', cityCode: '', cityName: '' };
  }
}

// ---------- 绑定（短信双通道：图形码优先、失败降级顶象滑块接力；密码登录仅滑块通道）----------

// POST /login/captcha：取图形验证码（validcodeimg 对服务器 IP 间歇可用；
// 风控窗口期 99000 时统一回 40035，前端据此降级滑块验证）
router.post('/login/captcha', async (req, res, next) => {
  try {
    const mobile = String((req.body && req.body.mobile) || '').trim();
    if (!/^1\d{10}$/.test(mobile)) return fail(res, 400, 40030, '手机号格式不正确');
    try {
      let image = await sgcc.loginCaptcha(mobile);
      // 商旅返回裸 base64（无 dataURL 前缀），统一补全；PNG 魔数 89504e47
      if (image && !image.startsWith('data:')) image = `data:image/png;base64,${image}`;
      return ok(res, { image });
    } catch (e) {
      return fail(res, 400, 40035, '图形验证码暂不可用，请使用滑块验证');
    }
  } catch (err) { return next(err); }
});

// POST /login/sms：发短信验证码（带 checkImgCode 走图形码 v2 通道；带 captchaToken 走滑块 v3 通道）
router.post('/login/sms', async (req, res, next) => {
  try {
    const { mobile, checkImgCode, captchaToken, constId } = req.body || {};
    if (!/^1\d{10}$/.test(String(mobile || ''))) return fail(res, 400, 40030, '手机号格式不正确');
    if (checkImgCode) {
      // 图形码通道（v2）：statusCode=200 即成功
      const d = await sgcc.loginSendSmsV2(String(mobile), String(checkImgCode).trim());
      if (!d || Number(d.statusCode) !== 200) {
        return fail(res, 400, 40031, (d && d.msg) || '短信发送失败，请重试');
      }
      return ok(res, null);
    }
    if (!captchaToken) return fail(res, 400, 40034, '请先完成图形或滑块验证');
    // 滑块通道（v3）：失败时 statusCode 也是 200，成败看 data.code（成功无 data 或 data.code=0）
    const d = await sgcc.loginSendSms(String(mobile), { captchaToken: String(captchaToken), constId: String(constId || '') });
    const bizFail = !d || Number(d.statusCode) !== 200 || (d.data && d.data.code != null && Number(d.data.code) !== 0);
    if (bizFail) {
      return fail(res, 400, 40031, (d && d.data && d.data.msg) || (d && d.msg) || '短信发送失败，请重试');
    }
    return ok(res, null);
  } catch (err) { return next(err); }
});

// POST /login/bind：短信码或密码换 token 完成绑定（同事写入设备口径）
router.post('/login/bind', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const { mobile, checkCode, password, captchaToken, constId } = req.body || {};
    const deviceType = String(req.body.deviceType || '').trim().slice(0, 64);
    const systemVersion = String(req.body.systemVersion || '').trim().slice(0, 64);
    if (!/^1\d{10}$/.test(String(mobile || ''))) return fail(res, 400, 40030, '手机号格式不正确');
    if (!checkCode && !password) return fail(res, 400, 40032, '请填写短信验证码或密码');
    // 密码登录双通道：带 checkImgCode 走图形码 token/v3；否则走滑块 token/v4（需 captchaToken）
    const { checkImgCode } = req.body || {};
    if (password && !checkImgCode && !captchaToken) return fail(res, 400, 40034, '密码登录请先完成图形或滑块验证');

    const risk = { captchaToken: String(captchaToken || ''), constId: String(constId || '') };
    const { token } = password
      ? (checkImgCode
        ? await sgcc.loginByPasswordV3(String(mobile), String(password), String(checkImgCode).trim())
        : await sgcc.loginByPassword(String(mobile), String(password), risk))
      : await sgcc.loginBySms(String(mobile), String(checkCode).trim(), risk);

    // 认领出工成员：班组内按昵称匹配（与 worklog member-sync 同口径）
    const [members] = await pool.query(
      'SELECT id FROM worklog_member WHERE team_id = ? AND name = ?',
      [req.team.id, req.user.nickname]
    );
    const memberId = members.length ? members[0].id : null;

    // 落库归并（人级唯一 uk_user/uk_member，token 跟人走）：
    // 本人已有绑定行（含调班前旧班行）→ 更新为当前班组/成员口径；无本人行但该成员有代绑行 → 接管回本人；
    // 两者并存且不同行（代绑残留）→ 删代绑行后更新本人行；设备口径沿用原 ODKU 语义（缺省值覆盖）
    const [byUser] = await pool.query('SELECT id FROM worklog_sgcc_account WHERE user_id = ?', [req.user.id]);
    const [byMember] = memberId
      ? await pool.query('SELECT id FROM worklog_sgcc_account WHERE member_id = ?', [memberId])
      : [[]];
    const devType = deviceType || 'Xiaomi 2509FPN0BC';
    const sysVer = systemVersion || 'Android 16';
    if (byUser.length && byMember.length && byUser[0].id !== byMember[0].id) {
      await pool.query('DELETE FROM worklog_sgcc_account WHERE id = ?', [byMember[0].id]);
    }
    if (byUser.length) {
      await pool.query(
        `UPDATE worklog_sgcc_account SET team_id = ?, member_id = ?, mobile = ?, token = ?,
           device_type = ?, system_version = ?, token_status = 1, last_check_at = NOW() WHERE id = ?`,
        [req.team.id, memberId, String(mobile), token, devType, sysVer, byUser[0].id]
      );
    } else if (byMember.length) {
      await pool.query(
        `UPDATE worklog_sgcc_account SET user_id = ?, team_id = ?, mobile = ?, token = ?,
           device_type = ?, system_version = ?, token_status = 1, last_check_at = NOW() WHERE id = ?`,
        [req.user.id, req.team.id, String(mobile), token, devType, sysVer, byMember[0].id]
      );
    } else {
      await pool.query(
        `INSERT INTO worklog_sgcc_account (team_id, user_id, member_id, mobile, token, device_type, system_version, token_status, last_check_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, 1, NOW())`,
        [req.team.id, req.user.id, memberId, String(mobile), token, devType, sysVer]
      );
    }

    // 重新登录成功：补核此前因登录态过期而核查失败的日期（异步不阻塞响应）
    if (memberId) recompenseAfterBind(memberId);
    return ok(res, { bound: true });
  } catch (err) { return next(err); }
});

// 绑定/重新登录成功后的补核（近 62 天、仅当日用车人，同定时核查口径；member 口径——调班后旧班失败记录也能补核）：
// 异步执行，日期间按 SGCC_SYNC_INTERVAL_MS 间隔防风控；补核完成的日期清掉对应 auth 失败记录
function recompenseAfterBind(memberId) {
  (async () => {
    const account = await accountByMember(memberId);
    if (!account) return;
    if (!(await probeAuth(account))) return; // 新 token 探测不过（异常）则不补核，避免误清失败记录
    const [fails] = await pool.query(
      `SELECT DISTINCT DATE_FORMAT(sync_date, '%Y-%m-%d') AS d FROM worklog_sync_log
       WHERE member_id = ? AND type = 'auth' AND result = 'fail'
         AND sync_date >= DATE_SUB(CURDATE(), INTERVAL 62 DAY) ORDER BY d`,
      [memberId]
    );
    for (const { d } of fails) {
      const [m] = await pool.query(
        `SELECT e.team_id FROM worklog_entry e JOIN worklog_entry_member em ON em.entry_id = e.id
         WHERE e.log_date = ? AND em.member_id = ? ORDER BY e.id LIMIT 1`,
        [d, memberId]
      );
      if (!m.length) continue; // 非当日用车人不补核
      try {
        await syncOne(account, d, 'daily', { teamId: m[0].team_id, memberId });
        await pool.query(
          `DELETE FROM worklog_sync_log WHERE member_id = ? AND sync_date = ? AND type = 'auth' AND result = 'fail'`,
          [memberId, d]
        );
      } catch (e) {
        console.error(`[商旅打卡] 登录后补核失败（成员 ${memberId} ${d}）：`, e.message);
      }
      await new Promise((r) => setTimeout(r, config.sgcc.syncIntervalMs));
    }
  })().catch((err) => console.error('[商旅打卡] 登录后补核失败：', err.message));
}

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

// GET /expired：本班登录已过期的绑定成员名单（全员可用；出工日志子应用进入时弹窗提醒的数据源）
router.get('/expired', async (req, res, next) => {
  try {
    if (!req.team) return ok(res, { list: [] });
    const [rows] = await pool.query(
      `SELECT a.member_id, m.name AS member_name, a.mobile
       FROM worklog_sgcc_account a LEFT JOIN worklog_member m ON m.id = a.member_id
       WHERE a.team_id = ? AND a.token_status = 0 ORDER BY m.sort IS NULL, m.sort, a.id`,
      [req.team.id]
    );
    return ok(res, {
      list: rows.map((r) => ({
        memberId: r.member_id,
        memberName: r.member_name || '',
        mobile: String(r.mobile || '').replace(/^(\d{3})\d{4}(\d{4})$/, '$1****$2'),
      })),
    });
  } catch (err) { return next(err); }
});

// ---------- 管理员维护绑定设备（网页端数据管理「商旅绑定设备」区；超管任意班组 / 班组管理员仅本班，同 worklog 字典口径） ----------
function requireDictAdmin(req, res, next) {
  if (req.user.role === 'admin') return next();
  if (req.user.role === 'team_admin' && req.team && req.user.team_id === req.team.id) return next();
  return fail(res, 403, 40304, '仅管理员可执行此操作');
}

// GET /accounts：本班组全部绑定账号（成员名 / 手机号脱敏 / 设备口径 / 登录态 / 最近核查时间）
router.get('/accounts', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const [rows] = await pool.query(
      `SELECT a.id, a.member_id, m.name AS member_name, a.mobile,
              a.device_type, a.system_version, a.token_status,
              DATE_FORMAT(a.last_check_at, '%Y-%m-%d %H:%i') AS last_check_at
       FROM worklog_sgcc_account a LEFT JOIN worklog_member m ON m.id = a.member_id
       WHERE a.team_id = ? ORDER BY m.sort IS NULL, m.sort, a.id`,
      [req.team.id]
    );
    return ok(res, {
      list: rows.map((r) => ({
        id: r.id,
        memberName: r.member_name || '', // 空 = 绑定账号昵称未匹配到出工成员
        mobile: String(r.mobile || '').replace(/^(\d{3})\d{4}(\d{4})$/, '$1****$2'),
        deviceType: r.device_type || '',
        systemVersion: r.system_version || '',
        tokenStatus: r.token_status,
        lastCheckAt: r.last_check_at || '',
      })),
    });
  } catch (err) { return next(err); }
});

// PUT /accounts/:id/device：管理员改绑定设备口径（设备型号 = 「厂商 型号」格式；作用范围限生效班组）
router.put('/accounts/:id/device', requireDictAdmin, async (req, res, next) => {
  try {
    const deviceType = String(req.body.deviceType || '').trim().slice(0, 64);
    const systemVersion = String(req.body.systemVersion || '').trim().slice(0, 64);
    if (!deviceType) return fail(res, 400, 40033, '设备型号不能为空');
    const [r] = await pool.query(
      'UPDATE worklog_sgcc_account SET device_type = ?, system_version = ? WHERE id = ? AND team_id = ?',
      [deviceType, systemVersion, Number(req.params.id) || 0, req.team ? req.team.id : 0]
    );
    if (!r.affectedRows) return fail(res, 404, 40400, '绑定记录不存在');
    return ok(res, null);
  } catch (err) { return next(err); }
});

// POST /accounts/bind：管理员代成员绑定商旅账号（成员过期又联系不上时兜底；网页端数据管理「商旅绑定设备」区）
// 登录链路同本人绑定（图形码/滑块双通道 + 短信/密码）；落库 user_id=NULL 表示代绑，
// 成员事后自助重绑时撞 uk_member 由 /login/bind 归并把 user_id 接管回本人（见 /login/bind）
router.post('/accounts/bind', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const { memberId, mobile, checkCode, password, captchaToken, constId } = req.body || {};
    if (!memberId) return fail(res, 400, 40036, '请选择要绑定的成员');
    if (!/^1\d{10}$/.test(String(mobile || ''))) return fail(res, 400, 40030, '手机号格式不正确');
    if (!checkCode && !password) return fail(res, 400, 40032, '请填写短信验证码或密码');
    // 密码登录双通道：带 checkImgCode 走图形码 token/v3；否则走滑块 token/v4（需 captchaToken）
    const { checkImgCode } = req.body || {};
    if (password && !checkImgCode && !captchaToken) return fail(res, 400, 40034, '密码登录请先完成图形或滑块验证');
    // 成员必须属于本班
    const [members] = await pool.query(
      'SELECT id FROM worklog_member WHERE id = ? AND team_id = ?',
      [Number(memberId) || 0, req.team.id]
    );
    if (!members.length) return fail(res, 404, 40400, '成员不存在或不属于本班组');

    const risk = { captchaToken: String(captchaToken || ''), constId: String(constId || '') };
    const { token } = password
      ? (checkImgCode
        ? await sgcc.loginByPasswordV3(String(mobile), String(password), String(checkImgCode).trim())
        : await sgcc.loginByPassword(String(mobile), String(password), risk))
      : await sgcc.loginBySms(String(mobile), String(checkCode).trim(), risk);

    // 代绑落库：user_id=NULL（管理员代绑）；不更新 user_id/device 口径——
    // 成员已自绑时只换 token（行仍归本人），设备信息保留原值
    await pool.query(
      `INSERT INTO worklog_sgcc_account (team_id, user_id, member_id, mobile, token, token_status, last_check_at)
       VALUES (?, NULL, ?, ?, ?, 1, NOW())
       ON DUPLICATE KEY UPDATE team_id = VALUES(team_id), mobile = VALUES(mobile), token = VALUES(token),
         member_id = VALUES(member_id), token_status = 1, last_check_at = NOW()`,
      [req.team.id, members[0].id, String(mobile), token]
    );

    // 与本人绑定同口径：补核此前因登录态过期而核查失败的日期（异步不阻塞响应）
    recompenseAfterBind(members[0].id);
    return ok(res, { bound: true });
  } catch (err) { return next(err); }
});

// ---------- 打卡区数据与打卡操作 ----------

// GET /day?date=YYYY-MM-DD：本班组当日相关成员的 绑定/登录态 + 两次打卡 + 费用（按 member_id 索引）
// 相关成员集 = 本班成员 ∪ 当日本班卡片用车人（人已调班但名字在卡上时，人级解析仍可见绑定/可取数代打卡）；
// 打卡/费用按 member_id 人级取数（去 team 过滤，调班日共享行两班均可见）。小程序卡片打卡区据此渲染（本地优先，只查本地表）
router.get('/day', async (req, res, next) => {
  try {
    const date = String(req.query.date || '');
    if (!DATE_RE.test(date)) return fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD');
    if (!req.team) return ok(res, { members: {} });
    const [mrows] = await pool.query(
      `SELECT id FROM worklog_member WHERE team_id = ?
       UNION
       SELECT em.member_id AS id FROM worklog_entry e
       JOIN worklog_entry_member em ON em.entry_id = e.id
       WHERE e.team_id = ? AND e.log_date = ?`,
      [req.team.id, req.team.id, date]
    );
    const memberIds = mrows.map((r) => r.id);
    if (!memberIds.length) return ok(res, { members: {} });
    // 人级解析绑定行（直连优先，user_id 兜底；一名成员理论多行时去重取首条）
    const [accounts] = await pool.query(
      `SELECT m.id AS card_member_id, a.token_status
       FROM worklog_member m
       JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
       WHERE m.id IN (?)
       ORDER BY (a.member_id = m.id) DESC`,
      [memberIds]
    );
    const [clockins] = await pool.query(
      `SELECT member_id, seq, detail_id, DATE_FORMAT(clock_time, '%Y-%m-%d %H:%i:%s') AS clock_time, position, work_hours
       FROM worklog_clockin WHERE member_id IN (?) AND clock_date = ?`,
      [memberIds, date]
    );
    const [fees] = await pool.query(
      'SELECT member_id, food_fee, transit_fee, cost_center_code, cost_center_name FROM worklog_fee WHERE member_id IN (?) AND fee_date = ?',
      [memberIds, date]
    );
    const members = {};
    accounts.forEach((a) => {
      if (!members[a.card_member_id]) members[a.card_member_id] = { bound: true, tokenStatus: a.token_status };
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

    const account = await requireMemberAccount(req, res, memberId, date);
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

    // 双写：从商旅全量刷新该日打卡流水（含工时；本地行落在卡片上下文 member/本班，代打卡含已调班成员）；
    // 随后补写城市信息（dayNew 明细不带城市编码，落库供后续打卡带入全套定位信息）
    await refreshClockins(account, date, memberId, req.team.id);
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
// 成本分配选项码 ↔ 文案（组件 id=4 的五个选项；商旅自动带出时可能只给名称/编码、选中码缺失或误为文案，须归一否则保存不生效）
const COST_ALLOC_OPTS = { 1: '成本中心', 2: 'WBS', 3: '内部订单号', 4: '成本中心&WBS', 5: '成本中心&内部订单号' };

// 归一 comp4 的 value/data：选项码空 → 默认「成本中心」(1)；误写为选项文案 → 映射回选项码；
// value.value=选项码 / data.value=选项文案（两字段不对称，勿同串覆盖）
function normalizeCostAllocComp(comp, v) {
  let code = String(v.value || '').trim();
  if (!code) code = '1';
  else if (!COST_ALLOC_OPTS[code]) {
    const rev = Object.entries(COST_ALLOC_OPTS).find(([, label]) => label === v.value);
    if (rev) code = rev[0];
  }
  const codeName = String(v.costCenterName || '');
  const codeCode = String(v.costCenterCode || '');
  comp.value = JSON.stringify({
    costCenterName: codeName, value: code,
    projectType: String(v.projectType || ''), projectTypeName: String(v.projectTypeName || ''),
    costCenterCode: codeCode,
  });
  comp.data = JSON.stringify({
    costCenterName: codeName, value: COST_ALLOC_OPTS[code] || '成本中心',
    projectType: String(v.projectType || ''), projectTypeName: String(v.projectTypeName || ''),
    costCenterCode: codeCode,
  });
  return code;
}

// 模板「成本分配」（id=4）兜底：商旅默认带出则归一后沿用并回报当前值；空值时用本成员最近一次本地成本中心回填。
// memberId = 卡片上下文成员（缺省账号自身成员；调班重名回退路径的旧成员行也能取到其成本中心历史）
// 返回 { code, name }（当前生效值，供本地摘要回写）或 null（无来源可填）
async function ensureCostCenter(tpl, account, memberId = account.member_id) {
  const comp = (tpl.dtComponentList || []).find((c) => c.id === 4);
  if (!comp) return null;
  let v = {};
  try { v = comp.value ? JSON.parse(comp.value) : {}; } catch (e) { v = {}; }
  if (v && v.costCenterCode) {
    // 编码已带出：选项码缺失/误写时归一（商旅自动带出常只给名称+编码不给选中码，原样透传保存不生效）
    normalizeCostAllocComp(comp, v);
    return { code: String(v.costCenterCode), name: String(v.costCenterName || '') };
  }
  const [rows] = await pool.query(
    `SELECT cost_center_code, cost_center_name FROM worklog_fee
     WHERE member_id = ? AND cost_center_code <> '' ORDER BY fee_date DESC LIMIT 1`,
    [memberId]
  );
  if (!rows.length) return null;
  normalizeCostAllocComp(comp, {
    costCenterName: String(rows[0].cost_center_name || ''),
    value: '',
    costCenterCode: String(rows[0].cost_center_code),
  });
  return { code: String(rows[0].cost_center_code), name: String(rows[0].cost_center_name || '') };
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
    const account = await requireMemberAccount(req, res, memberId, date);
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
            label: hit ? String(hit.label || '') : (COST_ALLOC_OPTS[String(v.value)] || ''),
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
    const account = await requireMemberAccount(req, res, memberId, date);
    if (!account) return;

    const d = await sgcc.getFeeInfoNew(account.token, await feeTplParams(memberId, date), devOpt(account));
    const body = d && Number(d.statusCode) === 200 && d.data && d.data.body;
    if (!body || !body.clockTemplate) return fail(res, 400, 40038, '获取商旅费用模板失败，请重试');

    // 成本分配（id=4）兜底：模板未带出时用本成员最近成本中心回填（勿用 overrides 同串覆盖，value/data 不对称）
    const cc = await ensureCostCenter(body.clockTemplate, account, memberId);

    // 补助明细（id=10）：伙食/交通；其余组件经 overrides 原样透传
    const foodFee = Number(req.body.foodFee) || 0;
    const transitFee = Number(req.body.transitFee) || 0;
    const overrides = { ...(req.body.overrides || {}) };
    overrides[10] = JSON.stringify({ foodFee: String(foodFee), arrive: String(transitFee) });

    const d2 = await sgcc.saveFeeInfoNew(account.token, date, body.clockTemplate, overrides, devOpt(account));
    if (!d2 || Number(d2.statusCode) !== 200) {
      return fail(res, 400, 40039, (d2 && d2.msg) || '商旅费用保存失败，请重试');
    }

    // 保存后复核（排查用日志，不影响响应）：重新取模板确认成本分配是否已落商旅
    sgcc.getFeeInfoNew(account.token, await feeTplParams(memberId, date), devOpt(account))
      .then((d3) => {
        const t3 = d3 && d3.data && d3.data.body && d3.data.body.clockTemplate;
        const cc3 = t3 ? extractCostCenter(t3) : null;
        console.log(`[商旅打卡] 费用保存复核（成员 ${memberId} ${date}）：成本分配 = ${cc3 && cc3.code ? `${cc3.code} ${cc3.name}` : '空'}`);
      })
      .catch(() => {});

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

// ---------- 照片同步（worklog 上传远端先行调用 + 改人名补传 + 手动重试）----------

// 多成员商旅循环的可选进度回调 prog（{ addTotal(n), step() }）：由 newPhotoOp 的任务记录供给，
// 每个成员处理完（含跳过）step 一次；失败中止不 step。addTotal 累加设计：改人名「剔除 + 补传」两段共用同一任务拼接连续进度。

// 把照片原文上传并关联进每个指定人名的当日商旅费用照片（reimbEnclosure/add → getFeeInfoNew → saveFeeInfoNew 关联上传图片组件）。
// 远端先行核心，两处共用：worklog 照片上传（本地落库前调用，buf 在内存）与 syncPhotoToSgcc（改人名补传 / resync，COS 回源 buf）。
// links 传已有链接（按成员去重，已链接者跳过，重试天然只补缺口）；未绑定成员跳过不计失败（记核查日志，与验证规则 a「未绑定者不参与」同口径）；
// 登录过期视为硬失败（远端先行口径：商旅失败即整个操作失败，跳过会造成本地与商旅必然不一致）。任一绑定成员失败即中止。
// 返回 { ok:true, links } / { ok:false, links, failedName, error }（links 含本次新成功成员，供调用方落库）
async function uploadPhotoToMembersRemote({ teamId, logDate, names, buf, fileName, links = {}, photoId = null, prog = null }) {
  const imgBase64Str = buf.toString('base64');
  const photoLabel = photoId ? `照片 ${photoId}` : '照片';
  if (prog) prog.addTotal(names.length);
  for (const name of names) {
    const [mrows] = await pool.query(
      'SELECT id FROM worklog_member WHERE team_id = ? AND name = ?', [teamId, name]
    );
    if (!mrows.length) { if (prog) prog.step(); continue; }
    const memberId = mrows[0].id;
    if (links[memberId]) { if (prog) prog.step(); continue; } // 该成员名下已同步过
    const account = await accountByMember(memberId); // 人级解析：人已调班仍可用其账号同步
    if (!account) {
      await pool.query(
        `INSERT INTO worklog_sync_log (team_id, member_id, sync_date, scope, type, result, detail)
         VALUES (?, ?, ?, 'daily', 'photo', 'fail', ?)`,
        [teamId, memberId, logDate, `${photoLabel} 同步跳过：成员「${name}」未绑定商旅账号`]
      );
      if (prog) prog.step();
      continue;
    }
    if (account.token_status !== 1) {
      await pool.query(
        `INSERT INTO worklog_sync_log (team_id, member_id, sync_date, scope, type, result, detail)
         VALUES (?, ?, ?, 'daily', 'photo', 'fail', ?)`,
        [teamId, memberId, logDate, `${photoLabel} 同步失败：成员「${name}」商旅登录已过期`]
      );
      return { ok: false, links, failedName: name, error: '商旅登录已过期，请重新登录商旅账号' };
    }
    try {
      const up = await sgcc.reimbEnclosureAdd(account.token, {
        imgBase64Str, fileName, fileSize: buf.length, ext: '.jpg',
      }, devOpt(account));
      const imgId = up && up.data && (up.data.id || (up.data.body && up.data.body.id));
      const imgUrl = up && up.data && (up.data.imageUrl || (up.data.body && up.data.body.imageUrl));
      if (!imgId) throw new Error('商旅图片上传未返回 id');

      // 关联进当日费用：上传图片组件（id=5）追加该图，整树重存；成本分配兜底同费用保存口径
      const fi = await sgcc.getFeeInfoNew(account.token, await feeTplParams(memberId, logDate), devOpt(account));
      const tpl = fi && fi.data && fi.data.body && fi.data.body.clockTemplate;
      if (!tpl) throw new Error('获取费用模板失败');
      await ensureCostCenter(tpl, account, memberId);
      const comp = (tpl.dtComponentList || []).find((c) => c.id === 5);
      let imgs = [];
      if (comp && comp.value) { try { imgs = JSON.parse(comp.value); } catch (e) { imgs = []; } }
      if (!Array.isArray(imgs)) imgs = [];
      // 元素形状与商旅 App 手工上传一致：{fileInfoId, url}（imgId 即 reimbEnclosureAdd 返回的 fileInfoId）
      imgs.push({ fileInfoId: imgId, url: imgUrl || '' });
      const sv = await sgcc.saveFeeInfoNew(account.token, logDate, tpl, { 5: JSON.stringify(imgs) }, devOpt(account));
      if (!sv || Number(sv.statusCode) !== 200) throw new Error('费用照片关联保存失败');

      links[memberId] = imgId;
    } catch (err) {
      console.error(`[商旅打卡] ${photoLabel} 同步到成员 ${name} 失败：`, err.message);
      await pool.query(
        `INSERT INTO worklog_sync_log (team_id, member_id, sync_date, scope, type, result, detail)
         VALUES (?, ?, ?, 'daily', 'photo', 'fail', ?)`,
        [teamId, memberId, logDate, `${photoLabel} 同步失败：成员「${name}」${String(err.message).slice(0, 200)}`]
      );
      return { ok: false, links, failedName: name, error: String(err.message).slice(0, 200) };
    }
    if (prog) prog.step(); // 成功路径 step（catch 已 return，失败中止不计进度）
  }
  return { ok: true, links };
}

// 单张照片同步进所属人名的当日商旅费用照片（同步等待，返回最终结果；改人名补传与 resync 手动重试调用）
// targetNames 缺省读库内 members；改人名时传新全量名单（已链接成员天然跳过，并自愈历史缺链接的在名成员）。
// sgcc_synced 判定：全部人名处理成功 =1，否则 =2（未绑定成员跳过不影响判定）；部分成功的链接即时落库，重试只补缺口。
// 返回 { ok:true } / { ok:false, failedName, error }
async function syncPhotoToSgcc(photoId, targetNames = null, prog = null) {
  const [rows] = await pool.query(
    `SELECT p.id, p.url, p.members, p.sgcc_img_id,
            DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, e.team_id
     FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id WHERE p.id = ?`,
    [photoId]
  );
  const photo = rows[0];
  if (!photo) return { ok: false, error: '照片不存在' };
  const names = Array.isArray(targetNames) && targetNames.length
    ? targetNames
    : (typeof photo.members === 'string' ? JSON.parse(photo.members) : (photo.members || []));
  const links = photo.sgcc_img_id ? JSON.parse(photo.sgcc_img_id) : {};

  // 图片原文（COS 回源）
  const resp = await fetch(photo.url, { signal: AbortSignal.timeout(60000) });
  if (!resp.ok) throw new Error(`照片回源失败（HTTP ${resp.status}）`);
  const buf = Buffer.from(await resp.arrayBuffer());

  const r = await uploadPhotoToMembersRemote({
    teamId: photo.team_id, logDate: photo.log_date, names, buf,
    fileName: `photo-${photoId}.jpg`, links, photoId, prog,
  });
  // 无论成败都落库最新链接与状态（部分成功的链接保留，重试只补缺口）
  await pool.query(
    'UPDATE worklog_photo SET sgcc_img_id = ?, sgcc_synced = ? WHERE id = ?',
    [JSON.stringify(r.links), r.ok ? 1 : 2, photoId]
  );
  if (!r.ok) return { ok: false, failedName: r.failedName, error: r.error };
  return { ok: true };
}

// 从某成员当日商旅费用照片组件移除指定图片（同步；成本分配兜底同费用保存口径）
// memberId = 卡片上下文成员（费用模板城市参数/成本中心兜底按其人级本地数据取）
// 返回 { ok: true }（含商旅侧本就没有该图）或 { ok: false, error }
async function removeFeeImageRemote(account, date, imgId, memberId = account.member_id) {
  const fi = await sgcc.getFeeInfoNew(account.token, await feeTplParams(memberId, date), devOpt(account));
  const tpl = fi && fi.data && fi.data.body && fi.data.body.clockTemplate;
  if (!tpl) return { ok: false, error: '获取费用模板失败' };
  await ensureCostCenter(tpl, account, memberId);
  const comp = (tpl.dtComponentList || []).find((c) => c.id === 5);
  let imgs = [];
  if (comp && comp.value) { try { imgs = JSON.parse(comp.value); } catch (e) { imgs = []; } }
  if (!Array.isArray(imgs)) imgs = [];
  const kept = imgs.filter((it) => String(it && (it.id ?? it.fileInfoId ?? it.imageId)) !== String(imgId));
  if (kept.length === imgs.length) return { ok: true }; // 商旅侧本就没有该图，视为已删除
  const sv = await sgcc.saveFeeInfoNew(account.token, date, tpl, { 5: JSON.stringify(kept) }, devOpt(account));
  if (!sv || Number(sv.statusCode) !== 200) return { ok: false, error: (sv && sv.msg) || '商旅费用保存失败' };
  return { ok: true };
}

// 照片人名剔除（同步远端先行，改人名接口在更新本地人名前调用）：逐个从被剔除成员的商旅费用照片移除该图。
// 一切以商旅平台为准：任一成员远端删除失败即中止并返回失败（本地人名不变更；未删链接全部保留，防核查回拉人名复活；
// 已成功成员的链接即时断开，重试编辑时由补传链路恢复其图片）；未绑定/登录过期视为失败（其商旅侧图片确实存在且无法操作）
async function removePhotoMembersRemote(photoId, removedNames, prog = null) {
  const names = Array.isArray(removedNames) ? removedNames : [];
  if (!names.length) return { ok: true };
  const [rows] = await pool.query(
    `SELECT p.sgcc_img_id, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, e.team_id
     FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id WHERE p.id = ?`,
    [photoId]
  );
  if (!rows.length || !rows[0].sgcc_img_id) return { ok: true };
  if (prog) prog.addTotal(names.length);
  let links = {};
  try { links = JSON.parse(rows[0].sgcc_img_id); } catch (e) { links = {}; }
  const logDate = rows[0].log_date;
  const teamId = rows[0].team_id;
  for (const name of names) {
    const [mrows] = await pool.query('SELECT id FROM worklog_member WHERE team_id = ? AND name = ?', [teamId, name]);
    if (!mrows.length) { if (prog) prog.step(); continue; }
    const memberId = mrows[0].id;
    const imgId = links[memberId];
    if (!imgId) { if (prog) prog.step(); continue; } // 该成员名下本就没同步成功过，无需远端删除
    const account = await accountByMember(memberId); // 人级解析：人已调班仍可用其账号操作
    let err = '';
    if (!account || account.token_status !== 1) {
      err = account && account.token_status === 0 ? '商旅登录已过期' : '未绑定商旅账号';
    } else {
      try {
        const r = await removeFeeImageRemote(account, logDate, imgId, memberId);
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
    if (prog) prog.step();
  }
  return { ok: true };
}

// 删除本地照片前解除全部所属人名的商旅费用照片关联（同步远端先行，删除接口在删本地前调用）。
// 一切以商旅平台为准：任一成员解除失败即中止并返回失败（本地照片不删除；已成功成员的链接即时断开，重试仅处理剩余链接）
async function unlinkPhotoFromSgcc(photoId, prog = null) {
  const [rows] = await pool.query(
    `SELECT p.sgcc_img_id, DATE_FORMAT(e.log_date, '%Y-%m-%d') AS log_date, e.team_id
     FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id WHERE p.id = ?`,
    [photoId]
  );
  if (!rows.length || !rows[0].sgcc_img_id) return { ok: true };
  let links = {};
  try { links = JSON.parse(rows[0].sgcc_img_id); } catch (e) { links = {}; }
  if (prog) prog.addTotal(Object.keys(links).length);
  const logDate = rows[0].log_date;
  const teamId = rows[0].team_id;
  for (const memberIdStr of Object.keys(links)) {
    const imgId = links[memberIdStr];
    const memberId = Number(memberIdStr);
    const account = await accountByMember(memberId); // 人级解析：人已调班仍可用其账号操作
    const [mrows] = await pool.query('SELECT name FROM worklog_member WHERE id = ?', [memberId]);
    const name = mrows.length ? mrows[0].name : String(memberId);
    let err = '';
    if (!account || account.token_status !== 1) {
      err = account && account.token_status === 0 ? '商旅登录已过期' : '未绑定商旅账号';
    } else {
      try {
        const r = await removeFeeImageRemote(account, logDate, imgId, memberId);
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
    if (prog) prog.step();
  }
  return { ok: true };
}


// POST /photos/:id/resync：同步失败的照片手动重试（任务化异步执行：登记后立即返回 opId，商旅重传在后台串行队列执行，
// 端侧凭 opId 轮询 GET /op/status 收尾；失败状态含成员与原因，sgcc_synced 随结果落库）
router.post('/photos/:id/resync', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const photoId = Number(req.params.id);
    const [rows] = await pool.query(
      'SELECT p.id, p.entry_id FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id WHERE p.id = ? AND e.team_id = ?',
      [photoId, req.team.id]
    );
    if (!rows.length) return fail(res, 404, 40400, '照片不存在');
    if (activePhotoOp(req.team.id, rows[0].entry_id)) {
      return fail(res, 409, 40909, '该卡片有商旅同步任务进行中，请稍后再试');
    }
    const op = newPhotoOp(req.team.id, 'resync', rows[0].entry_id, photoId);
    runPhotoOp(op.id, async (rec) => {
      const r = await syncPhotoToSgcc(photoId, null, op.prog);
      if (!r.ok) {
        rec.failedName = r.failedName || '';
        throw new Error(r.error || '商旅同步失败');
      }
    });
    return ok(res, { opId: op.id }, '已发起重新同步');
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
// （JSON 数组，元素含 id/url（App 手工上传可能为 imageUrl，两键兼容），结构见 esgcc/sgcc/tools/fee_probe3.js 联调口径）；比对键 = 商旅图片 id
// ctx = 卡片上下文 { teamId, memberId }（缺省账号自身口径）：本地照片集/挂卡/成员名/COS 路径均按 ctx 取——
// 核查按卡片走，人已调班时旧班卡片的照片对账仍落在旧班上下文；远端调用仍用 account.token/机型
// 规则：商旅有本地无 → 下载存 COS 入库（members=[成员名]、source=1、is_watermark=1、
//       sgcc_img_id={memberId:图片id}、verify_status='pending'，异步 Dify 验证回写沿用 writeBackPullVerify）；
//       相同照片（MD5 一致）合并：人名/链接并入已有照片，一图多人标注；
//       本地 source=1（商旅拉下的镜像）有而商旅无 → 整照删除（COS 对象 + worklog_photo 行）；
//       本地 source=0（壹匣上传）本成员链接有而商旅无 → 摘除本成员链接与人名（摘空后整照删除）
async function syncFeePhotos(account, date, remoteImgs, log, ctx = null) {
  const ctxTeamId = ctx ? ctx.teamId : account.team_id;
  const ctxMemberId = ctx ? ctx.memberId : account.member_id;
  // 商旅侧集合：商旅图片 id → 图片地址
  // id 键兼容：壹匣保存写入 {id,url}，App 手工上传为 {fileInfoId,url}（另兼容 imageUrl）；
  // 有数据但识别不出图片 id（键名再漂移）→ 跳过对账保护本地，防止误判「商旅无照片」删错
  const remote = new Map();
  for (const it of remoteImgs) {
    if (!it) continue;
    const rawId = it.id ?? it.fileInfoId ?? it.imageId;
    if (rawId === undefined || rawId === null || String(rawId) === '') continue;
    const url = typeof it.url === 'string' && it.url.trim()
      ? it.url.trim()
      : (typeof it.imageUrl === 'string' ? it.imageUrl.trim() : '');
    remote.set(String(rawId), url);
  }
  if (remoteImgs.length && !remote.size) {
    console.error(`[商旅打卡] 费用照片对账跳过（成员 ${ctxMemberId} ${date}）：远端 ${remoteImgs.length} 张但无可识别图片 id，样例 ${JSON.stringify(remoteImgs[0]).slice(0, 300)}`);
    await log('photo', 'fail', `费用照片：远端 ${remoteImgs.length} 张但无可识别图片 id（键名异常），已跳过对账（详见服务端日志）`);
    return;
  }

  // 本成员名（摘除/入库标注用；取卡片上下文成员）
  const [mnrows] = await pool.query('SELECT name FROM worklog_member WHERE id = ?', [ctxMemberId]);
  const memberName = mnrows.length ? mnrows[0].name : '';

  // 本地侧：卡片上下文班组当日照片中与本成员相关的行（sgcc_img_id JSON map 含本成员 key，值即该成员商旅图片 id）；
  // source=1 行参与删除对账，source=0（壹匣上传）行只比对不删除
  const [photos] = await pool.query(
    `SELECT p.id, p.cos_key, p.source, p.sgcc_img_id
     FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
     WHERE e.team_id = ? AND e.log_date = ?`,
    [ctxTeamId, date]
  );
  const memberKey = String(ctxMemberId);
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
    // 绑定人 username（Dify user 入参；成员名 memberName 已在函数头部取好）
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
      [ctxTeamId, ctxMemberId, date]
    );
    if (!erows.length) {
      await log('photo', 'fail', `费用照片：商旅侧 ${toPull.length} 张本地无，但成员当日无出工记录，已跳过入库`);
    } else if (!memberName) {
      await log('photo', 'fail', `费用照片：成员 ${ctxMemberId} 不存在，已跳过入库`);
    } else {
      const entry = erows[0];
      // COS key 规则同 worklog 照片：{prefix}{班组名}/{YYYY.MM.DD}/{entryId}-{ts}-{图片id}.jpg（带图片 id 防同毫秒撞键）
      const [trows] = await pool.query('SELECT name FROM sys_team WHERE id = ?', [ctxTeamId]);
      const teamName = trows.length ? trows[0].name : String(ctxTeamId);
      const prefix = config.worklog.cosPrefix;
      for (const [imgId, imgUrl] of toPull) {
        try {
          if (!/^https?:\/\//.test(imgUrl)) throw new Error('商旅侧未返回有效图片地址');
          const resp = await fetch(imgUrl, { signal: AbortSignal.timeout(60000) });
          if (!resp.ok) throw new Error(`照片下载失败（HTTP ${resp.status}）`);
          const buf = Buffer.from(await resp.arrayBuffer());
          const imgMd5 = crypto.createHash('md5').update(buf).digest('hex');
          // 相同照片内容合并（一图多人标注）：卡片上下文班组当日已有同 MD5 照片 → 人名/链接并入，不再新建
          const [dup] = await pool.query(
            `SELECT p.id, p.members, p.sgcc_img_id FROM worklog_photo p
             JOIN worklog_entry e ON e.id = p.entry_id
             WHERE e.team_id = ? AND e.log_date = ? AND p.md5 = ? LIMIT 1`,
            [ctxTeamId, date, imgMd5]
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
          // 拉下入库即视已同步（照片本就在商旅侧，勿落默认 0 误显「未同步」）
          const [r] = await pool.query(
            `INSERT INTO worklog_photo (entry_id, cos_key, url, members, is_watermark, source, sgcc_img_id, sgcc_synced, verify_status, md5)
             VALUES (?, ?, ?, ?, 1, 1, ?, 1, 'pending', ?)`,
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

  // 方向二：商旅侧已删 → 本地同步删除（一切以商旅平台为准）
  // source=1（商旅拉下的镜像）整照删除；source=0（壹匣上传）摘除本成员链接与人名，摘空后整照删除
  let deleted = 0;
  let unlinked = 0;
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
  for (const p of localUploaded.filter((x) => !remote.has(x.imgId))) {
    try {
      // 逐行读当前值（members/links 可能被并发改动），仅摘除本成员的链接与人名
      const [cur] = await pool.query('SELECT members, sgcc_img_id, cos_key FROM worklog_photo WHERE id = ?', [p.id]);
      if (!cur.length) continue;
      let names = typeof cur[0].members === 'string' ? JSON.parse(cur[0].members) : (cur[0].members || []);
      let links = {};
      if (cur[0].sgcc_img_id) {
        try { links = typeof cur[0].sgcc_img_id === 'string' ? JSON.parse(cur[0].sgcc_img_id) : cur[0].sgcc_img_id; } catch (e) { links = {}; }
      }
      delete links[memberKey];
      names = names.filter((n) => n !== memberName);
      if (!names.length) {
        if (cur[0].cos_key) await cos.deleteObject(cur[0].cos_key);
        await pool.query('DELETE FROM worklog_photo WHERE id = ?', [p.id]);
        deleted += 1;
      } else {
        await pool.query(
          'UPDATE worklog_photo SET members = ?, sgcc_img_id = ? WHERE id = ?',
          [JSON.stringify(names), JSON.stringify(links), p.id]
        );
        unlinked += 1;
      }
    } catch (err) {
      console.error(`[商旅打卡] 费用照片摘除失败（照片 ${p.id} 成员 ${memberName}）：`, err.message);
      await log('photo', 'fail', `费用照片：本地照片 ${p.id}（商旅图片 ${p.imgId}）摘除失败：${String(err.message).slice(0, 200)}`);
    }
  }

  await log('photo', pulled || deleted || merged || unlinked ? 'diff' : 'ok',
    `费用照片：拉下 ${pulled} 张 / 合并 ${merged} 张 / 删除 ${deleted} 张 / 摘除 ${unlinked} 人`);
}

// 单日单人对账：登录态 → 打卡 → 费用 → 费用照片（一律以商旅为准覆盖本地；照片双向对账：拉新入库并触发验证，商旅侧已删的同步照片本地同步删除）
// ctx = 卡片上下文 { teamId, memberId }（缺省账号自身口径）：核查按卡片走，sync_log 归属、费用/照片本地上下文均按 ctx；
// 打卡流水为 member 级唯一物理行（team_id 仅写入时快照），删除/刷新对账本就按 member 口径
// 登录态 + 打卡对账（syncOne 前段；11:00/18:00 打卡提醒前的轻量同步也复用本段）。
// 返回 false 表示登录失效已中止（后续费用/照片对账不再进行）
async function syncClockinPart(account, date, log, ctxTeamId, ctxMemberId) {
  // 登录态
  const valid = await probeAuth(account);
  if (!valid) {
    await log('auth', 'fail', '登录已失效，已标记置灰');
    return false;
  }

  // 打卡对账：先全量刷新，再删除本地 detailId 不在商旅返回集合内的行（商旅 0 条则全删，一律以商旅为准）
  const [before] = await pool.query(
    'SELECT COUNT(*) AS cnt FROM worklog_clockin WHERE member_id = ? AND clock_date = ?',
    [ctxMemberId, date]
  );
  const body = await refreshClockins(account, date, ctxMemberId, ctxTeamId);
  if (body) {
    const remoteIds = new Set(
      (Array.isArray(body.clockInDetailList) ? body.clockInDetailList : [])
        .map((it) => String(it.detailId ?? it.id ?? ''))
        .filter(Boolean)
    );
    if (remoteIds.size === 0) {
      await pool.query('DELETE FROM worklog_clockin WHERE member_id = ? AND clock_date = ?', [ctxMemberId, date]);
    } else {
      // 删除本地有但商旅已无的记录（按 detailId 匹配，避免按 seq 位置误删）
      const [localRows] = await pool.query(
        'SELECT id, detail_id FROM worklog_clockin WHERE member_id = ? AND clock_date = ?',
        [ctxMemberId, date]
      );
      for (const row of localRows) {
        if (!remoteIds.has(String(row.detail_id))) {
          await pool.query('DELETE FROM worklog_clockin WHERE id = ?', [row.id]);
        }
      }
    }
    await log('clockin', before[0].cnt === Math.min(remoteIds.size, 2) ? 'ok' : 'diff',
      `打卡：商旅 ${remoteIds.size} 条覆盖本地（本地原 ${before[0].cnt} 条）`);
  }
  return true;
}

async function syncOne(account, date, scope, ctx = null) {
  const ctxTeamId = ctx ? ctx.teamId : account.team_id;
  const ctxMemberId = ctx ? ctx.memberId : account.member_id;
  const log = (type, result, detail) => pool.query(
    `INSERT INTO worklog_sync_log (team_id, member_id, sync_date, scope, type, result, detail)
     VALUES (?, ?, ?, ?, ?, ?, ?)`,
    [ctxTeamId, ctxMemberId, date, scope, type, result, String(detail).slice(0, 500)]
  );

  if (!await syncClockinPart(account, date, log, ctxTeamId, ctxMemberId)) return;

  // 费用对账：以商旅为准回写本地摘要（含成本分配；城市参数补全取模板，缺省模板可能不带默认成本中心）
  const fi = await sgcc.getFeeInfoNew(account.token, await feeTplParams(ctxMemberId, date), devOpt(account));
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
      [ctxMemberId, date]
    );
    const changed = !old.length || Number(old[0].food_fee) !== food || Number(old[0].transit_fee) !== transit
      || String(old[0].cost_center_code || '') !== cc.code;
    await pool.query(
      `INSERT INTO worklog_fee (team_id, member_id, fee_date, food_fee, transit_fee, cost_center_code, cost_center_name, synced_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, NOW())
       ON DUPLICATE KEY UPDATE food_fee = VALUES(food_fee), transit_fee = VALUES(transit_fee),
         cost_center_code = VALUES(cost_center_code), cost_center_name = VALUES(cost_center_name), synced_at = NOW()`,
      [ctxTeamId, ctxMemberId, date, food, transit, cc.code, cc.name]
    );
    await log('fee', changed ? 'diff' : 'ok',
      `费用：伙食 ${food} / 交通 ${transit} / 成本中心 ${cc.code || '无'}${changed ? '，已按商旅覆盖' : '，一致'}`);
  }

  // 费用照片双向对账（以商旅为准）：数据源为费用模板 id=5「上传图片」组件 value（复用费用对账已取的 tpl）
  // 形状守卫：value 不是 JSON 数组（或组件缺失）时跳过对账并留诊断，防止误判「商旅无照片」把本地同步照片删光
  if (tpl) {
    const comp5 = (tpl.dtComponentList || []).find((c) => c.id === 5);
    let imgs = null;
    if (comp5) {
      if (!comp5.value || !String(comp5.value).trim()) {
        imgs = []; // value 为空 = 当日无费用照片（正常）
      } else {
        try {
          const v = JSON.parse(comp5.value);
          if (Array.isArray(v)) imgs = v;
        } catch (e) { /* 落下方诊断 */ }
      }
    }
    if (imgs) {
      console.log(`[商旅打卡] 费用照片对账（成员 ${ctxMemberId} ${date}）：远端 ${imgs.length} 张${imgs[0] ? `，样例键 ${Object.keys(imgs[0]).join('/')}` : ''}`);
      await syncFeePhotos(account, date, imgs, log, { teamId: ctxTeamId, memberId: ctxMemberId });
    } else {
      console.error(`[商旅打卡] 费用照片对账跳过（成员 ${ctxMemberId} ${date}）：comp5 ${comp5 ? `value 原文 ${String(comp5.value).slice(0, 300)}` : '缺失'}`);
      await log('photo', 'fail', `费用照片：上传图片组件形态异常，已跳过对账（详见服务端日志）`);
    }
  }
}

// 整班单日核查（卡片驱动：当日卡上有人即同步——人已调班也经人级解析找到其账号，不论账号现属哪个班；
// 非卡上成员跳过可省一轮商旅 API，也避免照片对账记「成员当日无出工记录」噪音日志）
// onlyMemberIds 传入时仅同步这些成员（卡片级「从商旅同步」：仅本卡用车人）；缺省为当日卡上成员中的全部绑定成员
// onStep：每处理完一名成员回调一次（手动拉取与每日定时核查的进度登记用）
async function syncTeamDay(teamId, date, scope, onlyMemberIds, onStep) {
  // 人级解析：直连优先（ORDER BY + JS 按卡上成员去重取首条），user_id 兜底覆盖调班重名回退路径的旧成员行
  let rows;
  if (Array.isArray(onlyMemberIds)) {
    if (!onlyMemberIds.length) return;
    [rows] = await pool.query(
      `SELECT a.*, m.id AS card_member_id FROM worklog_member m
       JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
       WHERE m.id IN (?)
       ORDER BY (a.member_id = m.id) DESC`,
      [onlyMemberIds]
    );
  } else {
    [rows] = await pool.query(
      `SELECT a.*, m.id AS card_member_id FROM worklog_entry e
       JOIN worklog_entry_member em ON em.entry_id = e.id
       JOIN worklog_member m ON m.id = em.member_id
       JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
       WHERE e.team_id = ? AND e.log_date = ?
       ORDER BY (a.member_id = m.id) DESC`,
      [teamId, date]
    );
  }
  const seen = new Set(); // 人级解析理论可命中多行（直连+兜底），按卡上成员去重
  for (const account of rows) {
    if (seen.has(account.card_member_id)) continue;
    seen.add(account.card_member_id);
    try {
      await syncOne(account, date, scope, { teamId, memberId: account.card_member_id });
    } catch (err) {
      console.error(`[商旅打卡] 核查失败（成员 ${account.card_member_id} ${date}）：`, err.message);
    }
    if (typeof onStep === 'function') onStep(); // 成败均计一步（失败明细见核查记录）
    // 成员间间隔，防商旅侧风控（SGCC_SYNC_INTERVAL_MS，默认 1500ms）
    await new Promise((r) => setTimeout(r, config.sgcc.syncIntervalMs));
  }
}
// 提醒前轻量同步（11:00 开始卡 / 18:00 结束卡提醒共用前置）：对当日卡上绑定成员跨班组逐人做
// 登录态探测 + 打卡对账（scope='remind'），确保提醒判定基于商旅最新数据（成员可能直接在商旅 App 打卡，
// 本地要等 23:00 核查才刷新）。不同步费用/照片（非提醒口径，省整班全量核查开销），不登记进度任务（不锁卡片）。
// 返回失败成员名单 [{member_id, team_id, name, reason}]（登录失效/同步异常均属无法确认打卡状态），
// 由调用方剔除出自动核查并通知人工核查
async function syncCardClockinsForRemind(date) {
  const [rows] = await pool.query(
    `SELECT a.*, m.id AS card_member_id, m.name AS member_name, e.team_id AS card_team_id
     FROM worklog_entry e
     JOIN worklog_entry_member em ON em.entry_id = e.id
     JOIN worklog_member m ON m.id = em.member_id
     JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
     WHERE e.log_date = ? AND e.team_id IS NOT NULL
     ORDER BY (a.member_id = m.id) DESC`,
    [date]
  );
  const seen = new Set(); // 人级解析可命中多行（直连+兜底），按卡上成员去重（跨班卡成员只同步一次）
  const failed = [];
  for (const account of rows) {
    if (seen.has(account.card_member_id)) continue;
    seen.add(account.card_member_id);
    const log = (type, result, detail) => pool.query(
      `INSERT INTO worklog_sync_log (team_id, member_id, sync_date, scope, type, result, detail)
       VALUES (?, ?, ?, 'remind', ?, ?, ?)`,
      [account.card_team_id, account.card_member_id, date, type, result, String(detail).slice(0, 500)]
    );
    try {
      const synced = await syncClockinPart(account, date, log, account.card_team_id, account.card_member_id);
      if (!synced) failed.push({ member_id: account.card_member_id, team_id: account.card_team_id, name: account.member_name, reason: '商旅登录已失效，无法确认打卡状态' });
    } catch (err) {
      console.error(`[商旅打卡] 提醒前同步失败（成员 ${account.card_member_id} ${date}）：`, err.message);
      failed.push({ member_id: account.card_member_id, team_id: account.card_team_id, name: account.member_name, reason: `同步异常：${String(err.message).slice(0, 120)}` });
    }
    // 成员间间隔，防商旅侧风控（SGCC_SYNC_INTERVAL_MS，默认 1500ms）
    await new Promise((r) => setTimeout(r, config.sgcc.syncIntervalMs));
  }
  console.log(`[商旅打卡] 提醒前同步完成：${seen.size} 名卡上绑定成员，失败 ${failed.length} 名（${date}）`);
  return failed;
}

// 提醒前同步失败 → 通知人工核查：微信（个人 wxid）与站内双侧同发 超管 + 涉及班组管理员
//（不发成员本人/班组群——状态未知避免误催；同步成功后的正常提醒才发未打卡人本人 + 所属班组群）
async function notifyRemindManualCheck(failed) {
  const teamIds = [...new Set(failed.map((f) => f.team_id).filter(Boolean))];
  const [admins] = await pool.query("SELECT id FROM sys_user WHERE role = 'admin' AND status = 1");
  let teamAdmins = [];
  if (teamIds.length) {
    [teamAdmins] = await pool.query(
      "SELECT id FROM sys_user WHERE role = 'team_admin' AND team_id IN (?) AND status = 1", [teamIds]
    );
  }
  const userIds = [...new Set([...admins.map((a) => a.id), ...teamAdmins.map((a) => a.id)])];
  if (!userIds.length) return;
  const lines = failed.map((f) => (f.name ? `· ${f.name}：${f.reason}` : `· ${f.reason}`));
  await require('../notice').push({ // 与 dispatch-sync 同型惰性加载
    userIds,
    targets: [],
    title: '打卡核查需人工确认',
    content: `以下 ${failed.length} 名成员提醒前商旅同步失败（打卡状态未知），本次已跳过其自动打卡核查，请人工确认其打卡状态（可先在出工日志「从商旅拉取」手动同步）：\n${lines.join('\n')}`,
    wxUserIds: userIds,
    wxTeamIds: [],
  });
}

// 提醒前置统一入口：轻量同步 → 失败成员通知人工核查并剔除（返回剔除名单）；同步整体失败通知人工核查并返回 null（本轮不自动核查）
async function beforeClockinRemind(date, label) {
  let failed;
  try {
    failed = await syncCardClockinsForRemind(date);
  } catch (err) {
    console.error(`[商旅打卡] ${label}提醒前同步失败：`, err.message);
    try {
      await notifyRemindManualCheck([{ member_id: 0, team_id: null, name: '', reason: `${label}提醒前同步异常：${String(err.message).slice(0, 200)}` }]);
    } catch (e2) {
      console.error(`[商旅打卡] ${label}人工核查通知发送失败：`, e2.message);
    }
    return null;
  }
  if (failed.length) {
    try {
      await notifyRemindManualCheck(failed);
    } catch (err) {
      console.error(`[商旅打卡] ${label}人工核查通知发送失败：`, err.message);
    }
  }
  return failed.map((f) => f.member_id);
}

// ---------- 照片单操作任务（上传/删除/改人名/resync；进程内存登记，TTL 30 分钟，重启清零仅作进度展示） ----------
// 任务化异步执行：路由同步校验后登记任务并立即返回 opId，商旅多成员循环与本地落库/COS 操作在后台执行，
// 端侧凭 GET /sync/active 的 ops（旁观者）与 GET /op/status（发起者收尾）渲染记录卡片进度条。
// 全局串行队列：商旅费用组件为 getFeeInfoNew → 改 → saveFeeInfoNew 的 read-modify-write，串行防并发互踩。
const photoOps = new Map(); // opId → { teamId, kind, entryId, photoId, total, done, status, error, failedName, photoUrl, at }
const PHOTO_OP_TTL = 30 * 60 * 1000;
let photoOpQueue = Promise.resolve();

// 登记任务：prog（{ addTotal, step }）直接写任务记录，供四个多成员商旅循环函数透传（其体内 prog 调用形状不变）
function newPhotoOp(teamId, kind, entryId, photoId) {
  const now = Date.now();
  for (const [k, v] of photoOps) { if (now - v.at > PHOTO_OP_TTL) photoOps.delete(k); } // 新建时顺手清理过期任务
  const id = `op-${teamId}-${now.toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
  const rec = {
    teamId, kind, entryId, photoId: photoId || null,
    total: 0, done: 0, status: 'running', error: '', failedName: '', photoUrl: '', at: now,
  };
  photoOps.set(id, rec);
  return {
    id,
    rec,
    prog: {
      addTotal(n) { rec.total += n; rec.at = Date.now(); },
      step() { rec.done += 1; rec.at = Date.now(); },
    },
  };
}

// 本卡进行中（status=running）的照片任务：entryId 互斥——一卡同时只允许一个照片任务（路由据此 40909 拦截）
function activePhotoOp(teamId, entryId) {
  for (const v of photoOps.values()) {
    if (v.teamId === teamId && v.entryId === entryId && v.status === 'running') return v;
  }
  return null;
}

// 串行执行：fn(rec) 成功置 ok；失败置 fail 并截断记录 error（任务体内可自填 rec.failedName / rec.photoUrl / rec.photoId）
function runPhotoOp(id, fn) {
  const rec = photoOps.get(id);
  photoOpQueue = photoOpQueue.then(async () => {
    try {
      await fn(rec);
      if (rec) rec.status = 'ok';
    } catch (err) {
      if (rec) {
        rec.status = 'fail';
        rec.error = String(err && err.message ? err.message : err).slice(0, 200);
      }
      console.error(`[商旅打卡] 照片任务 ${id}（${rec ? rec.kind : '?'}）失败：`, err && err.message ? err.message : err);
    }
    if (rec) rec.at = Date.now();
  });
}

// GET /op/status?op_id=xxx：单任务状态轮询（仅本班组；发起者收尾巴用，含终态/错误/产物）；
// 任务不存在或已过期按已完成回（与 /sync/progress 同口径，端侧据此收尾）
router.get('/op/status', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const v = photoOps.get(String(req.query.op_id || ''));
    if (!v || v.teamId !== req.team.id) return ok(res, { status: 'ok', done: 0, total: 0 });
    return ok(res, {
      status: v.status, done: v.done, total: v.total,
      error: v.error || '', failedName: v.failedName || '',
      photoId: v.photoId || null, photoUrl: v.photoUrl || '',
    });
  } catch (err) { return next(err); }
});

// ---------- 手动拉取 / 每日核查进度（进程内存登记；重启即清零仅作进度展示） ----------
// kind：batch=区段批量拉取（进行中锁定出工日志子应用，端侧轮询 /sync/active 出全页进度条）；
//       card=卡片级同步（仅本卡进度条）；daily=每日定时核查（当日记录卡片挂进度条且不可操作）
const syncJobs = new Map(); // jobId → { teamId, kind, date, total, done, finished, at }
const SYNC_JOB_TTL = 30 * 60 * 1000; // 任务保留 30 分钟供端侧收尾查询，新建任务时顺手清理过期任务

function newSyncJob(teamId, total, kind, date) {
  for (const [k, j] of syncJobs) {
    if (Date.now() - j.at > SYNC_JOB_TTL) syncJobs.delete(k);
  }
  const id = `${teamId}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
  syncJobs.set(id, { teamId, kind: kind || 'batch', date: date || '', total, done: 0, finished: total <= 0, at: Date.now() }); // 无可同步成员 = 即刻完成
  return id;
}

// 本班组进行中（未完成）的同类任务（batch 同时只允许一个，/sync/pull 已做冲突拦截；daily 由排程器单实例保证）
function activeJob(teamId, kind) {
  let hit = null;
  for (const j of syncJobs.values()) {
    if (j.teamId === teamId && j.kind === kind && !j.finished && (!hit || j.at > hit.at)) hit = j;
  }
  return hit;
}

function stepSyncJob(id) {
  const j = syncJobs.get(id);
  if (j) {
    j.done += 1;
    j.at = Date.now();
  }
}

function finishSyncJob(id) {
  const j = syncJobs.get(id);
  if (j) {
    j.finished = true;
    j.at = Date.now();
  }
}

// POST /sync/pull：手动从商旅拉取 {date: 'YYYY-MM-DD'} 或 {from, to}（区段最多跨 62 天）或 {entry_id}（卡片级：仅该卡用车人），team_id?
// 区段批量拉取（date/from/to）仅超管 / 本班班组管理员可发起；卡片级（entry_id）所有 work-log 权限用户可用：
// 触发本班组指定日期（区段）内当日用车人中的绑定成员从商旅拉取（一律以商旅为准覆盖本地）；
// entry_id 传入时仅同步该卡片的用车人（日期以卡片 log_date 为准，from/to 忽略）；
// team_id 仅超管生效（沿用 resolveReqTeam 口径：班组管理员/普通用户传了也被收敛到本班）；
// 区段逐日串行异步执行，结果见 GET /sync/logs
router.post('/sync/pull', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');

    // 卡片级：仅本卡用车人（日期以卡片为准）
    const entryId = Number((req.body && req.body.entry_id) || 0);
    let onlyMemberIds = null;
    let entryDate = '';
    if (entryId) {
      const [erows] = await pool.query(
        `SELECT id, DATE_FORMAT(log_date, '%Y-%m-%d') AS log_date FROM worklog_entry WHERE id = ? AND team_id = ?`,
        [entryId, req.team.id]
      );
      if (!erows.length) return fail(res, 404, 40400, '日志不存在');
      entryDate = erows[0].log_date;
      const [mrows] = await pool.query('SELECT member_id FROM worklog_entry_member WHERE entry_id = ?', [entryId]);
      onlyMemberIds = mrows.map((r) => r.member_id);
      if (!onlyMemberIds.length) return fail(res, 400, 40042, '该卡片暂无用车人，无需同步');
    }

    // 区段批量拉取仅超管 / 本班班组管理员（口径同 requireDictAdmin；卡片级不受限）
    if (!entryId && req.user.role !== 'admin'
      && !(req.user.role === 'team_admin' && req.user.team_id === req.team.id)) {
      return fail(res, 403, 40304, '仅管理员可执行此操作');
    }

    const from = entryDate || String((req.body && (req.body.from || req.body.date)) || today());
    const to = entryDate || String((req.body && req.body.to) || from);
    if (!DATE_RE.test(from) || !DATE_RE.test(to)) return fail(res, 400, 40000, '日期格式应为 YYYY-MM-DD');
    if (from > to) return fail(res, 400, 40000, '日期区段不正确');
    // 逐日展开（按 UTC 毫秒步进，避免本地时区影响）；最多跨 62 天
    const DAY_MS = 24 * 60 * 60 * 1000;
    const fromMs = Date.parse(`${from}T00:00:00Z`);
    const days = Math.round((Date.parse(`${to}T00:00:00Z`) - fromMs) / DAY_MS) + 1;
    if (days > 62) return fail(res, 400, 40000, '日期区段最多跨 62 天');
    // 区段批量门禁：区段内卡上成员的人级绑定账号任一登录过期即不允许发起（token_status 每晚定时核查保鲜；卡片级单卡同步不设门禁）
    if (!entryId) {
      const [expired] = await pool.query(
        `SELECT DISTINCT m.name AS nm
         FROM worklog_entry e
         JOIN worklog_entry_member em ON em.entry_id = e.id
         JOIN worklog_member m ON m.id = em.member_id
         JOIN worklog_sgcc_account a ON (a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)) AND a.token_status = 0
         WHERE e.team_id = ? AND e.log_date BETWEEN ? AND ?`,
        [req.team.id, from, to]
      );
      if (expired.length) {
        return fail(res, 409, 40910, `以下成员商旅登录已过期，请先重新绑定后再发起批量同步：${expired.map((x) => x.nm).join('、')}`);
      }
    }
    // 批量拉取进行中锁定出工日志子应用：同班组同时只允许一个批量任务（卡片级不在此限）
    if (!entryId && activeJob(req.team.id, 'batch')) {
      return fail(res, 409, 40909, '本班组已有批量从商旅同步进行中，请等待完成后再发起');
    }
    // 进度登记：总单元 = 区段内「日 × 卡上绑定成员」数（卡片级 = 本卡绑定用车人数；卡片驱动人级口径与 syncTeamDay 一致），
    // 端侧凭 jobId 轮询 GET /sync/progress 渲染进度条
    let total = 0;
    if (entryId) {
      const [c] = await pool.query(
        `SELECT COUNT(DISTINCT m.id) AS cnt FROM worklog_member m
         JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
         WHERE m.id IN (?)`,
        [onlyMemberIds]
      );
      total = c[0].cnt;
    } else {
      const [c] = await pool.query(
        `SELECT COUNT(DISTINCT m.id, e.log_date) AS cnt
         FROM worklog_entry e
         JOIN worklog_entry_member em ON em.entry_id = e.id
         JOIN worklog_member m ON m.id = em.member_id
         JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
         WHERE e.team_id = ? AND e.log_date BETWEEN ? AND ?`,
        [req.team.id, from, to]
      );
      total = c[0].cnt;
    }
    const jobId = newSyncJob(req.team.id, total, entryId ? 'card' : 'batch');
    (async () => {
      for (let i = 0; i < days; i += 1) {
        const d = new Date(fromMs + i * DAY_MS).toISOString().slice(0, 10);
        await syncTeamDay(req.team.id, d, 'daily', onlyMemberIds || undefined, () => stepSyncJob(jobId));
      }
    })()
      .catch((err) => console.error('[商旅打卡] 手动拉取失败：', err.message))
      .finally(() => finishSyncJob(jobId));
    // 响应文案口径：M月D日（不补零）
    const md = (s) => `${Number(s.slice(5, 7))}月${Number(s.slice(8, 10))}日`;
    const msg = entryId
      ? `已发起从商旅拉取（${md(from)}，仅本卡用车人）`
      : from === to ? `已发起从商旅拉取（${md(from)}）` : `已发起从商旅拉取（${md(from)} 至 ${md(to)}）`;
    return ok(res, { jobId, total }, msg);
  } catch (err) { return next(err); }
});

// GET /sync/progress?job_id=xxx：手动拉取进度轮询（仅本班组任务可查；任务不存在 / 已过期按已完成回，端侧据此收尾）
router.get('/sync/progress', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const j = syncJobs.get(String(req.query.job_id || ''));
    if (!j || j.teamId !== req.team.id) return ok(res, { total: 0, done: 0, finished: true });
    return ok(res, { total: j.total, done: j.done, finished: j.finished });
  } catch (err) { return next(err); }
});

// GET /sync/active：本班组同步进行态。batch = 区段批量拉取（出工日志子应用全局锁：任何端任何人进入时据此出进度遮罩，完成后解锁）；
// daily = 每日定时核查（仅当日本班：当日记录卡片挂进度条且不可操作，完成后解锁）；
// ops = 本班组进行中的照片单操作任务（上传/删除/改人名/resync：对应记录卡片挂进度条且整卡锁定，旁观者据此挂条）
router.get('/sync/active', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const b = activeJob(req.team.id, 'batch');
    const d = activeJob(req.team.id, 'daily');
    const dailyToday = d && d.date === today() ? d : null; // 仅当日核查参与卡片锁定
    const ops = [];
    for (const [opId, v] of photoOps) {
      if (v.teamId === req.team.id && v.status === 'running') {
        ops.push({ opId, kind: v.kind, entryId: v.entryId, photoId: v.photoId, total: v.total, done: v.done });
      }
    }
    return ok(res, {
      running: !!b,
      total: b ? b.total : 0,
      done: b ? b.done : 0,
      daily: dailyToday ? { running: true, total: dailyToday.total, done: dailyToday.done } : { running: false },
      ops,
    });
  } catch (err) { return next(err); }
});

// GET /sync/logs：核查记录。?month=YYYY-MM（按月、近 100 条，原口径）或 ?from=&to=（按区段、近 200 条，批量同步面板日志区用）
router.get('/sync/logs', async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const from = DATE_RE.test(String(req.query.from || '')) ? String(req.query.from) : '';
    const to = DATE_RE.test(String(req.query.to || '')) ? String(req.query.to) : '';
    const rangeSql = from && to && from <= to;
    const [rows] = rangeSql
      ? await pool.query(
          `SELECT l.id, DATE_FORMAT(l.sync_date, '%Y-%m-%d') AS sync_date, l.scope, l.type, l.result, l.detail,
                  DATE_FORMAT(l.created_at, '%Y-%m-%d %H:%i') AS created_at, m.name AS member_name
           FROM worklog_sync_log l LEFT JOIN worklog_member m ON m.id = l.member_id
           WHERE l.team_id = ? AND l.sync_date BETWEEN ? AND ?
           ORDER BY l.id DESC LIMIT 200`,
          [req.team.id, from, to]
        )
      : await pool.query(
          `SELECT l.id, DATE_FORMAT(l.sync_date, '%Y-%m-%d') AS sync_date, l.scope, l.type, l.result, l.detail,
                  DATE_FORMAT(l.created_at, '%Y-%m-%d %H:%i') AS created_at, m.name AS member_name
           FROM worklog_sync_log l LEFT JOIN worklog_member m ON m.id = l.member_id
           WHERE l.team_id = ? AND DATE_FORMAT(l.sync_date, '%Y-%m') = ?
           ORDER BY l.id DESC LIMIT 100`,
          [req.team.id, /^\d{4}-\d{2}$/.test(String(req.query.month || '')) ? req.query.month : today().slice(0, 7)]
        );
    return ok(res, { list: rows });
  } catch (err) { return next(err); }
});

// DELETE /sync/logs：管理员一键清除本班组全部同步记录（批量同步面板「清除同步记录」）
router.delete('/sync/logs', requireDictAdmin, async (req, res, next) => {
  try {
    if (!req.team) return fail(res, 403, 40310, '未分配班组，请联系管理员分配');
    const [r] = await pool.query('DELETE FROM worklog_sync_log WHERE team_id = ?', [req.team.id]);
    return ok(res, { deleted: r.affectedRows }, `已清除 ${r.affectedRows} 条同步记录`);
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
// 每日核查附加检查：当日已开始打卡但结束打卡未打的成员 → 按人投放通知本人 + 本班班组管理员 + 超管（见 16.3）
// 账号联表人级解析（member_id 直连或经 member.user_id 兜底）：调班后旧班打卡行的本人仍能收到通知
// 微信侧：本人个人 wxid 按人随站内通知发送；打卡班组群在循环后合并为一条（多人缺卡时群消息不逐人刷屏）
async function notifyMissingEndClockin(teamId, date) {
  const [rows] = await pool.query(
    `SELECT c.member_id, m.name, MAX(a.user_id) AS user_id,
            DATE_FORMAT(MAX(CASE WHEN c.seq = 1 THEN c.clock_time END), '%H:%i') AS start_hm
     FROM worklog_clockin c
     JOIN worklog_member m ON m.id = c.member_id
     LEFT JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
     WHERE c.team_id = ? AND c.clock_date = ?
     GROUP BY c.member_id, m.name
     HAVING MAX(CASE WHEN c.seq = 1 THEN 1 ELSE 0 END) = 1
        AND MAX(CASE WHEN c.seq = 2 THEN 1 ELSE 0 END) = 0`,
    [teamId, date]
  );
  if (!rows.length) return;
  const [admins] = await pool.query("SELECT id FROM sys_user WHERE role = 'admin' AND status = 1");
  const [teamAdmins] = await pool.query(
    "SELECT id FROM sys_user WHERE role = 'team_admin' AND team_id = ? AND status = 1", [teamId]
  );
  const managerIds = [...admins.map((a) => a.id), ...teamAdmins.map((a) => a.id)];
  for (const r of rows) {
    const userIds = [...new Set([r.user_id, ...managerIds].filter(Boolean))];
    if (!userIds.length) continue;
    // 微信推送：仅本人个人 wxid（班组群在循环后合并一条发送）
    await require('../notice').push({
      userIds,
      targets: [],
      title: '结束打卡未打',
      content: `成员「${r.name}」今日${r.start_hm ? `${r.start_hm} 已打开始卡` : '已打开始卡'}，尚未打结束卡，请提醒本人尽快补打（仅当日可打卡）。`,
      wxUserIds: r.user_id ? [r.user_id] : [],
    });
  }
  // 打卡班组群：合并为一条，逐行列出全部未打结束卡成员
  const lines = rows.map((r) => `· ${r.name}${r.start_hm ? `（${r.start_hm} 已打开始卡）` : ''}`);
  const res = await require('../notice/wxpush').sendTo({
    userIds: [],
    teamIds: [teamId],
    text: `结束打卡未打\n今日以下 ${rows.length} 名成员已打开始卡、尚未打结束卡，请提醒本人尽快补打（仅当日可打卡）：\n${lines.join('\n')}`,
    user: `clockin-missing-team-${teamId}`,
  });
  if (!res.ok) console.warn(`[商旅打卡] 结束打卡未打群通知未发送（班组 ${teamId}）：${res.reason || res.error}`);
}

// 每日核查结果日报：每班一封，按人投放 超管 + 本班班组管理员（每日都发，含全部正常）。
// 仅统计当次核查新产生的记录（afterId 分界，排除当日早些时候手动拉取的流水）；
// 汇总 一致/有差异已回写/失败 项数，失败项逐条全量列出
async function notifyDailySyncResult(teamId, date, afterId) {
  const [trows] = await pool.query('SELECT name FROM sys_team WHERE id = ?', [teamId]);
  const teamName = trows.length ? trows[0].name : String(teamId);
  const [rows] = await pool.query(
    `SELECT l.type, l.result, l.detail, l.member_id, m.name AS member_name
     FROM worklog_sync_log l LEFT JOIN worklog_member m ON m.id = l.member_id
     WHERE l.team_id = ? AND l.sync_date = ? AND l.id > ? ORDER BY l.id`,
    [teamId, date, afterId]
  );
  const TYPE_NAME = { clockin: '打卡', fee: '费用', photo: '照片', auth: '登录态' };
  const cnt = { ok: 0, diff: 0, fail: 0 };
  const memberIds = new Set();
  const fails = [];
  for (const r of rows) {
    cnt[r.result] = (cnt[r.result] || 0) + 1;
    if (r.member_id) memberIds.add(r.member_id);
    if (r.result === 'fail') fails.push(r);
  }
  const lines = [];
  if (!rows.length) {
    lines.push('今日无卡上绑定成员，未执行数据核查（仅做登录态探测）。');
  } else {
    lines.push(`今日核查 ${memberIds.size} 名成员：一致 ${cnt.ok || 0} / 回写 ${cnt.diff || 0} / 失败 ${cnt.fail || 0}`);
    if (fails.length) {
      lines.push('', '失败明细：');
      fails.forEach((r) => {
        lines.push(`- ${r.member_name || `成员${r.member_id || '?'}`}｜${TYPE_NAME[r.type] || r.type}：${r.detail}`);
      });
    }
  }
  const [admins] = await pool.query("SELECT id FROM sys_user WHERE role = 'admin' AND status = 1");
  const [teamAdmins] = await pool.query(
    "SELECT id FROM sys_user WHERE role = 'team_admin' AND team_id = ? AND status = 1", [teamId]
  );
  const userIds = [...new Set([...admins.map((a) => a.id), ...teamAdmins.map((a) => a.id)])];
  if (!userIds.length) return;
  // 微信推送：该班组群
  await require('../notice').push({
    userIds,
    targets: [],
    title: `商旅每日核查完成（${teamName}）`,
    content: lines.join('\n'),
    wxTeamIds: [teamId],
  });
}

function scheduleDaily() {
  const [hh, mm] = String(config.sgcc.syncTime || '23:00').split(':').map((s) => parseInt(s, 10));
  const { nextUtc, now } = nextDailyRunUtc(hh || 23, mm || 0);
  const timer = setTimeout(async () => {
    try {
      // 卡片驱动：有绑定账号的班 ∪ 当日卡上有绑定成员的班（token 跟人走后，卡在哪个班就同步哪个班）
      const [teams] = await pool.query(
        `SELECT DISTINCT team_id FROM (
           SELECT team_id FROM worklog_sgcc_account WHERE team_id IS NOT NULL
           UNION
           SELECT e.team_id FROM worklog_entry e
           JOIN worklog_entry_member em ON em.entry_id = e.id
           JOIN worklog_member m ON m.id = em.member_id
           JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
           WHERE e.log_date = ? AND e.team_id IS NOT NULL
         ) t`,
        [today()]
      );
      for (const t of teams) {
        // 日报分界：仅统计当次核查新产生的记录（排除当日早些时候手动拉取的流水）
        const [beforeRows] = await pool.query(
          'SELECT COALESCE(MAX(id), 0) AS maxId FROM worklog_sync_log WHERE team_id = ?',
          [t.team_id]
        );
        // 进度登记（kind=daily，仅当日）：端侧据此给当日记录卡片挂进度条并置不可操作（卡片驱动口径同 syncTeamDay）
        const [c] = await pool.query(
          `SELECT COUNT(DISTINCT m.id) AS cnt
           FROM worklog_entry e
           JOIN worklog_entry_member em ON em.entry_id = e.id
           JOIN worklog_member m ON m.id = em.member_id
           JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
           WHERE e.team_id = ? AND e.log_date = ?`,
          [t.team_id, today()]
        );
        const jobId = newSyncJob(t.team_id, c[0].cnt, 'daily', today());
        try {
          await syncTeamDay(t.team_id, today(), 'daily', undefined, () => stepSyncJob(jobId));
        } finally {
          finishSyncJob(jobId);
        }
        // 其余绑定账号（当日卡上成员以外）也做一次登录态探测：token_status 每晚保鲜，
        // 1→0 跳变由 probeAuth 内自动通知本人+超管；间隔同防风控口径
        const [rest] = await pool.query(
          `SELECT a.* FROM worklog_sgcc_account a
           WHERE a.team_id = ? AND a.id NOT IN (
             SELECT DISTINCT a2.id
             FROM worklog_entry e
             JOIN worklog_entry_member em ON em.entry_id = e.id
             JOIN worklog_member m ON m.id = em.member_id
             JOIN worklog_sgcc_account a2 ON a2.member_id = m.id OR (m.user_id IS NOT NULL AND a2.user_id = m.user_id)
             WHERE e.team_id = ? AND e.log_date = ?)`,
          [t.team_id, t.team_id, today()]
        );
        for (const acc of rest) {
          await probeAuth(acc);
          await new Promise((r) => setTimeout(r, config.sgcc.syncIntervalMs));
        }
        // 每日核查附加检查：开始打卡已打但结束打卡未打 → 通知本人/本班班组管理员/超管
        try {
          await notifyMissingEndClockin(t.team_id, today());
        } catch (err) {
          console.error('[商旅打卡] 结束打卡缺失检查失败：', err.message);
        }
        // 每日核查结果日报（每班一封，含全部正常）→ 超管 + 本班班组管理员（见 16.3）
        try {
          await notifyDailySyncResult(t.team_id, today(), beforeRows[0].maxId);
        } catch (err) {
          console.error('[商旅打卡] 每日核查日报通知失败：', err.message);
        }
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

// 结束打卡傍晚提醒：每日 SGCC_ENDCLOCK_REMIND_TIME（默认 18:00）对「当日已开始打卡但结束卡未打」的成员
// 提前提醒（23:00 每日核查前给本人留补打时间）；仅发微信（本人个人 wxid 按人 + 打卡班组群每班合并一条），不写站内通知。
// 提醒前由排程经 beforeClockinRemind 先跑轻量同步（登录态+打卡对账），按商旅最新数据判定；
// excludeIds = 同步失败成员（状态未知，已转人工核查通知），本函数对其不自动提醒
async function remindMissingEndClockin(excludeIds = []) {
  // 口径同 notifyMissingEndClockin（账号联表人级解析），但跨全部班组一次查出
  const exSql = excludeIds.length ? ' AND c.member_id NOT IN (?)' : '';
  const [rows] = await pool.query(
    `SELECT c.team_id, c.member_id, m.name, MAX(a.user_id) AS user_id,
            DATE_FORMAT(MAX(CASE WHEN c.seq = 1 THEN c.clock_time END), '%H:%i') AS start_hm
     FROM worklog_clockin c
     JOIN worklog_member m ON m.id = c.member_id
     LEFT JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
     WHERE c.clock_date = ?${exSql}
     GROUP BY c.team_id, c.member_id, m.name
     HAVING MAX(CASE WHEN c.seq = 1 THEN 1 ELSE 0 END) = 1
        AND MAX(CASE WHEN c.seq = 2 THEN 1 ELSE 0 END) = 0`,
    excludeIds.length ? [today(), excludeIds] : [today()]
  );
  if (!rows.length) {
    console.log('[商旅打卡] 结束打卡傍晚提醒：今日无未打结束卡成员');
    return;
  }
  const wxpush = require('../notice/wxpush'); // 惰性加载（同 notice 口径）
  const teamMap = new Map(); // team_id → 成员名单：班组群提醒按班合并一条（多人缺卡时群消息不逐人刷屏）
  for (const r of rows) {
    // 本人个人微信按人发送（内容针对本人）
    if (r.user_id) {
      const res = await wxpush.sendTo({
        userIds: [r.user_id],
        teamIds: [],
        text: `结束打卡提醒\n成员「${r.name}」今日${r.start_hm ? `${r.start_hm} 已打开始卡` : '已打开始卡'}，尚未打结束卡，请尽快补打（仅当日可打卡）。`,
        user: `clockin-remind-${r.member_id}`,
      });
      if (!res.ok) console.warn(`[商旅打卡] 结束打卡提醒未发送（成员「${r.name}」）：${res.reason || res.error}`);
    }
    if (r.team_id) {
      if (!teamMap.has(r.team_id)) teamMap.set(r.team_id, []);
      teamMap.get(r.team_id).push(r);
    }
  }
  for (const [teamId, list] of teamMap) { // 打卡班组群：每班合并一条，逐行列出未打结束卡成员
    const lines = list.map((r) => `· ${r.name}${r.start_hm ? `（${r.start_hm} 已打开始卡）` : ''}`);
    const res = await wxpush.sendTo({
      userIds: [],
      teamIds: [teamId],
      text: `结束打卡提醒\n今日以下 ${list.length} 名成员已打开始卡、尚未打结束卡，请提醒本人尽快补打（仅当日可打卡）：\n${lines.join('\n')}`,
      user: `clockin-remind-team-${teamId}`,
    });
    if (!res.ok) console.warn(`[商旅打卡] 结束打卡提醒群消息未发送（班组 ${teamId}）：${res.reason || res.error}`);
  }
  console.log(`[商旅打卡] 结束打卡傍晚提醒完成：${rows.length} 名成员未打结束卡`);
}

function scheduleEndClockRemind() {
  const [hh, mm] = String(config.sgcc.endClockRemindTime || '18:00').split(':').map((s) => parseInt(s, 10));
  const { nextUtc, now } = nextDailyRunUtc(hh || 18, mm || 0);
  const timer = setTimeout(async () => {
    // 先对当日卡上绑定成员做打卡轻量同步（成员可能直接在商旅 App 打卡），再按最新数据核查；
    // 同步失败成员不自动核查（状态未知），转「打卡核查需人工确认」通知；整体失败则本轮不核查
    try {
      const exclude = await beforeClockinRemind(today(), '结束打卡');
      if (exclude) await remindMissingEndClockin(exclude);
    } catch (err) {
      console.error('[商旅打卡] 结束打卡傍晚提醒失败：', err.message);
    }
    scheduleEndClockRemind(); // 排次日
  }, nextUtc - now);
  timer.unref();
  console.log(`[商旅打卡] 结束打卡傍晚提醒已排程：${new Date(nextUtc).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' })}（北京时间）`);
}
scheduleEndClockRemind();

// 开始打卡午间提醒：每日 SGCC_STARTCLOCK_REMIND_TIME（默认 11:00）对「当日卡上已绑定商旅但未打开始卡（首次打卡）」的成员
// 提前提醒（给本人留补打时间）；口径同核验规则 a（未绑定不参与），仅发微信（本人个人 wxid 按人 + 卡片班组群每班合并一条），不写站内通知。
// 提醒前由排程经 beforeClockinRemind 先跑轻量同步（登录态+打卡对账），按商旅最新数据判定；
// excludeIds = 同步失败成员（状态未知，已转人工核查通知），本函数对其不自动提醒
async function remindMissingStartClockin(excludeIds = []) {
  // 卡片驱动：当日卡上成员 ∩ 已绑定账号（账号联表人级解析，调班/跨班卡均可命中）；无当日 seq=1 打卡行即未首次打卡
  const exSql = excludeIds.length ? ' AND m.id NOT IN (?)' : '';
  const [rows] = await pool.query(
    `SELECT m.id AS member_id, m.name, MAX(a.user_id) AS user_id,
            GROUP_CONCAT(DISTINCT e.team_id) AS team_ids
     FROM worklog_entry e
     JOIN worklog_entry_member em ON em.entry_id = e.id
     JOIN worklog_member m ON m.id = em.member_id
     JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
     LEFT JOIN worklog_clockin c ON c.member_id = m.id AND c.clock_date = ? AND c.seq = 1
     WHERE e.log_date = ? AND e.team_id IS NOT NULL AND c.id IS NULL${exSql}
     GROUP BY m.id, m.name`,
    excludeIds.length ? [today(), today(), excludeIds] : [today(), today()]
  );
  if (!rows.length) {
    console.log('[商旅打卡] 开始打卡午间提醒：今日卡上成员均已首次打卡');
    return;
  }
  const wxpush = require('../notice/wxpush'); // 惰性加载（同 notice 口径）
  const teamMap = new Map(); // team_id → 成员姓名名单：班组群提醒按班合并一条（跨班卡成员计入每个涉及班组）
  for (const r of rows) {
    // 本人个人微信按人发送（内容针对本人）
    if (r.user_id) {
      const res = await wxpush.sendTo({
        userIds: [r.user_id],
        teamIds: [],
        text: `开始打卡提醒\n成员「${r.name}」今日有出工卡片，尚未打开始卡（首次打卡），请尽快打卡（仅当日可打卡）。`,
        user: `clockin-remind-${r.member_id}`,
      });
      if (!res.ok) console.warn(`[商旅打卡] 开始打卡提醒未发送（成员「${r.name}」）：${res.reason || res.error}`);
    }
    const teamIds = String(r.team_ids || '').split(',').map(Number).filter(Boolean);
    for (const tid of teamIds) {
      if (!teamMap.has(tid)) teamMap.set(tid, []);
      teamMap.get(tid).push(r.name);
    }
  }
  for (const [teamId, names] of teamMap) { // 卡片班组群：每班合并一条，逐行列出未首次打卡成员
    const res = await wxpush.sendTo({
      userIds: [],
      teamIds: [teamId],
      text: `开始打卡提醒\n今日以下 ${names.length} 名成员有出工卡片、尚未打开始卡（首次打卡），请提醒本人尽快打卡（仅当日可打卡）：\n${names.map((n) => `· ${n}`).join('\n')}`,
      user: `clockin-remind-team-${teamId}`,
    });
    if (!res.ok) console.warn(`[商旅打卡] 开始打卡提醒群消息未发送（班组 ${teamId}）：${res.reason || res.error}`);
  }
  console.log(`[商旅打卡] 开始打卡午间提醒完成：${rows.length} 名成员未首次打卡`);
}

function scheduleStartClockRemind() {
  const [hh, mm] = String(config.sgcc.startClockRemindTime || '11:00').split(':').map((s) => parseInt(s, 10));
  const { nextUtc, now } = nextDailyRunUtc(hh || 11, mm || 0);
  const timer = setTimeout(async () => {
    // 先对当日卡上绑定成员做打卡轻量同步（成员可能直接在商旅 App 打卡），再按最新数据核查；
    // 同步失败成员不自动核查（状态未知），转「打卡核查需人工确认」通知；整体失败则本轮不核查
    try {
      const exclude = await beforeClockinRemind(today(), '开始打卡');
      if (exclude) await remindMissingStartClockin(exclude);
    } catch (err) {
      console.error('[商旅打卡] 开始打卡午间提醒失败：', err.message);
    }
    scheduleStartClockRemind(); // 排次日
  }, nextUtc - now);
  timer.unref();
  console.log(`[商旅打卡] 开始打卡午间提醒已排程：${new Date(nextUtc).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' })}（北京时间）`);
}
scheduleStartClockRemind();

module.exports = router;
// 供 worklog 照片上传（远端先行）/改人名补传/删除钩子调用（config.sgcc.enabled 守卫在调用方）
module.exports.syncPhotoToSgcc = syncPhotoToSgcc;
module.exports.uploadPhotoToMembersRemote = uploadPhotoToMembersRemote;
module.exports.unlinkPhotoFromSgcc = unlinkPhotoFromSgcc;
module.exports.removePhotoMembersRemote = removePhotoMembersRemote;
// 供 worklog 上传/改人名/删除路由登记照片单操作任务（任务化异步执行，端侧轮询 /sync/active 与 /op/status）
module.exports.newPhotoOp = newPhotoOp;
module.exports.activePhotoOp = activePhotoOp;
module.exports.runPhotoOp = runPhotoOp;
