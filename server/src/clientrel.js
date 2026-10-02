// 内网客户端发布（uvmp-toolkit 安装包托管）：GitHub 打 tag 发版后，CI publish-web job 把 Release 资产推送到本站，
// 网页端 client.html（内网客户端页）据此展示最新版本并提供下载。
// 链路：POST /publish（逐资产 multipart 上传，先入 incoming/<version>/ 暂存）
//       → POST /publish/complete（整版转正：替换同名版本目录、写 manifest.json、清理历史版本）
// 鉴权：两个发布接口走 CLIENT_PUBLISH_TOKEN（Bearer，timing-safe 比对；未配置则发布不可用、查询/下载照常）；
//       /latest 与 /download 公开（安装包不涉敏感数据，便于直接把页面链接发给班组成员）
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const express = require('express');
const multer = require('multer');
const config = require('./config');
const { ok, fail } = require('./utils/resp');

const BASE_DIR = path.resolve(__dirname, '..', 'data', 'client-releases');
const INCOMING_DIR = path.join(BASE_DIR, 'incoming');
const MANIFEST_PATH = path.join(BASE_DIR, 'manifest.json');

const VERSION_RE = /^\d+\.\d+\.\d+$/;       // 版本号仅允许 x.y.z（同时防路径穿越）
const FILENAME_RE = /^[\w][\w.-]*$/;        // 安装包文件名白名单（产物命名规范本即 ASCII）
// 参与发布的资产扩展名（blockmap/最新版 yml 等 CI 附属文件不收）
const ASSET_EXT_RE = /\.(exe|zip|deb|tar\.gz)$/i;

const router = express.Router();

function validVersion(v) { return VERSION_RE.test(String(v || '')); }

// 发布令牌校验（Bearer，timing-safe；未配置时令牌校验直接不通过并提示）
function requirePublishToken(req, res, next) {
  const token = config.clientrel.publishToken;
  if (!token) return fail(res, 503, 50301, '服务端未配置发布令牌（CLIENT_PUBLISH_TOKEN）');
  const auth = String(req.headers.authorization || '');
  const got = auth.startsWith('Bearer ') ? auth.slice(7) : '';
  const a = Buffer.from(got);
  const b = Buffer.from(token);
  if (a.length !== b.length || !crypto.timingSafeEqual(a, b)) {
    return fail(res, 401, 40101, '发布令牌无效');
  }
  return next();
}

// multer 直写磁盘（安装包数百 MB，不走内存）：落 incoming/<version>/，版本号先于文件解析（fields 须排在 file 前）
const upload = multer({
  storage: multer.diskStorage({
    destination(req, file, cb) {
      const version = String(req.body.version || '');
      if (!validVersion(version)) return cb(new Error('版本号格式非法（须为 x.y.z）'));
      const dir = path.join(INCOMING_DIR, version);
      fs.mkdirSync(dir, { recursive: true });
      return cb(null, dir);
    },
    filename(req, file, cb) {
      const name = path.basename(file.originalname || '');
      if (!FILENAME_RE.test(name) || !ASSET_EXT_RE.test(name)) {
        return cb(new Error(`文件名不被接受：${name}`));
      }
      return cb(null, name);
    },
  }),
  limits: { fileSize: 1024 * 1024 * 1024, files: 1 },   // 单资产上限 1GB
});

// 发布：上传单个资产（CI 逐文件调用；同版本重复发布直接覆盖暂存区同名文件）
router.post('/publish', requirePublishToken, (req, res) => {
  upload.single('file')(req, res, (err) => {
    if (err) {
      const msg = err instanceof multer.MulterError && err.code === 'LIMIT_FILE_SIZE'
        ? '文件超出 1GB 上限' : (err.message || '上传失败');
      return fail(res, 400, 40002, msg);
    }
    if (!req.file) return fail(res, 400, 40003, '缺少文件（字段名 file）');
    return ok(res, { version: req.body.version, file: req.file.filename, size: req.file.size }, '已接收');
  });
});

// 定版：incoming/<version>/ 整体转正为正式版本目录，重写 manifest，清理历史版本（磁盘只留最新一版）
router.post('/publish/complete', requirePublishToken, (req, res) => {
  const version = String((req.body || {}).version || '');
  if (!validVersion(version)) return fail(res, 400, 40002, '版本号格式非法（须为 x.y.z）');
  const incoming = path.join(INCOMING_DIR, version);
  if (!fs.existsSync(incoming)) return fail(res, 400, 40004, `暂存区没有版本 ${version} 的资产（请先 /publish 上传）`);
  const target = path.join(BASE_DIR, version);
  try {
    fs.rmSync(target, { recursive: true, force: true });
    fs.renameSync(incoming, target);
    const assets = fs.readdirSync(target)
      .filter((f) => ASSET_EXT_RE.test(f))
      .map((f) => ({ file: f, size: fs.statSync(path.join(target, f)).size }))
      .sort((a, b) => a.file.localeCompare(b.file));
    if (!assets.length) {
      fs.rmSync(target, { recursive: true, force: true });
      return fail(res, 400, 40005, '暂存区内没有可发布的安装包（exe/zip/deb/tar.gz）');
    }
    const manifest = {
      version,
      publishedAt: new Date().toISOString(),
      notes: String((req.body || {}).notes || '').slice(0, 4000),
      assets,
    };
    fs.writeFileSync(MANIFEST_PATH, JSON.stringify(manifest, null, 2), 'utf8');
    // 只留最新版：清掉其余版本目录与残留的暂存区
    for (const entry of fs.readdirSync(BASE_DIR)) {
      if (entry === version || entry === 'incoming' || entry === 'manifest.json') continue;
      const p = path.join(BASE_DIR, entry);
      if (fs.statSync(p).isDirectory()) fs.rmSync(p, { recursive: true, force: true });
    }
    console.log(`[内网客户端] 版本 ${version} 发布完成：${assets.length} 个资产`);
    return ok(res, manifest, '发布完成');
  } catch (err) {
    console.error('[内网客户端] 定版失败：', err.message);
    return fail(res, 500, 50000, '定版失败：' + err.message);
  }
});

function readManifest() {
  try { return JSON.parse(fs.readFileSync(MANIFEST_PATH, 'utf8')); } catch (_e) { return null; }
}

// 最新版本清单（公开；未发布过则 available=false）
router.get('/latest', (req, res) => {
  const manifest = readManifest();
  if (!manifest || !manifest.version) return ok(res, { available: false });
  return ok(res, Object.assign({ available: true }, manifest));
});

// 资产下载（公开；文件名以 manifest 为准，防路径穿越）
router.get('/download/:file', (req, res) => {
  const manifest = readManifest();
  const name = path.basename(String(req.params.file || ''));
  const asset = manifest && (manifest.assets || []).find((a) => a.file === name);
  if (!asset) return fail(res, 404, 40404, '安装包不存在或已被新版本替换');
  const filePath = path.join(BASE_DIR, manifest.version, name);
  if (!fs.existsSync(filePath)) return fail(res, 404, 40404, '安装包文件缺失');
  return res.download(filePath, name);
});

module.exports = router;
