// 原生窗口外框读取回归：命令参数、物理像素→DIP 取整、负坐标与高 DPI、内缩量缓存，以及全部失败回退。
// 外部命令用注入的 execFileSync 断言，不在测试里真跑 PowerShell / xwininfo。
const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('path');

const {
  queryNativeWindowBounds,
  physicalToDip,
  clearNativeBoundsCache,
} = require('../electron/window-native-bounds.cjs');

const WINDOWS_HANDLE = Buffer.alloc(8);
WINDOWS_HANDLE.writeBigUInt64LE(0x00000000000a1b2cn);

// 内容区 1160x780 位于原生外框 (80,60)-(1258,878) 之内：
// 左 8 / 上 8 / 右 10 / 下 30 DIP 的内缩，正对应 Windows thickFrame 与 Linux CSD 阴影。
const CONTENT = { x: 88, y: 68, width: 1160, height: 780 };
const OUTER = { x: 80, y: 60, width: 1178, height: 818 };
const OUTER_PHYSICAL = '80 60 1258 878';

function fakeWindow({
  handle = WINDOWS_HANDLE,
  destroyed = false,
  content = CONTENT,
  contentBounds = true,
  omitHandle = false,
} = {}) {
  let rect = { ...content };
  const win = {
    isDestroyed: () => destroyed,
    getNativeWindowHandle: () => (omitHandle ? null : handle),
    getContentSize: () => [rect.width, rect.height],
    getBounds: () => ({ ...rect }),
    setContent: (next) => {
      rect = { x: rect.x, y: rect.y, ...next };
    },
  };
  if (contentBounds) win.getContentBounds = () => ({ ...rect });
  return win;
}

// 绝大多数用例不关心缓存：默认关掉，缓存行为单独几条用例验证。
function query(win, options = {}) {
  return queryNativeWindowBounds(win, { cacheTtl: 0, ...options });
}

function fakeScreen(scaleFactor = 1) {
  return {
    getDisplayMatching: () => ({ id: 1, scaleFactor }),
    getPrimaryDisplay: () => ({ id: 1, scaleFactor }),
  };
}

// 记录调用并返回预设输出（或抛错）
function fakeExec(output, { error = null } = {}) {
  const calls = [];
  const exec = (file, args, options) => {
    calls.push({ file, args, options });
    if (error) throw error;
    return output;
  };
  exec.calls = calls;
  return exec;
}

test('Windows：走系统 PowerShell 绝对路径 + NoProfile/NonInteractive/windowsHide，解析 GetWindowRect', () => {
  const exec = fakeExec(OUTER_PHYSICAL);
  const result = query(fakeWindow(), {
    platform: 'win32',
    execFileSync: exec,
    screen: fakeScreen(1),
    timeout: 2000,
  });
  assert.deepEqual(result, OUTER);

  const call = exec.calls[0];
  assert.ok(path.win32.isAbsolute(call.file), `PowerShell 必须用绝对路径：${call.file}`);
  assert.match(call.file, /System32\\WindowsPowerShell\\v1\.0\\powershell\.exe$/);
  for (const flag of ['-NoLogo', '-NoProfile', '-NonInteractive']) {
    assert.ok(call.args.includes(flag), `缺少 ${flag}`);
  }
  // 脚本整体作为一个 -Command 参数传入，不经过 shell 拼接
  assert.equal(call.args[call.args.length - 2], '-Command');
  const script = call.args[call.args.length - 1];
  assert.match(script, /GetWindowRect/);
  assert.match(script, /SetThreadDpiAwarenessContext/, '必须先切到 per-monitor DPI aware');
  assert.match(
    script,
    new RegExp(`\\[System\\.Int64\\]${WINDOWS_HANDLE.readBigUInt64LE(0)}`),
    'HWND 应以十进制传入'
  );
  assert.equal(call.options.timeout, 2000);
  assert.equal(call.options.windowsHide, true);
  assert.equal(call.options.shell, undefined, '禁止 shell:true');
  assert.ok(call.options.maxBuffer > 0);
});

test('Windows：句柄高位为负的旧式 HWND 按有符号 32 位解释', () => {
  const handle = Buffer.alloc(8);
  handle.writeBigInt64LE(-0x00000000fffffff0n);
  const exec = fakeExec(OUTER_PHYSICAL);
  const result = query(fakeWindow({ handle }), {
    platform: 'win32',
    execFileSync: exec,
    screen: fakeScreen(1),
  });
  assert.deepEqual(result, OUTER);
  const script = exec.calls[0].args[exec.calls[0].args.length - 1];
  assert.match(script, /\[System\.Int64\]-4294967280/, '负句柄不得被当成无符号大数');
});

