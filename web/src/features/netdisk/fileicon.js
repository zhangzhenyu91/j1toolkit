// 文件类型图标（对照旧 server/public/netdisk.html、share.html 的 fileIconHtml 口径）：
// 真实类型 PNG（filetypes/<ext>.png，随包构建，不依赖运行期 /assets 静态目录），
// 同类合并（doc→docx、xls→xlsx、jpeg→jpg、tgz/bz2/xz→zip 等），无映射→unknown.png；
// 图标加载失败由 onFtError 隐藏自身、露出 .ft-fb 回退（文件夹线性图标 / 类型字母块）。
// 团队网盘与分享页共用一份映射，避免两处漂移。

const PNGS = import.meta.glob('./filetypes/*.png', {
  eager: true,
  query: '?url',
  import: 'default',
});
const PNG_URL = {};
for (const [p, url] of Object.entries(PNGS)) {
  PNG_URL[p.replace('./filetypes/', '').replace(/\.png$/, '')] = url;
}

const FT_MAP = {
  doc: 'docx',
  docx: 'docx',
  xls: 'xlsx',
  xlsx: 'xlsx',
  csv: 'csv',
  ppt: 'pptx',
  pptx: 'pptx',
  pdf: 'pdf',
  txt: 'txt',
  md: 'md',
  log: 'txt',
  zip: 'zip',
  rar: 'rar',
  '7z': '7z',
  tar: 'tar',
  gz: 'gz',
  tgz: 'zip',
  bz2: 'zip',
  xz: 'zip',
  jpg: 'jpg',
  jpeg: 'jpg',
  png: 'png',
  gif: 'gif',
  bmp: 'bmp',
  webp: 'jpg',
  svg: 'png',
  mp4: 'mp4',
  mov: 'mov',
  webm: 'mp4',
  m4v: 'mp4',
  mp3: 'mp3',
  wav: 'wav',
};

// 字母块回退标签（覆盖常见办公/压缩/媒体格式，其余取扩展名前 4 位大写）
const EXT_LABEL = {
  doc: 'DOC',
  docx: 'DOC',
  xls: 'XLS',
  xlsx: 'XLS',
  csv: 'XLS',
  ppt: 'PPT',
  pptx: 'PPT',
  pdf: 'PDF',
  zip: 'ZIP',
  rar: 'ZIP',
  '7z': 'ZIP',
  tar: 'ZIP',
  gz: 'ZIP',
  tgz: 'ZIP',
  bz2: 'ZIP',
  xz: 'ZIP',
  txt: 'TXT',
  md: 'TXT',
  log: 'TXT',
  mp4: 'MP4',
  mov: 'MP4',
  webm: 'MP4',
  m4v: 'MP4',
  mp3: 'MP3',
  wav: 'MP3',
};

export function extOf(name) {
  const m = /\.([a-z0-9]+)$/i.exec(name || '');
  return m ? m[1].toLowerCase() : '';
}

export function typeLabel(name) {
  const e = extOf(name);
  return EXT_LABEL[e] || (e ? e.slice(0, 4).toUpperCase() : 'FILE');
}

// 字母块字号：随图标边长缩放（同旧 fileIconHtml 的 size * 0.26）
export function ftLbStyle(name, size) {
  return { fontSize: `${Math.max(7, Math.round(size * 0.26))}px` };
}

// 文件类型 PNG 地址（打包内静态 URL；folder.png 文件夹、unknown.png 无映射兜底）
export function fileIconUrl(name, isDir) {
  const key = isDir ? 'folder' : FT_MAP[extOf(name)] || 'unknown';
  return PNG_URL[key] || PNG_URL.unknown || '';
}

// img 加载失败回退：隐藏自身，露出兄弟 .ft-fb（文件夹线性图标 / 字母块）
export function onFtError(e) {
  const img = e && e.target;
  if (!img) return;
  img.style.display = 'none';
  const fb = img.nextElementSibling;
  if (fb && fb.classList.contains('ft-fb')) fb.hidden = false;
}
