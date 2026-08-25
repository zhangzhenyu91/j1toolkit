// 安全日活动记录路由（自 SafeDayLogs 独立服务合并而来，业务逻辑与请求/响应形状保持同构：
// 响应仍为 { ok, error, ... }，非主平台 {code,message,data} 信封）
// 鉴权：/callback 凭 SAFEDAY_CALLBACK_TOKEN 校验（不做登录）；其余接口需登录 + safe-day 应用权限
// 班组隔离：记录带 team 字段（班组名），产物存 docs/{班组名}/ 子目录；超管可 ?team_id= 指定或 all 全部
// 生成链路（新）：Dify 工作流只产出三段文字并经 /callback 回传 → 后端套模板渲染 docx 落盘（render.js），不再经 DOCX-MCP
const fs = require('fs');
const path = require('path');
const express = require('express');
const multer = require('multer');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const teamUtil = require('../utils/team');
const config = require('../config');
const store = require('./store');
const { mergePdfs } = require('./merge');
const dify = require('./dify');
const render = require('./render');
const { pool } = require('../db');

const DATA_DIR = config.safeday.dataDir;
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

// 记录产物路径：优先 docs/{班组}/ 子目录，旧记录回退 docs/ 根目录（迁移后一般不存在）
function recordFilePath(record) {
  const base = path.basename(record.fileName || '');
  if (record.team) {
    const nested = path.join(DOCS_DIR, record.team, base);
    if (fs.existsSync(nested)) return nested;
  }
  return path.join(DOCS_DIR, base);
}

