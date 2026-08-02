// 极简 .env 解析器（不依赖 dotenv）：文件存在才读，KEY=VALUE 逐行，
// 忽略空行和 # 注释，不覆盖已存在的 process.env
const fs = require('fs');
const path = require('path');

const ROOT = __dirname;
const envPath = path.join(ROOT, '.env');
if (fs.existsSync(envPath)) {
  for (const line of fs.readFileSync(envPath, 'utf8').split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#')) continue;
    const eq = trimmed.indexOf('=');
    if (eq === -1) continue;
    const key = trimmed.slice(0, eq).trim();
    const value = trimmed.slice(eq + 1).trim();
    if (key && process.env[key] === undefined) {
      process.env[key] = value;
    }
  }
}

const express = require('express');
const cookieParser = require('cookie-parser');
const jwt = require('jsonwebtoken');
const crypto = require('crypto');
const archiver = require('archiver');

const PORT = Number(process.env.PORT) || 3000;
// 主平台（检修一班工具箱）API 根地址，如 https://j1net.com/j1toolkit
const J1TOOLKIT_API_URL = (process.env.J1TOOLKIT_API_URL || '').trim().replace(/\/+$/, '');
if (!J1TOOLKIT_API_URL) {
  console.warn('[worklogs] 未配置 J1TOOLKIT_API_URL，登录与业务代理将不可用');
}
// 登录会话 JWT 签名密钥；未配置则随机兜底（重启后全部会话失效）
const SESSION_SECRET = process.env.SESSION_SECRET || crypto.randomBytes(32).toString('hex');
if (!process.env.SESSION_SECRET) {
  console.warn('[worklogs] 未配置 SESSION_SECRET，已使用随机密钥（服务重启后所有登录会话失效）');
}
// 会话名义有效期；实际与主平台 JWT 同寿命（主平台默认 7d，见 signSession 注释）
const SESSION_EXPIRES = process.env.SESSION_EXPIRES || '7d';
const COOKIE_SECURE = process.env.COOKIE_SECURE === 'true';
// 反代路径前缀（如 /wl；1Panel 不剥前缀转发时配置，与主平台同口径）
const PROXY_PREFIX = (process.env.PROXY_PREFIX || '').trim().replace(/\/+$/, '');
const COOKIE_NAME = 'wl_session';

const app = express();

// 反代前缀兼容：1Panel/Nginx 若保留 PROXY_PREFIX 前缀转发（proxy_pass 无 URI 部分），
// 这里剥离后再进入路由；前缀不存在时不影响任何请求（与主平台同口径）。
// 裸前缀（/wl，无尾斜杠）先 302 补尾斜杠：否则页内相对路径会按域根解析，丢失前缀
app.use((req, res, next) => {
  if (PROXY_PREFIX && req.url === PROXY_PREFIX) {
    return res.redirect(`${PROXY_PREFIX}/`);
  }
  if (PROXY_PREFIX && req.url.startsWith(`${PROXY_PREFIX}/`)) {
    req.url = req.url.slice(PROXY_PREFIX.length);
  }
  next();
});

// 照片 base64 与 ZIP 清单请求体较大：这两类路由单独放宽 JSON 上限
// （须在全局 express.json() 之前注册，body-parser 对已解析的请求自动跳过）
app.use('/api/wl', express.json({ limit: '20mb' }));
app.use('/api/zip', express.json({ limit: '5mb' }));
app.use(express.json());
app.use(cookieParser());

