// 扫描框窗 preload：向框窗页面暴露最小拖拽/缩放/隐藏通道
const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('scanbox', {
  boxId: new URLSearchParams(location.search).get('box') || 'A',
  // 拖动/缩放只报「开始(含模式)/结束」：位移量由主进程轮询 OS 光标得出
  // （渲染层坐标在悬浮窗上不可靠——已踩坑；详见 quizsearch/index.cjs _startBoxDrag）
  dragStart: (box, mode) => ipcRenderer.send('quizsearch:boxDragStart', box, mode),
  dragEnd: (box) => ipcRenderer.send('quizsearch:boxDragEnd', box),
  hide: (box) => ipcRenderer.send('quizsearch:boxHide', box),
});