// 一次性判定：文件存在即置 done，不存在即置 failed
// （Dify 回调到来时文件应已写完；不存在说明工作流未产出，判定失败）
function judgeOnce(record) {
  const filePath = recordFilePath(record);
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
// 新链路：工作流末尾 HTTP 节点回传三段文字（activity_content 活动内容 / recap_analysis 结合本次内容复盘分析 /
// job_analysis 结合实际岗位剖析内容）+ date + class，后端套模板渲染 docx 落盘 docs/{班组}/YYYY.MM.DD.docx；
// 未带文字内容时按旧口径仅做文件存在性终判（兼容存量工作流）
router.post('/callback', async (req, res) => {
  const token = config.safeday.callbackToken;
  if (token && req.query.token !== token) {
    return res.status(403).json({ ok: false, error: '回调 token 校验失败' });
  }
  try {
    const body = req.body || {};
    const date = typeof body.date === 'string' ? body.date.trim() : '';
    // class（班组名）可选：带上时只终判该班组处理中的记录，避免多班组并行生成时互相误判
    const className = typeof body.class === 'string' ? body.class.trim() : '';

    // ===== 新链路：回传文字内容 → 后端渲染落盘 =====
    const hasTexts = ['activity_content', 'recap_analysis', 'job_analysis']
      .some((k) => typeof body[k] === 'string');
    if (hasTexts) {
      const record = store.list().find((r) =>
        r.status === 'processing' && (!date || r.date === date) && (!className || (r.team || '') === className)
      );
      if (!record) return res.json({ ok: false, error: '未找到匹配的处理中记录（可能已被删除或重复回调）' });
      try {
        const f = record.form || {};
        const buf = render.renderRecord({
          班组名称: record.team || '',
          学习内容: record.name || '',
          活动时间: record.date || '',
          主持人: f.host || '',
          上级参加人员: f.superior || '',
          本班组参加人员: f.attendees || '',
          缺席人员: f.absentees || '无',
          缺席人员原因: f.absentReason || '无',
          // 三段正文截 20000 字符兜底（防异常超长；实测活动通报类内容可达 8 千字，6000 会截断正文）
          活动内容: String(body.activity_content || '').slice(0, 20000),
          结合本次内容复盘分析: String(body.recap_analysis || '').slice(0, 20000),
          结合实际岗位剖析内容: String(body.job_analysis || '').slice(0, 20000),
          记录人: f.recorder || '',
        });
        const outDir = path.join(DOCS_DIR, record.team || '');
        fs.mkdirSync(outDir, { recursive: true });
        fs.writeFileSync(path.join(outDir, path.basename(record.fileName)), buf);
        store.update(record.id, { status: 'done' });
        return res.json({ ok: true, done: 1, failed: 0 });
      } catch (e) {
        const msg = e && e.message ? e.message : String(e);
        store.update(record.id, { status: 'failed', error: `文档渲染失败：${msg}` });
        return res.status(500).json({ ok: false, error: `文档渲染失败：${msg}` });
      }
    }

    // ===== 旧链路兼容：仅按产物文件存在性终判 =====
    let records = store.list().filter((r) => r.status === 'processing');
    if (className) {
      records = records.filter((r) => (r.team || '') === className);
    }
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

// GET /form-meta：生成表单数据源（班组成员名单 + 按班组记忆的默认值）
// 成员口径：WORKLOG_ENABLED=true 时取出工成员字典（status=1，按 sort 即「点亮按钮顺序」，顺序1=默认主持人）；
// 否则回退班组账号昵称（sys_user）。默认值：superior 初始「任晓辉」、recorder 初始空（前端回落顺序1），生成成功后按班组记忆
router.get('/form-meta', async (req, res) => {
  try {
    const team = await teamUtil.resolveTeam(req.user, req.query.team_id);
    if (!team) return res.json({ ok: true, members: [], defaults: { superior: '任晓辉', recorder: '' } });
    let members = [];
    if (config.worklog && config.worklog.enabled) {
      const [rows] = await pool.query(
        'SELECT name FROM worklog_member WHERE team_id = ? AND status = 1 ORDER BY sort, id',
        [team.id]
      );
      members = rows.map((r) => r.name);
    } else {
      const [rows] = await pool.query(
        "SELECT nickname FROM sys_user WHERE team_id = ? AND status = 1 AND nickname <> '' ORDER BY id",
        [team.id]
      );
      members = rows.map((r) => r.nickname);
    }
    const saved = store.getFormDefaults(team.name);
    return res.json({
      ok: true,
      members,
      defaults: { superior: saved.superior || '任晓辉', recorder: saved.recorder || '' },
    });
  } catch (e) {
    return res.status(500).json({ ok: false, error: `表单数据加载失败：${e && e.message ? e.message : e}` });
  }
});

// 生成表单字段清洗：字符串、去首尾空格、限长（防异常超长入库存档）
function cutForm(v, n) {
  return (typeof v === 'string' ? v.trim() : '').slice(0, n);
}

// 生成记录：上传文件 + 生成表单（主持人/上级参加人员/参加与缺席人员/缺席原因/记录人）+ 触发 Dify 工作流
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

    // 生效班组：超管可用表单 team_id 指定；其余角色固定本班（未分配拒绝生成）
    const team = await teamUtil.resolveTeam(req.user, req.body && req.body.team_id);
    if (!team) {
      return res.status(400).json({ ok: false, error: '未分配班组，请联系管理员分配后再生成' });
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

    // 先建记录（同一班组同一 date 只保留最新一条；sources 记录上传源文件名，供列表副行展示；
    // form 存生成表单字段，回调渲染 docx 时使用）
    const record = store.create({
      name,
      date,
      fileName: `${date}.docx`,
      team: team.name,
      status: 'processing',
      sourceCount: files.length,
      sources: files.map((f) => f.originalname),
      form: {
        host: cutForm(req.body.host, 64),
        superior: cutForm(req.body.superior, 64),
        recorder: cutForm(req.body.recorder, 64),
        attendees: cutForm(req.body.attendees, 512),
        absentees: cutForm(req.body.absentees, 512),
        absentReason: cutForm(req.body.absentReason, 512),
      },
    });

    // 按班组记忆表单默认值（上级参加人员 / 记录人，供下次生成预填；空值不覆盖）
    store.saveFormDefaults(team.name, { superior: req.body.superior, recorder: req.body.recorder });

    // 上传 Dify 并触发工作流（触发后立即返回，不等工作流完成）；
    // 工作流产出三段文字后经 /callback 回传，由后端渲染 docx 落盘（render.js）
    try {
      fs.mkdirSync(path.join(DOCS_DIR, team.name), { recursive: true });
      await dify.uploadAndRun({
        fileBuffer,
        fileName,
        date,
        className: team.name,
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
// 班组过滤：超管 ?team_id=all 全部班组、?team_id=N 指定班组（缺省落自己/默认班组）；其余角色仅本班
router.get('/records', async (req, res) => {
  try {
    if (req.user.role === 'admin' && String(req.query.team_id || '') === 'all') {
      return res.json({ ok: true, records: store.list() });
    }
    const team = await teamUtil.resolveTeam(req.user, req.query.team_id);
    if (!team) return res.json({ ok: true, records: [] });
    return res.json({ ok: true, records: store.list(team.name) });
  } catch (e) {
    return res.status(500).json({
      ok: false,
      error: `读取记录失败：${e && e.message ? e.message : e}`,
    });
  }
});

// 记录归属校验：超管任意班组；其余角色仅本班（按班组名比对）
async function canAccess(req, record) {
  if (req.user.role === 'admin') return true;
  const team = await teamUtil.resolveTeam(req.user);
  return !!team && (record.team || '') === team.name;
}

// 下载产物
router.get('/records/:id/download', async (req, res) => {
  const record = store.get(req.params.id);
  if (!record || record.status !== 'done') {
    return res.status(404).json({ ok: false, error: '记录不存在或文件尚未生成' });
  }
  if (!(await canAccess(req, record))) {
    return res.status(403).json({ ok: false, error: '无权访问其他班组的记录' });
  }
  const filePath = recordFilePath(record);
  if (!fs.existsSync(filePath)) {
    return res.status(404).json({ ok: false, error: '文件不存在，可能已被清理' });
  }
  return res.download(filePath, record.fileName);
});

// 在线预览：拼接 basemetas 预览地址（预览服务器凭地址内 ?token= 回源拉取文件，见上方 token 映射中间件）
router.get('/records/:id/preview', async (req, res) => {
  const record = store.get(req.params.id);
  if (!record || record.status !== 'done') {
    return res.status(404).json({ ok: false, error: '记录不存在或文件尚未生成' });
  }
  if (!(await canAccess(req, record))) {
    return res.status(403).json({ ok: false, error: '无权访问其他班组的记录' });
  }
  const base = config.basemetas.url;
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
router.delete('/records/:id', async (req, res) => {
  try {
    const target = store.get(req.params.id);
    if (target && !(await canAccess(req, target))) {
      return res.status(403).json({ ok: false, error: '无权访问其他班组的记录' });
    }
    const record = store.remove(req.params.id);
    if (!record) {
      return res.status(404).json({ ok: false, error: '记录不存在' });
    }
    if (record.fileName) {
      const filePath = recordFilePath(record);
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