// ---------- 登录会话（账号、权限与全部业务数据均来自主平台，本站不存业务数据） ----------
// 签发会话 JWT：payload 携带主平台 JWT（pt）；会话寿命取主平台 token 的 exp-iat 差值
// （主平台 JWT 默认 7d，会话与其同寿命；SESSION_EXPIRES 仅作解码失败时的兜底）
function signSession(user, platformToken) {
  let expiresIn = SESSION_EXPIRES;
  const platform = jwt.decode(platformToken);
  if (platform && platform.exp && platform.iat) {
    expiresIn = platform.exp - platform.iat;
  }
  const token = jwt.sign(
    {
      uid: user.id,
      username: user.username,
      nickname: user.nickname,
      role: user.role,
      pt: platformToken,
    },
    SESSION_SECRET,
    { expiresIn }
  );
  const decoded = jwt.decode(token);
  return { token, maxAge: (decoded.exp - decoded.iat) * 1000 };
}

// 读取并校验会话 cookie，失败返回 null
function readSession(req) {
  const token = req.cookies ? req.cookies[COOKIE_NAME] : null;
  if (!token) return null;
  try {
    return jwt.verify(token, SESSION_SECRET);
  } catch (e) {
    return null;
  }
}

// API 鉴权：无有效会话一律 401（前端据此跳转登录页）
function requireAuth(req, res, next) {
  const payload = readSession(req);
  if (!payload) {
    return res.status(401).json({ ok: false, error: '未登录或登录已过期' });
  }
  req.user = payload;
  return next();
}

// 登录：第一步主平台账号密码登录拿 token，第二步带 token 访问 worklog/meta
// 校验「出工日志」应用权限，两步都通过才签发本地会话
app.post('/api/login', async (req, res) => {
  try {
    const username = String((req.body && req.body.username) || '').trim();
    const password = String((req.body && req.body.password) || '');
    if (!username || !password) {
      return res.status(400).json({ ok: false, error: '请输入账号和密码' });
    }
    if (!J1TOOLKIT_API_URL) {
      return res.status(503).json({ ok: false, error: '登录服务未配置（J1TOOLKIT_API_URL）' });
    }

    // 第一步：主平台账号密码登录
    let loginResp;
    let loginData;
    try {
      loginResp = await fetch(`${J1TOOLKIT_API_URL}/api/v1/auth/login`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
        signal: AbortSignal.timeout(10000),
      });
      loginData = await loginResp.json().catch(() => null);
    } catch (e) {
      console.error(`[worklogs] 主平台登录请求失败（${J1TOOLKIT_API_URL}/api/v1/auth/login）：${e && e.message ? e.message : e}`);
      return res.status(502).json({ ok: false, error: '主平台登录服务不可用，请稍后重试' });
    }
    if (!loginResp.ok || !loginData || loginData.code !== 0) {
      const code = loginData && loginData.code;
      if (code === 40111) {
        return res.status(401).json({ ok: false, error: '账号或密码错误' });
      }
      console.error(`[worklogs] 主平台登录返回异常：HTTP ${loginResp.status} ${JSON.stringify(loginData)}`);
      return res.status(502).json({
        ok: false,
        error: (loginData && loginData.message) || `主平台响应异常（${loginResp.status}）`,
      });
    }

    // 第二步：用主平台 token 访问 worklog/meta，校验「出工日志」应用权限
    const platformToken = loginData.data.token;
    let metaResp;
    let metaData;
    try {
      metaResp = await fetch(`${J1TOOLKIT_API_URL}/api/v1/worklog/meta`, {
        headers: { Authorization: `Bearer ${platformToken}` },
        signal: AbortSignal.timeout(10000),
      });
      metaData = await metaResp.json().catch(() => null);
    } catch (e) {
      console.error(`[worklogs] 主平台权限校验请求失败（${J1TOOLKIT_API_URL}/api/v1/worklog/meta）：${e && e.message ? e.message : e}`);
      return res.status(502).json({ ok: false, error: '主平台登录服务不可用，请稍后重试' });
    }
    if (!metaResp.ok || !metaData || metaData.code !== 0) {
      const code = metaData && metaData.code;
      if (code === 40301) {
        return res.status(403).json({ ok: false, error: '暂无「出工日志」应用权限，请联系管理员开通' });
      }
      console.error(`[worklogs] 主平台权限校验返回异常：HTTP ${metaResp.status} ${JSON.stringify(metaData)}`);
      return res.status(502).json({
        ok: false,
        error: (metaData && metaData.message) || `主平台响应异常（${metaResp.status}）`,
      });
    }

    // 第三步：签发本地会话（主平台 JWT 只存于服务端会话内，前端不可见）
    const user = loginData.data.user;
    const { token, maxAge } = signSession(user, platformToken);
    res.cookie(COOKIE_NAME, token, {
      httpOnly: true,
      sameSite: 'lax',
      secure: COOKIE_SECURE,
      path: '/',
      maxAge,
    });
    return res.json({
      ok: true,
      user: { id: user.id, username: user.username, nickname: user.nickname, role: user.role },
    });
  } catch (e) {
    return res.status(500).json({
      ok: false,
      error: `登录处理失败：${e && e.message ? e.message : e}`,
    });
  }
});

