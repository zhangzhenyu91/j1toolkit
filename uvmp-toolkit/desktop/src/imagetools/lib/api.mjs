// 图片窗口唯一的桥接层：只调用 Task 3 的 window.imageTools。
// 预览 URL 带本机真实路径（shade-image://local?path=…），渲染层仍不触碰 fs 与 window.core；
// 路径授权、realpath 与格式复核一律在主进程完成。

function bridge() {
  const api = window.imageTools;
  if (!api) throw new Error('图片工具桥接不可用，请从客户端内打开本窗口');
  return api;
}

export function available() {
  return !!(window.imageTools && typeof window.imageTools.list === 'function');
}

/** 取回当前待开文件：冷启动首帧可能早于页面订阅，挂载后补取一次。 */
export function ready() {
  return window.imageTools.ready();
}

export function pickFiles(filters) {
  return window.imageTools.pickFiles(filters);
}

export function pickDirectory(defaultPath) {
  return window.imageTools.pickDirectory(defaultPath);
}

export function list(path) {
  return window.imageTools.list(path);
}

export function preview(path, maxSize) {
  return window.imageTools.preview(path, maxSize);
}

export function rename(path, name) {
  return window.imageTools.rename(path, name);
}

export function runProcess(payload) {
  return window.imageTools.runProcess(payload);
}

export function cancelJob(jobId) {
  return window.imageTools.cancelJob(jobId);
}

export function getJob(jobId) {
  return window.imageTools.getJob(jobId);
}

export function getConfig() {
  return window.imageTools.getConfig();
}

export function saveQuickPhrases(phrases) {
  return window.imageTools.saveQuickPhrases(phrases);
}

export function showItem(path) {
  return window.imageTools.showItem(path);
}

export function openDefaultSettings() {
  return window.imageTools.openDefaultSettings();
}

export function setDefaultLinux() {
  return window.imageTools.setDefaultLinux();
}

export function getDefaultStatus() {
  return window.imageTools.getDefaultStatus();
}

/** 订阅「打开请求 + 任务事件」，返回取消订阅函数。 */
export function onEvent(callback) {
  if (!window.imageTools) return () => {};
  return window.imageTools.onEvent((payload) => callback(payload || {})) || (() => {});
}

/** 统一取错误文本：IPC 抛出的 Error 通常带 “Error invoking remote method …”。 */
export function errorText(error) {
  if (!error) return '未知错误';
  if (typeof error === 'string') return error;
  return String(error.message || error);
}

/** 去掉 IPC 前缀，只留下核心错误码与中文说明，避免把通道名展示给用户。 */
export function cleanError(error) {
  return errorText(error)
    .replace(/^Error invoking remote method '[^']*':\s*/i, '')
    .replace(/^Error:\s*/i, '')
    .trim();
}

/**
 * 窗口能力桥接对象：共享 TitleBar 需要与主窗口同名的接口
 * （winMinimize/winToggleMax/winClose/winIsMax/onMaxChange/appVersion）。
 * 只有一个入口，渲染层其它组件仍通过本模块调用，不直接摸 window.imageTools。
 */
export const windowApi = {
  winMinimize: () => window.imageTools.winMinimize(),
  winIsMax: () => window.imageTools.winIsMax(),
  winToggleMax: () => window.imageTools.winToggleMax(),
  winClose: () => window.imageTools.winClose(),
  onMaxChange: (cb) => window.imageTools.onMaxChange(cb),
  appVersion: () => window.imageTools.appVersion(),
};
