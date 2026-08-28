// 商旅平台协议层 —— 移植自逆向分析仓 esgcc/sgcc/tools/sgcc_client.js（全部已实测验证，勿改口径）
// 两条通道：
//   jsonm（H5，gwslapi）：AES-128-ECB + SM2 加密 AES 密钥；用于短信登录与 uniID 池
//   jsonx（App，gwslapi/gwslapizb）：RSA 分块加密（117B/128B，PKCS1）；
//     tenant=default 用 RSA_PUB/RSA_PRIV；tenant=slapp（新通道，费用保存与密码登录必须走它）用 dCu 加密 / wLA 解密
// 注意：两对 RSA 密钥各自只管一个方向（请求加密公钥 / 响应解密私钥模数并不相同）
// 2026-08-28 登录链路升级（顶象风控）：商旅登录按风控动态出图形码或顶象滑块；
//   图形验证码（validcodeimg）对云服务器 IP 间歇可用（风控窗口期返 99000，平峰正常），
//   故做双通道：图形码优先（体验好、无需 web-view），99000/失败自动降级顶象滑块接力：
//   captchaToken（滑块成功凭据）+ constId（顶象设备指纹）由前端滑块页
//   （server/public/sgcc-captcha.html，appId 取自当日抓包）采集后随登录接口上送。
const crypto = require('crypto');
const https = require('https');
const { sm2 } = require('sm-crypto');
const config = require('../config');

const HOST_H5 = 'gwslapi.esgcc.com.cn';   // jsonm 与 jsonx(default)
const HOST_ZB = 'gwslapizb.esgcc.com.cn'; // jsonx(slapp)
const VERSION = config.sgcc.version || '3.3.5';

// ---------- 基础工具 ----------
function b64url(b) { return Buffer.from(b).toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, ''); }
function toPem(b64, label) {
  const body = String(b64 || '').replace(/\s+/g, '');
  return `-----BEGIN ${label}-----\n${body.match(/.{1,64}/g).join('\n')}\n-----END ${label}-----`;
}
// 配置缺失时给出明确错误（模块启用但密钥未配置）
function need(name, v) {
  if (!v) throw new Error(`缺少环境变量 ${name}（商旅打卡协议密钥，见 .env.example）`);
  return v;
}

// H5/App 自签 JWT（HS256）：tenant 型用于 /api/ads；sub=slapp 型用于 jsonx 请求 uniID
function makeJwt(kind = 'tenant') {
  const now = Math.floor(Date.now() / 1000);
  const h = b64url(JSON.stringify({ alg: 'HS256', typ: 'JWT' }));
  const payload = kind === 'tenant'
    ? { tenant: 'newh5', jti: crypto.randomUUID(), exp: now + 120, iat: now }
    : { sub: 'slapp', jti: crypto.randomUUID(), exp: now + 120 };
  const p = b64url(JSON.stringify(payload));
  const sig = b64url(crypto.createHmac('sha256', need('SGCC_JWT_SECRET', config.sgcc.jwtSecret)).update(h + '.' + p).digest());
  return h + '.' + p + '.' + sig;
}

// ---------- jsonm 通道（AES + SM2）----------
function aesEncrypt(plainText, key16) {
  const c = crypto.createCipheriv('aes-128-ecb', Buffer.from(key16, 'utf8'), null);
  return Buffer.concat([c.update(plainText, 'utf8'), c.final()]).toString('base64');
}
function aesDecrypt(b64, key16) {
  const d = crypto.createDecipheriv('aes-128-ecb', Buffer.from(key16, 'utf8'), null);
  return Buffer.concat([d.update(Buffer.from(b64, 'base64')), d.final()]).toString('utf8');
}
function genAESKey() {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  let s = '';
  for (let i = 0; i < 16; i += 1) s += chars[crypto.randomInt(0, chars.length)];
  return s;
}
function sm2Encrypt(text) { return '04' + sm2.doEncrypt(text, need('SGCC_SM2_SERVER_PUB', config.sgcc.sm2ServerPub), 0); }
function sm2Decrypt(hexWith04) { return sm2.doDecrypt(hexWith04.slice(2), need('SGCC_SM2_CLIENT_PRIV', config.sgcc.sm2ClientPriv), 0); }

// sign 头（jsonm）：非空键逗号连接 + ';' + MD5(值串联 + r)
function makeSignHeaderM(headers, r) {
  const order = ['token', 'uniID', 'tenant', 'funcCode', 'systemCode'];
  let keys = ''; let vals = '';
  for (const k of order) {
    const v = headers[k];
    if (v) { keys += (keys ? ',' : '') + k; vals += v; }
  }
  if (keys) keys += ';';
  return keys + crypto.createHash('md5').update(vals + String(r), 'utf8').digest('hex');
}