// 退出登录：尽力通知主平台注销（失败忽略），再清除本地会话 cookie（无会话也可调用）
app.post('/api/logout', async (req, res) => {
  const session = readSession(req);
  if (session && session.pt && J1TOOLKIT_API_URL) {
    try {
      await fetch(`${J1TOOLKIT_API_URL}/api/v1/auth/logout`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${session.pt}` },
        signal: AbortSignal.timeout(10000),
      });
    } catch (e) {
      // 主平台注销失败不影响本地退出
    }
  }
  res.clearCookie(COOKIE_NAME, { path: '/' });
  return res.json({ ok: true });
});

// ---------- 登录门控 ----------
app.use((req, res, next) => {
  // 其余 API 一律要求登录（/api/login、/api/logout 已在上方先行注册）
  if (req.path.startsWith('/api/')) return requireAuth(req, res, next);
  // 主页面仅登录后可见；未登录重定向到登录页。
  // 用含 PROXY_PREFIX 的绝对路径：相对 Location 在裸前缀（/wl，无尾斜杠）下
  // 会被浏览器按域根解析丢失前缀；反代剥/不剥前缀两种模式下绝对路径均正确
  if ((req.path === '/' || req.path === '/index.html') && !readSession(req)) {
    return res.redirect(`${PROXY_PREFIX}/login.html`);
  }
  return next();
});

// ---------- 静态资源 ----------
app.use(express.static(path.join(ROOT, 'public')));

// favicon 放在项目根目录（与 SafeDayLogs 布局一致）
app.get('/worklogs.svg', (req, res) => {
  res.sendFile(path.join(ROOT, 'worklogs.svg'));
});

// ---------- API ----------
// 当前登录用户（门控已保证会话有效）
app.get('/api/me', (req, res) => {
  const { uid, username, nickname, role } = req.user;
  return res.json({ ok: true, user: { id: uid, username, nickname, role } });
});

// 业务代理：/api/wl/* → 主平台 /api/v1/worklog/*
// 服务端持主平台 JWT（会话内 pt）转发，前端只见本地会话 cookie；
// 主平台统一信封 {code,message,data} 与状态码原样回传（含 401，前端据此跳登录）
app.use('/api/wl', async (req, res) => {
  if (!J1TOOLKIT_API_URL) {
    return res.status(503).json({ ok: false, error: '业务代理未配置（J1TOOLKIT_API_URL）' });
  }
  if (!req.user || !req.user.pt) {
    return res.status(401).json({ ok: false, error: '未登录或登录已过期' });
  }
  // 照片上传含水印渲染，耗时较长，单独放宽超时
  const isPhotoUpload = req.method === 'POST' && /^\/logs\/[^/]+\/photos\/?$/.test(req.path);
  const timeout = isPhotoUpload ? 120000 : 30000;
  try {
    const options = {
      method: req.method,
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${req.user.pt}`,
      },
      signal: AbortSignal.timeout(timeout),
    };
    if (req.method !== 'GET' && req.method !== 'HEAD') {
      options.body = JSON.stringify(req.body || {});
    }
    // req.url 为剥离 /api/wl 后的路径（含 query），直接拼到主平台 worklog 根上
    const upstream = await fetch(`${J1TOOLKIT_API_URL}/api/v1/worklog${req.url}`, options);
    const text = await upstream.text();
    res.status(upstream.status);
    const contentType = upstream.headers.get('content-type');
    if (contentType) res.set('Content-Type', contentType);
    return res.send(text);
  } catch (e) {
    console.error(`[worklogs] 代理请求失败（${req.method} ${req.originalUrl}）：${e && e.message ? e.message : e}`);
    return res.status(502).json({ ok: false, error: '主平台服务不可用，请稍后重试' });
  }
});

