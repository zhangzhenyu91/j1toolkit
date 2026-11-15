// 图片预览性能与全目录预热实测：直接驱动真实 Electron、RPC 与 Chromium。
// UVMP_DESKTOP=<另一工作树 desktop> UVMP_PERF_BASELINE=1 可只采集旧版基线。
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawn, spawnSync } = require('child_process');
const WebSocket = require('ws');

const DESKTOP = path.resolve(process.env.UVMP_DESKTOP || path.join(__dirname, '..'));
const BASELINE = process.env.UVMP_PERF_BASELINE === '1';
const PORT = 9511 + Math.floor(Math.random() * 40);
const metrics = { baseline: BASELINE };
let failures = 0;

function check(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}${detail == null ? '' : ` — ${detail}`}`);
  if (!ok) failures += 1;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

class Cdp {
  constructor(url) {
    this.ws = new WebSocket(url);
    this.seq = 0;
    this.pending = new Map();
    this.ws.on('message', (raw) => {
      const msg = JSON.parse(String(raw));
      if (!msg.id || !this.pending.has(msg.id)) return;
      const { resolve, reject } = this.pending.get(msg.id);
      this.pending.delete(msg.id);
      if (msg.error) reject(new Error(msg.error.message));
      else resolve(msg.result || {});
    });
  }

  async ready() {
    if (this.ws.readyState === WebSocket.OPEN) return;
    await new Promise((resolve, reject) => {
      this.ws.once('open', resolve);
      this.ws.once('error', reject);
    });
  }

  send(method, params = {}) {
    const id = ++this.seq;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }

  async evaluate(expression) {
    const result = await this.send('Runtime.evaluate', {
      expression,
      returnByValue: true,
      awaitPromise: true,
    });
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
    return result.result ? result.result.value : undefined;
  }

  async waitFor(expression, timeout = 30000) {
    const deadline = Date.now() + timeout;
    let last;
    while (Date.now() < deadline) {
      try {
        last = await this.evaluate(expression);
        if (last) return last;
      } catch {
        // 页面重载期间短暂不可执行，继续等到超时。
      }
      await sleep(60);
    }
    throw new Error(`等待超时：${expression}；最后值=${JSON.stringify(last)}`);
  }
}

async function imagePage() {
  const deadline = Date.now() + 40000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`http://127.0.0.1:${PORT}/json/list`);
      const pages = await response.json();
      const found = pages.find((page) => page.type === 'page' && /imagetools\.html/.test(page.url));
      if (found) return found;
    } catch {
      // Electron 尚未开放调试端口。
    }
    await sleep(150);
  }
  throw new Error('图片窗口没有出现');
}

function python() {
  const candidates = [process.env.UVMP_PYTHON, 'python3.15', 'python', 'python3'].filter(Boolean);
  for (const cmd of candidates) {
    const probe = spawnSync(
      cmd,
      [
        '-c',
        'import sys, PIL, site; print(sys.executable); print(PIL.__version__); print(site.getusersitepackages())',
      ],
      { encoding: 'utf8', windowsHide: true }
    );
    if (probe.status !== 0) continue;
    const lines = String(probe.stdout).trim().split(/\r?\n/);
    if (lines[0]) return { exe: lines[0], version: lines[1] || '', userSite: lines[2] || '' };
  }
  throw new Error('需要带 Pillow 的 Python 3.15');
}

function makeSamples(directory, pythonExe) {
  const script = `
import os
from PIL import Image
out = os.environ['PERF_SAMPLES']
Image.new('RGB', (1920, 1080), (14, 61, 168)).save(os.path.join(out, '01-small.jpg'), quality=90)
Image.new('RGB', (6000, 4000), (32, 120, 72)).save(os.path.join(out, '02-large.jpg'), quality=90)
Image.new('RGB', (2400, 1600), (180, 100, 30)).save(os.path.join(out, '03-medium.png'))
Image.new('RGB', (1600, 1200), (100, 60, 160)).save(os.path.join(out, '04-small.webp'), quality=85)
Image.new('RGB', (3000, 2000), (120, 40, 50)).save(os.path.join(out, '05-second-large.jpg'), quality=88)
full = os.path.join(out, '01-small.jpg')
raw = open(full, 'rb').read()
sof = min(pos for pos in (raw.find(b'\\xff\\xc0'), raw.find(b'\\xff\\xc2')) if pos > 0)
cut = sof + 2 + int.from_bytes(raw[sof + 2:sof + 4], 'big')
open(os.path.join(out, '06-truncated.jpg'), 'wb').write(raw[:cut])
`;
  const result = spawnSync(pythonExe, ['-c', script], {
    env: { ...process.env, PERF_SAMPLES: directory },
    windowsHide: true,
    timeout: 120000,
  });
  if (result.status !== 0) throw new Error(String(result.stderr || '生成图片样本失败'));
}

function killTree(proc) {
  if (!proc) return;
  try {
    if (process.platform === 'win32') {
      spawnSync('taskkill', ['/T', '/F', '/PID', String(proc.pid)], { windowsHide: true });
    } else {
      proc.kill('SIGKILL');
    }
  } catch {
    // 已退出。
  }
}

