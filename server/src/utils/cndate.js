// 北京日历日与每日定时排程工具（容器本地时区不固定，云端 Docker 通常为 UTC；中国无夏令时，按固定 UTC+8 换算）
// worklog / sgccclockin / dispatch-sync / photoverify 共用：
// 「当前 UTC 时间戳 + 8h」取北京年月日，再以 Date.UTC(北京年月日, 配置时分) − 8h 得触发的 UTC 时间戳
const CN_OFFSET_MS = 8 * 60 * 60 * 1000; // 北京时间固定偏移（UTC+8）

// 日期串转点分格式（YYYY-MM-DD → YYYY.MM.DD，COS key 与 checkWatermark 的 logDate 口径）
function dots(dateStr) {
  return String(dateStr).replace(/-/g, '.');
}

// 北京日历日 YYYY-MM-DD（按本地时区取日时，北京时间 0-8 点会落在前一天）
function todayCn() {
  return new Date(Date.now() + CN_OFFSET_MS).toISOString().slice(0, 10);
}

// 按北京时间 hh:mm 算下次触发 UTC 时间戳，今日已过顺延次日（Date.UTC 自动处理跨月进位）
function nextDailyRunUtc(hh, mm) {
  const now = Date.now();
  const cn = new Date(now + CN_OFFSET_MS); // 其 UTC 年月日即北京日历日
  let nextUtc = Date.UTC(cn.getUTCFullYear(), cn.getUTCMonth(), cn.getUTCDate(), hh, mm, 0) - CN_OFFSET_MS;
  if (nextUtc <= now) {
    nextUtc = Date.UTC(cn.getUTCFullYear(), cn.getUTCMonth(), cn.getUTCDate() + 1, hh, mm, 0) - CN_OFFSET_MS;
  }
  return { nextUtc, now };
}

module.exports = { CN_OFFSET_MS, dots, todayCn, nextDailyRunUtc };