test('Windows：高 DPI 物理像素换算 DIP，原点向下取整、宽高向上取整，最右/最底像素不裁', () => {
  // 物理外框 1460x998 @1.25 → 1168 x 798.4 DIP：高度必须向上取到 799，
  // 否则最底部物理像素落在区域之外被裁掉。
  const content = { x: 100, y: 40, width: 1160, height: 780 };
  const result = query(fakeWindow({ content }), {
    platform: 'win32',
    execFileSync: fakeExec('115 40 1575 1038'),
    screen: fakeScreen(1.25),
  });
  assert.deepEqual(result, { x: 92, y: 32, width: 1168, height: 799 });
  // 覆盖校验：DIP 外框右缘/下缘不早于物理外框换算后的右缘/下缘
  assert.ok(result.x + result.width >= 1575 / 1.25 - 1e-9);
  assert.ok(result.y + result.height >= 1038 / 1.25 - 1e-9);
});

test('Windows：负坐标（副屏在左侧/上方）保留符号并正确取整', () => {
  const content = { x: -1530, y: -860, width: 940, height: 650 };
  const result = query(fakeWindow({ content }), {
    platform: 'win32',
    execFileSync: fakeExec('-1921 -1081 -743 -263'),
    screen: fakeScreen(1.25),
  });
  assert.deepEqual(result, { x: -1537, y: -865, width: 943, height: 655 });
});

test('Linux：把 XID 格式化成十六进制传给 xwininfo -id -stats，并用同一套 DIP 换算', () => {
  const xid = Buffer.alloc(8);
  xid.writeBigUInt64LE(0x2a00007n);
  const exec = fakeExec(
    [
      'xwininfo: Window id: 0x2a00007 "Shade 壹匣"',
      '',
      '  Absolute upper-left X:  -160',
      '  Absolute upper-left Y:  0',
      '  Width:  1178',
      '  Height: 818',
    ].join('\n')
  );
  const content = { x: -160, y: 0, width: 1178, height: 818 };
  const result = query(fakeWindow({ handle: xid, content }), {
    platform: 'linux',
    execFileSync: exec,
    screen: fakeScreen(1),
    xwininfoPath: '/usr/bin/xwininfo',
  });
  assert.deepEqual(result, { x: -160, y: 0, width: 1178, height: 818 });
  assert.equal(exec.calls[0].file, '/usr/bin/xwininfo');
  assert.deepEqual(exec.calls[0].args, ['-id', '0x2a00007', '-stats']);
  assert.equal(exec.calls[0].options.windowsHide, true);
  assert.equal(exec.calls[0].options.shell, undefined, '禁止 shell:true');
});

test('Linux CSD：外框比内容区大时按内缩量还原真实外框', () => {
  const exec = fakeExec(
    [
      '  Absolute upper-left X:  72',
      '  Absolute upper-left Y:  54',
      '  Width:  1194',
      '  Height: 842',
    ].join('\n')
  );
  const result = query(fakeWindow(), {
    platform: 'linux',
    execFileSync: exec,
    screen: fakeScreen(1),
    xwininfoPath: '/usr/bin/xwininfo',
  });
  assert.deepEqual(result, { x: 72, y: 54, width: 1194, height: 842 });
});

test('失败一律返回 null：平台不支持 / 窗口已销毁 / 缺显示器缩放 / 缺工具 / 超时 / 输出非法', () => {
  const okExec = fakeExec(OUTER_PHYSICAL);

  assert.equal(query(fakeWindow(), { platform: 'darwin', execFileSync: okExec }), null);
  assert.equal(
    query(fakeWindow({ destroyed: true }), {
      platform: 'win32',
      execFileSync: okExec,
      screen: fakeScreen(1),
    }),
    null
  );
  // screen 不可用：单测环境 require('electron') 拿不到 screen，按缺缩放处理
  assert.equal(
    query(fakeWindow(), { platform: 'win32', execFileSync: okExec, screen: null }),
    null
  );
  // 命令抛错（缺工具 / 超时 / 权限）不得冒泡
  assert.equal(
    query(fakeWindow(), {
      platform: 'win32',
      execFileSync: fakeExec('', {
        error: Object.assign(new Error('spawn ENOENT'), { code: 'ENOENT' }),
      }),
      screen: fakeScreen(1),
    }),
    null
  );
  // 输出不是四个整数 / 宽高非正
  for (const output of ['', 'GetWindowRect failed', '0 0 0 0', '0 0 -100 818', 'a b c d']) {
    assert.equal(
      query(fakeWindow(), {
        platform: 'win32',
        execFileSync: fakeExec(output),
        screen: fakeScreen(1),
      }),
      null,
      `输出 ${JSON.stringify(output)} 应视为失败`
    );
  }
  // Linux 缺 xwininfo：命令抛错 → null
  assert.equal(
    query(fakeWindow(), {
      platform: 'linux',
      execFileSync: fakeExec('', { error: new Error('spawn ENOENT') }),
      screen: fakeScreen(1),
      xwininfoPath: '/nonexistent/xwininfo',
    }),
    null
  );
  // 句柄不是 Buffer / 为空
  assert.equal(
    query(fakeWindow({ omitHandle: true }), {
      platform: 'win32',
      execFileSync: okExec,
      screen: fakeScreen(1),
    }),
    null
  );
});

