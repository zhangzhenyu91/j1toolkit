const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('returnbar', {
  returnToScreensaver: () => ipcRenderer.invoke('screensaver:return'),
  onUpdate: (cb) => {
    const listener = (_e, payload) => cb(payload);
    ipcRenderer.on('returnbar:update', listener);
    return () => ipcRenderer.removeListener('returnbar:update', listener);
  },
});
