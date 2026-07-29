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
const multer = require('multer');
const cookieParser = require('cookie-parser');
const jwt = require('jsonwebtoken');
const crypto = require('crypto');

const store = require('./src/store');
const { mergePdfs } = require('./src/merge');
const dify = require('./src/dify');

const PORT = Number(process.env.PORT) || 3000;
// 主平台（检修一班工具箱）API 根地址，如 https://j1net.com/j1toolkit
const J1TOOLKIT_API_URL = (process.env.J1TOOLKIT_API_URL || '').trim().replace(/\/+$/, '');
if (!J1TOOLKIT_API_URL) {
  console.warn('[safeday] 未配置 J1TOOLKIT_API_URL，登录接口将不可用');
}
// 登录会话 JWT 签名密钥；未配置则随机兜底（重启后全部会话失效）
const SESSION_SECRET = process.env.SESSION_SECRET || crypto.randomBytes(32).toString('hex');
if (!process.env.SESSION_SECRET) {
  console.warn('[safeday] 未配置 SESSION_SECRET，已使用随机密钥（服务重启后所有登录会话失效）');
}
const SESSION_EXPIRES = process.env.SESSION_EXPIRES || '7d';
const COOKIE_SECURE = process.env.COOKIE_SECURE === 'true';
// 反代路径前缀（如 /sdl；1Panel 不剥前缀转发时配置，与主平台同口径）
const PROXY_PREFIX = (process.env.PROXY_PREFIX || '').trim();
// 登录所需的主平台应用标识（sys_app.app_key）
const APP_KEY = 'safe-day';
const COOKIE_NAME = 'sdl_session';
const DOCS_DIR = path.join(ROOT, 'docs');
const UPLOADS_DIR = path.join(ROOT, 'uploads');
const DATA_DIR = path.join(ROOT, 'data');

// 启动时确保目录存在
for (const dir of [DOCS_DIR, UPLOADS_DIR, DATA_DIR]) {
  fs.mkdirSync(dir, { recursive: true });
}

const ALLOWED_EXT = ['pdf', 'doc', 'docx', 'ppt', 'pptx', 'xls', 'xlsx'];
const DATE_RE = /^\d{4}\.\d{2}\.\d{2}$/;

const app = express();

// 反代前缀兼容：1Panel/Nginx 若保留 PROXY_PREFIX 前缀转发（proxy_pass 无 URI 部分），
// 这里剥离后再进入路由；前缀不存在时不影响任何请求（与主平台同口径）
app.use((req, res, next) => {
  if (PROXY_PREFIX && req.url.startsWith(`${PROXY_PREFIX}/`)) {
    req.url = req.url.slice(PROXY_PREFIX.length);
  } else if (PROXY_PREFIX && req.url === PROXY_PREFIX) {
    req.url = '/';
  }
  next();
});

app.use(express.json());
app.use(cookieParser());

// ---------- 登录会话（账号与权限由主平台「检修一班工具箱」统一校验） ----------
// 签发会话 JWT，并换算 cookie 有效期（毫秒）
function signSession(user) {
  const token = jwt.sign(
    { uid: user.id, username: user.username, nickname: user.nickname },
    SESSION_SECRET,
    { expiresIn: SESSION_EXPIRES }
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

// 登录：转发主平台 /auth/app-login，一次完成「账号密码 + safe-day 应用权限」校验
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

    let resp;
    let data;
    try {
      resp = await fetch(`${J1TOOLKIT_API_URL}/api/v1/auth/app-login`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password, app_key: APP_KEY }),
        signal: AbortSignal.timeout(10000),
      });
      data = await resp.json().catch(() => null);
    } catch (e) {
      console.error(`[safeday] 主平台登录校验请求失败（${J1TOOLKIT_API_URL}/api/v1/auth/app-login）：${e && e.message ? e.message : e}`);
      return res.status(502).json({ ok: false, error: '主平台登录服务不可用，请稍后重试' });
    }
    if (!resp.ok || !data || data.code !== 0) {
      const code = data && data.code;
      if (code === 40111) {
        return res.status(401).json({ ok: false, error: '账号或密码错误' });
      }
      if (code === 40301) {
        return res.status(403).json({ ok: false, error: '暂无「安全日活动记录」应用权限，请联系管理员开通' });
      }
      console.error(`[safeday] 主平台登录校验返回异常：HTTP ${resp.status} ${JSON.stringify(data)}`);
      return res.status(502).json({
        ok: false,
        error: (data && data.message) || `主平台响应异常（${resp.status}）`,
      });
    }

    const user = data.data.user;
    const { token, maxAge } = signSession(user);
    res.cookie(COOKIE_NAME, token, {
      httpOnly: true,
      sameSite: 'lax',
      secure: COOKIE_SECURE,
      path: '/',
      maxAge,
    });
    return res.json({ ok: true, user });
  } catch (e) {
    return res.status(500).json({
      ok: false,
      error: `登录处理失败：${e && e.message ? e.message : e}`,
    });
  }
});

