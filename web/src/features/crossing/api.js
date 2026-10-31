// 交跨记录 API 层：组件只调用本文件导出方法，不直接 fetch。
// live 模式请求 Go /api/v1/crossing/*（{code,message,data} 信封，超管经 team_id 指定班组）；
// demo 模式返回 demo.js 静态演示数据（DEMO ONLY），不发起任何网络请求。
import { getApiMode } from '../../api.js';
import * as demo from './demo.js';
import { normalizeList } from '../core/list-response.js';
import { createClient } from '../core/request.js';

function isDemo() {
  return getApiMode() === 'demo';
}

// request 平台信封请求（共享请求层）：注入 Bearer、解析 {code,message,data}、
// 业务码非 0 抛带 code 的错误、401 清理会话；requestRaw 供 xlsx 下载取原始 Response。
const client = createClient();
const request = client.request;
const requestRaw = client.requestRaw;

// withTeam 超管指定班组时为 path 追加 team_id。
export function withTeam(path, teamId) {
  if (!teamId) return path;
  return path + (path.includes('?') ? '&' : '?') + 'team_id=' + teamId;
}

// teamBody POST/PUT body 注入 team_id。
export function teamBody(teamId, obj) {
  return teamId ? { team_id: teamId, ...obj } : { ...obj };
}

// ---- 端点 ----

// getDashboard 看板：{total,week_new,hazard,pending_lines,pending_conflicts,staging_batches}
export function getDashboard(teamId) {
  if (isDemo()) return demo.delay(demo.dashboard);
  return request(withTeam('/api/v1/crossing/dashboard', teamId));
}

// getRecords 记录列表：{lines:[{voltage_level,line_name,records}],unmatched}
export function getRecords(teamId) {
  if (isDemo()) return demo.delay(demo.records);
  return request(withTeam('/api/v1/crossing/records', teamId));
}

// deleteRecord 删除记录（班管/超管）
export function deleteRecord(id, teamId) {
  if (isDemo()) return demo.delay({ id });
  return request(withTeam(`/api/v1/crossing/records/${id}`, teamId), { method: 'DELETE' });
}

// createRecord 手动新增记录（班管/超管）：body 为 13 业务列（line_name 必填），
// pole_from/pole_to 数值锚点由后端按区段文本解析（带字母杆号区段原文保留、锚点可空）。
export function createRecord(body, teamId) {
  if (isDemo()) return demo.delay({ id: 0 });
  return request(withTeam('/api/v1/crossing/records', teamId), {
    method: 'POST',
    body: JSON.stringify(teamBody(teamId, body)),
  });
}

// updateRecord 编辑记录（班管/超管）：body 为业务列白名单（出现哪个键改哪个键），
// 原地更新保留记录 ID / 来源 / 直推幂等键 / 同步点，班组隔离由后端兜底。
export function updateRecord(id, body, teamId) {
  if (isDemo()) return demo.delay({ id });
  return request(withTeam(`/api/v1/crossing/records/${id}`, teamId), {
    method: 'PUT',
    body: JSON.stringify(teamBody(teamId, body)),
  });
}

// exportDownload 统计表 xlsx 下载（二进制；line= 单线路，split=1 按线路分表）。
export async function exportDownload(query, teamId) {
  if (isDemo()) throw new Error('演示模式不提供导出下载，请切换 live 模式');
  const res = await requestRaw(withTeam(`/api/v1/crossing/export/交跨统计表.xlsx${query}`, teamId));
  if (!res.ok) {
    let msg = `导出失败（${res.status}）`;
    try {
      const ej = await res.json();
      if (ej && ej.message) msg = ej.message;
    } catch (e) {
      // 非 JSON 错误体，沿用通用文案
    }
    const err = new Error(msg);
    err.code = res.status;
    throw err;
  }
  return res;
}

// stagingBatches 暂存批次列表（班管/超管）
export function stagingBatches(teamId) {
  if (isDemo()) return demo.delay(demo.stagingBatches);
  return request(withTeam('/api/v1/crossing/staging/batches', teamId));
}

// stagingBatch 批次详情：{batch,records}
export function stagingBatch(id, teamId) {
  if (isDemo()) return demo.delay(demo.stagingDetail(id));
  return request(withTeam(`/api/v1/crossing/staging/batches/${id}`, teamId));
}

