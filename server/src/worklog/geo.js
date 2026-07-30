// 出工日志：腾讯地图「地点 + 天气」取值封装（选项「选择照片并添加水印」无历史照片时预填当前值用）
// 逆地址解析 /ws/geocoder/v1（文档：lbs.qq.com/service/webService/webServiceGuide/address/Gcoder），
// 实时天气 /ws/weather/v1（…/weatherinfo）；坐标系 gcj02，与 wx.getLocation 一致；
// 两接口 location 参数均为「纬度,经度」顺序（腾讯文档约定，勿改成经度在前）。
// 鉴权：query 参数 key（腾讯位置服务控制台 WebServiceAPI 类型 key）。
const axios = require('axios');
const config = require('../config');

function configured() {
  return !!config.worklog.tencentMapKey;
}

// 按经纬度取 { location, weather }；任何一步失败均返回空串（前端留空手填），不抛出
// location 格式：区县·具体位置（如 孝义市·新安街街道）；weather 格式：天气·温度（如 多云·23℃）
async function fetchLocationWeather(lng, lat) {
  const empty = { location: '', weather: '' };
  if (!configured()) return empty;
  const coord = `${Number(lat).toFixed(6)},${Number(lng).toFixed(6)}`; // 腾讯：纬度在前
  const key = config.worklog.tencentMapKey;
  try {
    // allSettled：逆地理/天气互不影响，单项失败另一项仍可返回
    const [geoSet, wxSet] = await Promise.allSettled([
      axios.get('https://apis.map.qq.com/ws/geocoder/v1/', {
        params: { location: coord, key },
        timeout: 8000,
      }),
      axios.get('https://apis.map.qq.com/ws/weather/v1/', {
        params: { location: coord, type: 'now', key },
        timeout: 8000,
      }),
    ]);
    if (geoSet.status === 'rejected') {
      const d = geoSet.reason.response && geoSet.reason.response.data ? JSON.stringify(geoSet.reason.response.data) : geoSet.reason.message;
      console.error('[出工日志] 腾讯逆地址解析失败：', d, '｜入参:', coord);
    }
    if (wxSet.status === 'rejected') {
      const d = wxSet.reason.response && wxSet.reason.response.data ? JSON.stringify(wxSet.reason.response.data) : wxSet.reason.message;
      console.error('[出工日志] 腾讯天气获取失败：', d, '｜入参:', coord);
    }

    // 地点：区县·具体位置。具体位置优先取乡镇/街道（address_reference.town），
    // 无则回退道路（address_component.street）；区县为空回退市
    let location = '';
    const geoRes = geoSet.status === 'fulfilled' ? geoSet.value : null;
    const r = geoRes && geoRes.data && geoRes.data.result;
    if (geoRes && geoRes.data && geoRes.data.status === 0 && r) {
      const ac = r.address_component || {};
      const ref = r.address_reference || {};
      const area = ac.district || ac.city || '';
      const detail = (ref.town && ref.town.title) || ac.street || '';
      location = area && detail ? `${area}·${detail}` : area || detail;
    } else if (geoRes && geoRes.data && geoRes.data.status !== 0) {
      console.error('[出工日志] 腾讯逆地址解析返回异常：', geoRes.data.status, geoRes.data.message);
    }

    // 天气：天气·温度（如 多云·23℃）
    let weather = '';
    const wxRes = wxSet.status === 'fulfilled' ? wxSet.value : null;
    const rt = wxRes && wxRes.data && wxRes.data.result && wxRes.data.result.realtime;
    if (wxRes && wxRes.data && wxRes.data.status === 0 && rt && rt.length && rt[0].infos) {
      const infos = rt[0].infos;
      weather = `${infos.weather}·${Math.round(Number(infos.temperature))}℃`;
    } else if (wxRes && wxRes.data && wxRes.data.status !== 0) {
      console.error('[出工日志] 腾讯天气返回异常：', wxRes.data.status, wxRes.data.message);
    }
    return { location, weather };
  } catch (err) {
    const detail = err.response && err.response.data ? JSON.stringify(err.response.data) : err.message;
    console.error('[出工日志] 腾讯地点/天气获取失败：', detail, '｜入参:', coord);
    return empty;
  }
}

module.exports = { fetchLocationWeather, configured };