// ---------- jsonx 通道（RSA 分块）----------
function rsaEncryptLong(pubPem, text) {
  const data = Buffer.from(text, 'utf8');
  const out = [];
  for (let i = 0; i < data.length; i += 117) {
    out.push(crypto.publicEncrypt({ key: pubPem, padding: crypto.constants.RSA_PKCS1_PADDING }, data.subarray(i, i + 117)));
  }
  return Buffer.concat(out).toString('base64');
}
function rsaDecryptLong(privPem, b64) {
  const data = Buffer.from(String(b64).replace(/\s+/g, ''), 'base64');
  const out = [];
  for (let i = 0; i + 128 <= data.length; i += 128) {
    out.push(crypto.privateDecrypt({ key: privPem, padding: crypto.constants.RSA_PKCS1_PADDING }, data.subarray(i, i + 128)));
  }
  return Buffer.concat(out).toString('utf8');
}

// ---------- 底层 HTTP ----------
function post(host, path, headers, body) {
  return new Promise((resolve, reject) => {
    const req = https.request({
      hostname: host, path: '/api/' + path.replace(/^\//, ''), method: 'POST',
      headers: { ...headers, 'Content-Length': Buffer.byteLength(body) },
    }, (res) => {
      const chunks = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => resolve({ status: res.statusCode, raw: Buffer.concat(chunks).toString('utf8') }));
    });
    req.on('error', reject);
    req.setTimeout(20000, () => req.destroy(new Error('timeout')));
    req.write(body);
    req.end();
  });
}

// ---------- jsonm 调用（短信登录/ads 池）----------
let JWT_POOL = []; // /api/ads 换取的一次性 uniID 池
async function initSession() {
  const r = 1000000000 + crypto.randomInt(0, 8700000000);
  const key = genAESKey();
  const body = JSON.stringify({
    data: aesEncrypt(JSON.stringify({ adClientId: 'android', adSecret: '2u9tdiPtM7dUzekPqMc6Yyo85WUhpial', _r: r }), key),
    sign: sm2Encrypt(key),
  });
  const uniID = makeJwt('tenant');
  const headers = {
    'Content-Type': 'application/jsonm', 'Accept': 'application/jsonm',
    'tenant': 'newh5', 'uniID': uniID, 'version': VERSION, 'X-Requested-With': 'XMLHttpRequest',
  };
  headers.sign = makeSignHeaderM(headers, r);
  const resp = await post(HOST_H5, 'ads', headers, body);
  const j = JSON.parse(resp.raw);
  const dkey = sm2Decrypt(j.sign);
  const decoded = JSON.parse(aesDecrypt(j.data, dkey));
  if (resp.status === 200 && decoded.data && decoded.data.list) {
    JWT_POOL = decoded.data.list.slice();
    return true;
  }
  throw new Error('商旅 ads 初始化失败：HTTP ' + resp.status);
}

// jsonm 业务调用（登录三步用；uniID 从池取）
async function callJsonm(path, plainObj) {
  if (!JWT_POOL.length) await initSession();
  const uniID = JWT_POOL.shift();
  const r = 1000000000 + crypto.randomInt(0, 8700000000);
  const key = genAESKey();
  const body = JSON.stringify({ data: aesEncrypt(JSON.stringify({ ...(plainObj || {}), _r: r }), key), sign: sm2Encrypt(key) });
  const headers = {
    'Content-Type': 'application/jsonm', 'Accept': 'application/jsonm',
    'tenant': 'newh5', 'uniID': uniID, 'version': VERSION, 'X-Requested-With': 'XMLHttpRequest',
  };
  headers.sign = makeSignHeaderM(headers, r);
  const resp = await post(HOST_H5, path, headers, body);
  let decoded = null;
  try {
    const j = JSON.parse(resp.raw);
    decoded = JSON.parse(aesDecrypt(j.data, sm2Decrypt(j.sign)));
  } catch (e) { /* 未加密或异常响应 */ }
  return { status: resp.status, decoded };
}

