// 图片窗口 preload：只暴露 window.imageTools（沙箱内不可 require 业务模块）。
const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('imageTools', {
  // 在已授权的图片之间切换查看（新路径授权只走主进程与对话框）
  open: (payload) => ipcRenderer.invoke('imagetools:open', payload),
  // 取回当前待开文件（冷启动首帧可能早于渲染层订阅）
  ready: () => ipcRenderer.invoke('imagetools:ready'),
  pickFiles: (filters) => ipcRenderer.invoke('imagetools:pickFiles', filters),
  pickDirectory: (defaultPath) => ipcRenderer.invoke('imagetools:pickDirectory', defaultPath),
  // 同目录列表 / 单项信息
  list: (p) => ipcRenderer.invoke('imagetools:list', p),
  inspect: (p) => ipcRenderer.invoke('imagetools:inspect', p),
  // 预览：返回 shade-image://local?path=<编码后真实路径>，每次读取仍由主进程复核授权
  preview: (p, maxSize) => ipcRenderer.invoke('imagetools:preview', p, maxSize),
  rename: (p, name) => ipcRenderer.invoke('imagetools:rename', p, name),
  // 批处理任务
  runProcess: (payload) => ipcRenderer.invoke('imagetools:runProcess', payload),
  cancelJob: (jobId) => ipcRenderer.invoke('imagetools:cancelJob', jobId),
  getJob: (jobId) => ipcRenderer.invoke('imagetools:getJob', jobId),
  // 设置
  getConfig: () => ipcRenderer.invoke('imagetools:getConfig'),
  saveQuickPhrases: (phrases) => ipcRenderer.invoke('imagetools:saveQuickPhrases', phrases),
  // 文件定位与系统默认应用
  showItem: (p) => ipcRenderer.invoke('imagetools:showItem', p),
  openDefaultSettings: () => ipcRenderer.invoke('imagetools:openDefaultSettings'),
  setDefaultLinux: () => ipcRenderer.invoke('imagetools:setDefaultLinux'),
  getDefaultStatus: () => ipcRenderer.invoke('imagetools:getDefaultStatus'),
  // 订阅：打开请求与任务事件，返回取消订阅函数
  onEvent: (cb) => {
    const onOpen = (_e, payload) => cb(Object.assign({ event: 'open' }, payload));
    const onJob = (_e, payload) => cb(payload);
    ipcRenderer.on('imagetools:open', onOpen);
    ipcRenderer.on('imagetools:event', onJob);
    return () => {
      ipcRenderer.removeListener('imagetools:open', onOpen);
      ipcRenderer.removeListener('imagetools:event', onJob);
    };
  },
  // 自定义标题栏窗口控制（与主窗口 window.core 同名同义，48px 共享 TitleBar 直接复用）
  winMinimize: () => ipcRenderer.invoke('imagetools:winMinimize'),
  winIsMax: () => ipcRenderer.invoke('imagetools:winIsMax'),
  winToggleMax: () => ipcRenderer.invoke('imagetools:winToggleMax'),
  winClose: () => ipcRenderer.invoke('imagetools:winClose'),
  onMaxChange: (cb) => {
    const listener = (_e, maximized) => cb(maximized);
    ipcRenderer.on('imagetools:max', listener);
    return () => ipcRenderer.removeListener('imagetools:max', listener);
  },
  appVersion: () => ipcRenderer.invoke('imagetools:appVersion'),
});
