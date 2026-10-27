// 团队网盘 feature API：组件只调用本文件导出的方法。
//
// 两种适配器（与 src/api.js 同口径，模式由 getApiMode() 统一读取）：
// - demo：回填 features/netdisk/demo.js 的静态演示数据（只读），写操作一律明确拒绝，
//   不发起网络请求、不伪造成功；
// - live：请求 Go HTTP API /api/v1/netdisk（go/internal/apps/netdisk，信封 {code,message,data}）。
//
// 鉴权沿用 src/api.js 的登录态：Bearer token 取 sessionStorage 'shade.token'（不在公共 api.js 之上加改）。
// 注意：Go 端 bearerToken 仅认 Authorization 头、未开放 ?token= 直链（旧 Node 口径），故下载/缩略图
// 一律走 fetch + Bearer + Blob 落地，不拼浏览器直链。

import { getApiMode } from '../../api.js';
import * as demo from './demo.js';

const BASE = '/api/v1/netdisk';

// isDemo 当前是否演示适配层（页面顶部演示条用）。
export function isDemo() {
  return getApiMode() !== 'live';
}

// authToken 登录 JWT（sessionStorage，与 src/api.js 的 TOKEN_KEY 一致）。
function authToken() {
  try {
    return sessionStorage.getItem('shade.token') || '';
  } catch (e) {
    return '';
  }
}

// request live 适配层公共请求：注入 Bearer，解析 {code,message,data} 信封。
async function request(path, options = {}) {
  const headers = { ...(options.headers || {}) };
  if (!(options.body instanceof FormData) && !headers['Content-Type']) {
    headers['Content-Type'] = 'application/json';
  }
  const token = authToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(BASE + path, { ...options, headers });
  const body = await res.json().catch(() => ({
    code: 'http_error',
    message: `服务返回异常（HTTP ${res.status}）`,
  }));
  if (body.code !== 0) {
    const err = new Error(body.message || '请求失败');
    err.code = body.code;
    err.status = res.status;
    throw err;
  }
  return body.data;
}

// requestBlob live 适配层二进制请求（下载/缩略图）：非 2xx 时按信封错误抛出。
async function requestBlob(path) {
  const headers = {};
  const token = authToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(BASE + path, { headers });
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new Error((body && body.message) || `下载失败（HTTP ${res.status}）`);
  }
  return res;
}

// demoRead 演示读：延迟返回克隆数据，形状与 live 一致。
const demoRead = (data) =>
  new Promise((resolve) => setTimeout(() => resolve(structuredClone(data)), 120));

// demoWrite 演示写：明确拒绝，不伪造成功。
function demoWrite() {
  return Promise.reject(
    new Error(
      '演示模式不执行该写操作，请切换 live 适配层（localStorage.setItem("shade.apiMode","live")）'
    )
  );
}

// joinRel 相对路径拼接（'a/b' + 'c' → 'a/b/c'）。
function joinRel(dir, name) {
  return dir ? `${dir}/${name}` : name;
}

