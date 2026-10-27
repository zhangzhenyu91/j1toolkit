// KVM 远程管理 API 层：组件只调用本文件导出方法，不直接 fetch。
// live 模式请求 Go /api/v1/kvm/*（设备列表/带态跳转/文件互推）与 /api/v1/netdisk/list（互推选文件用）；
// demo 模式返回 demo.js 静态演示数据（DEMO ONLY），不发起任何网络请求。
import { getApiMode } from '../../api.js';
import * as demo from './demo.js';

const TOKEN_KEY = 'shade.token';

function isDemo() {
  return getApiMode() === 'demo';
}

function token() {
  try {
    return sessionStorage.getItem(TOKEN_KEY) || '';
  } catch (e) {
    return '';
  }
}

// request 信封请求：JSON 体默认 Content-Type，FormData 交由浏览器带 boundary；
// code!==0 抛错（err.code 为数字业务码），成功默认返回 body.data；
// options.withMessage=true 时返回 {...data, message}（data 为空对象时也能拿到汇总文案）。
async function request(path, options = {}) {
  const headers = { ...(options.headers || {}) };
  const isForm = typeof FormData !== 'undefined' && options.body instanceof FormData;
  if (!isForm && !headers['Content-Type']) headers['Content-Type'] = 'application/json';
  const t = token();
  if (t) headers.Authorization = `Bearer ${t}`;
  const res = await fetch(path, { ...options, headers });
  const body = await res.json().catch(() => ({
    code: 'http_error',
    message: `服务返回异常（HTTP ${res.status}）`,
  }));
  if (res.status === 401) {
    try {
      sessionStorage.removeItem('shade.user');
      sessionStorage.removeItem(TOKEN_KEY);
    } catch (e) {
      // 忽略
    }
  }
  if (body.code !== 0) {
    const err = new Error(body.message || '请求失败');
    err.code = body.code;
    throw err;
  }
  if (options.withMessage) {
    return { ...(body.data || {}), message: body.message || '' };
  }
  return body.data;
}

// withTeam 超管指定班组时为 path 追加 team_id。
export function withTeam(path, teamId) {
  if (!teamId) return path;
  return path + (path.includes('?') ? '&' : '?') + 'team_id=' + teamId;
}

// ---- 设备 ----

// getDevices 设备列表：{items,team,fallback,fallback_team}（items 为平台原始字段 + id/ddns/deviceGroupName/maintaining）
export function getDevices(teamId, q) {
  if (isDemo()) return demo.delay(demo.devices(q));
  let path = '/api/v1/kvm/devices';
  const qs = [];
  if (q) qs.push('q=' + encodeURIComponent(q));
  if (qs.length) path += '?' + qs.join('&');
  return request(withTeam(path, teamId));
}

// jump 带态跳转（to=term|desk）：返回 {url}（平台 jump.html，sid 走 hash）
export function jump(to, ddns, teamId) {
  if (isDemo()) return demo.delay({ url: 'https://example.invalid/jump.html#sid=demo&to=/' });
  return request(withTeam('/api/v1/kvm/jump', teamId), {
    method: 'POST',
    body: JSON.stringify(teamId ? { to, ddns, team_id: teamId } : { to, ddns }),
  });
}

// ---- 设备文件互推（文件转发点 fileshare） ----

// fileStatus 挂载状态：{shared:bool, files?:[{name,size}]}
export function fileStatus(id, teamId) {
  if (isDemo()) return demo.delay(demo.fileStatus);
  return request(withTeam(`/api/v1/kvm/devices/${id}/status`, teamId));
}

// fileList 盘内文件列表：{files:[{name,size}]}（共享中会先断开）
export function fileList(id, teamId) {
  if (isDemo()) return demo.delay(demo.fileList);
  return request(withTeam(`/api/v1/kvm/devices/${id}/files`, teamId));
}

