const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('path');
const fs = require('fs');

const { showMainWindow } = require('../electron/window-state.cjs');

function fakeWindow({ minimized = false, destroyed = false } = {}) {
  const calls = [];
  return {
    calls,
    isDestroyed: () => destroyed,
    isMinimized: () => minimized,
    restore: () => calls.push('restore'),
    show: () => calls.push('show'),
    focus: () => calls.push('focus'),
  };
}

test('最小化后显示主窗口：先恢复、再显示、最后聚焦', () => {
  const win = fakeWindow({ minimized: true });
  assert.equal(showMainWindow(win), true);
  assert.deepEqual(win.calls, ['restore', 'show', 'focus']);
});

test('隐藏但未最小化：不调用 restore，仍显示并聚焦', () => {
  const win = fakeWindow({ minimized: false });
  assert.equal(showMainWindow(win), true);
  assert.deepEqual(win.calls, ['show', 'focus']);
});

test('窗口已销毁时安全跳过', () => {
  const win = fakeWindow({ destroyed: true });
  assert.equal(showMainWindow(win), false);
  assert.deepEqual(win.calls, []);
  assert.equal(showMainWindow(null), false);
});

test('electron/icon.png 存在且安装包解包托盘图标', () => {
  const iconPath = path.join(__dirname, '..', 'electron', 'icon.png');
  assert.equal(fs.existsSync(iconPath), true);
  assert.ok(fs.statSync(iconPath).size > 0);

  const builder = fs.readFileSync(path.join(__dirname, '..', 'electron-builder.yml'), 'utf8');
  assert.match(builder, /electron\/\*\*/);
  assert.match(builder, /asarUnpack:[\s\S]*electron\/icon\.png/);
});
