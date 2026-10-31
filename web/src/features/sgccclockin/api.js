// 商旅打卡 feature API：直连 Go 服务 /api/v1/sgcc（go/internal/apps/sgccclockin）。
// 响应契约 {code,message,data}；本文件统一解包，失败抛出带 code 的 Error（err.data 附业务明细）。
// 鉴权与 web/src/api.js 同一 JWT（localStorage 优先、sessionStorage 回退的 'shade.token'）；不修改公共装配文件。
// 隐私口径：可见文案统称「商旅平台 / 商旅 App」，不出现平台所属方名称。

import { createClient } from '../core/request.js';

const BASE = '/api/v1/sgcc';

// request 共享请求层：注入 Bearer、解析平台信封、401/40101 统一清理会话。
const client = createClient();
function buildURL(path, params = {}) {
  return client.buildURL(path, params);
}
const request = client.request;

// ---- 账号绑定（图形码 / 滑块双通道）----

// loginCaptcha 取图形验证码（风控窗口期回 40035，前端降级滑块）。
export function loginCaptcha({ mobile } = {}) {
  return request(`${BASE}/login/captcha`, {
    method: 'POST',
    body: JSON.stringify({ mobile }),
  });
}

// loginSms 发短信：图形码通道传 checkImgCode；滑块通道传 captchaToken+constId。
export function loginSms({ mobile, checkImgCode, captchaToken, constId } = {}) {
  return request(`${BASE}/login/sms`, {
    method: 'POST',
    body: JSON.stringify({ mobile, checkImgCode, captchaToken, constId }),
  });
}

// bindAccount 短信码或密码换 token 完成绑定（返回 {bound:true}）。
export function bindAccount({
  mobile,
  checkCode,
  password,
  checkImgCode,
  captchaToken,
  constId,
  deviceType,
  systemVersion,
} = {}) {
  return request(`${BASE}/login/bind`, {
    method: 'POST',
    body: JSON.stringify({
      mobile,
      checkCode,
      password,
      checkImgCode,
      captchaToken,
      constId,
      deviceType,
      systemVersion,
    }),
  });
}

// ---- 本人账号 ----

// fetchAccount 本人绑定状态 + 今日打卡/费用摘要（未绑定回 {bound:false}）。
export function fetchAccount() {
  return request(`${BASE}/account`);
}

// updateAccountDevice 本人改设备口径。
export function updateAccountDevice({ deviceType, systemVersion } = {}) {
  return request(`${BASE}/account/device`, {
    method: 'PUT',
    body: JSON.stringify({ deviceType, systemVersion }),
  });
}

// unbindAccount 解绑（本人）。
export function unbindAccount() {
  return request(`${BASE}/account`, { method: 'DELETE' });
}

// fetchExpiredList 本班登录已过期的绑定成员名单。
export function fetchExpiredList() {
  return request(`${BASE}/expired`);
}

// fetchAccounts 本班组全部绑定账号（管理端）。
export function fetchAccounts() {
  return request(`${BASE}/accounts`);
}

// updateAccountDeviceAdmin 管理端改设备口径。
export function updateAccountDeviceAdmin(id, { deviceType, systemVersion } = {}) {
  return request(`${BASE}/accounts/${id}/device`, {
    method: 'PUT',
    body: JSON.stringify({ deviceType, systemVersion }),
  });
}

// ---- 当日打卡 / 费用 ----

// fetchDay 当日打卡/费用摘要（{date, clockin, fee}）。
export function fetchDay({ member_id: memberID, date } = {}) {
  return request(buildURL(`${BASE}/day`, { member_id: memberID, date }));
}

// postClockin 打卡（seq 1 首次 / 2 结束 / 0 更新地点，更新需带 detail_id）。
// 入参 snake_case 与 Go 路由一致（city_code / city_name / detail_id）。
export function postClockin({
  member_id: memberID,
  date,
  seq,
  position,
  longitude,
  latitude,
  city_code: cityCode,
  city_name: cityName,
  detail_id: detailId,
} = {}) {
  return request(`${BASE}/clockin`, {
    method: 'POST',
    body: JSON.stringify({
      member_id: memberID,
      date,
      seq,
      position,
      longitude,
      latitude,
      city_code: cityCode,
      city_name: cityName,
      detail_id: detailId,
    }),
  });
}

// fetchFee 费用弹层数据源（本地摘要 + 商旅实时模板 + 成本分配现状）。
export function fetchFee({ member_id: memberID, date } = {}) {
  return request(buildURL(`${BASE}/fee`, { member_id: memberID, date }));
}

// postFee 保存费用（本地优先 + 商旅完整模板 read-modify-write）。
export function postFee({ member_id: memberID, date, foodFee, transitFee, overrides } = {}) {
  return request(`${BASE}/fee`, {
    method: 'POST',
    body: JSON.stringify({
      member_id: memberID,
      date,
      foodFee,
      transitFee,
      ...(overrides ? { overrides } : {}),
    }),
  });
}

// ---- 照片任务 / 同步 ----

// resyncPhoto 商旅照片重新同步（任务化，返回 {opId}）。
export function resyncPhoto(photoId) {
  return request(`${BASE}/photos/${photoId}/resync`, { method: 'POST' });
}

// fetchOpStatus 照片任务状态（opId → PhotoOp：progress/step/failed/error）。
export function fetchOpStatus(opId) {
  return request(buildURL(`${BASE}/op/status`, { op_id: opId }));
}

// syncPull 手动从商旅拉取（逐成员口径：member_id+date，返回 {jobId}）。
export function syncPull({ member_id: memberID, date } = {}) {
  return request(`${BASE}/sync/pull`, {
    method: 'POST',
    body: JSON.stringify({ member_id: memberID, date }),
  });
}

// fetchSyncProgress 同步任务进度（{id,total,done,errors,kind,date,finished}）。
export function fetchSyncProgress(jobId) {
  return request(buildURL(`${BASE}/sync/progress`, { job_id: jobId }));
}

// fetchSyncActive 进行中的同步任务与照片任务（{jobs,ops}）。
export function fetchSyncActive() {
  return request(`${BASE}/sync/active`);
}

// fetchSyncLogs 核查流水（date/member_id/scope/type/result 过滤，最多 200 条）。
export function fetchSyncLogs({ date, member_id: memberID, scope, type, result } = {}) {
  return request(buildURL(`${BASE}/sync/logs`, { date, member_id: memberID, scope, type, result }));
}

// clearSyncLogs 清理核查流水（管理端）。
export function clearSyncLogs() {
  return request(`${BASE}/sync/logs`, { method: 'DELETE' });
}
