// 出工日志日期工具：本地日历日/月、中文日期、点分展示与月平移。
// 一律按本机时区拼年月日，禁用 toISOString().slice()——那是 UTC 日历日，
// 凌晨时段（UTC+8 的 0–8 点）会把「今天」退成昨天；月末 Date 同理。

export function pad2(n) {
  return String(n).padStart(2, '0');
}

// localDay 本地日历日 YYYY-MM-DD。
export function localDay(d = new Date()) {
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`;
}

// localMonth 本地年月 YYYY-MM。
export function localMonth(d = new Date()) {
  return localDay(d).slice(0, 7);
}

// fmtDateCN 日期中文（X 年 X 月 X 日）；空值/格式不合法返回空串。
export function fmtDateCN(d) {
  const [y, m, dd] = String(d || '').split('-');
  if (!y || !m || !dd) return '';
  return `${y} 年 ${Number(m)} 月 ${Number(dd)} 日`;
}

// dateDot 日期点分展示（2026-10-31 → 2026.10.31）。
export function dateDot(d) {
  return String(d || '').replace(/-/g, '.');
}

// monthLabel 年月标题（2026-10 → 2026 年 10 月）。
export function monthLabel(month) {
  const [y, m] = String(month || '')
    .split('-')
    .map(Number);
  if (!y || !m) return '';
  return `${y} 年 ${m} 月`;
}

// clampDayToMonth 把日号裁剪到该月最后一天（月末平移不溢出）。
export function clampDayToMonth(month, day) {
  const [y, m] = String(month || '')
    .split('-')
    .map(Number);
  if (!y || !m) return '';
  const maxDay = new Date(y, m, 0).getDate();
  const want = Math.min(Math.max(Number(day) || 1, 1), maxDay);
  return `${month}-${pad2(want)}`;
}

// shiftMonth 月份平移并返回新选中日（保留原日号，月末自动裁剪）。
export function shiftMonth(month, delta, selected) {
  const [y, m] = String(month || '')
    .split('-')
    .map(Number);
  const d = new Date(y, m - 1 + Number(delta), 1);
  const next = `${d.getFullYear()}-${pad2(d.getMonth() + 1)}`;
  const day = selected ? selected.slice(8, 10) : '01';
  return { month: next, selected: clampDayToMonth(next, day) };
}
