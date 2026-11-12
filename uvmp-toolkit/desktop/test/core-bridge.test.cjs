// CoreBridge 回归测试：退出/信号判定、RPC 超时、管道失败清理、stop/start 代际隔离、方法白名单。
const test = require('node:test');
const assert = require('node:assert/strict');
const { EventEmitter } = require('node:events');
const { PassThrough } = require('node:stream');

const { CoreBridge, RPC_METHODS } = require('../electron/core.cjs');

// 假子进程：可控 stdin/stdout/stderr，可模拟 exit/signal/stdin 报错
function fakeChild() {
  const child = new EventEmitter();
  child.stdin = new PassThrough();
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  child.exitCode = null;
  child.signalCode = null;
  child.writes = [];
  child.stdin.write = (chunk, cb) => {
    child.writes.push(String(chunk));
    if (cb) cb();
    return true;
  };
  child.kill = (sig) => {
    child.signalCode = sig || 'SIGTERM';
    child.emit('exit', null, child.signalCode);
    return true;
  };
  // 断言辅助：往 stdout 写一行响应
  child.reply = (obj) => child.stdout.write(JSON.stringify(obj) + '\n');
  child.lastRequest = () => JSON.parse(child.writes[child.writes.length - 1] || '{}');
  return child;
}

function makeBridge(options = {}) {
  const children = [];
  const bridge = new CoreBridge(
    Object.assign(
      {
        command: 'fake-core',
        spawn: () => {
          const child = fakeChild();
          children.push(child);
          return child;
        },
      },
      options
    )
  );
  return { bridge, children };
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

test('调用成功：响应按 id 回填 result 并清理 pending', async () => {
  const { bridge, children } = makeBridge();
  bridge.start();
  const pending = bridge.call('getApps', { a: 1 });
  const child = children[0];
  assert.equal(child.writes.length, 1);
  child.reply({ id: child.lastRequest().id, ok: true, result: { apps: ['x'] } });
  assert.deepEqual(await pending, { apps: ['x'] });
  assert.equal(bridge.pending.size, 0);
  bridge.stop();
});

test('中文参数仍按 \\uXXXX 转义写入（与核心 ensure_ascii 对称）', async () => {
  const { bridge, children } = makeBridge();
  bridge.start();
  const p = bridge.call('saveConfig', { sso: { username: '张三' } });
  const line = children[0].writes[0];
  assert.ok(!/[\u4e00-\u9fff]/.test(line));
  assert.ok(line.includes('\\u5f20\\u4e09'));
  children[0].reply({ id: children[0].lastRequest().id, ok: true, result: null });
  await p;
  bridge.stop();
});

test('health.running 同时看 exitCode 与 signalCode', () => {
  const { bridge, children } = makeBridge();
  bridge.start();
  assert.equal(bridge.health.running, true);
  // 被信号杀死时 exitCode 仍为 null——只看 exitCode 会误判为运行中
  children[0].signalCode = 'SIGTERM';
  assert.equal(bridge.health.running, false);

  const second = makeBridge();
  second.bridge.start();
  second.children[0].exitCode = 1;
  assert.equal(second.bridge.health.running, false);
});

test('RPC 超时：按配置超时 reject 并清空 pending', async () => {
  const { bridge } = makeBridge({ rpcTimeoutMs: 60 });
  bridge.start();
  const started = Date.now();
  const pending = bridge.call('getApps');
  // 定时器必须处于 ref 状态：假子进程不持有 libuv 句柄，一旦 unref，
  // 事件循环会先于定时器结束，超时保护形同虚设（Node 22 的 node --test 曾整文件被判 cancelled）。
  assert.equal(
    bridge.pending.values().next().value.timer.hasRef(),
    true,
    '超时定时器须钉住事件循环'
  );
  await assert.rejects(pending, /超时/);
  assert.ok(Date.now() - started < 2000);
  assert.equal(bridge.pending.size, 0);
  bridge.stop();
});

test('长耗时方法使用单独的超时预算', async () => {
  const { bridge } = makeBridge({
    rpcTimeoutMs: 40,
    rpcTimeouts: { slowMethod: 5000 },
    allowedMethods: ['slowMethod'],
  });
  bridge.start();
  const p = bridge.call('slowMethod');
  const timer = bridge.pending.values().next().value.timer;
  assert.ok(timer, '应登记超时定时器');
  await sleep(120);
  assert.equal(bridge.pending.size, 1, '长预算方法不应在默认超时后失败');
  const child = bridge.children || null;
  assert.equal(child, null);
  bridge._failAll(new Error('测试收尾'));
  await assert.rejects(p, /收尾/);
  bridge.stop();
});

test('进程退出：pending 全部 reject，并回调 onExit', async () => {
  const exits = [];
  const { bridge, children } = makeBridge({ onExit: (code) => exits.push(code) });
  bridge.start();
  const p = bridge.call('getApps');
  const child = children[0];
  child.exitCode = 3;
  child.emit('exit', 3, null);
  await assert.rejects(p, /已退出\(3\)/);
  assert.equal(bridge.pending.size, 0);
  assert.deepEqual(exits, [3]);
  bridge.stop();
});

test('stdin 报错：pending reject 并清理，后续调用直接失败', async () => {
  const { bridge, children } = makeBridge();
  bridge.start();
  const p = bridge.call('getApps');
  children[0].stdin.emit('error', new Error('EPIPE'));
  await assert.rejects(p, /EPIPE/);
  assert.equal(bridge.pending.size, 0);
  await assert.rejects(bridge.call('getApps'), /管道/);
  bridge.stop();
});

test('写入抛错：该次调用 reject 且不残留 pending', async () => {
  const { bridge, children } = makeBridge();
  bridge.start();
  children[0].stdin.write = () => {
    throw new Error('write EPIPE');
  };
  await assert.rejects(bridge.call('getApps'), /write EPIPE/);
  assert.equal(bridge.pending.size, 0);
  bridge.stop();
});

test('stop(): pending reject 为“已停止”，且旧进程迟到事件不影响新一代', async () => {
  const exits = [];
  const { bridge, children } = makeBridge({ onExit: (code) => exits.push(code) });
  bridge.start();
  const old = children[0];
  const first = bridge.call('getApps');
  bridge.stop();
  await assert.rejects(first, /已停止/);
  assert.equal(bridge.pending.size, 0);

  bridge.start();
  const fresh = children[1];
  const second = bridge.call('getApps');
  const id = fresh.lastRequest().id;
  // 旧进程迟到的 exit / 响应必须被代际隔离丢弃
  old.emit('exit', 9, null);
  old.reply({ id, ok: true, result: 'stale' });
  await sleep(20);
  assert.equal(bridge.pending.size, 1, '旧代际事件不得清空新代 pending');
  assert.deepEqual(exits, [], '主动 stop 不通知 onExit');
  fresh.reply({ id, ok: true, result: 'fresh' });
  assert.equal(await second, 'fresh');
  bridge.stop();
});

test('启动失败（spawn error）：health 不再是 running，后续调用立即失败', async () => {
  const { bridge, children } = makeBridge();
  bridge.start();
  const child = children[0];
  child.emit('error', new Error('spawn ENOTDIR'));
  assert.equal(bridge.health.running, false);
  assert.match(bridge.health.lastStderr.join('\n'), /ENOTDIR/);
  await assert.rejects(bridge.call('getApps'), /ENOTDIR/);
  bridge.stop();
});

test('方法白名单：未登记的方法直接 reject 且不写管道', async () => {
  const { bridge, children } = makeBridge({ allowedMethods: ['getApps'] });
  bridge.start();
  await assert.rejects(bridge.call('rm -rf'), /不支持/);
  assert.equal(children[0].writes.length, 0);
  const p = bridge.call('getApps');
  children[0].reply({ id: children[0].lastRequest().id, ok: true, result: [] });
  assert.deepEqual(await p, []);
  bridge.stop();
});

test('白名单集合来自 RPC_METHODS（渲染层真实用法）', () => {
  assert.ok(RPC_METHODS instanceof Set);
  for (const m of ['getApps', 'getConfig', 'saveConfig', 'runJob', 'cancelJob', 'preview']) {
    assert.ok(RPC_METHODS.has(m), '缺少渲染层使用方法: ' + m);
  }
  // 渲染层从不调用、权限过大的方法不应放行
  assert.equal(RPC_METHODS.has('getClusterNodes'), false);
  assert.equal(RPC_METHODS.has('clusterOpenPendingUpdate'), false);
});

test('真实子进程端到端：stdio 收发与停止', async (t) => {
  const script =
    'process.stdin.on("data", (buf) => {' +
    ' for (const line of String(buf).split("\\n")) {' +
    '  if (!line.trim()) continue;' +
    '  const msg = JSON.parse(line);' +
    '  process.stdout.write(JSON.stringify({id: msg.id, ok: true, result: {method: msg.method, params: msg.params}}) + "\\n");' +
    ' } });';
  const bridge = new CoreBridge({
    command: process.execPath,
    args: ['-e', script],
    rpcTimeoutMs: 5000,
    allowedMethods: ['ping'],
  });
  t.after(() => bridge.stop());
  bridge.start();
  await sleep(150);
  const result = await bridge.call('ping', { 名称: '测试' });
  assert.deepEqual(result, { method: 'ping', params: { 名称: '测试' } });
  assert.equal(bridge.health.running, true);
  bridge.stop();
  assert.equal(bridge.health.running, false);
});
