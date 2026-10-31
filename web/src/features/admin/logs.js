// 应用日志管理端共享工具：视图/类别/时间范围、UTC 本地化、筛选序列化、键集游标、
// 健康告警分级与 NDJSON 裸下载。纯函数为主，供 AppLogsView 与 features/admin/api.js 复用；
// 不依赖 Vue/TDesign，便于 node --test 直接覆盖。

export const LOG_DEFAULT_RANGE_HOURS = 168; // 默认近 7 天仅为查询窗口（保留策略为永久）
export const LOG_PENDING_ALERT_THRESHOLD = 10000;

// 视图：默认 activity 排除访问日志噪声；access 只看访问；all 全部。
export const LOG_VIEW_OPTIONS = [
  { label: '业务与系统', value: 'activity' },
  { label: '访问日志', value: 'access' },
  { label: '全部日志', value: 'all' },
];

// 类别映射随日志采集范围补齐 access/admin/audit/runtime/security。
const CATEGORY_LABELS = Object.freeze({
  business: '业务事件',
  external: '外部调用',
  scheduler: '定时任务',
  config: '配置变更',
  message: '微信消息',
  access: '访问日志',
  admin: '管理操作',
  audit: '写操作审计',
  runtime: '运行错误',
  security: '安全事件',
});

export const LOG_CATEGORY_OPTIONS = [
  { label: '全部类别', value: '' },
  { label: CATEGORY_LABELS.business, value: 'business' },
  { label: CATEGORY_LABELS.external, value: 'external' },
  { label: CATEGORY_LABELS.scheduler, value: 'scheduler' },
  { label: CATEGORY_LABELS.config, value: 'config' },
  { label: CATEGORY_LABELS.message, value: 'message' },
  { label: CATEGORY_LABELS.access, value: 'access' },
  { label: CATEGORY_LABELS.admin, value: 'admin' },
  { label: CATEGORY_LABELS.audit, value: 'audit' },
  { label: CATEGORY_LABELS.runtime, value: 'runtime' },
  { label: CATEGORY_LABELS.security, value: 'security' },
];

export const LOG_LEVEL_OPTIONS = [
  { label: '全部级别', value: '' },
  { label: '信息', value: 'info' },
  { label: '警告', value: 'warn' },
  { label: '错误', value: 'error' },
  { label: '调试', value: 'debug' },
];

export const LOG_RANGE_OPTIONS = [
  { label: '近 24 小时', value: 24 },
  { label: '近 3 天', value: 72 },
  { label: '近 7 天', value: 168 },
  { label: '近 30 天', value: 720 },
  { label: '近 90 天', value: 2160 },
  { label: '近 365 天', value: 8760 },
  { label: '全部时间', value: 0 },
];

export const LOG_LANG_OPTIONS = [
  { label: '中文说明', value: 'cn' },
  { label: '原始日志', value: 'raw' },
];

// normalizeLogView 视图归一：未知/空值回退 activity。
export function normalizeLogView(view) {
  const value = String(view == null ? '' : view)
    .trim()
    .toLowerCase();
  return value === 'access' || value === 'all' ? value : 'activity';
}

export function categoryName(value) {
  if (!value) return '—';
  return CATEGORY_LABELS[value] || value;
}

export function levelName(value) {
  return { info: '信息', warn: '警告', error: '错误', debug: '调试' }[value] || value;
}

export function levelClass(value) {
  return { error: 'red', warn: 'orange', info: 'blue', debug: '' }[value] || '';
}

// rangeSince 查询窗口起点（ISO）；0 表示全部时间，返回空串。
export function rangeSince(hours, now = Date.now()) {
  const value = Number(hours);
  if (!Number.isFinite(value) || value <= 0) return '';
  return new Date(now - value * 3600 * 1000).toISOString();
}

// formatLogTime 把 RFC3339/Date 按本地或指定时区显示为 YYYY-MM-DD HH:mm:ss。
// 旧实现直接删掉 Z 把 UTC 当本地时间显示，这里改为真实时区换算。
export function formatLogTime(value, { timeZone, fallback = '—' } = {}) {
  if (value == null || value === '') return fallback;
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return fallback;
  try {
    const parts = new Intl.DateTimeFormat('en-CA', {
      timeZone,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hourCycle: 'h23',
    }).formatToParts(date);
    const map = {};
    for (const part of parts) map[part.type] = part.value;
    return `${map.year}-${map.month}-${map.day} ${map.hour}:${map.minute}:${map.second}`;
  } catch {
    return fallback;
  }
}

