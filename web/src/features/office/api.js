// ONLYOFFICE 承载 feature API：组件只调用本文件导出的方法。
//
// 两种适配器（与 src/api.js 同口径，模式由 getApiMode() 统一读取）：
// - demo：不下发编辑器配置、不加载 OO 脚本（明确报错，不伪造成功）；
// - live：请求 Go HTTP API /api/v1/office（go/internal/apps/office，信封 {code,message,data}）。
//
// 链路同旧 server/public/office.html：POST /config 取签发配置（含 OO JWT）→ 动态加载
// OO_PUBLIC_URL 的 web-apps/.../api.js → DocsAPI.DocEditor 渲染；回源/回调为免登录票据路由。

import { getApiMode } from '../../api.js';
import { createClient } from '../core/request.js';

// isDemo 当前是否演示适配层。
export function isDemo() {
  return getApiMode() !== 'live';
}

// request 共享请求层：注入 Bearer、解析平台信封并统一 401。
const client = createClient();
const request = client.request;

const live = {
  // GET /api/v1/office/status → {enabled}
  getStatus: () => request('/api/v1/office/status'),

  // POST /api/v1/office/config body {src?,space,path,name,key?,ref?,team_id?} → {config, publicUrl}
  // （Go 端字段为 publicUrl；旧 Node 为 public_url，此处两者兼容读取）
  getConfig: (body) =>
    request('/api/v1/office/config', { method: 'POST', body: JSON.stringify(body) }),
};

const demoAdapter = {
  getStatus: () => Promise.resolve({ enabled: false }),
  getConfig: () =>
    Promise.reject(
      new Error(
        '演示模式不下发 ONLYOFFICE 编辑器配置，请切换 live 适配层（localStorage.setItem("shade.apiMode","live")）'
      )
    ),
};

function adapter() {
  return getApiMode() === 'live' ? live : demoAdapter;
}

// ---- 对组件暴露的统一 API ----

// getStatus ONLYOFFICE 启用状态（OO_PUBLIC_URL 已配置即启用）。
export function getStatus() {
  return adapter().getStatus();
}

// getConfig 签发编辑器配置。body 透传 query 形态：{src,space,path,name,key,ref,team_id,mode}。
export function getConfig(body) {
  return adapter().getConfig(body);
}

// editorBaseUrl 编辑器配置里的 publicUrl 兼容读取（Go publicUrl / 旧 Node public_url）。
export function editorBaseUrl(data) {
  return (data && (data.publicUrl || data.public_url)) || '';
}

// loadDocsScript 动态加载 ONLYOFFICE api.js（publicUrl 下的 web-apps/apps/api/documents/api.js）。
// 全局只注入一次；失败（服务不可达 / 被拦截）时抛错。
export function loadDocsScript(publicUrl) {
  if (window.DocsAPI && window.DocsAPI.DocEditor) return Promise.resolve();
  return new Promise((resolve, reject) => {
    const s = document.createElement('script');
    s.src = publicUrl.replace(/\/+$/, '') + '/web-apps/apps/api/documents/api.js';
    s.onload = () => {
      if (window.DocsAPI && window.DocsAPI.DocEditor) {
        resolve();
      } else {
        reject(new Error('编辑器组件加载异常，请刷新重试'));
      }
    };
    s.onerror = () =>
      reject(
        new Error(
          `编辑器资源加载失败：无法访问 ONLYOFFICE 服务（${publicUrl}），请检查网络与反代配置`
        )
      );
    document.head.appendChild(s);
  });
}
