// 出工日志：高德地图「地点 + 天气」取值封装（选项「选择照片并添加水印」无历史照片时预填当前值用）
// 逆地理编码 /v3/geocode/regeo、实时天气 /v3/weather/weatherInfo（文档：lbs.amap.com/api/webservice/guide/api/georegeo）；
// regeo location 参数为「经度,纬度」顺序（高德文档约定，小数点后不超 6 位）；坐标系 gcj02，与 wx.getLocation 一致；
// 天气接口按 adcode 查询，需先逆编码取 adcode（两步串行，逆编码失败则天气一并留空）。
// 鉴权：query 参数 key（高德开放平台「Web 服务」类型 key）。
// base URL 走 env AMAP_BASE_URL（默认 https://restapi.amap.com），费用原因可切换中转站。
const axios = require('axios');
const config = require('../config');

function configured() {
  return !!config.worklog.amapMapKey;
}

// 高德空值字段返回空数组（[]）而非空串，统一收敛为字符串
function gstr(v) {
  return typeof v === 'string' ? v.trim() : '';
}

// 最近 POI 距离上限（米）：取范围最小一级（POI 兴趣点）中距离最近者为地标，超出则回退乡镇/街道（道路不取值）
const POI_MAX_DIST = 200;

// 按经纬度取 { location, weather }；任何一步失败均返回空串（前端留空手填），不抛出
// location 格式：区县·具体位置（如 尧都区·临汾电力高级技工学校）；weather 格式：天气 温度（如 多云 32°C）
async function fetchLocationWeather(lng, lat) {
  const empty = { location: '', weather: '' };
  if (!configured()) return empty;
  const coord = `${Number(lng).toFixed(6)},${Number(lat).toFixed(6)}`; // 高德：经度在前
  const key = config.worklog.amapMapKey;
  const base = config.worklog.amapBaseUrl;
  try {
    // 逆地理编码：地点拼「区县·地标」（如 尧都区·临汾电力高级技工学校）——区县取 district（空回退 city）；
    // 地标 = 距离最近的 POI（pois 需 extensions=all；距离超 POI_MAX_DIST 米不用），回退 乡镇/街道 township → 道路 streetNumber.street
    const geoResp = await axios.get(`${base}/v3/geocode/regeo`, {
      params: { location: coord, extensions: 'all', key },
      timeout: 8000,
    });
    const gd = geoResp.data || {};
    const rg = gd.status === '1' && gd.regeocode;
    const ac = rg && rg.addressComponent;
    if (!ac) {
      console.error('[出工日志] 高德逆地址解析返回异常：', gd.status, gd.info, '｜入参:', coord);
      return empty;
    }
    const area = gstr(ac.district) || gstr(ac.city);
    // pois 不保证按距离升序，显式取距离最小者
    let nearest = null;
    for (const p of Array.isArray(rg.pois) ? rg.pois : []) {
      const name = gstr(p && p.name);
      const dist = Number(p && p.distance);
      if (!name || !Number.isFinite(dist)) continue;
      if (!nearest || dist < nearest.dist) nearest = { name, dist };
    }
    const detail =
      (nearest && nearest.dist <= POI_MAX_DIST ? nearest.name : '') ||
      gstr(ac.township) ||
      gstr(ac.streetNumber && ac.streetNumber.street);
    const location = area && detail ? `${area}·${detail}` : area || detail;

    // 实时天气：按逆编码所得 adcode 查询，拼「天气 温度」（如 多云 32°C）
    let weather = '';
    const adcode = gstr(ac.adcode);
    if (adcode) {
      try {
        const wxResp = await axios.get(`${base}/v3/weather/weatherInfo`, {
          params: { city: adcode, extensions: 'base', key },
          timeout: 8000,
        });
        const wd = wxResp.data || {};
        const live = wd.status === '1' && wd.lives && wd.lives[0];
        if (live) weather = `${live.weather} ${Math.round(Number(live.temperature))}°C`;
        else console.error('[出工日志] 高德天气返回异常：', wd.status, wd.info, '｜入参:', adcode);
      } catch (err) {
        const d = err.response && err.response.data ? JSON.stringify(err.response.data) : err.message;
        console.error('[出工日志] 高德天气获取失败：', d, '｜入参:', adcode);
      }
    }
    return { location, weather };
  } catch (err) {
    const detail = err.response && err.response.data ? JSON.stringify(err.response.data) : err.message;
    console.error('[出工日志] 高德地点/天气获取失败：', detail, '｜入参:', coord);
    return empty;
  }
}

module.exports = { fetchLocationWeather };
