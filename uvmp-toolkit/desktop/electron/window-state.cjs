// 统一“显示主窗口”语义：最小化时先恢复，再显示并聚焦。
// 托盘点击、托盘菜单与第二实例均走这里，避免最小化后只 show 不 restore。
function showMainWindow(win) {
  if (!win || typeof win.isDestroyed !== 'function' || win.isDestroyed()) return false;
  if (typeof win.isMinimized === 'function' && win.isMinimized()) {
    win.restore();
  }
  win.show();
  win.focus();
  return true;
}

module.exports = { showMainWindow };