test('命令执行期间内容区变了（窗口在动画）：内缩量不可信，返回 null 由调用方回退', () => {
  const win = fakeWindow();
  const exec = () => {
    win.setContent({ width: 1300, height: 900 });
    return OUTER_PHYSICAL;
  };
  assert.equal(query(win, { platform: 'win32', execFileSync: exec, screen: fakeScreen(1) }), null);
});

test('内缩量离谱（读到的不是本窗口）时判失败，不用坏外框裁窗口', () => {
  // 外框 200x200 远小于内容区 1160x780
  const result = query(fakeWindow(), {
    platform: 'win32',
    execFileSync: fakeExec('0 0 200 200'),
    screen: fakeScreen(1),
  });
  assert.equal(result, null);
});

test('命令超时被夹到上限，防止查询卡住主进程', () => {
  const exec = fakeExec(OUTER_PHYSICAL);
  query(fakeWindow(), {
    platform: 'win32',
    execFileSync: exec,
    screen: fakeScreen(1),
    timeout: 999999,
  });
  assert.equal(exec.calls[0].options.timeout, 10000);
});

test('缓存：内缩量按句柄 + DPI 缓存，缩放窗口不再启动子进程；过期或换 DPI 才重新查询', () => {
  clearNativeBoundsCache();
  let now = 1000;
  let scaleFactor = 1;
  const screen = {
    getDisplayMatching: () => ({ id: 1, scaleFactor }),
    getPrimaryDisplay: () => ({ id: 1, scaleFactor }),
  };
  const exec = fakeExec(OUTER_PHYSICAL);
  const win = fakeWindow();
  const options = { platform: 'win32', execFileSync: exec, screen, now: () => now };

  assert.deepEqual(queryNativeWindowBounds(win, options), OUTER);
  assert.equal(exec.calls.length, 1);

  // 拖拽缩放：内容区变了，但内缩量没变 → 不跑命令，直接按新内容区推外框
  win.setContent({ width: 1280, height: 900 });
  assert.deepEqual(queryNativeWindowBounds(win, options), {
    x: 80,
    y: 60,
    width: 1298,
    height: 938,
  });
  assert.equal(exec.calls.length, 1, '缩放不得重新启动 PowerShell');

  // TTL 过期后重新查询（系统主题/边框设置可能变）
  now += 60001;
  queryNativeWindowBounds(win, options);
  assert.equal(exec.calls.length, 2);

  // 换屏改 DPI：DIP 换算基准变了，必须重新查询
  scaleFactor = 1.25;
  assert.equal(queryNativeWindowBounds(win, options), null, '旧内缩量在新 DPI 下不成立时判失败');
  assert.equal(exec.calls.length, 3);
  clearNativeBoundsCache();
});

test('缓存：cacheTtl=0 或查询失败时不写缓存，恢复后下一次即可拿到外框', () => {
  clearNativeBoundsCache();
  const win = fakeWindow();
  const exec = fakeExec(OUTER_PHYSICAL);
  const options = { platform: 'win32', execFileSync: exec, screen: fakeScreen(1) };

  queryNativeWindowBounds(win, { ...options, cacheTtl: 0 });
  queryNativeWindowBounds(win, { ...options, cacheTtl: 0 });
  assert.equal(exec.calls.length, 2, 'cacheTtl=0 每次都要重新查询');

  clearNativeBoundsCache();
  let fail = true;
  const flaky = () => {
    if (fail) throw Object.assign(new Error('超时'), { code: 'ETIMEDOUT' });
    return OUTER_PHYSICAL;
  };
  assert.equal(queryNativeWindowBounds(win, { ...options, execFileSync: flaky }), null);
  fail = false;
  assert.deepEqual(
    queryNativeWindowBounds(win, { ...options, execFileSync: flaky }),
    OUTER,
    '失败不得写缓存，恢复后应立即成功'
  );
  clearNativeBoundsCache();
});

test('physicalToDip：取整不裁最右/最底像素，并拒绝非法缩放', () => {
  assert.deepEqual(physicalToDip({ left: 0, top: 0, width: 100, height: 50 }, 1), {
    x: 0,
    y: 0,
    width: 100,
    height: 50,
  });
  assert.deepEqual(physicalToDip({ left: 0, top: 0, width: 101, height: 51 }, 1.25), {
    x: 0,
    y: 0,
    width: 81,
    height: 41,
  });
  assert.deepEqual(physicalToDip({ left: -3, top: -3, width: 101, height: 51 }, 1.5), {
    x: -2,
    y: -2,
    width: 68,
    height: 34,
  });
  assert.equal(physicalToDip({ left: 0, top: 0, width: 100, height: 50 }, 0), null);
  assert.equal(physicalToDip({ left: 0, top: 0, width: 100, height: 50 }, Number.NaN), null);
});