// pushFiles 推送文件到盘（multipart files 字段；单文件真实文件名走表单字段 filename）。
// 逐文件串行上传，返回 {ok, fail, firstErr}。
export async function pushFiles(id, files, teamId) {
  if (isDemo()) {
    await demo.delay({ pushed: files.length });
    return { ok: files.length, fail: 0, firstErr: '' };
  }
  let ok = 0;
  let fail = 0;
  let firstErr = '';
  for (const f of files) {
    const fd = new FormData();
    fd.append('files', f, f.name);
    if (files.length === 1) fd.append('filename', f.name);
    try {
      await request(withTeam(`/api/v1/kvm/devices/${id}/push`, teamId), {
        method: 'POST',
        body: fd,
      });
      ok++;
    } catch (e) {
      fail++;
      if (!firstErr) firstErr = e.message || String(e);
    }
  }
  return { ok, fail, firstErr };
}

// downloadFile 下载盘内文件（二进制 Response 由调用方落盘）
export async function downloadFile(id, name, teamId) {
  if (isDemo()) throw new Error('演示模式不提供下载，请切换 live 模式');
  const headers = {};
  const t = token();
  if (t) headers.Authorization = `Bearer ${t}`;
  const res = await fetch(
    withTeam(`/api/v1/kvm/devices/${id}/download?name=${encodeURIComponent(name)}`, teamId),
    { headers }
  );
  if (!res.ok) {
    let msg = `下载失败（${res.status}）`;
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

// mount 挂载（共享）到被控机
export function mount(id, teamId) {
  if (isDemo()) return demo.delay({ mounted: true });
  return request(withTeam(`/api/v1/kvm/devices/${id}/mount`, teamId), { method: 'POST' });
}

// deleteFiles 删除盘内文件（names 文件名数组）
export function deleteFiles(id, names, teamId) {
  if (isDemo()) return demo.delay({ deleted: names.length });
  return request(withTeam(`/api/v1/kvm/devices/${id}/delete`, teamId), {
    method: 'POST',
    body: JSON.stringify(teamId ? { names, team_id: teamId } : { names }),
  });
}

// pullFromNetdisk 网盘 → 设备：{space,paths:[...]}（1-20 个）；返回 {items:[{name,ok,error}], message}，
// message 为服务端汇总文案（部分失败 200 带汇总，全部失败 502 抛错）。
export function pullFromNetdisk(id, space, paths, teamId) {
  if (isDemo())
    return demo.delay({
      items: paths.map((p) => ({ name: p.split('/').pop(), ok: true })),
      message: '演示模式不推送',
    });
  return request(withTeam(`/api/v1/kvm/devices/${id}/pull-from-netdisk`, teamId), {
    method: 'POST',
    body: JSON.stringify(teamId ? { space, paths, team_id: teamId } : { space, paths }),
    withMessage: true,
  });
}

// saveToNetdisk 设备 → 网盘：{name,space,dir}；返回 {path}
export function saveToNetdisk(id, name, space, dir, teamId) {
  if (isDemo()) return demo.delay({ path: `/${dir}/${name}` });
  const body = { name, space, dir };
  if (teamId) body.team_id = teamId;
  return request(withTeam(`/api/v1/kvm/devices/${id}/save-to-netdisk`, teamId), {
    method: 'POST',
    body: JSON.stringify(body),
  });
}

// ---- 网盘浏览（互推选文件用，netdisk 应用权限由服务端校验） ----

// netdiskList 目录列表：{space,path,manageable,items:[{name,is_dir,size,modified}]}
export function netdiskList(space, path) {
  if (isDemo()) return demo.delay(demo.netdiskList(path));
  return request('/api/v1/netdisk/list', {
    method: 'POST',
    body: JSON.stringify({ space, path }),
  });
}

// listTeams 超管班组切换器数据源（GET /admin/teams，status===1 生效班组）。
export async function listTeams() {
  if (isDemo()) return demo.delay(demo.teams);
  const data = await request('/api/v1/admin/teams');
  return (data.list || []).filter((t) => t.status === 1);
}
