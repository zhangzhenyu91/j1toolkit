// Python 核心桥：spawn uvmp-core（stdio JSON-RPC）并做 Promise 化调用与事件分发。
// 不 import electron（保持可在纯 node 下测试）；由 main.cjs 注入运行参数。
const { spawn } = require('child_process');
const readline = require('readline');
const fs = require('fs');
const path = require('path');

// 渲染层真实调用的 RPC 方法（白名单唯一来源）：src/ 下 call('xxx') 的并集。
// 核心另有 ping/getClusterNodes/clusterOpenPendingUpdate 等方法仅供调试或内部使用，不对外放行。
const RPC_METHODS = new Set([
  'approveClusterNode',
  'cancelJob',
  'clearLogs',
  'clusterAddUpdatePackage',
  'clusterCancelExport',
  'clusterCancelUpdate',
  'clusterGetExport',
  'clusterGetPendingUpdate',
  'clusterGetUpdate',
  'clusterListUpdatePackages',
  'clusterPushUpdate',
  'clusterRemoveUpdatePackage',
  'clusterStartExport',
  'deleteHistory',
  'getActiveJob',
  'getAppStatus',
  'getApps',
  'getClusterStatus',
  'getConfig',
  'getDevScriptEnv',
  'getHistory',
  'getJob',
  'getJobLog',
  'getLogStats',
  'getSystemInfo',
  'getUsbAgentStatus',
  'launchAppBrowser',
  'openPath',
  'preview',
  'rejectClusterNode',
  'runJob',
  'saveConfig',
]);

// 默认 RPC 超时：核心单次调用应在秒级返回（长任务走 runJob 异步）。
const DEFAULT_RPC_TIMEOUT = 60000;
// 会读盘/打包/唤醒浏览器的重方法单独放宽，避免误杀（渲染层有各自的等待提示）。
const SLOW_RPC_TIMEOUT = 180000;
const SLOW_METHODS = new Set([
  'clusterAddUpdatePackage',
  'clusterPushUpdate',
  'clusterStartExport',
  'deleteHistory',
  'getJobLog',
  'launchAppBrowser',
  'runJob',
]);

class CoreBridge {
  /**
   * opts:
   *   command     可执行文件路径（生产=resources/core/uvmp-core(.exe)）
   *   args        附加参数（默认 ['rpc']）
   *   devCwd      开发模式下 python 源码方式运行时的项目根
   *   env         附加环境变量
   *   spawn       注入的 spawn（测试用，默认 child_process.spawn）
   *   allowedMethods  方法白名单（默认 RPC_METHODS；测试可传子集）
   *   rpcTimeoutMs    默认 RPC 超时
   *   rpcTimeouts     按方法覆盖超时 { method: ms }
   *   onEvent     fn(payload)  核心主动推送（job.log/progress/done）
   *   onExit      fn(code)     核心进程退出（主动 stop 不回调）
   */
  constructor(opts = {}) {
    this.opts = opts;
    this.proc = null;
    this.seq = 0;
    this.pending = new Map();
    this.onEvent = opts.onEvent || (() => {});
    this.onExit = opts.onExit || (() => {});
    this.lastStderr = []; // 核心 stderr 环形缓冲（起不来时亮给界面）
    this._lastCmd = null;
    this._spawn = opts.spawn || spawn;
    this._allowed = opts.allowedMethods ? new Set(opts.allowedMethods) : RPC_METHODS;
    this._timeouts = opts.rpcTimeouts || {};
    this._exited = false;
    this._broken = null; // 管道级故障（stdin 报错等）：后续调用直接失败
    // 代际：每次 start/stop 递增，旧进程的迟到事件（exit/stdout）据此丢弃，
    // 避免「重启核心」时旧进程退出把新一代的 pending 全部 reject（已踩坑）。
    this._gen = 0;
    this._session = null;
  }

  get health() {
    // exitCode 在「被信号杀死」时仍为 null，必须同时看 signalCode。
    const proc = this.proc;
    return {
      running: !!(proc && proc.exitCode === null && proc.signalCode === null && !this._exited),
      command: this._lastCmd,
      lastStderr: this.lastStderr.slice(-30),
    };
  }

  resolveCommand() {
    if (this.opts.command) {
      return { cmd: this.opts.command, args: this.opts.args || ['rpc'] };
    }
    // 开发模式：用系统 Python 跑源码（UVMP_PYTHON 可指定解释器）
    const proj = this.opts.devCwd || path.resolve(__dirname, '..', '..');
    return {
      cmd: process.env.UVMP_PYTHON || 'python3',
      args: [path.join(proj, 'toolkit', 'app.py'), 'rpc'],
    };
  }