// serializeLogQuery 列表/导出共用筛选序列化：空值省略，view 恒有值。
export function serializeLogQuery({
  view = '',
  appCode = '',
  level = '',
  category = '',
  keyword = '',
  since = '',
  until = '',
  beforeTime = '',
  beforeId = '',
  page = 1,
  pageSize = 20,
} = {}) {
  const q = new URLSearchParams();
  q.set('view', normalizeLogView(view));
  if (appCode) q.set('app_code', String(appCode).trim());
  if (level) q.set('level', String(level).trim());
  if (category) q.set('category', String(category).trim());
  const k = String(keyword == null ? '' : keyword).trim();
  if (k) q.set('keyword', k);
  if (since) q.set('since', since instanceof Date ? since.toISOString() : String(since));
  if (until) q.set('until', until instanceof Date ? until.toISOString() : String(until));
  if (beforeTime && beforeId) {
    q.set(
      'before_time',
      beforeTime instanceof Date ? beforeTime.toISOString() : String(beforeTime)
    );
    q.set('before_id', String(beforeId));
  }
  q.set('page', String(page || 1));
  q.set('page_size', String(pageSize || 20));
  return q.toString();
}

// keysetFromItems 取当页最后一条作为下一页键集锚点；缺少时间/ID 时返回 null。
export function keysetFromItems(items) {
  if (!Array.isArray(items) || items.length === 0) return null;
  const last = items[items.length - 1];
  if (!last || !last.id || !last.occurred_at) return null;
  return { beforeTime: String(last.occurred_at), beforeId: Number(last.id) };
}

// mergeLogItems 追加「加载更多」结果，按 id 去重并保持顺序。
export function mergeLogItems(existing, incoming) {
  const seen = new Set();
  const out = [];
  for (const item of [...(existing || []), ...(incoming || [])]) {
    if (!item || seen.has(item.id)) continue;
    seen.add(item.id);
    out.push(item);
  }
  return out;
}

// isTotalKnown 后端 total=-1 表示未计数（关键词/深分页/键集）。
export function isTotalKnown(total) {
  if (total === null || total === undefined || total === '') return false;
  const n = Number(total);
  return Number.isFinite(n) && n >= 0;
}

// healthAlerts 健康快照派生告警（磁盘/入库为 error，积压/数据库未接入为 warn）。
export function healthAlerts(health) {
  const h = health || {};
  const out = [];
  if (h.write_errors > 0) {
    out.push({ level: 'error', text: `日志文件写入失败 ${h.write_errors} 次` });
  }
  if (h.disk_fallbacks > 0) {
    out.push({ level: 'error', text: `磁盘写入失败后回退数据库直写 ${h.disk_fallbacks} 次` });
  }
  if (h.insert_errors > 0) {
    out.push({ level: 'error', text: `日志入库失败 ${h.insert_errors} 次` });
  }
  if (Number(h.pending) >= LOG_PENDING_ALERT_THRESHOLD) {
    out.push({ level: 'warn', text: `待补录日志积压 ${h.pending} 条` });
  }
  if (h.db_connected === false) {
    out.push({ level: 'warn', text: '日志数据库未接入，日志仅写入文件' });
  }
  return out;
}

export function healthSeverity(health) {
  const alerts = healthAlerts(health);
  if (alerts.some((a) => a.level === 'error')) return 'error';
  return alerts.length ? 'warn' : 'ok';
}

// logEventTime 事件时间：occurred_at 为 Go 零值时间（0001-01-01）时回退 created_at，
// 避免零值字符串遮住真实记录时间。
export function logEventTime(row) {
  if (!row) return '';
  const occurred = row.occurred_at;
  if (occurred) {
    const date = occurred instanceof Date ? occurred : new Date(occurred);
    if (!Number.isNaN(date.getTime()) && date.getUTCFullYear() > 1) {
      return occurred;
    }
  }
  return row.created_at || '';
}

// syncViewForCategory 视图/类别联动：
// - 选「访问日志」类别且当前 activity → 切 access；
// - access 视图选非 access 类别 → 切回 activity（access 日志类别恒为 access）；
// - all 视图保持全部。
export function syncViewForCategory(view, category) {
  const current = normalizeLogView(view);
  const value = String(category == null ? '' : category).trim();
  if (value === 'access') {
    return current === 'all' ? 'all' : 'access';
  }
  if (value && current === 'access') {
    return 'activity';
  }
  return current;
}

// syncCategoryForView 切换视图时清理互斥类别，避免 access 视图带 business 类别等空结果。
export function syncCategoryForView(view, category) {
  const current = normalizeLogView(view);
  const value = String(category == null ? '' : category).trim();
  if (current === 'access' && value && value !== 'access') return '';
  if (current === 'activity' && value === 'access') return '';
  return value;
}