// ---------- jsonx 调用（打卡/费用业务）----------
// tenant='default'：default 通道（RSA_PUB 加密 / RSA_PRIV 解密，主机 gwslapi）
// tenant='slapp'：  slapp 通道（dCu 加密 / wLA 解密，主机 gwslapizb；saveFeeInfoNew 必须走它）
async function callJsonx(path, plainObj, { token, tenant = 'default', deviceType = 'Pixel 7', systemVersion = 'Android 13' } = {}) {
  const isSlapp = tenant === 'slapp';
  const pubPem = toPem(need(isSlapp ? 'SGCC_DCU_PUB' : 'SGCC_RSA_PUB', isSlapp ? config.sgcc.dcuPub : config.sgcc.rsaPub), 'PUBLIC KEY');
  const privPem = toPem(need(isSlapp ? 'SGCC_WLA_PRIV' : 'SGCC_RSA_PRIV', isSlapp ? config.sgcc.wlaPriv : config.sgcc.rsaPriv), 'PRIVATE KEY');
  const r = 1000000000 + crypto.randomInt(0, 8700000000);
  const body = rsaEncryptLong(pubPem, JSON.stringify({ ...(plainObj || {}), _r: r }));
  const uniID = makeJwt('app');
  const headers = {
    'Content-Type': 'application/jsonx;charset=utf-8', 'accept': 'application/jsonx',
    'user-agent': 'http/3.9.4', 'source': '1',
    'tenant': tenant, 'uniID': uniID,
    'platform': isSlapp ? '1' : 'android',
    'systemVersion': systemVersion, 'deviceType': deviceType,
    'encFlag': '2', 'version': VERSION,
  };
  if (isSlapp) headers.secretKeyType = '2';
  if (token) headers.token = token;
  // App 版 sign：keyList「token,uniID,tenant;」+ md5(值串联 + r)
  let keyList = ''; let vals = '';
  for (const [k, sep] of [['token', ','], ['uniID', ','], ['tenant', ';']]) {
    const v = headers[k];
    if (v && v !== 'null') { keyList += k + sep; vals += v; }
  }
  headers.sign = keyList + crypto.createHash('md5').update(vals + String(r), 'utf8').digest('hex');
  const resp = await post(isSlapp ? HOST_ZB : HOST_H5, path, headers, body);
  let decoded = null;
  try { decoded = JSON.parse(rsaDecryptLong(privPem, resp.raw)); } catch (e) { /* 异常响应 */ }
  return { status: resp.status, decoded, raw: resp.raw.slice(0, 300) };
}

// ---------- 业务封装 ----------

// 短信登录双通道（见文件头注释）：
//   图形码通道（优先，风控平峰可用）：validcodeimg 取图 → sendSmsCode/v2 发短信
//   滑块通道（降级，顶象）：滑块页采集 captchaToken + constId → sendSmsCode/v3 发短信
// 两条通道最后都走 smstoken/v4 短信换 token（图形码通道 captchaToken 传空即可，与旧口径一致）

// 取图形验证码（裸 base64；风控窗口期会 99000，路由层据此降级滑块）
async function loginCaptcha(mobile) {
  const r = await callJsonm('user/validcodeimg', { mobile });
  const img = r.decoded && r.decoded.data;
  if (!img) throw new Error('获取图形验证码失败：' + JSON.stringify(r.decoded || r.status));
  return img;
}
// 图形码通道发短信（v2）
async function loginSendSmsV2(mobile, checkImgCode) {
  const r = await callJsonm('user/sendSmsCode/v2', { mobile, checkImgCode });
  return r.decoded;
}

// 滑块通道发短信（v3）：顶象滑块 token + constId 由前端滑块页采集
// 注意 v3 失败时 HTTP/statusCode 仍是 200，成败要看 data.code（成功无 data 或 data.code=0）
async function loginSendSms(mobile, { captchaToken, constId } = {}) {
  const r = await callJsonm('user/sendSmsCode/v3', {
    mobile, source: '3', constId: constId || '', captchaToken: captchaToken || '', riskFlag: 'Y',
  });
  return r.decoded;
}
async function loginBySms(mobile, checkCode, { captchaToken, constId } = {}) {
  const r = await callJsonm('user/smstoken/v4', {
    mobile, checkCode, source: '3', constId: constId || '', captchaToken: captchaToken || '', riskFlag: 'Y',
  });
  const d = r.decoded && r.decoded.data;
  const token = d && (d.token || d.accessToken || (typeof d === 'string' ? d : null));
  if (!token) throw new Error('短信登录失败：' + JSON.stringify(r.decoded || r.status));
  return { token };
}

// 账号密码登录（双通道，与短信同理按风控动态出图形码/滑块）：
//   图形码通道 token/v3（checkImgCode）；滑块通道 user/token/v4（顶象 captchaToken + constId）
// 注意 token/v3 无前缀（user/token/v3 为 404）；jsonm/jsonx-default 均 503，按 token/v4 同口径走 slapp（通道待云端实测）
async function loginByPasswordV3(mobile, password, checkImgCode) {
  const r = await callJsonx('token/v3', {
    mobile, password, checkImgCode, source: '3', constId: '', captchaToken: '', riskFlag: 'Y',
  }, { tenant: 'slapp' });
  const d = r.decoded && r.decoded.data;
  const token = d && (d.token || d.accessToken || (typeof d === 'string' ? d : null));
  if (!token) throw new Error('密码登录失败：' + JSON.stringify(r.decoded || r.status));
  return { token };
}