// 退出登录：清除会话 cookie（无会话也可调用）
app.post('/api/logout', (req, res) => {
  res.clearCookie(COOKIE_NAME, { path: '/' });
  return res.json({ ok: true });
});

// ---------- 登录门控 ----------
app.use((req, res, next) => {
  // Dify 回调豁免（自带 CALLBACK_TOKEN 校验）
  if (req.path === '/api/callback') return next();
  // 其余 API 一律要求登录（/api/login、/api/logout 已在上方先行注册）
  if (req.path.startsWith('/api/')) return requireAuth(req, res, next);
  // 主页面仅登录后可见；未登录重定向到登录页（相对 Location，兼容反代前缀部署）
  if ((req.path === '/' || req.path === '/index.html') && !readSession(req)) {
    return res.redirect('login.html');
  }
  return next();
});

const upload = multer({
  storage: multer.memoryStorage(),
  limits: { fileSize: 50 * 1024 * 1024, files: 10 },
});

function getExt(fileName) {
  const idx = fileName.lastIndexOf('.');
  return idx === -1 ? '' : fileName.slice(idx + 1).toLowerCase();
}

// 一次性判定：文件存在即置 done，不存在即置 failed
// （Dify 回调到来时文件应已写完；不存在说明工作流未产出，判定失败）
function judgeOnce(record) {
  const filePath = path.join(DOCS_DIR, path.basename(record.fileName));
  let exists = false;
  try {
    exists = fs.statSync(filePath).isFile();
  } catch (e) {
    exists = false;
  }
  if (exists) {
    store.update(record.id, { status: 'done' });
    return 'done';
  }
  store.update(record.id, {
    status: 'failed',
    error: `回调后未检测到生成文件：${record.fileName}`,
  });
  return 'failed';
}

// ---------- 静态资源 ----------
app.use(express.static(path.join(ROOT, 'public')));

// favicon 和公安备案图标放在项目根目录（保持现有布局）
app.get('/safe.svg', (req, res) => {
  res.sendFile(path.join(ROOT, 'safe.svg'));
});
app.get('/public-security.png', (req, res) => {
  res.sendFile(path.join(ROOT, 'public-security.png'));
});

// ---------- API ----------
// 当前登录用户（门控已保证会话有效）
app.get('/api/me', (req, res) => {
  const { uid, username, nickname } = req.user;
  return res.json({ ok: true, user: { id: uid, username, nickname } });
});

// 生成记录：上传文件 + 触发 Dify 工作流
app.post('/api/generate', upload.array('files', 10), async (req, res) => {
  try {
    const files = req.files || [];
    // multer 1.x 默认按 latin1 解析文件名，中文名需转回 UTF-8
    for (const f of files) {
      f.originalname = Buffer.from(f.originalname, 'latin1').toString('utf8');
    }
    const name = String(req.body.name || '').trim();
    const date = String(req.body.date || '').trim();

    if (files.length < 1) {
      return res.status(400).json({ ok: false, error: '请至少上传一个文件' });
    }
    if (!DATE_RE.test(date)) {
      return res.status(400).json({ ok: false, error: '日期格式不正确，应为 YYYY.MM.DD' });
    }
    if (!name) {
      return res.status(400).json({ ok: false, error: '请填写学习文件名称' });
    }

    // 扩展名白名单校验
    for (const f of files) {
      const ext = getExt(f.originalname);
      if (!ALLOWED_EXT.includes(ext)) {
        return res.status(400).json({
          ok: false,
          error: `不支持的文件格式：${f.originalname}（仅支持 ${ALLOWED_EXT.join('/')}）`,
        });
      }
    }

    // 多文件（≥2）时所有文件必须是 .pdf（纯 npm 合并方案）
    if (files.length >= 2 && files.some((f) => getExt(f.originalname) !== 'pdf')) {
      return res.status(400).json({
        ok: false,
        error: '多文件合并仅支持 PDF 格式，请上传 PDF 文件或改为单文件上传',
      });
    }

    // 合并或取单文件 buffer
    let fileBuffer;
    let fileName;
    if (files.length >= 2) {
      try {
        fileBuffer = await mergePdfs(files.map((f) => f.buffer));
      } catch (e) {
        return res.status(400).json({
          ok: false,
          error: `PDF 合并失败：${e && e.message ? e.message : e}`,
        });
      }
      fileName = 'merged.pdf';
    } else {
      fileBuffer = files[0].buffer;
      fileName = files[0].originalname;
    }

    // 先建记录（同一 date 只保留最新一条）
    const record = store.create({
      name,
      date,
      fileName: `${date}.docx`,
      status: 'processing',
      sourceCount: files.length,
    });

    // 上传 Dify 并触发工作流（触发后立即返回，不等工作流完成）
    try {
      await dify.uploadAndRun({
        fileBuffer,
        fileName,
        date,
        name,
        onFailed: (error) => {
          store.update(record.id, { status: 'failed', error });
        },
      });
    } catch (e) {
      const error = e && e.message ? e.message : String(e);
      store.update(record.id, { status: 'failed', error });
      return res.status(500).json({ ok: false, error });
    }

    return res.json({ ok: true, record });
  } catch (e) {
    return res.status(500).json({
      ok: false,
      error: `生成请求处理失败：${e && e.message ? e.message : e}`,
    });
  }
});