// updateStagingRecord 更新暂存行业务列（白名单字段）
export function updateStagingRecord(id, body, teamId) {
  if (isDemo()) return demo.delay({ id });
  return request(withTeam(`/api/v1/crossing/staging/records/${id}`, teamId), {
    method: 'PUT',
    body: JSON.stringify(teamBody(teamId, body)),
  });
}

// importBatch 转入正式表（recordIds 缺省 = 全部）
export function importBatch(batchId, recordIds, teamId) {
  if (isDemo()) return demo.delay({ imported: recordIds ? recordIds.length : 2 });
  return request(withTeam(`/api/v1/crossing/staging/batches/${batchId}/import`, teamId), {
    method: 'POST',
    body: JSON.stringify(teamBody(teamId, recordIds ? { record_ids: recordIds } : {})),
  });
}

// discardBatch 废弃批次（物理删除，班管/超管）
export function discardBatch(batchId, teamId) {
  if (isDemo()) return demo.delay({ id: batchId });
  return request(withTeam(`/api/v1/crossing/staging/batches/${batchId}`, teamId), {
    method: 'DELETE',
  });
}

// getConflicts 冲突列表（班管/超管）：[{id,record_id,server_snapshot,intranet_snapshot,created_at,
// 13 业务列当前值}]——pending 冲突，人工二选一裁决（交跨无处理状态行）。
export function getConflicts(teamId) {
  if (isDemo()) return demo.delay(demo.conflicts);
  return request(withTeam('/api/v1/crossing/conflicts', teamId));
}

// resolveConflict 冲突裁决：keep='server' 保留系统值 / keep='intranet' 用内网值覆盖记录。
export function resolveConflict(id, keep, teamId) {
  if (isDemo()) return demo.delay({ id });
  return request(withTeam(`/api/v1/crossing/conflicts/${id}/resolve`, teamId), {
    method: 'POST',
    body: JSON.stringify(teamBody(teamId, { keep })),
  });
}

// getSyncStatus 内网同步状态：{enabled,running,last:{kind,result,created_at},nextNight}。
// enabled=false 表示服务端未配置同步设备/链路，如实展示不假成功。
export function getSyncStatus(teamId) {
  if (isDemo()) return demo.delay({ enabled: false, running: false, last: null, nextNight: null });
  return request(withTeam('/api/v1/crossing/sync/status', teamId));
}

// syncPushNow 手动触发一轮内网同步（异步执行，started=true 仅表示已开始，结果以 /sync/status 与站内通知为准）。
// 未配置（50301）/同步中（42301）/权限不足均抛错由调用方显式提示，不假成功。
export function syncPushNow(teamId) {
  if (isDemo()) return Promise.reject(new Error('演示模式不提供内网同步，请切换 live 模式'));
  return request(withTeam('/api/v1/crossing/sync/push-now', teamId), {
    method: 'POST',
    body: JSON.stringify(teamBody(teamId, {})),
  });
}

// weknoraPushNow 知识库快照同步：{ok,created,updated,deleted,skipped,errors}。
// errors 非空即部分失败（ok=false），调用方须如实提示；未配置（50301）抛错不假成功。
export function weknoraPushNow(teamId) {
  if (isDemo()) return Promise.reject(new Error('演示模式不提供知识库同步，请切换 live 模式'));
  return request(withTeam('/api/v1/crossing/weknora-sync/push-now', teamId), {
    method: 'POST',
    body: JSON.stringify(teamBody(teamId, {})),
  });
}

// exportPreview 导出在线预览地址：{url}（OO 承载页或微软查看器；query 同 exportDownload）。
export function exportPreview(query, teamId) {
  if (isDemo()) return Promise.reject(new Error('演示模式不提供导出预览，请切换 live 模式'));
  return request(withTeam(`/api/v1/crossing/export/preview${query}`, teamId));
}

// listTeams 超管班组切换器数据源（GET /admin/teams，data 兼容直接数组与 list/teams/items 包装，
// status===1 生效班组）。
export async function listTeams() {
  if (isDemo()) return demo.delay(demo.teams);
  const data = await request('/api/v1/admin/teams');
  return normalizeList(data).filter((t) => t.status === 1);
}
