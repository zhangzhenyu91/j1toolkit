// 通用小工具

// 按时段生成问候语
function greeting() {
  const h = new Date().getHours();
  if (h < 6) return '夜深了';
  if (h < 9) return '早上好';
  if (h < 12) return '上午好';
  if (h < 14) return '中午好';
  if (h < 18) return '下午好';
  return '晚上好';
}

function pad(n) {
  return n < 10 ? `0${n}` : `${n}`;
}

// 文件大小展示：B / KB / MB（各分包曾各自重复定义，统一收编于此）
function fmtSize(n) {
  if (n >= 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  if (n >= 1024) return `${Math.round(n / 1024)} KB`;
  return `${n} B`;
}

// 取文件名扩展名（小写，无扩展名返回空串）
function extOf(name) {
  const i = (name || '').lastIndexOf('.');
  return i < 0 ? '' : name.slice(i + 1).toLowerCase();
}

// iOS 兼容的日期解析：Date / 时间戳直传；字符串含 'T'（ISO 格式）直接 new Date；
// 后端 DATE_FORMAT 输出的 'yyyy-MM-dd HH:mm:ss'（空格分隔）在 iOS 下 new Date 得 Invalid Date，
// 先把 '-' 换成 '/'（iOS 仅认 'yyyy/MM/dd HH:mm:ss'）；解析失败返回 null
function parseDate(input) {
  if (!input) return null;
  if (input instanceof Date) return Number.isNaN(input.getTime()) ? null : input;
  if (typeof input === 'number') {
    const dn = new Date(input);
    return Number.isNaN(dn.getTime()) ? null : dn;
  }
  let s = String(input).trim();
  if (!s) return null;
  if (s.indexOf('T') < 0) s = s.replace(/-/g, '/');
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? null : d;
}

// 时间输入（Date / 时间戳 / 日期字符串）→ 'MM-DD HH:mm'
function formatTime(input) {
  const d = parseDate(input);
  if (!d) return '';
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// 获取当前设备微信的登录 code（失败时返回空串，调用方自行兜底）
function wxLoginCode() {
  return new Promise((resolve) => {
    wx.login({
      success: (res) => resolve(res.code || ''),
      fail: () => resolve(''),
    });
  });
}

module.exports = { greeting, formatTime, parseDate, wxLoginCode, pad, fmtSize, extOf };
