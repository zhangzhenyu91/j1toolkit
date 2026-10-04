// Python 核心桥：spawn uvmp-core（stdio JSON-RPC）并做 Promise 化调用与事件分发。
// 不 import electron（保持可在纯 node 下测试）；由 main.cjs 注入运行参数。
const { spawn } = require('child_process');
const readline = require('readline');
const fs = require('fs');
const path = require('path');

class CoreBridge {
  /**
   * opts:
   *   command     可执行文件路径（生产=resources/core/uvmp-core(.exe)）
   *   args        附加参数（默认 ['rpc']）
   *   devCwd      开发模式下 python 源码方式运行时的项目根
   *   onEvent     fn(payload)  核心主动推送（job.log/progress/done）
   *   onExit      fn(code)     核心进程退出
   */
  constructor(opts = {}) {
    this.opts = opts;
    this.proc = null;
    this.seq = 0;
    this.pending = new Map();
    this.onEvent = opts.onEvent || (() => {});
    this.onExit = opts.onExit || (() => {});
    this.lastStderr = [];      // 核心 stderr 环形缓冲（起不来时亮给界面）
    this._lastCmd = null;
  }

  get health() {
    return {
      running: !!(this.proc && this.proc.exitCode === null),
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
    const { cmd, args } = this.resolveCommand();
    this._lastCmd = cmd + ' ' + args.join(' ');
    this.proc = spawn(cmd, args, {
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: true,   // 核心无窗口
      env: Object.assign({}, process.env, this.opts.env || {}),
    });
    this.proc.on('error', (err) => {
      this.lastStderr.push('核心进程启动失败: ' + err.message);
      this._failAll(new Error('核心进程启动失败: ' + err.message));
    });
    this.proc.on('exit', (code) => {
      this._failAll(new Error('核心进程已退出(' + code + ')'));
      this.onExit(code);
    });
    const rl = readline.createInterface({ input: this.proc.stdout });
    rl.on('line', (line) => this._onLine(line));
    // 核心 stderr 进主进程日志 + 环形缓冲（起不来时亮给界面）
    this.proc.stderr.on('data', (d) => {
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
    try { msg = JSON.parse(line); } catch { return; }
    if (msg.event) {
      this.onEvent(msg);
      return;
    }
    if (msg.id !== undefined && this.pending.has(msg.id)) {
      const { resolve, reject } = this.pending.get(msg.id);
      this.pending.delete(msg.id);
      if (msg.ok) resolve(msg.result);
      else reject(new Error(msg.error || '未知错误'));
    }
  }

  _failAll(err) {
    for (const { reject } of this.pending.values()) reject(err);
    this.pending.clear();
  }

  call(method, params = {}) {
    return new Promise((resolve, reject) => {
      if (!this.proc || this.proc.exitCode !== null) {
        reject(new Error('核心进程未运行'));
        return;
      }
      const id = ++this.seq;
      this.pending.set(id, { resolve, reject });
      this.proc.stdin.write(JSON.stringify({ id, method, params }) + '\n');
    });
  }

  stop() {
    if (this.proc) {
      try { this.proc.stdin.end(); } catch {}
      try { this.proc.kill(); } catch {}
      this.proc = null;
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

module.exports = { CoreBridge, prodCoreCommand };