// 记录列表（纯读取；完成判定只在 Dify 回调时进行一次，防止误判运行中的空文件）
app.get('/api/records', (req, res) => {
  try {
    return res.json({ ok: true, records: store.list() });
  } catch (e) {
    return res.status(500).json({
      ok: false,
      error: `读取记录失败：${e && e.message ? e.message : e}`,
    });
  }
});

// 下载产物
app.get('/api/records/:id/download', (req, res) => {
  const record = store.get(req.params.id);
  if (!record || record.status !== 'done') {
    return res.status(404).json({ ok: false, error: '记录不存在或文件尚未生成' });
  }
  const filePath = path.join(DOCS_DIR, record.fileName);
  if (!fs.existsSync(filePath)) {
    return res.status(404).json({ ok: false, error: '文件不存在，可能已被清理' });
  }
  return res.download(filePath, record.fileName);
});

// 删除记录（连带删除已生成的 docx 文件）
app.delete('/api/records/:id', (req, res) => {
  try {
    const record = store.remove(req.params.id);
    if (!record) {
      return res.status(404).json({ ok: false, error: '记录不存在' });
    }
    if (record.fileName) {
      const filePath = path.join(DOCS_DIR, path.basename(record.fileName));
      try {
        if (fs.existsSync(filePath)) {
          fs.unlinkSync(filePath);
        }
      } catch (e) {
        return res.json({
          ok: true,
          warning: `记录已删除，但文件删除失败：${e && e.message ? e.message : e}`,
        });
      }
    }
    return res.json({ ok: true });
  } catch (e) {
    return res.status(500).json({
      ok: false,
      error: `删除失败：${e && e.message ? e.message : e}`,
    });
  }
});

// Dify 工作流结束回调（可选，配合 CALLBACK_TOKEN 使用）
app.post('/api/callback', (req, res) => {
  const token = process.env.CALLBACK_TOKEN;
  if (token && req.query.token !== token) {
    return res.status(403).json({ ok: false, error: '回调 token 校验失败' });
  }
  try {
    const body = req.body || {};
    const date = typeof body.date === 'string' ? body.date.trim() : '';
    let records = store.list().filter((r) => r.status === 'processing');
    if (date) {
      records = records.filter((r) => r.date === date);
    }
    // 回调即终判：仅检查一次文件是否存在，存在 → done，不存在 → failed
    let done = 0;
    let failed = 0;
    for (const record of records) {
      if (judgeOnce(record) === 'done') {
        done++;
      } else {
        failed++;
      }
    }
    return res.json({ ok: true, done, failed });
  } catch (e) {
    return res.status(500).json({
      ok: false,
      error: `回调处理失败：${e && e.message ? e.message : e}`,
    });
  }
});

// multer 错误（超限等）与兜底错误处理，统一返回 { ok:false, error }
app.use((err, req, res, next) => {
  if (err instanceof multer.MulterError) {
    let error = `文件上传失败：${err.message}`;
    if (err.code === 'LIMIT_FILE_SIZE') {
      error = '文件大小超出限制（单文件最大 50MB）';
    } else if (err.code === 'LIMIT_FILE_COUNT' || err.code === 'LIMIT_UNEXPECTED_FILE') {
      error = '文件数量超出限制（最多 10 个）';
    }
    return res.status(400).json({ ok: false, error });
  }
  if (err) {
    return res.status(500).json({
      ok: false,
      error: `服务器内部错误：${err && err.message ? err.message : err}`,
    });
  }
  return next();
});

app.listen(PORT, '0.0.0.0', () => {
  console.log(`[safeday] 服务已启动: http://0.0.0.0:${PORT}`);
});
