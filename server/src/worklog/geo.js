// 出工日志：和风天气「地点 + 天气」取值封装（选项「选择照片并添加水印」无历史照片时预填当前值用）
// 逆地理用 GeoAPI /v2/city/lookup，实时天气用 /v7/weather/now；坐标系 gcj02，与 wx.getLocation 一致。
// 鉴权：请求头 X-QW-Api-Key（和风控制台 API KEY）；QWEATHER_API_ID 仅作配置记录，和风按 KEY 计费。
const axios = require('axios');
const config = require('../config');

function configured() {
  return !!config.worklog.qweatherApiKey;
}

// 按经纬度取 { location, weather }；任何一步失败均返回空串（前端留空手填），不抛出
async function fetchLocationWeather(lng, lat) {
  const empty = { location: '', weather: '' };
  if (!configured()) return empty;
  const coord = `${Number(lng).toFixed(6)},${Number(lat).toFixed(6)}`;
  const headers = { 'X-QW-Api-Key': config.worklog.qweatherApiKey };
  try {
    const [geoRes, wxRes] = await Promise.all([
      axios.get('https://geoapi.qweather.com/v2/city/lookup', { params: { location: coord }, headers, timeout: 8000 }),
      axios.get('https://devapi.qweather.com/v7/weather/now', { params: { location: coord }, headers, timeout: 8000 }),
    ]);

    // 地点：adm2·name（如 吕梁·中阳县）；市县同名时只保留一级
    let location = '';
    const locs = geoRes.data && geoRes.data.location;
    if (geoRes.data && String(geoRes.data.code) === '200' && locs && locs.length) {
      const g = locs[0];
      location = g.adm2 && g.name && g.adm2 !== g.name ? `${g.adm2}·${g.name}` : g.name || g.adm2 || '';
    }

    // 天气：多云 24°C 东北风2级（与今日水印相机样式一致）
    let weather = '';
    const now = wxRes.data && wxRes.data.now;
    if (wxRes.data && String(wxRes.data.code) === '200' && now) {
      weather = `${now.text} ${now.temp}°C ${now.windDir}${now.windScale}级`;
    }
    return { location, weather };
  } catch (err) {
    const detail = err.response && err.response.data ? JSON.stringify(err.response.data) : err.message;
    console.error('[出工日志] 和风地点/天气获取失败：', detail, '｜入参:', coord);
    return empty;
  }
}

module.exports = { fetchLocationWeather, configured };
