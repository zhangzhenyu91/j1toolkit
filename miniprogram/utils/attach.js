// Call Me 附件白名单（与后端 go/internal/apps/callme/ingest.go attachmentExts 对齐，含 mp4）。
// wx.chooseMessageFile 的 extension 过滤 + 端侧复核共用同一份清单。
const { extOf } = require('./util');

const ATTACH_EXTS = [
  'pdf',
  'docx',
  'doc',
  'pptx',
  'ppt',
  'xlsx',
  'xls',
  'epub',
  'mhtml',
  'csv',
  'md',
  'markdown',
  'txt',
  'json',
  'html',
  'mp3',
  'wav',
  'm4a',
  'flac',
  'ogg',
  'aac',
  'bmp',
  'tiff',
  'tif',
  'mp4',
];

// 音频扩展名（附件芯片图标区分用；mp4 为视频，不在此列）
const AUDIO_EXTS = ['mp3', 'wav', 'm4a', 'flac', 'ogg', 'aac'];

function isAllowedAttachment(name) {
  const ext = extOf(name);
  return !!ext && ATTACH_EXTS.includes(ext);
}

function isAudioAttachment(name) {
  return AUDIO_EXTS.includes(extOf(name));
}

module.exports = { ATTACH_EXTS, AUDIO_EXTS, isAllowedAttachment, isAudioAttachment };