async function main() {
  const py = python();
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'it-preview-perf-'));
  const samples = path.join(tmp, 'samples');
  fs.mkdirSync(samples);
  makeSamples(samples, py.exe);
  const config = path.join(tmp, 'config.json');
  fs.copyFileSync(path.join(DESKTOP, '..', 'toolkit', 'config.example.json'), config);
  const localAppData = path.join(tmp, 'LocalAppData');
  const appData = path.join(tmp, 'AppData');
  fs.mkdirSync(localAppData, { recursive: true });
  fs.mkdirSync(appData, { recursive: true });
  const override = String(process.env.UVMP_ELECTRON_EXE || '').trim();
  const electron =
    override ||
    path.join(
      DESKTOP,
      'node_modules',
      'electron',
      'dist',
      process.platform === 'win32' ? 'electron.exe' : 'electron'
    );
  if (!fs.existsSync(electron)) throw new Error(`找不到 Electron：${electron}`);
  const first = path.join(samples, '02-large.jpg');
  const proc = spawn(
    electron,
    [
      ...(override ? [] : ['.']),
      `--remote-debugging-port=${PORT}`,
      '--remote-allow-origins=*',
      first,
    ],
    {
      cwd: DESKTOP,
      env: {
        ...process.env,
        UVMP_PYTHON: py.exe,
        UVMP_TOOLKIT_CONFIG: config,
        LOCALAPPDATA: localAppData,
        APPDATA: appData,
        PYTHONPATH: [py.userSite, process.env.PYTHONPATH].filter(Boolean).join(path.delimiter),
        UVMP_CI_SMOKE: '1',
        UVMP_CI_IMAGE_SMOKE: '1',
        ELECTRON_ENABLE_LOGGING: '0',
      },
      stdio: ['ignore', 'ignore', 'pipe'],
      windowsHide: true,
    }
  );
  let stderr = '';
  proc.stderr.on('data', (chunk) => {
    stderr = (stderr + String(chunk)).slice(-8000);
  });
  let cdp;
  try {
    const started = Date.now();
    const found = await imagePage();
    cdp = new Cdp(found.webSocketDebuggerUrl);
    await cdp.ready();
    await cdp.send('Runtime.enable');
    await cdp.waitFor(
      `(() => {
      const img = document.querySelector('.it-photo');
      return !!img && img.complete && img.naturalWidth > 0 && !img.classList.contains('is-hidden');
    })()`,
      60000
    );
    metrics.firstVisibleMs = Date.now() - started;

    async function step(buttonLabel, expectedName) {
      const before = Date.now();
      await cdp.evaluate(`(() => {
        const previous = document.querySelector('.it-photo');
        const previousSrc = previous ? previous.src : '';
        window.__perfStepLoaded = false;
        window.__perfStepListener = (event) => {
          const img = event.target;
          const name = (document.querySelector('.it-fname') || {}).textContent || '';
          if (!(img instanceof HTMLImageElement) || !img.classList.contains('it-photo')) return;
          if (!name.includes(${JSON.stringify(expectedName)}) || img.src === previousSrc) return;
          window.__perfStepLoaded = true;
          document.removeEventListener('load', window.__perfStepListener, true);
        };
        document.addEventListener('load', window.__perfStepListener, true);
        const button = document.querySelector('button[aria-label=${JSON.stringify(buttonLabel)}]');
        if (!button || button.disabled) throw new Error('切图按钮不可用');
        button.click();
        return true;
      })()`);
      await cdp.waitFor('window.__perfStepLoaded === true', 45000);
      return Date.now() - before;
    }

    metrics.immediateNextMs = await step('下一张', '03-medium.png');
    let stats = null;
    if (!BASELINE) {
      await cdp.evaluate('window.__imageToolsSmoke.previewWhenIdle()');
      stats = await cdp.evaluate('window.__imageToolsSmoke.previewStats()');
      metrics.afterPreload = stats;
      check('全目录支持图片均进入后台加载', stats.visited >= 5, JSON.stringify(stats));
      check('后台静默跳过坏图但记录失败', stats.failed >= 1, `failed=${stats.failed}`);
      check(
        '缓存条目与字节预算有效',
        stats.cacheSize <= 48 &&
          stats.blobBytes <= 128 * 1024 * 1024 &&
          stats.decodedBytes <= 256 * 1024 * 1024,
        JSON.stringify({
          cacheSize: stats.cacheSize,
          blobBytes: stats.blobBytes,
          decodedBytes: stats.decodedBytes,
        })
      );
    } else {
      await sleep(1500);
    }

    const readsBefore = stats ? stats.reads : 0;
    metrics.preloadedNextMs = await step('下一张', '04-small.webp');
    metrics.preloadedPrevMs = await step('上一张', '03-medium.png');
    if (!BASELINE) {
      const after = await cdp.evaluate('window.__imageToolsSmoke.previewStats()');
      metrics.afterCachedNavigation = after;
      check(
        '预热后切图不再重复读取协议字节',
        after.reads === readsBefore,
        `${readsBefore} → ${after.reads}`
      );
      check('预热后切图命中浏览器缓存', after.hits >= 2, `hits=${after.hits}`);
    }

    await step('下一张', '04-small.webp');
    await step('下一张', '05-second-large.jpg');
    const brokenStarted = Date.now();
    await cdp.evaluate('document.querySelector(\'button[aria-label="下一张"]\').click()');
    await cdp.waitFor(
      `(() => {
      const error = document.querySelector('.it-error');
      return !!error && /IMG_(DECODE_FAILED|UNSUPPORTED)/.test(error.innerText);
    })()`,
      30000
    );
    metrics.brokenFailureMs = Date.now() - brokenStarted;
    check('截断 JPEG 在真实 Chromium 中进入失败态', true);
  } catch (error) {
    check('性能冒烟执行完成', false, `${error.message}；stderr=${stderr.slice(-600)}`);
  } finally {
    if (cdp) {
      try {
        cdp.ws.close();
      } catch {
        // 已关闭。
      }
    }
    killTree(proc);
    await sleep(500);
    fs.rmSync(tmp, { recursive: true, force: true });
  }
  console.log(`PERF_METRICS ${JSON.stringify(metrics)}`);
  process.exit(failures ? 1 : 0);
}

main().catch((error) => {
  console.error(error);
  process.exit(2);
});