  start() {
    this._gen += 1;
    const gen = this._gen;
    const session = { gen, proc: null, stdinBroken: false };
    this._session = session;
    this._exited = false;
    this._broken = null;
    const { cmd, args } = this.resolveCommand();
    this._lastCmd = cmd + ' ' + args.join(' ');
    const proc = this._spawn(cmd, args, {
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: true, // 核心无窗口
      env: Object.assign({}, process.env, this.opts.env || {}),
    });
    session.proc = proc;
    this.proc = proc;
    // 旧代际的迟到事件一律丢弃
    const current = () => this._session === session && this._gen === gen;

    proc.on('error', (err) => {
      if (!current()) return;
      this._exited = true;
      this._broken = '核心进程启动失败: ' + err.message;
      this.lastStderr.push(this._broken);
      this._failAll(new Error(this._broken), gen);
    });
    proc.on('exit', (code, signal) => {
      if (!current()) return;
      this._exited = true;
      this._failAll(new Error('核心进程已退出(' + (code === null ? signal : code) + ')'), gen);
      this.onExit(code === null ? signal || null : code);
    });
    const rl = readline.createInterface({ input: proc.stdout });
    session.rl = rl;
    rl.on('line', (line) => {
      if (current()) this._onLine(line);
    });
    // 管道故障（核心崩掉/被外部杀掉时 EPIPE）：当轮 pending 立刻失败，后续调用不再白等
    const onStdinError = (err) => {
      if (!current()) return;
      session.stdinBroken = true;
      this._broken = '核心管道不可用: ' + (err && err.message ? err.message : err);
      this.lastStderr.push(this._broken);
      this._failAll(new Error(this._broken), gen);
    };
    proc.stdin.on('error', onStdinError);
    proc.stdin.on('close', () => {
      if (!current() || session.stdinBroken) return;
      session.stdinBroken = true;
      this._broken = '核心管道已关闭';
      this._failAll(new Error(this._broken), gen);
    });
    // 核心 stderr 进主进程日志 + 环形缓冲（起不来时亮给界面）
    proc.stderr.on('data', (d) => {
      const text = String(d).trim();
      if (text) {
        this.lastStderr.push(...text.split('\n'));
        if (this.lastStderr.length > 50) {
          this.lastStderr.splice(0, this.lastStderr.length - 50);
        }
      }
      console.error('[core-stderr]', text);
    });
    return this;
  }

  _onLine(line) {
    let msg;
    try {
      msg = JSON.parse(line);
    } catch {
      return;
    }
    if (msg.event) {
      this.onEvent(msg);
      return;
    }
    if (msg.id !== undefined && this.pending.has(msg.id)) {
      const entry = this.pending.get(msg.id);
      this.pending.delete(msg.id);
      if (entry.timer) clearTimeout(entry.timer);
      if (msg.ok) entry.resolve(msg.result);
      else entry.reject(new Error(msg.error || '未知错误'));
    }
  }

  // 只失败指定代际的 pending（旧进程退出不得波及新进程）
  _failAll(err, gen) {
    for (const [id, entry] of this.pending.entries()) {
      if (gen !== undefined && entry.gen !== gen) continue;
      this.pending.delete(id);
      if (entry.timer) clearTimeout(entry.timer);
      entry.reject(err);
    }
  }

  _timeoutFor(method) {
    const override = this._timeouts[method];
    if (override) return override;
    if (SLOW_METHODS.has(method)) return SLOW_RPC_TIMEOUT;
    return this.opts.rpcTimeoutMs || DEFAULT_RPC_TIMEOUT;
  }

  call(method, params = {}) {
    return new Promise((resolve, reject) => {
      if (!this._allowed.has(method)) {
        reject(new Error('不支持的 RPC 方法: ' + method));
        return;
      }
      if (this._broken) {
        reject(new Error(this._broken));
        return;
      }
      if (
        this._exited ||
        !this.proc ||
        this.proc.exitCode !== null ||
        this.proc.signalCode !== null
      ) {
        reject(new Error('核心进程未运行'));
        return;
      }
      const id = ++this.seq;
      const gen = this._gen;
      const timer = setTimeout(() => {
        if (!this.pending.has(id)) return;
        this.pending.delete(id);
        reject(new Error('核心调用超时(' + method + ')'));
      }, this._timeoutFor(method));
      // 超时定时器必须保持 ref：在飞调用就是在飞工作，必须钉住事件循环。
      // 一旦 unref，进程里再无其它句柄时（Node 22 的 node --test 即是）循环会提前空转，
      // 超时永远不会到期，调用方只能空等；退出路径由 stop() 统一清定时器，不会拖住退出。
      this.pending.set(id, { resolve, reject, timer, gen });
      // 与核心响应的 ensure_ascii=True 对称：只传 ASCII，避免 Windows 用系统代码页误读 UTF-8 中文。
      const payload = JSON.stringify({ id, method, params }).replace(
        /[^\x00-\x7F]/g,
        (ch) => '\\u' + ch.charCodeAt(0).toString(16).padStart(4, '0')
      );
      try {
        this.proc.stdin.write(payload + '\n', (err) => {
          if (!err) return;
          const entry = this.pending.get(id);
          if (!entry) return;
          this.pending.delete(id);
          if (entry.timer) clearTimeout(entry.timer);
          entry.reject(new Error('核心管道写入失败: ' + (err.message || err)));
        });
      } catch (e) {
        const entry = this.pending.get(id);
        if (entry) {
          this.pending.delete(id);
          if (entry.timer) clearTimeout(entry.timer);
        }
        reject(new Error('核心管道写入失败: ' + (e && e.message ? e.message : e)));
      }
    });
  }

  stop() {
    // 先换代：之后旧进程的任何 exit/stdout 事件都不再影响新会话
    this._gen += 1;
    const session = this._session;
    this._session = null;
    const proc = this.proc;
    this.proc = null;
    this._exited = false;
    this._broken = null;
    this._failAll(new Error('核心进程已停止'), undefined);
    if (session) {
      session.stdinBroken = true;
      try {
        session.rl.close();
      } catch {}
    }
    if (proc) {
      proc.removeAllListeners('exit');
      proc.removeAllListeners('error');
      try {
        proc.stdin.end();
      } catch {}
      try {
        proc.kill();
      } catch {}
    }
  }
}

// 生产模式核心路径：resources/core/uvmp-core(.exe)
function prodCoreCommand(resourcesPath) {
  const dir = path.join(resourcesPath, 'core');
  for (const name of ['uvmp-core.exe', 'uvmp-core']) {
    const p = path.join(dir, name);
    if (fs.existsSync(p)) return p;
  }
  return null;
}

module.exports = { CoreBridge, prodCoreCommand, RPC_METHODS };
