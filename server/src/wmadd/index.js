// 水印添加（移动端独立子应用，app_key wm-add）：复制出工日志「拍摄/选择照片并添加水印」能力，
// 与出工日志解耦——仅渲染水印并 base64 回图，不传 COS、不入库、不触发 Dify 验证（设计稿 design/wm-add.html）。
// 地理/杆塔/渲染均复用出工日志模块；本应用无业务表。
const express = require('express');
const auth = require('../middleware/auth');
const requireApp = require('../middleware/requireApp');
const { ok, fail } = require('../utils/resp');
const geo = require('../worklog/geo');
const { getTowers } = require('../worklog/towers');
const { renderWatermarkedPhoto } = require('../worklog/render-photo');
const Watermark = require('../worklog/watermark');

const router = express.Router();
router.use(auth, requireApp('wm-add'));

// GET /geo?lng=&lat=：按经纬度取当前「地点 + 天气」（腾讯），与出工日志同口径；
// 未配置 TENCENT_MAP_KEY 或调用失败时返回空串，前端留空手填
router.get('/geo', async (req, res, next) => {
  try {
    const lng = Number(req.query.lng);
    const lat = Number(req.query.lat);
    if (!Number.isFinite(lng) || !Number.isFinite(lat) || Math.abs(lng) > 180 || Math.abs(lat) > 90) {
      return fail(res, 400, 40022, '经纬度参数无效');
    }
    const r = await geo.fetchLocationWeather(lng, lat);
    return ok(res, r);
  } catch (err) {
    return next(err);
  }
});

// GET /towers：检修一班杆塔坐标全量（行 = [电压等级, 线路名称, 杆塔号, 经度, 纬度]），数据读写见 worklog/towers.js
router.get('/towers', async (req, res, next) => {
  try {
    return ok(res, getTowers());
  } catch (err) {
    return next(err);
  }
});

// 水印字段清洗（与出工日志 sanitizeWm 同口径）：字符串、去首尾空格、按渲染列宽截断
function sanitizeWm(wm) {
  const cut = (v, n) => (typeof v === 'string' ? v.trim().slice(0, n) : '');
  const fields = {
    content: cut(wm.content, 500),
    time: cut(wm.time, 32),
    weather: cut(wm.weather, 64),
    location: cut(wm.location, 250),
    longitude: cut(wm.longitude, 32),
    latitude: cut(wm.latitude, 32),
  };
  // 防伪码：14 位字符集内才采信前端值，否则服务端重新生成（不由用户输入）
  const code = cut(wm.antiCode, 14);
  fields.antiCode = /^[A-HJ-NP-Z2-9]{14}$/.test(code) ? code : Watermark.randomCode(14);
  return fields;
}

// POST /render：原图 base64 + wm 字段（含 EXIF orientation）→ 渲染水印 → base64 JPEG 回图。
// 只回图：不传 COS、不入库、不触发 Dify 验证
router.post('/render', async (req, res, next) => {
  try {
    const { image, wm } = req.body || {};
    const match = /^data:image\/(jpeg|jpg|png);base64,(.+)$/.exec(image || '');
    if (!match) return fail(res, 400, 40023, '照片格式应为 jpeg/png（base64 dataURL）');
    const buf = Buffer.from(match[2], 'base64');
    if (!buf.length || buf.length > 15 * 1024 * 1024) {
      return fail(res, 400, 40024, '照片大小应在 15MB 以内');
    }
    const out = await renderWatermarkedPhoto(buf, sanitizeWm(wm || {}), (wm && wm.orientation) || '');
    return ok(res, { image: `data:image/jpeg;base64,${out.toString('base64')}` });
  } catch (err) {
    return next(err);
  }
});

module.exports = router;
