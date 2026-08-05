// 安全日活动记录路由（自 SafeDayLogs 独立服务合并而来，业务逻辑与请求/响应形状保持同构：
// 响应仍为 { ok, error, ... }，非主平台 {code,message,data} 信封）
// 鉴权：/callback 凭 SAFEDAY_CALLBACK_TOKEN 校验（不做登录）；其余接口需登录 + safe-day 应用权限
const fs = require('fs');
const path = require('path');
const express = require('express');
const multer = require('multer');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const config = require('../config');
const store = require('./store');
const { mergePdfs } = require('./merge');
const dify = require('./dify');

const DATA_DIR = path.resolve(config.safeday.dataDir);
const DOCS_DIR = path.join(DATA_DIR, 'docs');

// 初始化：确保记录与产物目录存在（本模块仅在 SAFEDAY_ENABLED=true 时被加载）
for (const dir of [DATA_DIR, DOCS_DIR]) {
  fs.mkdirSync(dir, { recursive: true });
}

const ALLOWED_EXT = ['pdf', 'doc', 'docx', 'ppt', 'pptx', 'xls', 'xlsx'];
const DATE_RE = /^\d{4}\.\d{2}\.\d{2}$/;

const router = express.Router();

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

// Dify 工作流结束回调：不做登录鉴权，凭 SAFEDAY_CALLBACK_TOKEN 校验
// （token 未配置时不校验，与原 CALLBACK_TOKEN 行为一致；须挂在登录门控之前）
router.post('/callback', (req, res) => {
  const token = config.safeday.callbackToken;
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

// 文件预览服务器回源拉取下载地址时无法附带请求头：
// 无 Authorization 头且 query 带 token 时，映射为 Authorization: Bearer 再走统一鉴权
// （仅作用于本模块；/callback 挂在上方，不受影响）
router.use((req, res, next) => {
  if (!req.headers.authorization && typeof req.query.token === 'string' && req.query.token) {
    req.headers.authorization = `Bearer ${req.query.token}`;
  }
  next();
});

// 其余接口一律需登录 + safe-day 应用权限
router.use(auth, requireApp('safe-day'));

// 生成记录：上传文件 + 触发 Dify 工作流
router.post('/generate', upload.array('files', 10), async (req, res) => {
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

    // 先建记录（同一 date 只保留最新一条；sources 记录上传源文件名，供列表副行展示）
    const record = store.create({
      name,
      date,
      fileName: `${date}.docx`,
      status: 'processing',
      sourceCount: files.length,
      sources: files.map((f) => f.originalname),
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
router.get('/records', (req, res) => {
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
router.get('/records/:id/download', (req, res) => {
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

// 在线预览：拼接 basemetas 预览地址（预览服务器凭地址内 ?token= 回源拉取文件，见上方 token 映射中间件）
router.get('/records/:id/preview', (req, res) => {
  const record = store.get(req.params.id);
  if (!record || record.status !== 'done') {
    return res.status(404).json({ ok: false, error: '记录不存在或文件尚未生成' });
  }
  const base = config.basemetas.url.replace(/\/+$/, '');
  if (!base) {
    return res.json({ ok: false, error: '未配置文件预览服务' });
  }
  // 反代后 req.protocol 恒为 http（未开 trust proxy）：优先取 X-Forwarded-Proto 头回退
  const proto = req.headers['x-forwarded-proto'] || req.protocol;
  const downloadUrl = `${proto}://${req.get('host')}/api/v1/safeday/records/` +
    `${encodeURIComponent(record.id)}/download?token=${encodeURIComponent(req.token)}`;
  const url = `${base}/preview/view?url=${encodeURIComponent(downloadUrl)}` +
    `&fileName=${encodeURIComponent(record.fileName)}` +
    `&displayName=${encodeURIComponent(record.name || record.fileName)}`;
  return res.json({ ok: true, url });
});

// 删除记录（连带删除已生成的 docx 文件）
router.delete('/records/:id', (req, res) => {
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

// multer 错误（超限等）统一返回 { ok:false, error }
// eslint-disable-next-line no-unused-vars
router.use((err, req, res, next) => {
  if (err instanceof multer.MulterError) {
    let error = `文件上传失败：${err.message}`;
    if (err.code === 'LIMIT_FILE_SIZE') {
      error = '文件大小超出限制（单文件最大 50MB）';
    } else if (err.code === 'LIMIT_FILE_COUNT' || err.code === 'LIMIT_UNEXPECTED_FILE') {
      error = '文件数量超出限制（最多 10 个）';
    }
    return res.status(400).json({ ok: false, error });
  }
  return next(err);
});

module.exports = router;
