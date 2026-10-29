// 水印添加纯工具（移植 server/public/wmadd.html 同名函数，与小程序 pkg-wmadd 同口径）：
// 时间/防伪码/坐标抖动/方向后缀/WGS84→GCJ02/位图转 JPEG/下载/文件选择。
// 无业务依赖，供 features/wmadd 各组件共用。

// pad 两位补零。
export function pad(n) {
  return (n < 10 ? '0' : '') + n;
}

// fmtWmTime 水印拍摄时间格式：2026.07.30 11:02（与今日水印相机样式一致）。
export function fmtWmTime(d) {
  return (
    d.getFullYear() +
    '.' +
    pad(d.getMonth() + 1) +
    '.' +
    pad(d.getDate()) +
    ' ' +
    pad(d.getHours()) +
    ':' +
    pad(d.getMinutes())
  );
}

// 防伪码字符集：14 位大写字母+数字，去 0/O、1/I 等易混淆字符（与服务端校验规则一致）。
const CODE_CHARS = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789';

// genAntiCode 生成一枚防伪码。
export function genAntiCode() {
  let out = '';
  for (let i = 0; i < 14; i += 1)
    out += CODE_CHARS.charAt(Math.floor(Math.random() * CODE_CHARS.length));
  return out;
}

// jitterCoord 经纬度随机偏移：角度随机，半径 ≤maxMeters（杆塔坐标带入取 50，同小程序）。
export function jitterCoord(lng, lat, maxMeters) {
  const r = Math.random() * maxMeters;
  const a = Math.random() * Math.PI * 2;
  const dLat = (r * Math.sin(a)) / 111320;
  const cosLat = Math.cos((lat * Math.PI) / 180);
  const dLng = (r * Math.cos(a)) / (111320 * (Math.abs(cosLat) > 1e-6 ? cosLat : 1e-6));
  return { lng: (lng + dLng).toFixed(6), lat: (lat + dLat).toFixed(6) };
}

// withDegSuffix 经纬度补方向后缀：只填数字时自动补 °E/°N（已带符号则原样，同小程序）。
export function withDegSuffix(v, suffix) {
  const s = String(v || '').trim();
  if (!s) return '';
  return /[°NSEWnsew]/.test(s) ? s : s + suffix;
}

// wgs2gcj WGS84 → GCJ02：浏览器 geolocation 为 WGS84，/geo 逆地址解析为高德 GCJ02 口径；中国境外原样返回。
export function wgs2gcj(lng, lat) {
  const a = 6378245.0;
  const ee = 0.006693421622965943;
  if (lng < 72.004 || lng > 137.8347 || lat < 0.8293 || lat > 55.8271) return [lng, lat];
  function tLat(x, y) {
    let r = -100.0 + 2.0 * x + 3.0 * y + 0.2 * y * y + 0.1 * x * y + 0.2 * Math.sqrt(Math.abs(x));
    r += ((20.0 * Math.sin(6.0 * x * Math.PI) + 20.0 * Math.sin(2.0 * x * Math.PI)) * 2.0) / 3.0;
    r += ((20.0 * Math.sin(y * Math.PI) + 40.0 * Math.sin((y / 3.0) * Math.PI)) * 2.0) / 3.0;
    r +=
      ((160.0 * Math.sin((y / 12.0) * Math.PI) + 320.0 * Math.sin((y * Math.PI) / 30.0)) * 2.0) /
      3.0;
    return r;
  }
  function tLng(x, y) {
    let r = 300.0 + x + 2.0 * y + 0.1 * x * x + 0.1 * x * y + 0.1 * Math.sqrt(Math.abs(x));
    r += ((20.0 * Math.sin(6.0 * x * Math.PI) + 20.0 * Math.sin(2.0 * x * Math.PI)) * 2.0) / 3.0;
    r += ((20.0 * Math.sin(x * Math.PI) + 40.0 * Math.sin((x / 3.0) * Math.PI)) * 2.0) / 3.0;
    r +=
      ((150.0 * Math.sin((x / 12.0) * Math.PI) + 300.0 * Math.sin((x / 30.0) * Math.PI)) * 2.0) /
      3.0;
    return r;
  }
  const dLat0 = tLat(lng - 105.0, lat - 35.0);
  const dLng0 = tLng(lng - 105.0, lat - 35.0);
  const radLat = (lat / 180.0) * Math.PI;
  let magic = Math.sin(radLat);
  magic = 1 - ee * magic * magic;
  const sqrtMagic = Math.sqrt(magic);
  const dLat = (dLat0 * 180.0) / (((a * (1 - ee)) / (magic * sqrtMagic)) * Math.PI);
  const dLng = (dLng0 * 180.0) / ((a / sqrtMagic) * Math.cos(radLat) * Math.PI);
  return [lng + dLng, lat + dLat];
}

// tsName 时间戳文件名：20260730_110203。
export function tsName(d) {
  return (
    '' +
    d.getFullYear() +
    pad(d.getMonth() + 1) +
    pad(d.getDate()) +
    '_' +
    pad(d.getHours()) +
    pad(d.getMinutes()) +
    pad(d.getSeconds())
  );
}

// downloadDataUrl dataURL → Blob 触发浏览器下载（被拦截时调用方提示手动下载）。
export function downloadDataUrl(dataUrl, name) {
  const bin = atob(String(dataUrl).replace(/^data:image\/\w+;base64,/, ''));
  const arr = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i += 1) arr[i] = bin.charCodeAt(i);
  const url = URL.createObjectURL(new Blob([arr], { type: 'image/jpeg' }));
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => {
    URL.revokeObjectURL(url);
  }, 4000);
}

// bitmapToJpeg 位图 → JPEG dataURL：长边压到 maxEdge 内，黑底兜透明通道（同小程序裁剪导出口径）。
export function bitmapToJpeg(bmp, maxEdge) {
  const k = Math.min(1, maxEdge / Math.max(bmp.width, bmp.height));
  const w = Math.max(1, Math.round(bmp.width * k));
  const h = Math.max(1, Math.round(bmp.height * k));
  const cv = document.createElement('canvas');
  cv.width = w;
  cv.height = h;
  const cx = cv.getContext('2d');
  cx.fillStyle = '#000000';
  cx.fillRect(0, 0, w, h);
  cx.drawImage(bmp, 0, 0, w, h);
  return cv.toDataURL('image/jpeg', 0.92);
}

// pickImageFile 唤起系统文件选择（单张 jpeg/png）；capture=true 时移动浏览器直接调起相机。
// 返回 Promise<File>；取消选择 resolve(null)。
export function pickImageFile(capture) {
  return new Promise((resolve) => {
    const inp = document.createElement('input');
    inp.type = 'file';
    inp.accept = 'image/jpeg,image/png';
    if (capture) inp.setAttribute('capture', 'environment');
    inp.style.display = 'none';
    document.body.appendChild(inp);
    inp.addEventListener('change', () => {
      const f = inp.files && inp.files[0];
      inp.remove();
      resolve(f || null);
    });
    inp.click();
  });
}
