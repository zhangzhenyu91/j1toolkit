// 结果窗 preload：接收匹配结果推送 + 拖动/缩放起止通道（位移由主进程轮询 OS 光标得出）
const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('resultPanel', {
  onState: (cb) => {
    const listener = (_e, payload) => cb(payload);
    ipcRenderer.on('quizsearch:state', listener);
    return () => ipcRenderer.removeListener('quizsearch:state', listener);
  },
  dragStart: (mode) => ipcRenderer.send('quizsearch:resultDragStart', mode || 'move'),
  dragEnd: () => ipcRenderer.send('quizsearch:resultDragEnd'),
});
