const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('screensaver', {
  ready: () => ipcRenderer.invoke('screensaver:ready'),
  requestClose: () => ipcRenderer.invoke('screensaver:requestClose'),
  focusInstance: (id) => ipcRenderer.invoke('screensaver:focus', id),
  requestStopAll: () => ipcRenderer.invoke('screensaver:stopAll'),
  onUpdate: (cb) => {
    const listener = (_e, payload) => cb(payload);
    ipcRenderer.on('screensaver:update', listener);
    return () => ipcRenderer.removeListener('screensaver:update', listener);
  },
});
