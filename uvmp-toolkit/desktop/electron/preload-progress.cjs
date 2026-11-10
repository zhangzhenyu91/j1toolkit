// 机巡进度浮窗 preload：只读核心状态 + 拖动/缩放/关闭通道。
const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('core', {
  call: (method, params) => ipcRenderer.invoke('core:call', method, params),
});

contextBridge.exposeInMainWorld('progressPanel', {
  dragStart: (mode) => ipcRenderer.send('defectProgress:dragStart', mode || 'move'),
  dragEnd: () => ipcRenderer.send('defectProgress:dragEnd'),
  close: () => ipcRenderer.send('defectProgress:closeWindow'),
});
