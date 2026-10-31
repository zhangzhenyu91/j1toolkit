// KVM 展示工具：下载落地与当前用户取共享 record-fields 单一口径。
import { currentUserObj, saveBlob } from '../core/record-fields.js';

export { currentUserObj, saveBlob };

// fmtSize 文件大小展示（B/KB/MB/GB）。
export function fmtSize(n) {
  const v = Number(n) || 0;
  if (v < 1024) return `${v} B`;
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB`;
  if (v < 1024 * 1024 * 1024) return `${(v / 1024 / 1024).toFixed(1)} MB`;
  return `${(v / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

// fmtDate 时间展示 'YYYY-MM-DD HH:mm'（unix 秒或 ISO 串）。
export function fmtDate(d, withTime = true) {
  if (!d) return '';
  const dt = typeof d === 'number' ? new Date(d * 1000) : new Date(d);
  if (Number.isNaN(dt.getTime())) return '';
  const p = (n) => String(n).padStart(2, '0');
  const day = `${dt.getFullYear()}-${p(dt.getMonth() + 1)}-${p(dt.getDate())}`;
  return withTime ? `${day} ${p(dt.getHours())}:${p(dt.getMinutes())}` : day;
}
