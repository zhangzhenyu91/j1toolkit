// 树障隐患展示与表单工具（与旧 server/public/treehazard.html 口径一致）。

// dash 空值展示占位。
export function dash(v) {
  const s = String(v == null ? '' : v).trim();
  return s || '—';
}

// fmtKv 电压字段展示：纯数字补 kV，空值 '—'，其余原样。
export function fmtKv(v) {
  if (v === null || v === undefined) return '—';
  const s = String(v).trim();
  if (!s) return '—';
  return /^\d+(?:\.\d+)?$/.test(s) ? s + 'kV' : String(v);
}

// fmtM 距离展示幂等：纯数字拼 m 后缀；已带单位或其他文本原样（空 → —）。
export function fmtM(v) {
  const s = String(v == null ? '' : v).trim();
  if (!s) return '—';
  return /^\d+(?:\.\d+)?$/.test(s) ? s + 'm' : s;
}

// fmtDate 时间展示 'YYYY-MM-DD HH:mm'（withDate=false 仅日期）。
export function fmtDate(d, withTime = true) {
  const dt = d ? new Date(d) : new Date();
  if (Number.isNaN(dt.getTime())) return '';
  const p = (n) => String(n).padStart(2, '0');
  const day = `${dt.getFullYear()}-${p(dt.getMonth() + 1)}-${p(dt.getDate())}`;
  return withTime ? `${day} ${p(dt.getHours())}:${p(dt.getMinutes())}` : day;
}

// todayStr 当日日期 'YYYY-MM-DD'（处理时间默认值）。
export function todayStr() {
  return fmtDate(new Date(), false);
}

// overThreshold 垂直距离是否超阈值（vertical_num 非空且 ≤threshold）。
export function overThreshold(r, threshold) {
  return (
    threshold !== null &&
    threshold !== undefined &&
    r.vertical_num !== null &&
    r.vertical_num !== undefined &&
    Number(r.vertical_num) <= Number(threshold)
  );
}

// voltageNum 电压等级数值提取（线路卡排序用；非数字回退 0）。
export function voltageNum(v) {
  const n = parseFloat(String(v == null ? '' : v).replace(/[^\d.]/g, ''));
  return Number.isNaN(n) ? 0 : n;
}

// lineStats 单线路计数：未处理数 / 要紧数（未处理且超阈值）/ 总数。
export function lineStats(recs, threshold) {
  let unh = 0;
  let key = 0;
  recs.forEach((r) => {
    if (r.status === 'handled') return;
    unh++;
    if (overThreshold(r, threshold)) key++;
  });
  return { unh, key, total: recs.length };
}

// sortLineGroups 线路卡排序：要紧数降序 > 未处理数降序 > 电压等级数值降序 > 线路名称升序。
export function sortLineGroups(list, threshold) {
  const stats = {};
  list.forEach((g) => {
    stats[g.line_name] = lineStats(g.records || [], threshold);
  });
  return list.slice().sort((a, b) => {
    const sa = stats[a.line_name];
    const sb = stats[b.line_name];
    if (sb.key !== sa.key) return sb.key - sa.key;
    if (sb.unh !== sa.unh) return sb.unh - sa.unh;
    const dv = voltageNum(b.voltage_level) - voltageNum(a.voltage_level);
    if (dv) return dv;
    return String(a.line_name || '').localeCompare(String(b.line_name || ''), 'zh-CN');
  });
}

// readPhotoDataUrl 选片 → canvas 重绘导出 jpeg dataURL
// （统一格式、归一 EXIF 方向；长边压到 2560 以内，控制 15MB 上限）。
export function readPhotoDataUrl(file) {
  return new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onerror = () => reject(new Error('照片读取失败'));
    fr.onload = () => {
      const img = new Image();
      img.onload = () => {
        const w = img.naturalWidth;
        const h = img.naturalHeight;
        const sc = Math.min(1, 2560 / Math.max(w, h));
        const cv = document.createElement('canvas');
        cv.width = Math.max(1, Math.round(w * sc));
        cv.height = Math.max(1, Math.round(h * sc));
        cv.getContext('2d').drawImage(img, 0, 0, cv.width, cv.height);
        resolve(cv.toDataURL('image/jpeg', 0.88));
      };
      img.onerror = () => reject(new Error('照片解析失败，请更换图片'));
      img.src = fr.result;
    };
    fr.readAsDataURL(file);
  });
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

// BIZ_FIELDS 15 业务列（发现 12 列 + 处理 3 列）：暂存区编辑共用（key 与后端列名一致）。
export const BIZ_FIELDS = [
  ['voltage_level', '电压等级'],
  ['line_name', '线路名称'],
  ['section', '区段'],
  ['tree_species', '树种'],
  ['tree_count', '数量'],
  ['dist_small', '距小号'],
  ['dist_big', '距大号'],
  ['diameter', '直径'],
  ['horizontal', '水平'],
  ['vertical', '垂直'],
  ['finder_name', '发现人'],
  ['found_at', '发现时间'],
  ['handler_name', '处理人'],
  ['handle_note', '处理情况'],
  ['handled_at', '处理时间'],
];

// COMPACT_FIELDS 卡片紧凑信息行字段（区段/垂距在标题行，树种×数量/水平/发现时间在次级行）。
export const COMPACT_FIELDS = [
  ['position_note', '位置备注'],
  ['diameter', '直径(cm)'],
  ['dist_small', '距小号(m)'],
  ['dist_big', '距大号(m)'],
  ['finder_name', '发现人'],
];
