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

// 时间输入（Date / 时间戳 / 日期字符串）→ 'MM-DD HH:mm'
function formatTime(input) {
  if (!input) return '';
  const d = input instanceof Date ? input : new Date(input);
  if (Number.isNaN(d.getTime())) return '';
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// 轻提示
function toast(title) {
  wx.showToast({ title, icon: 'none', duration: 2000 });
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

module.exports = { greeting, formatTime, toast, wxLoginCode };
