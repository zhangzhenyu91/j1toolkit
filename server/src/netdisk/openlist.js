// OpenList API 客户端：服务账户登录、令牌缓存与自动重登、fs / multipart / 下载接口封装
// 部署口径：OpenList 仅监听回环/内网不对公网暴露，双端所有访问经本模块中转（见 开发指南.md 团队网盘章节）
// 接口报文以 fox.oplist.org 文档与 OpenList 源码核实：
//   上传 init/chunk 走请求头（File-Path 需 URL 编码、X-File-Size、X-Upload-Id、X-Chunk-Index）；
//   统一信封 {code,message,data}，成功 code=200（与主平台 code=0 不同，注意区分）
const axios = require('axios');
const config = require('../config');

const TOKEN_TTL_MS = 47 * 3600 * 1000; // OpenList JWT 默认 48h，提前 1h 重登
let cached = { token: '', at: 0 };

// 未配置服务账户时抛可读错误（路由层映射 50301）
function ensureConfigured() {
  if (!config.netdisk.username || !config.netdisk.password) {
    const err = new Error('团队网盘未配置：请在环境变量设置 NETDISK_USERNAME / NETDISK_PASSWORD（OpenList 服务账户）');
    err.expose = true;
    err.status = 503;
    err.code = 50301;
    throw err;
  }
}

async function login() {
  ensureConfigured();
  const r = await axios.post(`${config.netdisk.apiUrl}/api/auth/login`, {
    username: config.netdisk.username,
    password: config.netdisk.password,
  }, { timeout: 15000 });
  const body = r.data || {};
  if (body.code !== 200 || !body.data || !body.data.token) {
    const err = new Error(`OpenList 登录失败：${body.message || `HTTP ${r.status}`}`);
    err.expose = true;
    throw err;
  }
  cached = { token: body.data.token, at: Date.now() };
  return cached.token;
}

async function token(force = false) {
  if (force || !cached.token || Date.now() - cached.at > TOKEN_TTL_MS) return login();
  return cached.token;
}

// JSON 调用：解 OpenList 信封；401 强制重登重试一次；上游业务码挂在 err.upstreamCode
async function call(method, apiPath, opts = {}, retried = false) {
  const tk = await token();
  let r;
  try {
    r = await axios({
      method,
      url: `${config.netdisk.apiUrl}${apiPath}`,
      headers: { Authorization: tk, ...(opts.headers || {}) },
      data: opts.data,
      params: opts.params,
      timeout: opts.timeout === undefined ? 60000 : opts.timeout,
      maxBodyLength: Infinity,
      maxContentLength: Infinity,
    });
  } catch (err) {
    // 网络层异常（OpenList 未启动/断连）：不可读文案归一，路由层归 50201
    err.upstreamCode = 0;
    throw err;
  }
  const body = r.data || {};
  if (r.status === 401 || body.code === 401) {
    if (retried) {
      const err = new Error('OpenList 登录态失效，重登后仍未通过');
      err.expose = true;
      throw err;
    }
    await token(true);
    return call(method, apiPath, opts, true);
  }
  if (body.code !== 200) {
    const err = new Error(body.message || `OpenList 返回异常（${body.code || r.status}）`);
    err.expose = true;
    err.upstreamCode = body.code || r.status;
    throw err;
  }
  return body.data;
}

// 流式下载：/d<path>?sign=（驱动直链 302 由 axios 自动跟随），返回上游响应流与响应头
async function downloadStream(absPath) {
  const info = await call('post', '/api/fs/get', { data: { path: absPath, password: '' } });
  const tk = await token();
  const sign = info && info.sign ? `?sign=${encodeURIComponent(info.sign)}` : '';
  const r = await axios({
    method: 'get',
    url: `${config.netdisk.apiUrl}/d${absPath.split('/').map(encodeURIComponent).join('/')}${sign}`,
    headers: { Authorization: tk },
    responseType: 'stream',
    timeout: 0, // 大文件经两跳转发耗时不可预估，不限超时
  });
  return r;
}

// ---- fs 基础操作 ----
const fsList = (path) => call('post', '/api/fs/list', {
  data: { path, password: '', page: 1, per_page: 0, refresh: false },
});
const fsGet = (path) => call('post', '/api/fs/get', { data: { path, password: '' } });
const fsSearch = (parent, keywords, page, perPage) => call('post', '/api/fs/search', {
  data: { parent, keywords, scope: 0, page, per_page: perPage },
});
const fsMkdir = (path) => call('post', '/api/fs/mkdir', { data: { path } });
const fsRename = (path, name) => call('post', '/api/fs/rename', { data: { path, name } });
const fsRemove = (dir, names) => call('post', '/api/fs/remove', { data: { dir, names } });
const fsMove = (srcDir, dstDir, names) => call('post', '/api/fs/move', {
  data: { src_dir: srcDir, dst_dir: dstDir, names },
});
const fsCopy = (srcDir, dstDir, names) => call('post', '/api/fs/copy', {
  data: { src_dir: srcDir, dst_dir: dstDir, names },
});

