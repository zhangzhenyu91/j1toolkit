// 真实文件类型图标（与 Web 端同源：{BASE_URL}/assets/filetypes/<name>.png，
// 映射口径同 server/public/netdisk.html 的 FT_MAP：doc→docx、xls→xlsx、ppt→pptx、jpeg→jpg、
// tgz/bz2/xz→zip、webm/m4v→mp4、webp→jpg、svg→png、log→txt；无映射→unknown.png）
// 包内共享：pkg-netdisk 各页面相对 require（../../fileicon），不进主包、不跨分包
const config = require('../config');
const { extOf } = require('../utils/util');

const FT_BASE = `${config.BASE_URL}/assets/filetypes/`;

const FT_MAP = {
  doc: 'docx', docx: 'docx', xls: 'xlsx', xlsx: 'xlsx', csv: 'csv',
  ppt: 'pptx', pptx: 'pptx', pdf: 'pdf', txt: 'txt', md: 'md', log: 'txt',
  zip: 'zip', rar: 'rar', '7z': '7z', tar: 'tar', gz: 'gz', tgz: 'zip', bz2: 'zip', xz: 'zip',
  jpg: 'jpg', jpeg: 'jpg', png: 'png', gif: 'gif', bmp: 'bmp', webp: 'jpg', svg: 'png',
  mp4: 'mp4', mov: 'mov', webm: 'mp4', m4v: 'mp4', mp3: 'mp3', wav: 'wav',
};

// 目录 → folder.png；文件按扩展名映射，无映射 → unknown.png
function fileIconUrl(name, isDir) {
  if (isDir) return `${FT_BASE}folder.png`;
  return `${FT_BASE}${FT_MAP[extOf(name)] || 'unknown'}.png`;
}

module.exports = { fileIconUrl };