// ---------- 照片 ZIP 打包下载 ----------
const ZIP_MAX_PHOTOS = 300;
const PHOTO_MAX_BYTES = 50 * 1024 * 1024;

// 文件名净化：去路径分隔符与非法字符，防止 zip 内路径穿越
function sanitizeFileName(name) {
  const cleaned = String(name || '')
    .replace(/[\\/:*?"<>|\x00-\x1f]/g, '_')
    .trim();
  return cleaned || 'photo.jpg';
}

// 批量打包：逐张下载照片流式写入 zip；失败记录进清单继续，全部失败才报错
app.post('/api/zip', async (req, res) => {
  const photos = req.body && Array.isArray(req.body.photos) ? req.body.photos : [];
  if (!photos.length) {
    return res.status(400).json({ ok: false, error: '没有可下载的照片' });
  }
  if (photos.length > ZIP_MAX_PHOTOS) {
    return res.status(400).json({ ok: false, error: `一次最多打包 ${ZIP_MAX_PHOTOS} 张照片` });
  }
  const list = [];
  for (const p of photos) {
    const url = String((p && p.url) || '');
    if (!/^https?:\/\//i.test(url)) {
      return res.status(400).json({ ok: false, error: '照片地址不合法（仅支持 http/https）' });
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
  archive.on('warning', (e) => console.warn(`[worklogs] ZIP 警告：${e && e.message ? e.message : e}`));
  archive.on('error', (e) => console.error(`[worklogs] ZIP 错误：${e && e.message ? e.message : e}`));
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
      console.warn(`[worklogs] 照片下载失败（${item.url}）：${e && e.message ? e.message : e}`);
      failed.push(item);
    }
  }

  if (success === 0) {
    // 尚无字节写出，可安全改回 JSON 错误响应
    archive.unpipe(res);
    archive.destroy();
    res.removeHeader('Content-Type');
    res.removeHeader('Content-Disposition');
    return res.status(500).json({ ok: false, error: '照片下载失败' });
  }

  if (failed.length) {
    const content = failed.map((f) => `${f.name} ${f.url}`).join('\n');
    archive.append(content, { name: '下载失败清单.txt' });
  }

  try {
    await archive.finalize();
  } catch (e) {
    console.error(`[worklogs] ZIP 打包失败：${e && e.message ? e.message : e}`);
    archive.destroy();
  }
});

// 兜底错误处理（含 body 解析失败/超限），统一返回 { ok:false, error }
app.use((err, req, res, next) => {
  if (!err) return next();
  if (res.headersSent) return next(err);
  if (err.type === 'entity.too.large') {
    return res.status(413).json({ ok: false, error: '请求体过大' });
  }
  if (err.type === 'entity.parse.failed') {
    return res.status(400).json({ ok: false, error: '请求体不是合法 JSON' });
  }
  return res.status(err.status || 500).json({
    ok: false,
    error: `服务器内部错误：${err && err.message ? err.message : err}`,
  });
});

app.listen(PORT, '0.0.0.0', () => {
  console.log(`[worklogs] 服务已启动: http://0.0.0.0:${PORT}`);
});
