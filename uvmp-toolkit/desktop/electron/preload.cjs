// preload：向渲染进程暴露最小安全 API（contextIsolation 下唯一通道）
const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('core', {
  // 客户端版本号（标题栏常驻显示；沙箱 preload 不能 require 包外文件，经 IPC 取 app.getVersion()）
  appVersion: () => ipcRenderer.invoke('app:version'),
  // 调用 Python 核心 RPC 方法
  call: (method, params) => ipcRenderer.invoke('core:call', method, params),
  // 核心健康状态（running/command/lastStderr）与重启
  health: () => ipcRenderer.invoke('core:health'),
  restartCore: () => ipcRenderer.invoke('core:restart'),
  // 订阅核心事件（job.log/job.progress/job.done/core.exit），返回取消订阅函数
  onEvent: (cb) => {
    const listener = (_e, payload) => cb(payload);
    ipcRenderer.on('core:event', listener);
    return () => ipcRenderer.removeListener('core:event', listener);
  },
  // 原生文件/目录对话框
  pickFile: (filters) => ipcRenderer.invoke('dialog:pickFile', filters),
  pickDirectory: (defaultPath) => ipcRenderer.invoke('dialog:pickDirectory', defaultPath),
  // 在文件管理器中定位文件/目录
  showItem: (p) => ipcRenderer.invoke('shell:showItem', p),
  // 自定义标题栏窗口控制
  winMinimize: () => ipcRenderer.invoke('win:minimize'),
  winToggleMax: () => ipcRenderer.invoke('win:toggleMax'),
  winClose: () => ipcRenderer.invoke('win:close'),
  onMaxChange: (cb) => {
    const listener = (_e, maximized) => cb(maximized);
    ipcRenderer.on('win:max', listener);
    return () => ipcRenderer.removeListener('win:max', listener);
  },
  // UI 配置变更（设置页保存 ui.* 后调用，主进程实时生效）
  uiChanged: (ui) => ipcRenderer.send('app:uiConfig', ui),
  // 任务起停计数（+1/-1，托盘「退出」时据以弹确认，防误杀长任务）
  jobRunning: (delta) => ipcRenderer.send('app:jobRunning', delta),
  // 题库搜题（壳层功能：截屏 OCR + 悬浮窗，不经 Python 核心）
  quizSearch: {
    getState: () => ipcRenderer.invoke('quizSearch:getState'),
    start: () => ipcRenderer.invoke('quizSearch:start'),
    stop: () => ipcRenderer.invoke('quizSearch:stop'),
    selftest: () => ipcRenderer.invoke('quizSearch:selftest'),
    setBoxVisible: (box, visible) => ipcRenderer.invoke('quizSearch:setBoxVisible', box, visible),
    resetBoxes: () => ipcRenderer.invoke('quizSearch:resetBoxes'),
    setOptions: (opts) => ipcRenderer.invoke('quizSearch:setOptions', opts),
    bankImport: (payload) => ipcRenderer.invoke('quizSearch:bankImport', payload),
    bankRemove: (id) => ipcRenderer.invoke('quizSearch:bankRemove', id),
    bankRename: (id, name) => ipcRenderer.invoke('quizSearch:bankRename', id, name),
    bankSetEnabled: (id, enabled) => ipcRenderer.invoke('quizSearch:bankSetEnabled', id, enabled),
    testMatch: (text) => ipcRenderer.invoke('quizSearch:testMatch', text),
    readFile: (p) => ipcRenderer.invoke('quizSearch:readFile', p),
    saveFile: (payload) => ipcRenderer.invoke('quizSearch:saveFile', payload),
    debugCapture: () => ipcRenderer.invoke('quizSearch:debugCapture'),
    onEvent: (cb) => {
      const listener = (_e, payload) => cb(payload);
      ipcRenderer.on('quizsearch:event', listener);
      return () => ipcRenderer.removeListener('quizsearch:event', listener);
    },
  },
});