// logTotalText total=-1（未计数）时区分「仍有更早日志」与「已全部加载」，不误导用户。
export function logTotalText(total, loaded, hasMore) {
  const count = Number(loaded) || 0;
  if (isTotalKnown(total)) {
    return `共 ${Number(total)} 条，已加载 ${count} 条`;
  }
  if (hasMore) {
    return `已加载 ${count} 条，仍有更早日志（未统计总数）`;
  }
  return `已加载全部 ${count} 条（未统计总数）`;
}

// exportWarning 磁盘回退过 DB 直写时，文件副本可能缺少这些记录，导出前必须明确提示。
// 兼容 health 与 ticket 响应任一来源（health 拉取失败时仍以 ticket 字段提示）。
export function exportWarning(health, ticket) {
  const healthFallbacks = Number(health && health.disk_fallbacks) || 0;
  const ticketFallbacks = Number(ticket && (ticket.diskFallbacks ?? ticket.disk_fallbacks)) || 0;
  const incomplete = !!(ticket && (ticket.fileCopyIncomplete ?? ticket.file_copy_incomplete));
  const count = Math.max(healthFallbacks, ticketFallbacks);
  if (count <= 0 && !incomplete) return '';
  const suffix = count > 0 ? `（磁盘回退 ${count} 次）` : '';
  return `日志文件副本可能不含数据库直写记录${suffix}，导出可能不完整。`;
}

// exportStartedMessage 中性下载提示：只说明已发起，要求核对流末 complete/failed，
// 不暗示浏览器已保存完整文件。
export function exportStartedMessage(filename = 'app-logs.ndjson') {
  return `已发起下载 ${filename}，请核对文件末行 complete/failed（failed 表示导出中断）。`;
}

// normalizeExportTicket 校验一次性导出票据响应，只允许同源导出下载 URL。
export function normalizeExportTicket(data) {
  const ticket = String((data && data.ticket) || '').trim();
  if (!ticket) {
    throw new Error('导出票据响应不完整：缺少 ticket');
  }
  const url = String((data && data.url) || '').trim();
  if (!url) {
    throw new Error('导出票据响应不完整：缺少下载地址');
  }
  if (!url.startsWith('/api/v1/app-logs/export/download?ticket=')) {
    throw new Error('导出票据下载地址非法');
  }
  const query = new URLSearchParams(url.split('?')[1] || '');
  if (query.get('ticket') !== ticket) {
    throw new Error('导出票据与下载地址不匹配');
  }
  const filename = String((data && data.filename) || '').trim() || 'app-logs.ndjson';
  return {
    ticket,
    url,
    filename,
    expiresIn: Number(data && data.expires_in) || 0,
    fileCopyIncomplete: !!(data && data.file_copy_incomplete),
    diskFallbacks: Number(data && data.disk_fallbacks) || 0,
  };
}

// triggerBrowserDownload 用原生 <a download> 触发浏览器下载：文件由浏览器流式落盘，
// 不经过 fetch/blob，也不把导出内容读入 JS 内存。
export function triggerBrowserDownload(
  url,
  filename = 'app-logs.ndjson',
  { documentRef = typeof document !== 'undefined' ? document : null } = {}
) {
  if (!documentRef || typeof documentRef.createElement !== 'function') return false;
  const link = documentRef.createElement('a');
  link.href = url;
  link.download = filename;
  link.rel = 'noopener';
  if (link.style) link.style.display = 'none';
  const parent = documentRef.body || documentRef.documentElement;
  if (parent && typeof parent.appendChild === 'function') parent.appendChild(link);
  link.click();
  if (typeof link.remove === 'function') link.remove();
  return true;
}

// isUnauthorizedError 401 判定（兼容 request 的 code/status 与裸通道 status）。
function isUnauthorizedError(err) {
  if (!err) return false;
  return err.status === 401 || err.code === 401 || err.code === 'unauthorized';
}

// startLogExport 导出入口：先申请一次性短时 ticket，再让浏览器原生下载。
// 不读取响应正文；401 且此前未处理时回调 onUnauthorized（request 通道已自动清会话）。
export async function startLogExport(params, { issueTicket, onUnauthorized, documentRef } = {}) {
  if (typeof issueTicket !== 'function') {
    throw new Error('导出票据签发未接入');
  }
  let data;
  try {
    data = await issueTicket(params);
  } catch (err) {
    if (onUnauthorized && isUnauthorizedError(err) && !(err && err.sessionHandled)) {
      onUnauthorized(err);
    }
    throw err;
  }
  const ticket = normalizeExportTicket(data);
  if (!triggerBrowserDownload(ticket.url, ticket.filename, { documentRef })) {
    const err = new Error('当前环境无法触发浏览器下载，请稍后重试');
    err.code = 'download_unavailable';
    throw err;
  }
  return ticket;
}