// ---- 压缩包预览/解压（报文以 OpenList 源码 server/handles/archive.go 为准）----
// meta：{path, archive_pass} → {encrypted, comment, sign, content 树}；sign 供 /ad 包内下载
const fsArchiveMeta = (path, archivePass) => call('post', '/api/fs/archive/meta', {
  data: { path, password: '', refresh: false, archive_pass: archivePass || '' },
});
// list：按 inner_path 平铺列内层目录
const fsArchiveList = (path, innerPath, archivePass) => call('post', '/api/fs/archive/list', {
  data: {
    path, password: '', refresh: false, archive_pass: archivePass || '',
    inner_path: innerPath || '/', page: 1, per_page: 0,
  },
});
// decompress：{src_dir, dst_dir, name[], inner_path, archive_pass, cache_full, put_into_new_dir, overwrite}
const fsArchiveDecompress = (srcDir, dstDir, names, opts = {}) => call('post', '/api/fs/archive/decompress', {
  data: {
    src_dir: srcDir,
    dst_dir: dstDir,
    name: names,
    inner_path: opts.innerPath || '/',
    archive_pass: opts.archivePass || '',
    cache_full: false,
    put_into_new_dir: opts.putIntoNewDir !== false,
    overwrite: !!opts.overwrite,
  },
  timeout: 0, // 大压缩包经驱动回源解压耗时不可预估
});

// 包内文件下载：先 meta 取 sign，再 GET /ad<压缩包路径>?inner=&sign=（驱动直链 302 自动跟随）
async function archiveDownloadStream(absPath, innerPath, archivePass) {
  const meta = await fsArchiveMeta(absPath, archivePass);
  const tk = await token();
  const encPath = absPath.split('/').map(encodeURIComponent).join('/');
  const sign = meta && meta.sign ? `&sign=${encodeURIComponent(meta.sign)}` : '';
  const r = await axios({
    method: 'get',
    url: `${config.netdisk.apiUrl}/ad${encPath}?inner=${encodeURIComponent(innerPath)}${sign}`,
    headers: { Authorization: tk },
    responseType: 'stream',
    timeout: 0,
  });
  return r;
}

// 逐级建目录（已存在/失败均忽略，用于个人空间与公共区根目录自愈）
async function ensureDir(absPath) {
  const segs = absPath.split('/').filter(Boolean);
  let cur = '';
  for (const seg of segs) {
    cur += `/${seg}`;
    try { await fsMkdir(cur); } catch (err) { /* 已存在等错误忽略 */ }
  }
}

// 空文件：multipart 不接受 0 字节，走 /fs/put 直传空体
function putEmpty(absPath) {
  return call('put', '/api/fs/put', {
    headers: { 'File-Path': encodeURI(absPath), 'Content-Length': '0' },
    data: '',
  });
}

// 直传（Buffer 或可读流）：供「保存到网盘」共享通道使用（服务端侧已有文件内容，无需分片）
function fsPut(absPath, body, size) {
  return call('put', '/api/fs/put', {
    headers: {
      'File-Path': encodeURI(absPath),
      'Content-Type': 'application/octet-stream',
      ...(Number.isFinite(size) && size > 0 ? { 'Content-Length': String(size) } : {}),
    },
    data: body,
    timeout: 0, // 大文件经两跳转发耗时不可预估
  });
}

// ---- multipart 分片上传（报文以 OpenList 源码 server/handles/multipart.go 为准）----
const mpInit = (absPath, size) => call('post', '/api/fs/multipart/init', {
  headers: { 'File-Path': encodeURI(absPath), 'X-File-Size': String(size) },
});
const mpChunk = (uploadId, chunkIndex, buf) => call('put', '/api/fs/multipart/chunk', {
  headers: {
    'X-Upload-Id': uploadId,
    'X-Chunk-Index': String(chunkIndex),
    'Content-Type': 'application/octet-stream',
  },
  data: buf,
  timeout: 0,
});
const mpComplete = (uploadId) => call('post', '/api/fs/multipart/complete', {
  headers: { 'X-Upload-Id': uploadId },
  timeout: 0, // complete 阻塞到驱动落盘终态（115 回源耗时不可预估）
});
const mpAbort = (uploadId) => call('post', '/api/fs/multipart/abort', {
  headers: { 'X-Upload-Id': uploadId },
});
const mpStatus = (query) => call('get', '/api/fs/multipart/status', { params: query });

module.exports = {
  ensureConfigured,
  call,
  downloadStream,
  fsList,
  fsGet,
  fsSearch,
  fsMkdir,
  fsRename,
  fsRemove,
  fsMove,
  fsCopy,
  fsArchiveMeta,
  fsArchiveList,
  fsArchiveDecompress,
  archiveDownloadStream,
  ensureDir,
  putEmpty,
  fsPut,
  mpInit,
  mpChunk,
  mpComplete,
  mpAbort,
  mpStatus,
};
