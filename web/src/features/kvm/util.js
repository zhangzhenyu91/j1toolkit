// KVM 展示工具。

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

// saveBlob 二进制 Response 落地为下载文件（文件名先解析 Content-Disposition）。
export async function saveBlob(res, fallbackName) {
  const cd = res.headers.get('Content-Disposition') || '';
  let name = fallbackName;
  const star = /filename\*=UTF-8''([^;]+)/i.exec(cd);
  if (star) {
    try {
      name = decodeURIComponent(star[1]);
    } catch (e) {
      name = star[1];
    }
  } else {
    const plain = /filename="?([^";]+)"?/i.exec(cd);
    if (plain) name = plain[1];
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

// currentUserObj 当前登录用户（会话标记，与 api.js 同存储）。
export function currentUserObj() {
  try {
    const raw = sessionStorage.getItem('shade.user');
    return raw ? JSON.parse(raw) : {};
  } catch (e) {
    return {};
  }
}
