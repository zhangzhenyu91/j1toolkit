const { contextBridge, ipcRenderer } = require('electron');

// 只暴露大屏页面真正用到的通道：关闭/停止全部都由页面内二次确认后走 closeNow/stopAllNow，
// 因此不再保留 requestClose / requestStopAll（主进程已删除对应 handler）。
contextBridge.exposeInMainWorld('screensaver', {
  ready: () => ipcRenderer.invoke('screensaver:ready'),
  focusInstance: (id) => ipcRenderer.invoke('screensaver:focus', id),
  stopAllNow: () => ipcRenderer.invoke('screensaver:stopAllNow'),
  closeNow: () => ipcRenderer.invoke('screensaver:closeNow'),
  onUpdate: (cb) => {
    const listener = (_e, payload) => cb(payload);
    ipcRenderer.on('screensaver:update', listener);
    return () => ipcRenderer.removeListener('screensaver:update', listener);
  },
});
