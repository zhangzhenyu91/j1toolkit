// 树障隐患展示与表单工具（与旧 server/public/treehazard.html 口径一致）。
// 通用展示/日期/数字等值/下载与字段表统一在 features/core/record-fields.js，
// 本文件只保留树障特有逻辑，避免与交跨各维护一份。
import { TREE_FIELDS, voltageNum } from '../core/record-fields.js';

export {
  dash,
  fmtKv,
  fmtM,
  fmtDate,
  todayStr,
  voltageNum,
  saveBlob,
  currentUserObj,
  NUMERIC_KEYS_TREE,
  TREE_FIELDS,
} from '../core/record-fields.js';

// BIZ_FIELDS 15 业务列（发现 12 列 + 处理 3 列）：暂存区编辑共用（key 与后端列名一致）。
export const BIZ_FIELDS = TREE_FIELDS.bizFields;

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
// （统一格式、归一 EXIF 方向；长边压到 2560 以内，上传前再转 multipart Blob，最大 50MB）。
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

// COMPACT_FIELDS 卡片紧凑信息行字段（区段/垂距在标题行，树种×数量/水平/发现时间在次级行）。
export const COMPACT_FIELDS = [
  ['position_note', '位置备注'],
  ['diameter', '直径(cm)'],
  ['dist_small', '距小号(m)'],
  ['dist_big', '距大号(m)'],
  ['finder_name', '发现人'],
];
