// 执行历史纯函数：跨六个工具合并、排序、筛选与状态语义。
import { NAV_ITEMS } from './navigation.mjs';

const BUSINESS = NAV_ITEMS.filter((item) => item.business);
const NAME_BY_ID = Object.fromEntries(NAV_ITEMS.map((item) => [item.id, item.title]));

export function mergeHistory(recordsByApp = {}) {
  const merged = [];
  for (const item of BUSINESS) {
    for (const rec of recordsByApp[item.id] || []) {
      merged.push({
        appID: item.id,
        appName: item.title,
        rec,
      });
    }
  }
  merged.sort((a, b) => {
    const at = String(a.rec.time || '');
    const bt = String(b.rec.time || '');
    if (at === bt) return String(b.rec.job_id || '').localeCompare(String(a.rec.job_id || ''));
    return bt.localeCompare(at);
  });
  return merged;
}

export function validHistoryDate(date) {
  const value = String(date || '').trim();
  if (!value) return true;
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const [year, month, day] = value.split('-').map(Number);
  const parsed = new Date(Date.UTC(year, month - 1, day));
  return (
    parsed.getUTCFullYear() === year &&
    parsed.getUTCMonth() === month - 1 &&
    parsed.getUTCDate() === day
  );
}

export function historyStatusKey(rec) {
  if ((rec?.result || {}).paused) return 'paused';
  return String(rec?.status || '');
}

export function historyStatusText(rec) {
  const key = historyStatusKey(rec);
  const map = {
    pending: { text: '排队中', theme: 'default' },
    running: { text: '执行中', theme: 'primary' },
    success: { text: '成功', theme: 'success' },
    failed: { text: '失败', theme: 'danger' },
    cancelled: { text: '已取消', theme: 'default' },
    paused: { text: '已暂停', theme: 'warning' },
  };
  return { key, ...(map[key] || { text: key || '未知', theme: 'default' }) };
}

export function historySearchText(entry) {
  const status = historyStatusText(entry.rec);
  return [
    entry.appID,
    entry.appName,
    entry.summary,
    entry.rec?.job_id,
    entry.rec?.time,
    status.text,
    entry.rec?.error,
    (entry.rec?.result || {}).dir,
  ]
    .filter(Boolean)
    .join('\n')
    .toLowerCase();
}

export function filterHistory(entries, filter = {}) {
  const date = String(filter.date || '').trim();
  if (!validHistoryDate(date)) return [];
  const appID = String(filter.appID || '').trim();
  const status = String(filter.status || '').trim();
  const keyword = String(filter.keyword || '')
    .trim()
    .toLowerCase();
  return entries.filter((entry) => {
    if (appID && entry.appID !== appID) return false;
    if (status && historyStatusKey(entry.rec) !== status) return false;
    if (
      date &&
      (String(entry.rec?.time || '').length < date.length ||
        String(entry.rec?.time || '').slice(0, date.length) !== date)
    ) {
      return false;
    }
    if (keyword && !historySearchText(entry).includes(keyword)) return false;
    return true;
  });
}

export function appName(appID) {
  return NAME_BY_ID[appID] || appID;
}

export const HISTORY_TOOL_OPTIONS = [
  { id: '', label: '全部工具' },
  ...BUSINESS.map((item) => ({ id: item.id, label: item.title })),
];

export const HISTORY_STATUS_OPTIONS = [
  { id: '', label: '全部状态' },
  { id: 'pending', label: '排队中' },
  { id: 'running', label: '执行中' },
  { id: 'success', label: '成功' },
  { id: 'failed', label: '失败' },
  { id: 'cancelled', label: '已取消' },
  { id: 'paused', label: '已暂停' },
];