// 滑块通道密码登录（user/token/v4）：jsonm/jsonx-default 通道均 503，必须走 slapp 通道（实测 App 口径）
async function loginByPassword(mobile, password, { captchaToken, constId } = {}) {
  const r = await callJsonx('user/token/v4', {
    mobile, password, source: '3', constId: constId || '', captchaToken: captchaToken || '', riskFlag: 'Y',
  }, { tenant: 'slapp' });
  const d = r.decoded && r.decoded.data;
  const token = d && (d.token || d.accessToken || (typeof d === 'string' ? d : null));
  if (!token) throw new Error('密码登录失败：' + JSON.stringify(r.decoded || r.status));
  return { token };
}

// 某日打卡详情（token 校验探测也用它：statusCode=200 即登录态有效）
async function dayNew(token, date, opt = {}) {
  const r = await callJsonx('clockin/calendar/dayNew', {
    clockInDate: date, orderNo: '', postClockInNo: '', reimOrderNo: '', informantId: '', isSubmitPlatform: '',
  }, { token, ...opt });
  return r.decoded;
}

// 打卡（首次/二次/更新）：seq=1 首次 markNew；seq=2 带 NextClockIn；更新走 updateMark
async function markNew(token, { clockInDate, cityCode, cityName, position, longitude, latitude, remarks }, isSecond, opt = {}) {
  const body = { clockInDate, cityCode, cityName, position, longitude, latitude, remarks };
  if (isSecond) body.NextClockIn = true;
  const r = await callJsonx('clockin/markNew', body, { token, ...opt });
  return r.decoded;
}
async function updateMark(token, { detailId, cityCode, cityName, position, longitude, latitude }, opt = {}) {
  const r = await callJsonx('clockin/updateMark', { detailId, cityCode, cityName, position, longitude, latitude }, { token, ...opt });
  return r.decoded;
}

// 费用模板（提交装配的数据源）
async function getFeeInfoNew(token, { clockInDate, cityName = '', cityCode = '', position = '' }, opt = {}) {
  const r = await callJsonx('clockin/getFeeInfoNew',
    { clockInDate, cityName, cityCode, position, isPostMark: '0' }, { token, ...opt });
  return r.decoded;
}

// DtComponentListBean 78 字段表（Moshi 语义：原始类型恒输出，装箱/String/List 仅非空）
// 取自 classes4.dex ApplyDetailBean$DataBean$DtComponentListBean（逆向分析仓 esgcc/sgcc）
const COMP_FIELDS = require('./comp_fields.json');
function toMoshi(comp) {
  const out = {};
  for (const f of COMP_FIELDS) {
    const v = comp[f.name];
    if (f.type === 'I' || f.type === 'J') out[f.name] = v == null ? 0 : Number(v);
    else if (f.type === 'Z') out[f.name] = v == null ? false : Boolean(v);
    else if (v !== undefined && v !== null) out[f.name] = v;
  }
  return out;
}

// 费用保存（PatrolFeeModel.d 实锤结构；slapp 通道）
// overrides: { compId: valueString } 按组件 id 覆盖 value/data（如 id=10 补助明细、id=5 上传图片）
async function saveFeeInfoNew(token, date, clockTemplate, overrides = {}, opt = {}) {
  const comps = clockTemplate.dtComponentList.map((c) => {
    if (overrides[c.id] !== undefined) return toMoshi({ ...c, value: overrides[c.id], data: overrides[c.id] });
    return toMoshi(c);
  });
  const tplBean = {
    id: clockTemplate.id, name: clockTemplate.name, createtime: clockTemplate.createtime,
    type: clockTemplate.type, no: clockTemplate.no, dtComponentList: comps,
  };
  const payload = { clockInDate: date, changeSubsidyFlag: 0, dtContentDetail: { dtContent: JSON.stringify(tplBean) } };
  const r = await callJsonx('clockin/saveFeeInfoNew', payload, { token, tenant: 'slapp', ...opt });
  return r.decoded;
}

// 费用凭证照片上传（返 {id, imageUrl}；再经 saveFeeInfoNew 上传图片组件关联入当日费用）
async function reimbEnclosureAdd(token, { imgBase64Str, fileName, fileSize, ext = '.png' }, opt = {}) {
  const r = await callJsonx('clockin/reimbEnclosure/add', { imgBase64Str, ext, fileName, fileSize }, { token, ...opt });
  return r.decoded;
}

module.exports = {
  loginCaptcha, loginSendSmsV2, loginSendSms, loginBySms, loginByPasswordV3, loginByPassword,
  dayNew, markNew, updateMark, getFeeInfoNew, saveFeeInfoNew, reimbEnclosureAdd,
};