// ---- live / demo 适配器 ----
const live = {
  listSpaces: () => request('/spaces'),

  listDir: ({ space, path = '', refresh = false }) =>
    request('/list', {
      method: 'POST',
      body: JSON.stringify({ space, path, refresh }),
    }),

  search: ({ space, keywords, page = 1, perPage = 100 }) =>
    request('/search', {
      method: 'POST',
      body: JSON.stringify({ space, keywords, page, per_page: perPage }),
    }),

  mkdir: ({ space, path, name }) =>
    request('/mkdir', {
      method: 'POST',
      body: JSON.stringify({ space, path, name }),
    }),

  rename: ({ space, path, name }) =>
    request('/rename', {
      method: 'POST',
      body: JSON.stringify({ space, path, name }),
    }),

  removeItems: ({ space, paths }) =>
    request('/remove', {
      method: 'POST',
      body: JSON.stringify({ space, paths }),
    }),

  previewSign: ({ space, path }) =>
    request('/preview/sign', {
      method: 'POST',
      body: JSON.stringify({ space, path }),
    }),

  uploadInit: ({ space, path, name, size }) =>
    request('/upload/init', {
      method: 'POST',
      body: JSON.stringify({ space, path, name, size: String(size) }),
    }),

  uploadChunk: async ({ uploadId, index, blob }) => {
    const headers = {
      'Content-Type': 'application/octet-stream',
      'X-Upload-Id': uploadId,
      'X-Chunk-Index': String(index),
    };
    const token = authToken();
    if (token) headers.Authorization = `Bearer ${token}`;
    const res = await fetch(BASE + '/upload/chunk', {
      method: 'PUT',
      headers,
      body: blob,
    });
    const body = await res.json().catch(() => null);
    if (!body || body.code !== 0) {
      const err = new Error((body && body.message) || `分片上传失败（HTTP ${res.status}）`);
      err.retry = !!(
        (body && body.data && body.data.retry) ||
        res.status === 429 ||
        res.status === 409
      );
      throw err;
    }
    return body.data || {};
  },

  uploadComplete: ({ uploadId }) =>
    request('/upload/complete', {
      method: 'POST',
      body: JSON.stringify({ upload_id: uploadId }),
    }),

  uploadStatus: ({ uploadId }) =>
    request(`/upload/status?upload_id=${encodeURIComponent(uploadId)}`),

  uploadAbort: ({ uploadId }) =>
    request('/upload/abort', {
      method: 'POST',
      body: JSON.stringify({ upload_id: uploadId }),
    }),

  listShares: (scope = 'mine') => request(`/shares?scope=${encodeURIComponent(scope)}`),

  createShare: ({ space, paths, password, expireDays }) =>
    request('/shares', {
      method: 'POST',
      body: JSON.stringify({ space, paths, password, expire_days: expireDays }),
    }),

  disableShare: (id) => request(`/shares/${encodeURIComponent(id)}/disable`, { method: 'POST' }),

  enableShare: (id) => request(`/shares/${encodeURIComponent(id)}/enable`, { method: 'POST' }),

  deleteShare: (id) => request(`/shares/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  download: ({ space, path, disposition = 'attachment' }) =>
    requestBlob(
      `/download?space=${encodeURIComponent(space)}&path=${encodeURIComponent(path)}&disposition=${disposition}`
    ),

  thumb: ({ space, path }) =>
    requestBlob(`/thumb?space=${encodeURIComponent(space)}&path=${encodeURIComponent(path)}`),

  downloadDir: ({ space, path }) =>
    requestBlob(
      `/download/dir?space=${encodeURIComponent(space)}&path=${encodeURIComponent(path)}`
    ),

  moveItems: ({ srcSpace, srcDir, names, dstSpace, dstDir }) =>
    request('/move', {
      method: 'POST',
      body: JSON.stringify({
        src_space: srcSpace,
        src_dir: srcDir,
        names,
        dst_space: dstSpace,
        dst_dir: dstDir,
      }),
    }),

  copyItems: ({ srcSpace, srcDir, names, dstSpace, dstDir }) =>
    request('/copy', {
      method: 'POST',
      body: JSON.stringify({
        src_space: srcSpace,
        src_dir: srcDir,
        names,
        dst_space: dstSpace,
        dst_dir: dstDir,
      }),
    }),

  renameBatch: ({ space, items }) =>
    request('/rename/batch', {
      method: 'POST',
      body: JSON.stringify({ space, items }),
    }),

  dirSize: ({ space, path }) =>
    request(`/dirsize?space=${encodeURIComponent(space)}&path=${encodeURIComponent(path)}`),

  archiveMeta: ({ space, path, archivePass }) =>
    request('/archive/meta', {
      method: 'POST',
      body: JSON.stringify({ space, path, archive_pass: archivePass || '' }),
    }),

  archiveList: ({ space, path, innerPath, archivePass }) =>
    request('/archive/list', {
      method: 'POST',
      body: JSON.stringify({
        space,
        path,
        inner_path: innerPath || '/',
        archive_pass: archivePass || '',
      }),
    }),

  archiveDecompress: ({ srcSpace, srcDir, names, dstSpace, dstDir, innerPath, archivePass }) =>
    request('/archive/decompress', {
      method: 'POST',
      body: JSON.stringify({
        src_space: srcSpace,
        src_dir: srcDir,
        names,
        dst_space: dstSpace,
        dst_dir: dstDir,
        inner_path: innerPath || '/',
        archive_pass: archivePass || '',
        put_into_new_dir: 'true',
        overwrite: 'false',
      }),
    }),

  archiveDownload: ({ space, path, inner, archivePass }) =>
    requestBlob(
      `/archive/download?space=${encodeURIComponent(space)}&path=${encodeURIComponent(path)}` +
        `&inner=${encodeURIComponent(inner || '/')}&archive_pass=${encodeURIComponent(archivePass || '')}`
    ),
};

const demoAdapter = {
  listSpaces: () => demoRead(demo.demoSpaces),

  listDir: ({ space, path = '' }) => {
    const items = demo.demoDirs[`${space}:${path}`] || [];
    return demoRead({ space, path, manageable: true, items });
  },

  search: ({ keywords }) => {
    const kw = String(keywords || '').toLowerCase();
    const items = Object.values(demo.demoDirs)
      .flat()
      .filter((it) => it.name.toLowerCase().includes(kw));
    return demoRead({ items, total: items.length, page: 1, per_page: 100 });
  },

  mkdir: demoWrite,
  rename: demoWrite,
  removeItems: demoWrite,
  previewSign: demoWrite,
  uploadInit: demoWrite,
  uploadChunk: demoWrite,
  uploadComplete: demoWrite,
  uploadStatus: demoWrite,
  uploadAbort: demoWrite,

  listShares: () => demoRead(demo.demoShares),
  createShare: demoWrite,
  disableShare: demoWrite,
  enableShare: demoWrite,
  deleteShare: demoWrite,
  download: demoWrite,
  thumb: demoWrite,
  downloadDir: demoWrite,
  moveItems: demoWrite,
  copyItems: demoWrite,
  renameBatch: demoWrite,
  dirSize: () => demoRead({ size: 0, files: 0, dirs: 0, truncated: false }),
  archiveMeta: demoWrite,
  archiveList: demoWrite,
  archiveDecompress: demoWrite,
  archiveDownload: demoWrite,
};

function adapter() {
  return getApiMode() === 'live' ? live : demoAdapter;
}

// ---- 对组件暴露的统一 API（形状稳定，适配器可整体替换）----

// listSpaces 空间列表（我的空间恒有；公共区按班组，未分配班组不下发）。
export function listSpaces() {
  return adapter().listSpaces();
}

// listDir 目录列表。path 为相对空间根路径（'' 或 'a/b'）。
export function listDir(params) {
  return adapter().listDir(params);
}

// search 文件名搜索（范围限定当前空间根）。
export function search(params) {
  return adapter().search(params);
}

// mkdir 新建文件夹。
export function mkdir(params) {
  return adapter().mkdir(params);
}

// rename 重命名（path 为条目相对路径）。
export function rename(params) {
  return adapter().rename(params);
}

// removeItems 批量删除（paths 为条目相对路径数组）。
export function removeItems(params) {
  return adapter().removeItems(params);
}

// previewSign 预览回源票据（/preview/:token 免登录路由用）。
export function previewSign(params) {
  return adapter().previewSign(params);
}

// listShares 分享列表（scope='mine' 我的分享 / 'all' 全部，后者仅班管/超管）。
export function listShares(scope) {
  return adapter().listShares(scope);
}

// createShare 创建分享（password 为 4 位字母数字提取码，expireDays 0=永久）。
export function createShare(params) {
  return adapter().createShare(params);
}

// disableShare / enableShare / deleteShare 分享启停与删除。
export function disableShare(id) {
  return adapter().disableShare(id);
}
export function enableShare(id) {
  return adapter().enableShare(id);
}
export function deleteShare(id) {
  return adapter().deleteShare(id);
}

// download 下载文件（返回 fetch Response，配合 saveBlob 落地）。
export function download(params) {
  return adapter().download(params);
}

// downloadDir 目录整包 ZIP 下载（返回 fetch Response）。
export function downloadDir(params) {
  return adapter().downloadDir(params);
}

// thumb 图片缩略图（≤384px webp，返回 fetch Response）。
export function thumb(params) {
  return adapter().thumb(params);
}

// moveItems / copyItems 移动 / 复制（names 为源目录下的条目名，dstSpace 可跨空间）。
export function moveItems(params) {
  return adapter().moveItems(params);
}
export function copyItems(params) {
  return adapter().copyItems(params);
}

// renameBatch 批量重命名（items: [{path,name}]，1-100 条；返回 {renamed, failed}）。
export function renameBatch(params) {
  return adapter().renameBatch(params);
}

// dirSize 目录大小统计 → {size, files, dirs, truncated}（触顶上限时 truncated=true）。
export function dirSize(params) {
  return adapter().dirSize(params);
}

// ---- 压缩包（archive/*）----

// archiveMeta 压缩包元信息（OpenList 原样载荷；encrypted=true 时需 archivePass）。
export function archiveMeta(params) {
  return adapter().archiveMeta(params);
}

// archiveList 包内目录列表（innerPath 为包内相对路径）。
export function archiveList(params) {
  return adapter().archiveList(params);
}

// archiveDecompress 解压到目标目录（names 为包文件名，返回 OpenList 原样载荷）。
export function archiveDecompress(params) {
  return adapter().archiveDecompress(params);
}

// archiveDownload 包内文件下载（返回 fetch Response，配合 saveBlob 落地）。
export function archiveDownload(params) {
  return adapter().archiveDownload(params);
}

// ARCHIVE_EXTS 可按压缩包管理的扩展名。
export const ARCHIVE_EXTS = ['zip', 'rar', '7z', 'tar', 'gz', 'tgz', 'bz2', 'xz'];

// isArchiveName 文件名是否可按压缩包打开。
export function isArchiveName(name) {
  const ext = String(name || '')
    .split('.')
    .pop()
    .toLowerCase();
  return ARCHIVE_EXTS.includes(ext);
}

// IMAGE_EXTS 可走缩略图预览的图片扩展名（与 Go handleThumb 口径一致）。
export const IMAGE_EXTS = ['jpg', 'jpeg', 'png', 'gif', 'webp', 'bmp'];

// isImageName 文件名是否可缩略图预览。
export function isImageName(name) {
  const ext = String(name || '')
    .split('.')
    .pop()
    .toLowerCase();
  return IMAGE_EXTS.includes(ext);
}

// saveBlob 响应落地为本地文件：文件名优先 Content-Disposition，缺省 fallbackName。
export async function saveBlob(res, fallbackName) {
  const cd = res.headers.get('Content-Disposition') || '';
  const m = /filename\*=UTF-8''([^;]+)/i.exec(cd);
  let name = fallbackName || '下载文件';
  if (m) {
    try {
      name = decodeURIComponent(m[1]);
    } catch (e) {
      name = m[1];
    }
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 4000);
  return name;
}

// shareLinkUrl 公开分享链接（免登录 /share 页，hash 路由 /share/:id，见 views/ShareView.vue；
// 旧 Node 静态 share.html 口径不再拼接，路由由主程序注册 path:'/share/:id'）。
export function shareLinkUrl(shareId) {
  return `${location.origin}/#/share/${shareId}`;
}

// randShareCode 4 位提取码（字母数字大写）。
export function randShareCode() {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789';
  let s = '';
  for (let i = 0; i < 4; i++) s += chars[Math.floor(Math.random() * chars.length)];
  return s;
}

// itemRel 条目相对路径拼接。
export function itemRel(dir, name) {
  return joinRel(dir, name);
}

// expandRanges 断点续传：分片区间数组 [[a,b],…] → 已收分片下标集合。
function expandRanges(ranges) {
  const set = new Set();
  (ranges || []).forEach((r) => {
    if (!Array.isArray(r)) return;
    for (let i = r[0]; i <= r[1]; i++) set.add(i);
  });
  return set;
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// uploadFile 分片上传单文件（init → chunk ×N → complete，片内并发 3、429/409 退避重传、
// 断点续传跳过已收分片；complete 失败时用 status 轮询兜底终态）。
// opts: { file, space, path, onProgress({received,size,doneChunks,totalChunks}), isCancelled() }
export async function uploadFile({ file, space, path, onProgress, isCancelled }) {
  const cancelled = () => (isCancelled ? isCancelled() : false);
  const init = await adapter().uploadInit({ space, path, name: file.name, size: file.size });
  if (cancelled()) return { aborted: true };
  if (init.instant || init.state === 'completed') {
    if (onProgress)
      onProgress({ received: file.size, size: file.size, doneChunks: 0, totalChunks: 0 });
    return { instant: true };
  }
  const uploadId = init.upload_id;
  const chunkSize = init.chunk_size || 4 * 1024 * 1024;
  const totalChunks = init.total_chunks || Math.ceil(file.size / chunkSize) || 1;
  let received = init.received_bytes || 0;
  const done = expandRanges(init.received);
  const indices = [];
  for (let i = 0; i < totalChunks; i++) if (!done.has(i)) indices.push(i);
  let doneChunks = totalChunks - indices.length;
  const report = () => {
    if (onProgress) onProgress({ received, size: file.size, doneChunks, totalChunks });
  };
  report();

  // 单分片上传：429/409 带 retry 标记退避 1~2s 重传，每片最多 5 次
  const putChunk = async (idx) => {
    const start = idx * chunkSize;
    const blob = file.slice(start, Math.min(start + chunkSize, file.size));
    for (let attempt = 1; ; attempt++) {
      if (cancelled()) return;
      try {
        const snap = await adapter().uploadChunk({ uploadId, index: idx, blob });
        received =
          snap.received_bytes != null
            ? snap.received_bytes
            : Math.min(received + blob.size, file.size);
        return;
      } catch (e) {
        if (e.retry && attempt < 5) {
          await sleep(1000 + Math.random() * 1000);
          continue;
        }
        throw e;
      }
    }
  };

  // 片级并发 3
  let cursor = 0;
  const workers = [];
  for (let w = 0; w < 3; w++) {
    workers.push(
      (async () => {
        while (cursor < indices.length && !cancelled()) {
          const idx = indices[cursor++];
          await putChunk(idx);
          doneChunks++;
          report();
        }
      })()
    );
  }
  await Promise.all(workers);
  if (cancelled()) {
    await adapter()
      .uploadAbort({ uploadId })
      .catch(() => {});
    return { aborted: true };
  }

  // 合并落盘（可能长时间阻塞；被掐断时用 status 轮询兜底终态）
  try {
    const fin = await adapter().uploadComplete({ uploadId });
    if (fin && fin.received_bytes != null) received = fin.received_bytes;
  } catch (e) {
    let settled = false;
    for (let i = 0; i < 3 && !settled; i++) {
      await sleep(2000);
      try {
        const s = await adapter().uploadStatus({ uploadId });
        if (s && s.received_bytes != null) {
          received = s.received_bytes;
          report();
        }
        if (s && s.state === 'completed') settled = true;
      } catch (err) {
        /* 继续轮询 */
      }
    }
    if (!settled) throw e;
  }
  received = file.size;
  report();
  return { uploadId };
}
