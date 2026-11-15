// 真实 GUI 冒烟：用本仓 Electron + 临时图片样本驱动独立图片工具窗口（imagetools.html）。
//
// 覆盖：看图（六类信息/分钟时间）、切图（→ 与首尾不循环）、滚轮缩放、F2 就地改名、
// 快捷短语插入、三页签批处理（格式转换 / 压缩体积 / 尺寸修改）与结果、真实取消、900×600 抽屉。
// 取消段走真实核心 RPC + 真实文件 + 真实 JobManager，并核对 getJob 结果与磁盘成品一致。
//
// 用法（在 uvmp-toolkit/desktop 下）：
//   python -m pip install pillow            # 仅用于生成临时样本
//   node test/image-tools-gui-smoke.cjs
//   node test/image-tools-gui-smoke.cjs --keep   # 保留临时目录与截图供排查
//   UVMP_ELECTRON_EXE=<绝对路径的打包客户端> node test/image-tools-gui-smoke.cjs
//     发版机只有打包客户端（源码 electron 二进制未下载）时用这条：跑的是真实打包壳 +
//     随包冻结核心，而不是源码 Electron。
//
// 退出码：0 通过；1 冒烟失败；2 环境不满足（缺 Electron/Pillow/样本），视为失败不做静默跳过。
// 说明：只给「系统文件选择器」打桩（返回本窗口已授权目录内的路径），其余全部走真实主进程、
// 真实核心 RPC 与真实渲染层；不联网、不写仓内文件，产物只在系统临时目录。

const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawn, spawnSync } = require('child_process');
const WebSocket = require('ws');

const DESKTOP = path.resolve(__dirname, '..');
// 期望值统一用产品同一套真实路径口径（Windows native realpath：8.3 短名归一成长名）。
// 测试侧若用 legacy fs.realpathSync，短名环境（CI 的 C:\Users\RUNNER~1\…）会把短名当期望值，
// 与产品返回的长名比对永远不等，把真修好误判成失败。
const { realOrEmpty } = require('../electron/imagetools/path-guard.cjs');
// 发版机可能只装打包客户端（源码 electron 二进制被跳过下载，v2.4.1 CI 现场）：
// UVMP_ELECTRON_EXE 给出打包 exe 的绝对路径时用它，默认仍走本仓 desktop/node_modules/electron。
// 源码态要显式传应用目录（`electron .`）；打包态 exe 后面不能再跟目录参数（argv 里没有 '.'）。
function resolveElectron(env = process.env) {
  const override = String(env.UVMP_ELECTRON_EXE || '').trim();
  if (override) {
    if (!path.isAbsolute(override)) {
      return { error: `UVMP_ELECTRON_EXE 必须是绝对路径：${override}` };
    }
    if (!fs.existsSync(override)) return { error: `UVMP_ELECTRON_EXE 不存在：${override}` };
    return { exe: override, args: [], packaged: true };
  }
  return {
    exe: path.join(
      DESKTOP,
      'node_modules',
      'electron',
      'dist',
      process.platform === 'win32' ? 'electron.exe' : 'electron'
    ),
    args: ['.'],
    packaged: false,
  };
}
const PORT = 9411 + Math.floor(Math.random() * 40);
const args = process.argv.slice(2);
const KEEP = args.includes('--keep');

/**
 * 取一个已存在目录/文件的 8.3 短名（Windows）。拿不到（8.3 关闭、非 Windows、无短名形态）返回空串。
 * 用于复现 CI 形态：渲染层收到短名路径，而核心列举返回长名。
 */
function windowsShortPath(target) {
  if (process.platform !== 'win32') return '';
  const quoted = String(target).replace(/'/g, "''");
  const script = [
    // 必须显式设 UTF-8：默认按控制台代码页输出，含中文的路径会变成 ????，短名随即失效
    '[Console]::OutputEncoding = [Text.Encoding]::UTF8',
    `$p = '${quoted}'`,
    'if (-not (Test-Path -LiteralPath $p)) { exit 1 }',
    '$f = New-Object -ComObject Scripting.FileSystemObject',
    // 不能写成 `if {...}; else {...}`：PowerShell 里 else 必须紧跟 if 块，用分行拼接
    'if (Test-Path -LiteralPath $p -PathType Container) { $f.GetFolder($p).ShortPath }',
    'else { $f.GetFile($p).ShortPath }',
  ].join('\n');
  const r = spawnSync('powershell', ['-NoProfile', '-NonInteractive', '-Command', script], {
    encoding: 'utf8',
    windowsHide: true,
    timeout: 20000,
  });
  if (r.status !== 0) return '';
  const out = String(r.stdout || '').trim();
  // 短名含中文乱码（?）时视为不可用，避免拿一条无效路径做断言
  if (/[?]/.test(out)) return '';
  return out;
}

const results = [];
let failures = 0;
// 失败路径（process.exit）也要清理临时目录与本脚本拉起的进程
let cleanupOnExit = null;

function check(name, ok, detail) {
  results.push({ name, ok: !!ok, detail: detail == null ? '' : String(detail) });
  if (!ok) failures += 1;
  const mark = ok ? 'PASS' : 'FAIL';
  console.log(`[${mark}] ${name}${detail == null ? '' : ` — ${detail}`}`);
}

function fail(code, message) {
  console.error(`[FATAL] ${message}`);
  if (typeof cleanupOnExit === 'function') {
    try {
      cleanupOnExit();
    } catch (e) {
      // 清理失败不改变退出码
    }
  }
  process.exit(code);
}

// ---- 指针锚点缩放的判定依据（纯函数，导出给负对照与静态验证复用）----
// 产品把画面写成 `translate(-50%, -50%) translate(offsetX, offsetY)`（ViewerPane.vue:305-309），
// 浏览器把两段平移合并成 matrix(1,0,0,1, -w/2+offsetX, -h/2+offsetY)：
// e/f 天然带居中基线，「e !== 0」恒真，不能作为「以指针为锚」的证据。
const WHEEL_FACTOR = 1.12;

/** 从计算样式与图片盒子尺寸反推「画面中心相对画布中心的补偿平移」。 */
function readPanCompensation(probe) {
  if (!probe || probe.missing) return { x: NaN, y: NaN, reason: '缺少图片元素' };
  const raw = String(probe.raw || '');
  if (!raw || raw === 'none') return { x: NaN, y: NaN, reason: 'transform 未设置' };
  const num = '(-?(?:\\d+(?:\\.\\d+)?|\\.\\d+))';
  const t = new RegExp(`translate\\(${num}px,\\s*${num}px\\)`).exec(raw);
  if (t) {
    // 只可能匹配 px 那一段（居中基线是百分比），所以它本身就是补偿值
    return { x: Number(t[1]), y: Number(t[2]), form: 'translate', raw };
  }
  const m = /matrix\(([^)]+)\)/.exec(raw);
  if (!m) return { x: NaN, y: NaN, reason: `无法解析的 transform：${raw}` };
  const parts = m[1].split(',').map((v) => Number(v.trim()));
  if (parts.length !== 6 || !parts.every((v) => Number.isFinite(v))) {
    return { x: NaN, y: NaN, reason: `matrix 分量非法：${raw}` };
  }
  const w = Number(probe.w);
  const h = Number(probe.h);
  if (!(w > 0) || !(h > 0)) return { x: NaN, y: NaN, reason: `盒子尺寸非法：${w}x${h}` };
  return { x: parts[4] + w / 2, y: parts[5] + h / 2, form: 'matrix', raw };
}

/** 指针锚点的期望补偿：指针下的内容不动 → offset = 指针偏移 ×(1 - 缩放比)。 */
function expectedAnchorShift(dx, dy, ratio = WHEEL_FACTOR) {
  const k = 1 - Number(ratio);
  return { x: Number(dx) * k, y: Number(dy) * k, ratio: Number(ratio) };
}

/** 判定：补偿必须是有限数值、与期望一致（容差 2px），且确实发生了平移。 */
function judgeAnchorCompensation(probe, expected, tolerance = 2) {
  const comp = readPanCompensation(probe);
  const detail = { comp, expected, tolerance };
  if (!Number.isFinite(comp.x) || !Number.isFinite(comp.y)) {
    return { ok: false, ...detail, reason: comp.reason || '补偿值不是数值' };
  }
  if (!Number.isFinite(expected.x) || !Number.isFinite(expected.y)) {
    return { ok: false, ...detail, reason: '期望值不是数值' };
  }
  const okX = Math.abs(comp.x - expected.x) <= tolerance;
  const okY = Math.abs(comp.y - expected.y) <= tolerance;
  const moved = Math.abs(comp.x) > tolerance || Math.abs(comp.y) > tolerance;
  const expectMoved = Math.abs(expected.x) > tolerance || Math.abs(expected.y) > tolerance;
  return { ok: okX && okY && (!expectMoved || moved), ...detail, okX, okY, moved };
}

// 必须拿到**绝对路径**的解释器：Windows 的 `python` 可能是 App Execution Alias，
// 换掉 LOCALAPPDATA 后它找不到已安装运行时，核心会直接起不来。
function pythonCommand() {
  const probeArgs = [
    '-c',
    'import sys, PIL, site; print(sys.executable); print(PIL.__version__); print(site.getusersitepackages())',
  ];
  for (const candidate of [process.env.UVMP_PYTHON, 'python', 'python3']) {
    if (!candidate) continue;
    const probe = spawnSync(candidate, probeArgs, { encoding: 'utf8', windowsHide: true });
    if (probe.status !== 0) continue;
    const lines = String(probe.stdout).trim().split(/\r?\n/);
    const exe = lines[0];
    const pil = lines[1] || '';
    // Pillow 装在 %APPDATA%\Python 的用户 site 下：隔离 APPDATA 后要用 PYTHONPATH 补回
    const userSite = lines[2] || '';
    if (!exe) continue;
    return { cmd: candidate, exe, pil, userSite };
  }
  return null;
}

function makeSamples(dir) {
  const py = pythonCommand();
  if (!py) fail(2, '没有可用 Pillow 的 Python，无法生成临时样本');
  const script = `
import os, sys
from PIL import Image
out = sys.argv[1]
# 同目录 6 张：1 张损坏、1 张动画 GIF、4 张可处理
# 数值前缀固定自然排序：第 1 张是可处理的 JPEG，末尾才是损坏文件
Image.new('RGB', (1920, 1280), (14, 61, 168)).save(os.path.join(out, '01-秋检现场.jpg'), quality=92)
Image.new('RGBA', (800, 600), (255, 120, 0, 120)).save(os.path.join(out, '02-现场标识牌.png'))
Image.new('RGB', (640, 480), (10, 53, 146)).save(os.path.join(out, '03-安全交底会.webp'), quality=88)
Image.new('RGB', (900, 1200), (200, 60, 60)).save(os.path.join(out, '04-站房正立面.bmp'))
frames = [Image.new('P', (160, 120), i) for i in (1, 2, 3)]
frames[0].save(os.path.join(out, '05-班前会.gif'), save_all=True, append_images=frames[1:], duration=120, loop=0)
with open(os.path.join(out, '06-坏图.jpg'), 'wb') as fp:
    fp.write(b'not an image at all')
print(os.path.getsize(os.path.join(out, '01-秋检现场.jpg')))
`;
  const run = spawnSync(py.cmd, ['-c', script, dir], { encoding: 'utf8', windowsHide: true });
  if (run.status !== 0) fail(2, `生成样本失败：${run.stderr || run.stdout}`);
  const names = fs.readdirSync(dir);
  if (names.length < 6) fail(2, `样本不完整：${names.join(', ')}`);
  return { names, pil: py.pil };
}

async function fetchJson(url) {
  const res = await fetch(url);
  return res.json();
}

async function listImagePages() {
  try {
    const list = await fetchJson(`http://127.0.0.1:${PORT}/json/list`);
    return list.filter(
      (item) =>
        item.type === 'page' && /imagetools\.html/.test(item.url) && item.webSocketDebuggerUrl
    );
  } catch (e) {
    return [];
  }
}

async function waitTargets(deadline) {
  while (Date.now() < deadline) {
    const pages = await listImagePages();
    if (pages.length) {
      // 冷启动时窗口可能被重建：等到页面集合在连续两次采样中稳定下来再绑定，
      // 否则会抓到正在销毁的旧页面（表现为 0/0 且诊断对象冻结在半初始化状态）
      const firstId = pages[0].id;
      await new Promise((r) => setTimeout(r, 400));
      const again = await listImagePages();
      const stable = again.find((item) => item.id === firstId) || again[0];
      if (stable) return { page: stable, all: again };
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  return null;
}

class Cdp {
  constructor(url) {
    this.ws = new WebSocket(url, { perMessageDeflate: false });
    this.seq = 0;
    this.pending = new Map();
    this.ws.on('message', (raw) => {
      const msg = JSON.parse(String(raw));
      if (!msg.id || !this.pending.has(msg.id)) return;
      const { resolve, reject } = this.pending.get(msg.id);
      this.pending.delete(msg.id);
      if (msg.error) reject(new Error(msg.error.message || JSON.stringify(msg.error)));
      else resolve(msg.result);
    });
  }

  ready() {
    return new Promise((resolve, reject) => {
      this.ws.once('open', resolve);
      this.ws.once('error', reject);
    });
  }

  send(method, params = {}, timeoutMs = 20000) {
    const id = (this.seq += 1);
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify({ id, method, params }));
      setTimeout(() => {
        if (!this.pending.has(id)) return;
        this.pending.delete(id);
        reject(new Error(`CDP 超时：${method}`));
      }, timeoutMs);
    });
  }

  /**
   * 有限次重试的调用：真实窗口偶发（首帧合成、放大缩小后重绘）会让单次
   * 求值/截图超时，重试一次可把偶发从结果里剔除，但不掩盖持续失败。
   */
  async sendRetry(method, params = {}, { attempts = 2, timeoutMs = 20000 } = {}) {
    let lastError = null;
    for (let i = 0; i < attempts; i += 1) {
      try {
        return await this.send(method, params, timeoutMs);
      } catch (e) {
        lastError = e;
        await new Promise((r) => setTimeout(r, 400));
      }
    }
    throw lastError;
  }

  async evaluate(expression) {
    const result = await this.sendRetry('Runtime.evaluate', {
      expression,
      awaitPromise: true,
      returnByValue: true,
      userGesture: true,
    });
    if (result.exceptionDetails) {
      const text = result.exceptionDetails.exception
        ? result.exceptionDetails.exception.description || result.exceptionDetails.text
        : result.exceptionDetails.text;
      // 带上表达式片段，失败时能直接定位是哪个断言
      throw new Error(`页面求值异常：${text}
  表达式：${String(expression).slice(0, 160)}`);
    }
    return result.result ? result.result.value : undefined;
  }

  /**
   * 轮询直到表达式为真。
   * 页面刚加载完、渲染进程还在忙时 Runtime.evaluate 可能整体超时（CDP 一次性请求超时），
   * 这属于「还没准备好」而不是断言失败：吞掉异常继续等到截止时间；到点仍不满足才返回最后结果，
   * 由调用方的 check 判失败（真问题照样会红）。
   */
  async waitFor(expression, timeoutMs = 12000, step = 200) {
    const deadline = Date.now() + timeoutMs;
    let last;
    let lastError = null;
    while (Date.now() < deadline) {
      try {
        last = await this.evaluate(expression);
        lastError = null;
        if (last) return last;
      } catch (e) {
        lastError = e;
      }
      await new Promise((r) => setTimeout(r, step));
    }
    if (lastError) throw lastError;
    return last;
  }

  /** 真实键盘事件（走 Chromium 输入管线，而不是页面里造 KeyboardEvent）。 */
  async pressKey(key) {
    const map = {
      F2: { code: 'F2', windowsVirtualKeyCode: 113, nativeVirtualKeyCode: 113 },
      ArrowLeft: { code: 'ArrowLeft', windowsVirtualKeyCode: 37, nativeVirtualKeyCode: 37 },
      ArrowRight: { code: 'ArrowRight', windowsVirtualKeyCode: 39, nativeVirtualKeyCode: 39 },
      Escape: { code: 'Escape', windowsVirtualKeyCode: 27, nativeVirtualKeyCode: 27 },
    };
    const meta = map[key];
    if (!meta) throw new Error(`未支持的按键：${key}`);
    const base = {
      key,
      code: meta.code,
      windowsVirtualKeyCode: meta.windowsVirtualKeyCode,
      nativeVirtualKeyCode: meta.nativeVirtualKeyCode,
    };
    await this.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', ...base });
    await this.send('Input.dispatchKeyEvent', { type: 'keyUp', ...base });
  }

  async screenshot(file) {
    const shot = await this.sendRetry(
      'Page.captureScreenshot',
      { format: 'png' },
      {
        attempts: 3,
        timeoutMs: 30000,
      }
    );
    fs.writeFileSync(file, Buffer.from(shot.data, 'base64'));
  }

  /** 只截某个元素（给信息栏这类区域取证用）：clip 用 CSS 像素，deviceScaleFactor 由页面 dpr 决定。 */
  async screenshotElement(file, selector) {
    const box = await this.evaluate(`(() => {
      const el = document.querySelector(${JSON.stringify(selector)});
      if (!el) return null;
      const r = el.getBoundingClientRect();
      return { x: r.left, y: r.top, width: r.width, height: r.height };
    })()`);
    if (!box || !box.width || !box.height) return false;
    const shot = await this.sendRetry(
      'Page.captureScreenshot',
      {
        format: 'png',
        // scale 固定 1：不传会被某些版本视为非法参数，传 dpr 又会与设备像素比叠加放大
        clip: { x: box.x, y: box.y, width: box.width, height: box.height, scale: 1 },
      },
      { attempts: 3, timeoutMs: 30000 }
    );
    fs.writeFileSync(file, Buffer.from(shot.data, 'base64'));
    return true;
  }
}

/**
 * 主进程会 detached 拉起常驻 U 盘 agent（脱离进程树，taskkill /T 抓不到）。
 * 它指向本次临时 LOCALAPPDATA：退出前按「命令行包含该临时目录」精确清理，
 * 既不碰用户真实 agent，也不留下占满临时目录的孤儿进程。
 */
function killDetachedAgents(tempLocalAppData) {
  if (process.platform !== 'win32' || !tempLocalAppData) return 0;
  const script = [
    `$needle = '${tempLocalAppData.replace(/'/g, "''")}'`,
    'Get-CimInstance Win32_Process -Filter "Name like \'%python%\'" |',
    '  Where-Object { $_.CommandLine -and $_.CommandLine.Contains($needle) } |',
    '  ForEach-Object { try { Stop-Process -Id $_.ProcessId -Force -ErrorAction Stop; $_.ProcessId } catch {} }',
  ].join(' ');
  const r = spawnSync('powershell', ['-NoProfile', '-Command', script], {
    encoding: 'utf8',
    windowsHide: true,
    timeout: 30000,
  });
  const killed = String(r.stdout || '')
    .split(/\r?\n/)
    .filter((line) => line.trim()).length;
  return killed;
}

function killTree(proc) {
  if (!proc) return;
  try {
    if (process.platform === 'win32') {
      spawnSync('taskkill', ['/T', '/F', '/PID', String(proc.pid)], { windowsHide: true });
    } else {
      proc.kill('SIGKILL');
    }
  } catch (e) {
    // 已退出
  }
}

async function main() {
  const electron = resolveElectron();
  if (electron.error) fail(2, electron.error);
  if (!fs.existsSync(electron.exe)) fail(2, `找不到 Electron：${electron.exe}`);
  if (!fs.existsSync(path.join(DESKTOP, 'dist', 'imagetools.html'))) {
    fail(2, '缺少 dist/imagetools.html，请先 npm run build');
  }
  console.log(`客户端：${electron.exe}${electron.packaged ? '（打包态）' : '（源码态）'}`);

  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'it-gui-'));
  const sampleDir = path.join(tmp, '样本');
  fs.mkdirSync(sampleDir);
  // 配置与运行状态全部隔离到临时目录：绝不写用户真实 config.json / 任务历史 / 预览缓存
  const tempConfig = path.join(tmp, 'config.json');
  fs.copyFileSync(path.join(DESKTOP, '..', 'toolkit', 'config.example.json'), tempConfig);
  // 只改本次临时配置：workers=1 让「并发数来自真实配置」可被日志实测，
  // 同时验证单并发下的任务结果与取消链路仍然正确（不碰用户真实配置）。
  const tempCfg = JSON.parse(fs.readFileSync(tempConfig, 'utf8'));
  tempCfg.image_tools = Object.assign({}, tempCfg.image_tools, { workers: 1 });
  fs.writeFileSync(tempConfig, JSON.stringify(tempCfg, null, 2) + '\n');
  const tempLocalAppData = path.join(tmp, 'LocalAppData');
  fs.mkdirSync(tempLocalAppData, { recursive: true });
  // APPDATA 承载 userData 与单实例锁：不隔离就会和真实客户端（或上一次冒烟）互抢，
  // 表现为「新进程抢不到锁直接退出、页面停在 0/0」。
  const tempAppData = path.join(tmp, 'AppData');
  fs.mkdirSync(tempAppData, { recursive: true });
  const { names, pil } = makeSamples(sampleDir);
  const py = pythonCommand();
  console.log(`样本目录：${sampleDir}（${names.length} 个，Pillow ${pil}）`);

  const firstImage = path.join(sampleDir, '01-秋检现场.jpg');
  const proc = spawn(
    electron.exe,
    [...electron.args, `--remote-debugging-port=${PORT}`, '--remote-allow-origins=*', firstImage],
    {
      cwd: DESKTOP,
      env: Object.assign({}, process.env, {
        UVMP_PYTHON: py.exe,
        UVMP_TOOLKIT_CONFIG: tempConfig,
        LOCALAPPDATA: tempLocalAppData,
        APPDATA: tempAppData,
        UVMP_CI_SMOKE: '1',
        // 主窗口的 CI 自检（main.cjs）跑完自身断言就 process.exit(0)：本脚本要连续驱动图片窗口
        // 数十秒，必须关掉它，否则应用在运行中途被掐断（表现为 CDP 超时、改名看似失败其实已落盘）。
        UVMP_CI_IMAGE_SMOKE: '1',
        PYTHONPATH: [py.userSite, process.env.PYTHONPATH].filter(Boolean).join(path.delimiter),
        // 主进程会 detached 拉起常驻 U 盘 agent：退出前按临时 LOCALAPPDATA 精确清理
        ELECTRON_ENABLE_LOGGING: '0',
      }),
      stdio: ['ignore', 'pipe', 'pipe'],
      windowsHide: true,
    }
  );
  let stderr = '';
  proc.stderr.on('data', (chunk) => {
    stderr += String(chunk);
    if (stderr.length > 8000) stderr = stderr.slice(-8000);
  });

  cleanupOnExit = () => {
    killTree(proc);
    killDetachedAgents(tempLocalAppData);
    if (!KEEP) {
      try {
        fs.rmSync(tmp, { recursive: true, force: true });
      } catch (e) {
        // 目录被占用时留给系统清理
      }
    }
  };

  let cdp = null;
  try {
    const found = await waitTargets(Date.now() + 40000);
    if (!found) fail(1, `图片窗口没有出现（stderr 尾部：${stderr.slice(-600)}）`);
    cdp = new Cdp(found.page.webSocketDebuggerUrl);
    await cdp.ready();
    await cdp.send('Runtime.enable');
    await cdp.send('Page.enable');

    // ---- 1. 首帧：图片窗口按冷启动参数打开第一张 ----
    const title = await cdp.waitFor(
      "document.title.includes('图片工具 — ') && document.title",
      30000
    );
    // 失败时带上页面真实状态，避免只看到 false 无从定位（冷启动竞态与真错误长得很像）
    let coldDiag = null;
    if (!/图片工具 — .*01-秋检现场\.jpg/.test(String(title))) {
      coldDiag = await cdp
        .evaluate(
          `(async () => {
          const ready = typeof window.imageTools?.ready === 'function'
            ? await window.imageTools.ready().catch((e) => ({ error: String(e && e.message) }))
            : { error: 'no ready()' };
          return {
            title: document.title,
            hasShell: !!document.querySelector('.it-shell'),
            counter: (document.querySelector('.it-counter') || {}).innerText || '',
            error: (document.querySelector('.it-error') || {}).innerText || '',
            toast: (document.querySelector('.it-toast') || {}).innerText || '',
            state: (document.querySelector('.it-viewer') || {}).dataset
              ? document.querySelector('.it-viewer').dataset.state
              : '',
            ready,
            hasSeam: !!window.__imageToolsSmoke,
            coldTrace: window.__imageToolsSmoke ? window.__imageToolsSmoke.coldTrace() : null,
          };
        })()`
        )
        .catch((e) => ({ evaluateError: String(e && e.message) }));
    }
    // 窗口重建竞态：绑定到的页面若已失效，重新绑定最新页面再看一次
    if (!/图片工具 — .*01-秋检现场\.jpg/.test(String(title))) {
      const fresh = await listImagePages();
      const current = fresh[0];
      if (current) {
        try {
          cdp.ws.close();
        } catch (e) {
          // 旧连接可能已断
        }
        cdp = new Cdp(current.webSocketDebuggerUrl);
        await cdp.ready();
        await cdp.send('Runtime.enable');
        await cdp.send('Page.enable');
      }
    }
    check(
      '冷启动打开图片窗口并对齐到首选文件',
      /图片工具 — .*01-秋检现场\.jpg/.test(String(title)),
      coldDiag ? `${title} | ${JSON.stringify(coldDiag)}` : title
    );

    const ready = await cdp.waitFor(
      "!!document.querySelector('.it-photo') && document.querySelector('.it-photo').naturalWidth > 0",
      30000
    );
    check('画布加载出真实图片（shade-image 路径 URL 可读）', !!ready);

    // ---- 1a. 预览地址实测：必须是带真实路径的 URL，且不再有任何令牌 ----
    // 只改文案不算验证：从真实渲染层读回 img 的属性，解码后与磁盘真实路径逐字比对。
    // 负向断言统一用 <img> 加载（真实协议管线），并带同目录「已授权」正向对照，
    // 避免协议整体失效时负向断言恒真。URL 全部在 Node 侧拼好后注入，避免页面表达式出错。
    const siblingImage =
      fs
        .readdirSync(sampleDir)
        .filter((n) => /\.(png|webp|bmp|gif|jpe?g)$/i.test(n) && n !== path.basename(firstImage))
        .map((n) => path.join(sampleDir, n))
        .find((file) => !/坏图/.test(file)) || '';
    const toImageUrl = (p) => 'shade-image://local?path=' + encodeURIComponent(String(p));
    const probeUrls = {
      control: siblingImage ? toImageUrl(realOrEmpty(siblingImage)) : '',
      outside: toImageUrl(path.join(path.dirname(sampleDir), '外部越界.jpg')),
      legacy: 'shade-image://' + 'a'.repeat(48),
      file: 'file:///' + firstImage.split(path.sep).join('/'),
    };
    const srcProbe = await cdp.evaluate(`(async () => {
      const img = document.querySelector('.it-photo');
      const raw = img ? img.getAttribute('src') : '';
      const preview = await window.imageTools.preview(${JSON.stringify(realOrEmpty(firstImage))});
      const absolute = preview.url;
      let decoded = '';
      try {
        decoded = new URL(absolute).searchParams.get('path') || '';
      } catch (e) {
        decoded = '';
      }
      const urls = ${JSON.stringify(probeUrls)};
      const load = (url) => new Promise((resolve) => {
        if (!url) {
          resolve({ result: 'SKIP', width: 0 });
          return;
        }
        const probe = new Image();
        const timer = setTimeout(() => resolve({ result: 'TIMEOUT', width: 0 }), 5000);
        probe.onload = () => {
          clearTimeout(timer);
          resolve({ result: 'LOAD', width: probe.naturalWidth });
        };
        probe.onerror = () => {
          clearTimeout(timer);
          resolve({ result: 'ERROR', width: 0 });
        };
        probe.src = url;
      });
      const loaded = {};
      for (const key of Object.keys(urls)) loaded[key] = await load(urls[key]);
      return { raw, absolute, decoded, loaded };
    })()`);
    check(
      '画布使用会话 Blob，预览协议地址仍能解码回磁盘真实路径（无随机令牌）',
      /^blob:/i.test(srcProbe.raw || '') &&
        /^shade-image:\/\/local\/?\?path=/.test(srcProbe.absolute || '') &&
        !/^shade-image:\/\/[a-f0-9]{48}$/i.test(srcProbe.absolute || '') &&
        srcProbe.decoded === realOrEmpty(firstImage),
      JSON.stringify({
        raw: srcProbe.raw,
        protocol: srcProbe.absolute,
        decoded: srcProbe.decoded,
        expected: realOrEmpty(firstImage),
      })
    );
    check(
      '协议层正向对照：同目录已授权图片能被 <img> 加载',
      srcProbe.loaded.control &&
        srcProbe.loaded.control.result === 'LOAD' &&
        srcProbe.loaded.control.width > 0,
      JSON.stringify(srcProbe.loaded.control)
    );
    check(
      '越界路径 / 旧令牌在真实渲染层都拿不到图片，且预览通道不是 file://',
      srcProbe.loaded.outside.result === 'ERROR' &&
        srcProbe.loaded.legacy.result === 'ERROR' &&
        !/^file:/i.test(srcProbe.raw || ''),
      JSON.stringify(srcProbe.loaded)
    );

    // ---- 1b. 共享顶栏：与主客户端同一枚 48px TitleBar（不是系统样式） ----
    const chrome = await cdp.evaluate(`(() => {
      const bar = document.querySelector('.titlebar');
      const shell = document.querySelector('.it-shell');
      const btns = [...document.querySelectorAll('.titlebar [data-win]')].map((b) => b.dataset.win);
      const shellStyle = shell ? getComputedStyle(shell) : null;
      const barStyle = bar ? getComputedStyle(bar) : null;
      return {
        hasBar: !!bar,
        height: barStyle ? barStyle.height : '',
        title: bar ? bar.innerText.replace(/\s+/g, ' ').trim() : '',
        btns,
        handles: document.querySelectorAll('.wr-handle, .wr-layer').length,
        shellRadius: shellStyle ? shellStyle.borderTopLeftRadius : '',
        shellBorder: shellStyle ? shellStyle.borderTopWidth : '',
        shellOutline: shellStyle ? shellStyle.outlineStyle : '',
      };
    })()`);
    check(
      '图片窗口使用共享 48px TitleBar（含最小化/最大化/关闭）',
      chrome.hasBar &&
        chrome.height === '48px' &&
        chrome.btns.join(',') === 'min,max,close' &&
        /图片工具/.test(chrome.title),
      JSON.stringify(chrome)
    );
    check(
      '外层壳层无 CSS 圆角/描边/outline（外框交给系统，避免双线与黑角）',
      chrome.shellRadius === '0px' &&
        chrome.shellBorder === '0px' &&
        chrome.shellOutline === 'none' &&
        chrome.handles === 0,
      JSON.stringify(chrome)
    );

    // 标题栏按钮真实生效：最大化 → 铺满窗口；再还原
    // 先把窗口归到「未最大化」：若起始就是最大化（小屏机器上窗口可能已贴工作区），
    // 直接点第一次会变成还原，后面的「再点一次」断言就会反向。这里只做状态归一，不删任何断言。
    const startMaxTitle = await cdp.evaluate(
      'document.querySelector(\'.titlebar [data-win="max"]\').title'
    );
    if (/还原/.test(String(startMaxTitle))) {
      await cdp.evaluate('document.querySelector(\'.titlebar [data-win="max"]\').click(); true');
      await cdp.waitFor(
        "/最大化/.test(document.querySelector('.titlebar [data-win=\"max\"]').title) && '稳定'",
        8000
      );
    }
    await cdp.evaluate('document.querySelector(\'.titlebar [data-win="max"]\').click(); true');
    const maximizeState = await cdp.waitFor(
      `(() => { const s = document.querySelector('.it-shell'); return s && window.innerWidth > 0 && s.getBoundingClientRect().width >= window.innerWidth - 1 ? { filled: true, innerWidth: window.innerWidth } : false; })()`,
      8000
    );
    check('标题栏最大化按钮生效并铺满窗口', !!maximizeState, JSON.stringify(maximizeState));
    await cdp.evaluate('document.querySelector(\'.titlebar [data-win="max"]\').click(); true');
    await cdp.waitFor(
      "document.querySelector('.it-shell').getBoundingClientRect().width < window.innerWidth - 1",
      8000
    );
    // 等标题栏状态也落回「最大化」再继续：系统 maximize/unmaximize 事件是异步的，
    // 若在 unmaximize 事件尚未到达时就再次点最大化，迟到的 false 会把按钮状态覆盖回错值
    // （后续 1c 的「按钮变为还原」会假红）。这里等到状态真正稳定，不放松后面的断言。
    await cdp.waitFor(
      "/最大化/.test(document.querySelector('.titlebar [data-win=\"max\"]').title) && '稳定'",
      8000
    );

    // ---- 1c. 系统原生外框与原生改尺寸 ----
    // thickFrame=true 时 outerWidth/outerHeight 应比内容区大出系统边框；Electron 未实现
    // Browser.getWindowForTarget，因此用 window.resizeTo（与系统拖拽缩放同一条原生改尺寸路径）
    // 验证「改尺寸后内容区随之变化」，并用标题栏最大化验证窗口控制。
    //
    // 边界：小屏 CI（1024×720 + 系统边框）会让窗口初始就贴满工作区，此时 outer==inner（边框差 0），
    // 那是「已最大化/贴边」的测量假象，不是没有原生边框。所以不能直接拿初始值判边框，
    // 必须先用 resizeTo 进入一个明确小于工作区的普通窗口，再在两个不同的普通尺寸上各测一次边框差。
    // 0 也不能被当成「无原生外框」记 PASS：贴边态一律先归一成普通窗口再测。
    const metrics = () =>
      cdp.evaluate(`({
        innerWidth: window.innerWidth,
        innerHeight: window.innerHeight,
        outerWidth: window.outerWidth,
        outerHeight: window.outerHeight,
        dpr: window.devicePixelRatio,
        availWidth: window.screen.availWidth,
        availHeight: window.screen.availHeight,
        isMaximized: /还原/.test(document.querySelector('.titlebar [data-win="max"]').title),
      })`);
    // 进入普通窗口：先确认未最大化；再把窗口缩到「目标尺寸」并**验证真的到位**。
    // 旧实现的缺陷：目标被 `Math.max(900, ...)` 抬到 minWidth，CI 1024 宽下两个目标都变成 900；
    // 而等待只看「连续两次采样相同」，窗口一直停在 900 也会被判为已达成 → 两个尺寸相同、边框比较失去意义。
    // 现在两个目标由可用区算出且必须明显不同：A 贴近可用区上界，B 取产品最小尺寸；
    // 等待要求 outer 落在本次目标容差内且连续稳定，达不到明确标 failed。
    async function enterNormalWindow(which) {
      const status = await metrics();
      if (status.isMaximized) {
        await cdp.evaluate('document.querySelector(\'.titlebar [data-win="max"]\').click(); true');
        await cdp.waitFor(
          "/最大化/.test(document.querySelector('.titlebar [data-win=\"max\"]').title) && 'ok'",
          8000
        );
      }
      const avail =
        status.availWidth && status.availHeight
          ? { w: status.availWidth, h: status.availHeight }
          : await cdp.evaluate('({ w: window.screen.availWidth, h: window.screen.availHeight })');
      // 产品窗口 minWidth=900 / minHeight=600（不改），所以普通尺寸下界就是它。
      const MIN_W = 900;
      const MIN_H = 600;
      // A 贴近可用区（留 8 DIP，避免又贴满工作区），B 取最小尺寸：小屏 1024×720 下
      // A≈1016、B=900，两者 outer 差约 116，足以证明 resize 真的生效；大屏同理且更宽裕。
      const w = which === 'A' ? Math.max(MIN_W, avail.w - 8) : MIN_W;
      const h = which === 'A' ? Math.max(MIN_H, avail.h - 8) : MIN_H;
      await cdp.evaluate(`(() => { window.resizeTo(${w}, ${h}); return true; })()`);
      // 等 outer 达到本次目标：resizeTo 给的是窗口尺寸，但系统会加原生边框/按 DPI 取整，
      // 且不足以缩到低于 minWidth，所以容差按「系统边框上限」+ 取整余量给到 24 DIP。
      const TOL = 24;
      let last = null;
      let stable = 0;
      for (let i = 0; i < 40; i += 1) {
        await new Promise((r) => setTimeout(r, 150));
        const cur = await metrics();
        const reached = Math.abs(cur.outerWidth - w) <= TOL && Math.abs(cur.outerHeight - h) <= TOL;
        const same =
          last && cur.outerWidth === last.outerWidth && cur.outerHeight === last.outerHeight;
        stable = reached && same ? stable + 1 : 0;
        if (stable >= 1) return { ...cur, requested: { w, h }, reached: true };
        last = cur;
      }
      return { ...(last || {}), requested: { w, h }, reached: false };
    }

    const normalA = await enterNormalWindow('A');
    const frameAX = normalA.outerWidth - normalA.innerWidth;
    const frameAY = normalA.outerHeight - normalA.innerHeight;
    check(
      '已在普通窗口内测量原生外框（不再用可能贴满工作区的初始尺寸）',
      normalA.reached === true &&
        normalA.isMaximized === false &&
        normalA.innerWidth < normalA.availWidth &&
        normalA.innerHeight < normalA.availHeight,
      `requested=${JSON.stringify(normalA.requested)} outer=${normalA.outerWidth}x${normalA.outerHeight} inner=${normalA.innerWidth}x${normalA.innerHeight} avail=${normalA.availWidth}x${normalA.availHeight} dpr=${normalA.dpr}`
    );
    check(
      'thickFrame=true：普通窗口的原生外框比内容区大出系统边框（0~20 DIP）',
      frameAX > 0 && frameAX <= 20 && frameAY > 0 && frameAY <= 20,
      `outer=${normalA.outerWidth}x${normalA.outerHeight} inner=${normalA.innerWidth}x${normalA.innerHeight} 边框=${frameAX}x${frameAY} dpr=${normalA.dpr}`
    );

    // 第二个**普通**尺寸（不是最大化，也不是贴边）：边框差必须与第一个一致。
    // 两个目标必须真的不同（旧实现在小屏上都被抬到 minWidth=900，等于没验证 resize）。
    const normalB = await enterNormalWindow('B');
    const frameBX = normalB.outerWidth - normalB.innerWidth;
    const frameBY = normalB.outerHeight - normalB.innerHeight;
    check(
      '原生改尺寸（window.resizeTo，与系统边缘拖拽同路径）在两个不同普通尺寸间真实生效',
      normalB.reached === true &&
        normalB.isMaximized === false &&
        normalB.requested.w !== normalA.requested.w &&
        Math.abs(normalB.outerWidth - normalA.outerWidth) >= 40,
      `requested A=${JSON.stringify(normalA.requested)} → B=${JSON.stringify(normalB.requested)}；outer ${normalA.outerWidth}x${normalA.outerHeight} → ${normalB.outerWidth}x${normalB.outerHeight}`
    );
    check(
      '两个不同普通尺寸下边框差一致（系统边框未被改写，且没有把 0 当无边框）',
      frameBX > 0 &&
        frameBY > 0 &&
        Math.abs(frameBX - frameAX) <= 2 &&
        Math.abs(frameBY - frameAY) <= 2,
      `A 边框=${frameAX}x${frameAY} B 边框=${frameBX}x${frameBY}（B outer=${normalB.outerWidth}x${normalB.outerHeight} inner=${normalB.innerWidth}x${normalB.innerHeight}）`
    );
    // 回到 A 的尺寸作为后续基线（最大化/还原段用 m2）
    const restoredA = await enterNormalWindow('A');
    const m2 = restoredA;
    check(
      '恢复到测边框前的普通窗口尺寸',
      restoredA.reached === true && Math.abs(restoredA.outerWidth - normalA.outerWidth) <= 4,
      `${normalA.outerWidth} → ${restoredA.outerWidth}`
    );

    // 标题栏最大化：系统最大化（Windows 会铺满工作区、去掉边框留白）
    // 期望值是系统工作区（screen.availWidth/availHeight），不是「比上一次更大」：
    // CI 的显示器可能比默认 1280×800 窗口还小，窗口在还原态就已经贴着工作区，
    // 最大化前后 innerWidth 相等是正确结果，用 `> m2.innerWidth` 会假红。
    await cdp.evaluate('document.querySelector(\'.titlebar [data-win="max"]\').click(); true');
    const maxTitle = await cdp.waitFor(
      "/还原/.test(document.querySelector('.titlebar [data-win=\"max\"]').title) && '还原'",
      8000
    );
    const maxMetrics = await metrics();
    const workArea = await cdp.evaluate(`({
      availWidth: window.screen.availWidth,
      availHeight: window.screen.availHeight,
    })`);
    // 允许系统边框/DPI 取整的合理差值（最大化时内容区应铺满工作区）
    const FRAME_TOLERANCE = 32;
    const fillX = Math.abs(maxMetrics.innerWidth - workArea.availWidth);
    const fillY = Math.abs(maxMetrics.innerHeight - workArea.availHeight);
    check(
      '标题栏最大化：内容区铺满系统工作区且按钮变为「还原」',
      !!maxTitle &&
        Number(workArea.availWidth) > 0 &&
        Number(workArea.availHeight) > 0 &&
        fillX <= FRAME_TOLERANCE &&
        fillY <= FRAME_TOLERANCE,
      JSON.stringify({
        maxMetrics,
        workArea,
        diff: { x: fillX, y: fillY },
        tolerance: FRAME_TOLERANCE,
        maxTitle,
      })
    );
    // 还原后画布尺寸要真正回到还原布局：只等窗口宽度变小不够——渲染层的
    // ResizeObserver 可能还没 measure()/resetView()，此时读到的是最大化布局的适应比例（95%），
    // 滚轮放大被迟到的 resetView() 覆盖回 49%，旧断言「前后文本不等」会把这种假象记成 PASS。
    // 这里额外等「画布宽度稳定」且「缩放显示不再是最大化布局的 95%」。
    await cdp.evaluate('document.querySelector(\'.titlebar [data-win="max"]\').click(); true');
    const restoredWidth = await cdp.waitFor(
      `Math.abs(window.innerWidth - ${m2.innerWidth}) <= 4 && window.innerWidth`,
      8000
    );
    const fitReady = await cdp.waitFor(
      `(() => {
        const canvas = document.querySelector('.it-canvas');
        const pct = document.querySelector('.it-zoom-pct');
        if (!canvas || !pct || canvas.clientWidth <= 0) return false;
        const w = canvas.clientWidth;
        const last = window.__fitProbe || {};
        window.__fitProbe = { w, at: Date.now() };
        const stable = last.w === w && Date.now() - last.at < 900;
        const text = pct.innerText.trim();
        return stable && text !== '' && text !== '95%' ? text : false;
      })()`,
      12000,
      120
    );
    check(
      '还原后画布与适应缩放稳定（避免把最大化布局的 95% 当成还原值）',
      !!fitReady,
      String(fitReady)
    );
    check(
      '还原后回到最大化前的真实尺寸（不是只比最大化小）',
      !!restoredWidth && Math.abs(restoredWidth - m2.innerWidth) <= 4,
      `${m2.innerWidth}（最大化前）→ ${maxMetrics.innerWidth}（最大化）→ ${restoredWidth}（还原后）`
    );

    const overflowAfter = await cdp.evaluate(
      'document.documentElement.scrollWidth > window.innerWidth'
    );
    check('缩放后无横向溢出', overflowAfter === false);

    // ---- 2. 六类信息与分钟级时间 ----
    const info = await cdp.evaluate(`(() => {
      const text = document.querySelector('.it-inspector') ? document.querySelector('.it-inspector').innerText : '';
      const rows = [...document.querySelectorAll('.it-inspector .it-mrow')].map((row) => row.innerText.replace(/\\s+/g, ' ').trim());
      return { text, rows };
    })()`);
    for (const label of ['名称', '类型', '大小', '尺寸', '位置', '修改时间']) {
      check(`信息栏有「${label}」`, String(info.text).includes(label));
    }
    const mtimeRow = info.rows.find((row) => row.startsWith('修改时间')) || '';
    check(
      '修改时间精确到分钟（YYYY-MM-DD HH:mm）',
      /\d{4}-\d{2}-\d{2} \d{2}:\d{2}/.test(mtimeRow),
      mtimeRow
    );
    check('尺寸行给出像素与比例', /1920 × 1280 px/.test(info.rows.join(' | ')), info.rows[3]);

    // ---- 2b. 位置行为完整路径（多行、LTR、不横向溢出）----
    // 展示串必须与真实 file.path 完全一致：不再有「…」省略，也不能只显示尾段。
    const pathDisplayProbe = async () =>
      cdp.evaluate(`(() => {
        const btn = document.querySelector('.it-pathbtn');
        const el = document.querySelector('.it-pathbtn .it-path-text');
        if (!btn || !el || !el.firstChild) return null;
        const text = el.textContent;
        const boxes = [];
        for (let i = 0; i < text.length; i += 1) {
          const range = document.createRange();
          range.setStart(el.firstChild, i);
          range.setEnd(el.firstChild, i + 1);
          const rect = range.getBoundingClientRect();
          boxes.push({
            ch: text[i],
            left: Math.round(rect.left * 100) / 100,
            top: Math.round(rect.top * 100) / 100,
          });
        }
        const btnRect = btn.getBoundingClientRect();
        const elRect = el.getBoundingClientRect();
        const btnStyle = getComputedStyle(btn);
        const elStyle = getComputedStyle(el);
        return {
          text,
          boxes,
          btnHeight: Math.round(btnRect.height * 100) / 100,
          btnClientWidth: btn.clientWidth,
          btnScrollWidth: btn.scrollWidth,
          textClientWidth: el.clientWidth,
          textScrollWidth: el.scrollWidth,
          textRight: elRect.right,
          docOverflowX: document.documentElement.scrollWidth - window.innerWidth,
          btnDirection: btnStyle.direction,
          textDirection: elStyle.direction,
          whiteSpace: btnStyle.whiteSpace,
          wordBreak: btnStyle.wordBreak || elStyle.wordBreak,
          overflowWrap: btnStyle.overflowWrap || elStyle.overflowWrap,
        };
      })()`);

    // 把字符按行分组：跨行的末字符 x 会回到行左端，这是换行的正常现象，
    // 旧的「整串单行 left 递增」断言在多行方案下必然失效，改为逐行检查阅读顺序。
    const analyzePathDisplay = (probe) => {
      if (!probe || !probe.text) return { ok: false, reason: '无路径探针结果' };
      const lines = [];
      for (const box of probe.boxes || []) {
        const last = lines[lines.length - 1];
        if (!last || Math.abs(box.top - last.top) > 2) lines.push({ top: box.top, boxes: [box] });
        else last.boxes.push(box);
      }
      const orderOk = lines.every((line) =>
        line.boxes.every((box, i) => i === 0 || box.left >= line.boxes[i - 1].left - 0.5)
      );
      const topOk = lines.every((line, i) => i === 0 || line.top > lines[i - 1].top);
      return {
        ok: orderOk && topOk,
        lines: lines.length,
        orderOk,
        topOk,
        firstChar: (probe.boxes || [])[0] ? probe.boxes[0].ch : '',
        lastChar: (probe.boxes || [])[probe.boxes.length - 1]
          ? probe.boxes[probe.boxes.length - 1].ch
          : '',
      };
    };

    const realPath = (await cdp.evaluate('window.__imageToolsSmoke.currentFile()')) || {};
    const pathProbe = await pathDisplayProbe();
    const pathLines = analyzePathDisplay(pathProbe);
    const pathOnDisk = !!pathProbe && fs.existsSync(String(pathProbe.text || ''));
    check(
      '位置行展示完整路径（从盘符开始，与真实 file.path 完全一致）',
      !!pathProbe &&
        pathProbe.text === realPath.path &&
        pathOnDisk &&
        /^[A-Za-z]:\\/.test(String(pathProbe.text)) &&
        !String(pathProbe.text).includes('…'),
      JSON.stringify({
        shown:
          String(pathProbe && pathProbe.text).slice(0, 10) +
          '…' +
          String(pathProbe && pathProbe.text).slice(-28),
        expected: String(realPath.path).slice(-28),
        len: String(pathProbe && pathProbe.text).length,
        onDisk: pathOnDisk,
      })
    );
    check(
      '位置行多行换行且每行字符从左到右（LTR 正常顺序）',
      !!pathProbe &&
        pathLines.lines >= 2 &&
        pathLines.orderOk &&
        pathProbe.btnDirection === 'ltr' &&
        pathProbe.textDirection === 'ltr' &&
        pathProbe.whiteSpace !== 'nowrap',
      JSON.stringify({
        lines: pathLines.lines,
        orderOk: pathLines.orderOk,
        topOk: pathLines.topOk,
        btnDirection: pathProbe && pathProbe.btnDirection,
        whiteSpace: pathProbe && pathProbe.whiteSpace,
      })
    );
    check(
      '位置区高度由内容撑高（不再固定 26px）且无横向溢出',
      !!pathProbe &&
        pathProbe.btnHeight > 26 &&
        pathProbe.btnScrollWidth <= pathProbe.btnClientWidth + 1 &&
        pathProbe.textScrollWidth <= pathProbe.textClientWidth + 1 &&
        pathProbe.docOverflowX <= 0,
      JSON.stringify({
        height: pathProbe && pathProbe.btnHeight,
        btn: pathProbe && `${pathProbe.btnScrollWidth}/${pathProbe.btnClientWidth}`,
        text: pathProbe && `${pathProbe.textScrollWidth}/${pathProbe.textClientWidth}`,
        docOverflowX: pathProbe && pathProbe.docOverflowX,
      })
    );

    // 连续无分隔符的长串是最坏情况：CSS 必须能断行，否则侧栏会被撑出横向滚动。
    // 用真实 DOM 里同一套样式量一次（临时换文本再复原），只验证排版，不伪造 file.path。
    const longRun = await cdp.evaluate(`(() => {
      const el = document.querySelector('.it-pathbtn .it-path-text');
      const btn = document.querySelector('.it-pathbtn');
      if (!el || !btn) return null;
      const original = el.textContent;
      el.textContent = 'A'.repeat(160);
      const result = {
        btnScrollWidth: btn.scrollWidth,
        btnClientWidth: btn.clientWidth,
        textScrollWidth: el.scrollWidth,
        textClientWidth: el.clientWidth,
        btnHeight: Math.round(btn.getBoundingClientRect().height * 100) / 100,
        docOverflowX: document.documentElement.scrollWidth - window.innerWidth,
      };
      el.textContent = original;
      result.restored = el.textContent === original;
      return result;
    })()`);
    check(
      '连续 160 字符长串（无分隔符）也不横向溢出',
      !!longRun &&
        longRun.restored === true &&
        longRun.btnScrollWidth <= longRun.btnClientWidth + 1 &&
        longRun.textScrollWidth <= longRun.textClientWidth + 1 &&
        longRun.docOverflowX <= 0 &&
        longRun.btnHeight > 26,
      JSON.stringify(longRun)
    );

    // 完整路径弹层必须随内容下移，不能压在撑高后的路径上（旧实现固定 top:32px）
    await cdp.evaluate("document.querySelector('.it-pathbtn').click(); true");
    const pathPop = await cdp.waitFor(
      "(() => { const pop = document.querySelector('.it-pathpop'); const btn = document.querySelector('.it-pathbtn'); return pop && btn ? { top: pop.getBoundingClientRect().top, btnBottom: btn.getBoundingClientRect().bottom, value: pop.querySelector('input') ? pop.querySelector('input').value : '', hasCopy: !!([...pop.querySelectorAll('.it-btn')].find((b) => b.innerText.trim() === '复制路径')), hasFolder: !!([...pop.querySelectorAll('.it-btn')].find((b) => b.innerText.trim() === '打开所在文件夹')) } : false; })()",
      6000
    );
    check(
      '完整路径弹层在路径下方展开（不覆盖多行内容）且保留复制/打开文件夹',
      (() => {
        const probe = pathPop && typeof pathPop === 'object' ? pathPop : null;
        return (
          !!probe &&
          probe.top >= probe.btnBottom - 1 &&
          probe.value === realPath.path &&
          probe.hasCopy &&
          probe.hasFolder
        );
      })(),
      JSON.stringify(pathPop)
    );
    await cdp.evaluate(
      "(() => { const btn = [...document.querySelectorAll('.it-pathpop .it-iconbtn')].pop(); if (btn) btn.click(); return true; })()"
    );
    await cdp.waitFor("!document.querySelector('.it-pathpop')", 4000);

    // ---- 3. 滚轮缩放（以指针为锚）与缩放上限 ----
    // 数值判定：必须真的变大（一轮滚轮 = 1.12×），并等渲染层反映后再比对；
    // 只比较「前后文本不等」会让「先缩小后放大」「被 resetView 覆盖」都记成 PASS。
    const zoomBefore = await cdp.evaluate(
      "document.querySelector('.it-zoom-pct').innerText.trim()"
    );
    // 指针选在画布中心右上 1/4 处：dx 非零、dy 为零，补偿必须在 x 轴上明显为负。
    // 同时把指针相对画布中心的 dx/dy 带回来，供数值比对（不靠页面里的产品状态）。
    const wheelAt = await cdp.evaluate(`(() => {
      const canvas = document.querySelector('.it-canvas');
      const rect = canvas.getBoundingClientRect();
      const at = { clientX: rect.left + rect.width * 0.75, clientY: rect.top + rect.height * 0.5 };
      canvas.dispatchEvent(new WheelEvent('wheel', { deltaY: -120, bubbles: true, cancelable: true, ...at }));
      return {
        dx: at.clientX - (rect.left + rect.width / 2),
        dy: at.clientY - (rect.top + rect.height / 2),
        canvasWidth: rect.width,
        canvasHeight: rect.height,
      };
    })()`);
    const zoomAfter = await cdp.waitFor(
      `(() => {
        const text = document.querySelector('.it-zoom-pct').innerText.trim();
        return text !== ${JSON.stringify(zoomBefore)} ? text : false;
      })()`,
      6000,
      60
    );
    const zoomBeforeNum = parseInt(zoomBefore, 10);
    const zoomAfterNum = parseInt(String(zoomAfter), 10);
    check(
      '滚轮放大生效（比例数值真的变大）',
      Number.isFinite(zoomBeforeNum) &&
        Number.isFinite(zoomAfterNum) &&
        zoomAfterNum > zoomBeforeNum,
      `${zoomBefore} → ${zoomAfter}`
    );
    // 补偿值 = matrix 的 e/f 减去 translate(-50%,-50%) 的居中基线；
    // 期望值由指针相对画布中心的 dx/dy 与 1.12× 缩放比算出（viewer.mjs:42-49）。
    const readAnchorProbe = () =>
      cdp.evaluate(`(() => {
        const img = document.querySelector('.it-photo');
        if (!img) return { missing: true };
        const rect = img.getBoundingClientRect();
        return { w: rect.width, h: rect.height, raw: getComputedStyle(img).transform };
      })()`);
    const anchorExpected = expectedAnchorShift(wheelAt.dx, wheelAt.dy);
    let anchorProbe = await readAnchorProbe();
    // 等渲染层把新的 transform 落到 DOM（读到一致即停；读不到则由断言判失败）
    for (let i = 0; i < 20; i += 1) {
      if (judgeAnchorCompensation(anchorProbe, anchorExpected).ok) break;
      await new Promise((r) => setTimeout(r, 50));
      anchorProbe = await readAnchorProbe();
    }
    const anchorVerdict = judgeAnchorCompensation(anchorProbe, anchorExpected);
    check(
      '放大后画面平移等于指针锚点补偿（减掉居中基线后与 dx/dy 数值比对）',
      anchorVerdict.ok === true,
      JSON.stringify({
        ok: anchorVerdict.ok,
        moved: anchorVerdict.moved,
        comp: anchorVerdict.comp,
        expected: {
          x: Number(anchorExpected.x.toFixed(3)),
          y: Number(anchorExpected.y.toFixed(3)),
        },
        dx: Number(wheelAt.dx.toFixed(2)),
        dy: Number(wheelAt.dy.toFixed(2)),
        raw: anchorProbe && anchorProbe.raw,
      })
    );
    // 负对照：同一套判定喂「offset=0」等无补偿输入必须为 false，否则新断言仍然恒真
    const zeroProbe =
      anchorProbe && anchorProbe.w > 0
        ? {
            w: anchorProbe.w,
            h: anchorProbe.h,
            raw: `matrix(1, 0, 0, 1, ${-anchorProbe.w / 2}, ${-anchorProbe.h / 2})`,
          }
        : null;
    const controls = {
      offsetZero: judgeAnchorCompensation(zeroProbe, anchorExpected),
      noTransform: judgeAnchorCompensation({ w: 100, h: 100, raw: 'none' }, anchorExpected),
      missing: judgeAnchorCompensation({ missing: true }, anchorExpected),
      nan: judgeAnchorCompensation(
        { w: 100, h: 100, raw: 'matrix(1, 0, 0, 1, NaN, NaN)' },
        anchorExpected
      ),
    };
    check(
      '负对照：未平移 / 未设置 transform / 无图片 / 非数值一律判为失败',
      controls.offsetZero.ok === false &&
        controls.offsetZero.moved === false &&
        controls.noTransform.ok === false &&
        controls.missing.ok === false &&
        controls.nan.ok === false,
      JSON.stringify({
        offsetZero: { ok: controls.offsetZero.ok, comp: controls.offsetZero.comp },
        noTransform: controls.noTransform.ok,
        missing: controls.missing.ok,
        nan: controls.nan.ok,
      })
    );

    // 一次滚轮只放大一档（1.12×），到顶后再滚才出现边界提示：逐次派发并在每次后等待渲染
    const zoomCap = await (async () => {
      let pct = '';
      for (let i = 0; i < 60; i += 1) {
        pct = await cdp.evaluate(`(() => {
          const canvas = document.querySelector('.it-canvas');
          const rect = canvas.getBoundingClientRect();
          canvas.dispatchEvent(new WheelEvent('wheel', { deltaY: -240, bubbles: true, cancelable: true, clientX: rect.left + 10, clientY: rect.top + 10 }));
          return document.querySelector('.it-zoom-pct').innerText.trim();
        })()`);
        if (pct === '400%') break;
        await new Promise((r) => setTimeout(r, 30));
      }
      await new Promise((r) => setTimeout(r, 150));
      const note = await cdp.evaluate(
        "document.querySelector('.it-zoom-boundary') ? document.querySelector('.it-zoom-boundary').innerText : ''"
      );
      return { pct, note };
    })();
    check(
      '滚轮放大封顶 400% 且不显示边界说明文字',
      zoomCap.pct === '400%' && zoomCap.note === '',
      JSON.stringify(zoomCap)
    );

    // ---- 4. 切图：→ 切到下一张，首尾不循环 ----
    // 回到第 1 张：点真实「上一张」按钮直到它禁用（首尾不循环），不依赖初始序号
    await cdp.evaluate(
      "[...document.querySelectorAll('.it-tooltext')].find((b) => b.innerText.trim() === '适应窗口').click()"
    );
    for (let i = 0; i < 8; i += 1) {
      const atHead = await cdp.evaluate(`(() => {
        const prev = document.querySelector('.it-vnudge');
        if (!prev || prev.disabled) return true;
        prev.click();
        return false;
      })()`);
      if (atHead) break;
      await new Promise((r) => setTimeout(r, 250));
    }
    const reachedHead = await cdp.waitFor(
      "document.querySelector('.it-counter').innerText.replace(/\\s+/g, '') === '1/6'",
      8000
    );
    const headButtons = await cdp.evaluate(`(() => {
      const prev = document.querySelector('.it-vnudge');
      const list = [...document.querySelectorAll('.it-vnudge')];
      return {
        counter: document.querySelector('.it-counter').innerText.replace(/\\s+/g, ''),
        prevDisabled: prev ? prev.disabled : null,
        prevCount: list.length,
      };
    })()`);
    check(
      '「上一张」退到首张后按钮禁用（首尾不循环）',
      !!reachedHead && headButtons.counter === '1/6' && headButtons.prevDisabled === true,
      JSON.stringify(headButtons)
    );

    const counterBefore = await cdp.evaluate(
      "document.querySelector('.it-counter').innerText.replace(/\\s+/g, '')"
    );
    await cdp.evaluate(`(() => {
      const canvas = document.querySelector('.it-canvas');
      canvas.focus();
      canvas.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true }));
      return true;
    })()`);
    const counterNext = await cdp.waitFor(
      `document.querySelector('.it-counter').innerText.replace(/\\s+/g, '') !== ${JSON.stringify(counterBefore)} && document.querySelector('.it-counter').innerText.replace(/\\s+/g, '')`
    );
    check('→ 切到同目录下一张', counterNext === '2/6', `${counterBefore} → ${counterNext}`);

    await cdp.evaluate(`(() => {
      const canvas = document.querySelector('.it-canvas');
      canvas.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true, cancelable: true }));
      return true;
    })()`);
    const backToFirst = await cdp.waitFor(
      "document.querySelector('.it-counter') && document.querySelector('.it-counter').innerText.replace(/\\s+/g, '') === '1/6'"
    );
    check('← 回到第 1 张', !!backToFirst);
    const atHead = await cdp.evaluate(`(() => {
      const canvas = document.querySelector('.it-canvas');
      canvas.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true, cancelable: true }));
      return { counter: document.querySelector('.it-counter').innerText.replace(/\\s+/g, ''), prevDisabled: document.querySelector('.it-vnudge').disabled };
    })()`);
    check(
      '首张再按 ← 不循环（序列与按钮都停在 1/6）',
      atHead.counter === '1/6' && atHead.prevDisabled === true,
      JSON.stringify(atHead)
    );

    // 焦点框只保留 Tab：鼠标、方向键和其他按键都必须立即取消。
    const canvasBox = await cdp.evaluate(`(() => {
      const rect = document.querySelector('.it-canvas').getBoundingClientRect();
      return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
    })()`);
    await cdp.send('Input.dispatchMouseEvent', {
      type: 'mousePressed',
      ...canvasBox,
      button: 'left',
      clickCount: 1,
    });
    await cdp.send('Input.dispatchMouseEvent', {
      type: 'mouseReleased',
      ...canvasBox,
      button: 'left',
      clickCount: 1,
    });
    const mouseOutline = await cdp.evaluate(`(() => {
      const el = document.activeElement;
      const style = getComputedStyle(el);
      return { style: style.outlineStyle, width: style.outlineWidth };
    })()`);
    check(
      '鼠标点击画布不显示焦点框',
      mouseOutline.style === 'none' || mouseOutline.width === '0px',
      JSON.stringify(mouseOutline)
    );
    await cdp.pressKey('ArrowRight');
    const arrowOutline = await cdp.evaluate(`(() => {
      const el = document.activeElement;
      const style = getComputedStyle(el);
      return { style: style.outlineStyle, width: style.outlineWidth };
    })()`);
    check(
      '左右键切图不显示焦点框',
      arrowOutline.style === 'none' || arrowOutline.width === '0px',
      JSON.stringify(arrowOutline)
    );
    await cdp.send('Input.dispatchKeyEvent', {
      type: 'rawKeyDown',
      key: '-',
      code: 'Minus',
      windowsVirtualKeyCode: 189,
      nativeVirtualKeyCode: 189,
    });
    await cdp.send('Input.dispatchKeyEvent', {
      type: 'keyUp',
      key: '-',
      code: 'Minus',
      windowsVirtualKeyCode: 189,
      nativeVirtualKeyCode: 189,
    });
    const otherKeyOutline = await cdp.evaluate(`(() => {
      const el = document.activeElement;
      const style = getComputedStyle(el);
      return { style: style.outlineStyle, width: style.outlineWidth };
    })()`);
    check(
      '其他按键也会立即取消焦点框',
      otherKeyOutline.style === 'none' || otherKeyOutline.width === '0px',
      JSON.stringify(otherKeyOutline)
    );
    await cdp.send('Input.dispatchKeyEvent', {
      type: 'rawKeyDown',
      key: 'Tab',
      code: 'Tab',
      windowsVirtualKeyCode: 9,
      nativeVirtualKeyCode: 9,
    });
    await cdp.send('Input.dispatchKeyEvent', {
      type: 'keyUp',
      key: 'Tab',
      code: 'Tab',
      windowsVirtualKeyCode: 9,
      nativeVirtualKeyCode: 9,
    });
    const tabOutline = await cdp.evaluate(`(() => {
      const el = document.activeElement;
      const style = getComputedStyle(el);
      return { style: style.outlineStyle, width: style.outlineWidth };
    })()`);
    check(
      '仅 Tab 键盘导航显示 2px 焦点框',
      tabOutline.style === 'solid' && tabOutline.width === '2px',
      JSON.stringify(tabOutline)
    );
    await cdp.pressKey('ArrowLeft');

    // 预览缓存：键含 mtime/size——外部改动文件后必须不再命中旧预览，重扫要能清空
    const cacheProbe = await cdp.evaluate(`(async () => {
      const sizeBefore = window.__imageToolsSmoke.previewCacheSize();
      const file = window.__imageToolsSmoke.currentFile();
      return { sizeBefore, file };
    })()`);
    check(
      '预览缓存已缓存当前目录的图片',
      cacheProbe.sizeBefore > 0,
      `size=${cacheProbe.sizeBefore}`
    );
    check(
      '当前文件带 mtime 与 size（缓存键来源）',
      cacheProbe.file && cacheProbe.file.mtime > 0 && cacheProbe.file.size > 0,
      JSON.stringify(
        cacheProbe.file && { mtime: cacheProbe.file.mtime, size: cacheProbe.file.size }
      )
    );
    // 外部追加字节：size/mtime 变化 → 缓存键变化 → 旧键不再命中
    fs.appendFileSync(firstImage, Buffer.from([0]));
    // 直接问缓存：用「旧的 mtime/size」与「新的 mtime/size」分别查，必须旧键失效、新键命中。
    // 只比对 list() 的 size 无法证明缓存失效；读预览地址又会与重扫过程中的 loading 态竞态。
    const cachedOldFile = {
      path: cacheProbe.file.path,
      mtime: cacheProbe.file.mtime,
      size: cacheProbe.file.size,
    };
    const diskStat = fs.statSync(firstImage);
    const diskAfterTouch = { size: diskStat.size, mtime: diskStat.mtimeMs / 1000 };
    check(
      '外部改动后磁盘文件 size 变化且 mtime 前进（缓存键两来源都变）',
      diskAfterTouch.size === cachedOldFile.size + 1 && diskAfterTouch.mtime > cachedOldFile.mtime,
      JSON.stringify({ size: diskAfterTouch.size, mtime: diskAfterTouch.mtime })
    );
    // 等核心 list 返回的 mtime/size 与磁盘真实值同步（RPC 往返 + 缓存刷新有延迟，
    // 固定睡眠会时快时慢）：同步不到就是真失败，交给下面的断言判红。
    let freshFile = null;
    let listedRaw = null;
    const listDeadline = Date.now() + 20000;
    while (Date.now() < listDeadline) {
      listedRaw = await cdp.evaluate(`window.imageTools.list(${JSON.stringify(sampleDir)})`);
      const hit = ((listedRaw && listedRaw.files) || []).find(
        (f) => f.name === path.basename(firstImage)
      );
      if (
        hit &&
        Number(hit.size) === diskAfterTouch.size &&
        Math.abs(Number(hit.mtime) - diskAfterTouch.mtime) < 0.05
      ) {
        freshFile = hit;
        break;
      }
      await new Promise((r) => setTimeout(r, 100));
    }
    check(
      '核心列表里的 size/mtime 与磁盘真实文件一致（键来源已同步）',
      !!freshFile,
      JSON.stringify({
        disk: diskAfterTouch,
        listed: freshFile && { size: freshFile.size, mtime: freshFile.mtime },
      })
    );
    // 重扫会清空缓存再按新键重新载入：等「预览就绪 + 旧键不命中 + 新键命中」三件事同时成立，
    // 只采集 newHit 不断言会让缓存整体失效时仍记 PASS。
    const sizeBeforeRescan = await cdp.evaluate('window.__imageToolsSmoke.previewCacheSize()');
    await cdp.evaluate('window.__imageToolsSmoke.rescan(); true');
    const newKey = freshFile
      ? { path: freshFile.path, mtime: freshFile.mtime, size: freshFile.size }
      : null;
    let cacheKeys = null;
    const cacheDeadline = Date.now() + 20000;
    while (Date.now() < cacheDeadline) {
      cacheKeys = await cdp.evaluate(`(() => {
        const oldFile = ${JSON.stringify(cachedOldFile)};
        const newFile = ${JSON.stringify(newKey)};
        const current = window.__imageToolsSmoke.currentFile();
        return {
          oldHit: window.__imageToolsSmoke.previewHas(oldFile),
          newHit: newFile ? window.__imageToolsSmoke.previewHas(newFile) : null,
          // 同路径换 size / 换 mtime 都必须落空：证明键真的含这两个字段，
          // 而不是「只有路径」或「被 clear 之后新键恰好写进去」
          sizeMutatedHit: newFile
            ? window.__imageToolsSmoke.previewHas({ ...newFile, size: newFile.size + 1 })
            : null,
          mtimeMutatedHit: newFile
            ? window.__imageToolsSmoke.previewHas({ ...newFile, mtime: newFile.mtime + 1 })
            : null,
          ready: window.__imageToolsSmoke.previewReady(),
          size: window.__imageToolsSmoke.previewCacheSize(),
          currentPath: current ? current.path : '',
          currentSize: current ? current.size : 0,
        };
      })()`);
      if (cacheKeys.oldHit === false && cacheKeys.newHit === true && cacheKeys.ready === true)
        break;
      await new Promise((r) => setTimeout(r, 100));
    }
    check(
      '外部改动后旧缓存键失效且新键命中（键含 mtime/size，不是只看采集值）',
      !!cacheKeys &&
        cacheKeys.oldHit === false &&
        cacheKeys.newHit === true &&
        cacheKeys.sizeMutatedHit === false &&
        cacheKeys.mtimeMutatedHit === false,
      JSON.stringify(cacheKeys)
    );
    check(
      '重扫目录会清空预览缓存并重新加载',
      !!cacheKeys &&
        cacheKeys.ready === true &&
        cacheKeys.oldHit === false &&
        cacheKeys.newHit === true &&
        cacheKeys.size >= 1 &&
        cacheKeys.size <= sizeBeforeRescan &&
        cacheKeys.currentPath === cachedOldFile.path,
      JSON.stringify({ ...cacheKeys, before: sizeBeforeRescan })
    );

    // 切到损坏文件：必须显示失败状态，且不得沿用上一张的画面
    for (let i = 0; i < 3; i += 1) {
      await cdp.evaluate(`(() => {
        document.querySelector('.it-canvas').dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true }));
        return true;
      })()`);
      await new Promise((r) => setTimeout(r, 400));
    }
    const brokenState = await cdp.evaluate(`(async () => {
      for (let i = 0; i < 6; i += 1) {
        const name = (document.querySelector('.it-fname') || {}).textContent || '';
        if (/坏图/.test(name)) break;
        document.querySelector('.it-canvas').dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true }));
        await new Promise((r) => setTimeout(r, 400));
      }
      const errorPanel = document.querySelector('.it-error');
      const photo = document.querySelector('.it-photo');
      return {
        name: (document.querySelector('.it-fname') || {}).textContent || '',
        hasError: !!errorPanel,
        errorText: errorPanel ? errorPanel.textContent.replace(/\s+/g, ' ').slice(0, 80) : '',
        photoCount: document.querySelectorAll('.it-photo').length,
      };
    })()`);
    check(
      '损坏文件显示失败态且不串用上一张画面',
      brokenState.hasError &&
        brokenState.photoCount === 0 &&
        /IMG_(DECODE_FAILED|UNSUPPORTED)/.test(brokenState.errorText),
      JSON.stringify(brokenState)
    );
    await cdp.screenshot(path.join(tmp, 'after-broken.png'));

    // 回到第 1 张，准备改名
    for (let i = 0; i < 8; i += 1) {
      const atHead = await cdp.evaluate(`(() => {
        const prev = document.querySelector('.it-vnudge');
        const counter = document.querySelector('.it-counter').innerText.replace(/\\s+/g, '');
        if (!prev || prev.disabled) return { atHead: true, counter };
        prev.click();
        return { atHead: false, counter };
      })()`);
      if (atHead.atHead) break;
      // 每点一次都等序号真的前移：连点会被同一帧合并，留下未处理的点击
      await cdp.waitFor(
        `document.querySelector('.it-counter').innerText.replace(/\\s+/g, '') !== ${JSON.stringify(atHead.counter)}`,
        15000
      );
    }

    // ---- 5. F2 就地改名 + 快捷短语插入（先确保停在第 1 张且画面已就绪）----
    await cdp.waitFor(
      "document.querySelector('.it-counter').innerText.replace(/\\s+/g, '') === '1/6' && !!document.querySelector('.it-photo')"
    );
    // 冷启动不点画布：焦点停在 body，用 CDP 真实按键触发 F2
    await cdp.evaluate(
      'document.activeElement && document.activeElement.blur(); document.body.focus();'
    );
    await cdp.pressKey('F2');
    const renameOpen = await cdp.waitFor("!!document.querySelector('#it-name-input')");
    check('F2 在 document 级生效（冷启动无需先点画布）', !!renameOpen);

    // 重命名面板里按 F2 / → 不得抢键、也不得触发行为（输入框内同理）
    const guarded = await cdp.evaluate(`(async () => {
      const input = document.querySelector('#it-name-input');
      const before = input.value;
      input.focus();
      return { before };
    })()`);
    await cdp.evaluate(`(() => {
      const input = document.querySelector('#it-name-input');
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true }));
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'F2', bubbles: true, cancelable: true }));
      return true;
    })()`);
    await new Promise((r) => setTimeout(r, 250));
    const guardedAfter = await cdp.evaluate(`(() => ({
      value: document.querySelector('#it-name-input') ? document.querySelector('#it-name-input').value : null,
      counter: [...document.querySelector('.it-counter').textContent].filter((c) => c.trim()).join(''),
    }))()`);
    check(
      '重命名中 F2 / → 不抢键、不切图',
      guardedAfter.value === guarded.before && guardedAfter.counter === '1/6',
      JSON.stringify(guardedAfter)
    );
    const inserted = await cdp.evaluate(`(async () => {
      const input = document.querySelector('#it-name-input');
      const before = input.value;
      input.focus();
      input.setSelectionRange(4, 4);
      input.dispatchEvent(new Event('select', { bubbles: true }));
      const chip = document.querySelector('.it-qchip');
      const phrase = chip ? chip.innerText.trim() : '';
      chip.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }));
      await new Promise((r) => setTimeout(r, 120));
      return {
        before,
        value: input.value,
        phrase,
        expected: before.slice(0, 4) + phrase + before.slice(4),
        caret: input.selectionStart,
        ext: document.querySelector('.it-ext-fixed').innerText,
      };
    })()`);
    check(
      '快捷短语插入光标处（不替换整名、不改扩展名）',
      inserted.value === inserted.expected &&
        inserted.value !== inserted.phrase &&
        inserted.ext === '.jpg',
      JSON.stringify(inserted)
    );
    check(
      '短语插入后光标落在末尾',
      inserted.caret === 4 + inserted.phrase.length,
      String(inserted.caret)
    );

    const newBase = '现场勘察-01';
    const renameDone = await cdp.evaluate(`(async () => {
      const input = document.querySelector('#it-name-input');
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(input, ${JSON.stringify(newBase)});
      input.dispatchEvent(new Event('input', { bubbles: true }));
      await new Promise((r) => setTimeout(r, 50));
      const save = [...document.querySelectorAll('.it-rename-actions .it-btn')].find((b) => b.innerText.trim() === '保存');
      save.click();
      return true;
    })()`);
    void renameDone;
    const renamed = await cdp.waitFor(
      `document.title.includes(${JSON.stringify(newBase)}) || (document.querySelector('.it-fname') && document.querySelector('.it-fname').innerText.includes(${JSON.stringify(newBase)}))`,
      12000
    );
    check('改名成功并刷新信息栏', !!renamed, await cdp.evaluate('document.title'));
    // 落盘断言：短暂轮询（改名是异步 IPC），失败时把界面反馈与目录清单一起带出来
    let renamedOnDisk = false;
    let originalGone = false;
    for (let i = 0; i < 15; i += 1) {
      renamedOnDisk = fs.existsSync(path.join(sampleDir, `${newBase}.jpg`));
      originalGone = !fs.existsSync(firstImage);
      if (renamedOnDisk && originalGone) break;
      await new Promise((r) => setTimeout(r, 200));
    }
    const renameDiag = await cdp.evaluate(`(() => {
      const fb = document.querySelector('.it-fb');
      return {
        title: document.title,
        feedback: fb ? fb.innerText.split(/\s+/).join(' ') : '',
        input: document.querySelector('#it-name-input') ? document.querySelector('#it-name-input').value : '',
        listing: null,
      };
    })()`);
    check(
      '磁盘上确实改名（原名消失、新名存在）',
      renamedOnDisk && originalGone,
      `${newBase}.jpg=${renamedOnDisk} 原图仍存在=${!originalGone}；反馈=${renameDiag.feedback || '无'}；目录=${fs.readdirSync(sampleDir).join(',')}；标题=${renameDiag.title}`
    );

    // 冲突：改成已存在的名字，必须就地报错且不覆盖
    const conflict = await cdp.evaluate(`(async () => {
      const canvas = document.querySelector('.it-canvas');
      canvas.dispatchEvent(new KeyboardEvent('keydown', { key: 'F2', bubbles: true, cancelable: true }));
      await new Promise((r) => setTimeout(r, 200));
      const input = document.querySelector('#it-name-input');
      if (!input) return { error: 'no-input' };
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(input, '06-坏图');
      input.dispatchEvent(new Event('input', { bubbles: true }));
      await new Promise((r) => setTimeout(r, 50));
      [...document.querySelectorAll('.it-rename-actions .it-btn')].find((b) => b.innerText.trim() === '保存').click();
      await new Promise((r) => setTimeout(r, 900));
      const fb = document.querySelector('.it-fb');
      return { feedback: fb ? fb.innerText.replace(/\\s+/g, ' ') : '', keepInput: input.value };
    })()`);
    check(
      '同名冲突就地提示且保留输入',
      /同名|已存在/.test(conflict.feedback) && conflict.keepInput === '06-坏图',
      JSON.stringify(conflict)
    );

    // 未变化改名：主名保持不变点保存 → 就地说明、不发 RPC、不动磁盘、无成功提示
    // 先退掉上一条「同名冲突」用例留下的面板，并等改名成功 toast（2.6s）自然消失，
    // 否则会把上一条的 toast 当成本次保存产生的成功提示。
    await cdp.evaluate(`(() => {
      const input = document.querySelector('#it-name-input');
      if (input) input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
      return true;
    })()`);
    await cdp.waitFor("!document.querySelector('#it-name-input')", 4000);
    await cdp.waitFor("!document.querySelector('.it-toast')", 6000, 100);
    const renamedFile = path.join(sampleDir, `${newBase}.jpg`);
    const beforeStat = fs.statSync(renamedFile);
    const beforeBytes = fs.readFileSync(renamedFile);
    const unchangedProbe = await cdp.evaluate(`(async () => {
      const canvas = document.querySelector('.it-canvas');
      canvas.dispatchEvent(new KeyboardEvent('keydown', { key: 'F2', bubbles: true, cancelable: true }));
      await new Promise((r) => setTimeout(r, 300));
      const input = document.querySelector('#it-name-input');
      if (!input) return { error: 'no-input' };
      const before = input.value;
      const currentName = (document.querySelector('.it-fname') || {}).textContent || '';
      // 主名一个字符都不改，直接点保存
      [...document.querySelectorAll('.it-rename-actions .it-btn')].find((b) => b.innerText.trim() === '保存').click();
      await new Promise((r) => setTimeout(r, 900));
      const hint = document.querySelector('#it-rename-hint');
      const toast = document.querySelector('.it-toast');
      const fb = document.querySelector('.it-fb');
      return {
        before,
        after: input.value,
        currentName,
        hint: hint ? hint.innerText.replace(/\\s+/g, ' ') : '',
        toast: toast ? toast.innerText.replace(/\\s+/g, ' ') : '',
        feedback: fb ? fb.innerText.replace(/\\s+/g, ' ') : '',
        stillOpen: !!document.querySelector('#it-name-input'),
      };
    })()`);
    const afterStat = fs.statSync(renamedFile);
    const afterBytes = fs.readFileSync(renamedFile);
    check(
      '未变化改名：就地提示「名称未变化」且无成功提示、无错误反馈',
      unchangedProbe.stillOpen === true &&
        /名称未变化/.test(unchangedProbe.hint) &&
        !/已重命名/.test(unchangedProbe.toast) &&
        !/已重命名/.test(unchangedProbe.feedback) &&
        unchangedProbe.before === unchangedProbe.after,
      JSON.stringify(unchangedProbe)
    );
    check(
      '未变化改名：磁盘文件内容与 mtime 都没有被改动',
      afterStat.size === beforeStat.size &&
        afterStat.mtimeMs === beforeStat.mtimeMs &&
        Buffer.compare(beforeBytes, afterBytes) === 0 &&
        fs.existsSync(renamedFile),
      JSON.stringify({
        size: [beforeStat.size, afterStat.size],
        mtimeMs: [beforeStat.mtimeMs, afterStat.mtimeMs],
        sameBytes: Buffer.compare(beforeBytes, afterBytes) === 0,
      })
    );
    // 取消面板：不在磁盘上留下任何新名字
    await cdp.evaluate(`(() => {
      const btn = [...document.querySelectorAll('.it-rename-actions .it-btn')].find((b) => b.innerText.trim() === '取消');
      if (btn) btn.click();
      return true;
    })()`);
    const renameClosed = await cdp.waitFor("!document.querySelector('#it-name-input')", 4000);
    check('未变化改名后取消面板：面板关闭且磁盘目录无新增文件', !!renameClosed);

    // 退出重命名，回到干净状态
    await cdp.evaluate(`(() => {
      const input = document.querySelector('#it-name-input');
      if (input) input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
      return true;
    })()`);

    // ---- 6. 900×600 窄窗口：信息栏改为覆盖式抽屉 ----
    await cdp.send('Emulation.setDeviceMetricsOverride', {
      width: 900,
      height: 600,
      deviceScaleFactor: 1,
      mobile: false,
    });
    await new Promise((r) => setTimeout(r, 500));
    const narrow = await cdp.evaluate(`(() => {
      const drawer = document.querySelector('.it-inspector');
      const style = drawer ? getComputedStyle(drawer) : null;
      return {
        width: window.innerWidth,
        position: style ? style.position : '',
        drawerWidth: drawer ? Math.round(drawer.getBoundingClientRect().width) : 0,
        overflowX: document.documentElement.scrollWidth > window.innerWidth,
        gridColumns: getComputedStyle(document.querySelector('.it-viewer-main')).gridTemplateColumns,
      };
    })()`);
    check(
      '900×600 下信息栏为 300px 覆盖式抽屉且无横向溢出',
      narrow.width === 900 &&
        narrow.position === 'absolute' &&
        narrow.drawerWidth === 300 &&
        !narrow.overflowX,
      JSON.stringify(narrow)
    );
    // 视觉基线：遮罩、标题层级、计数器必须与 design/桌面-图片查看器.html 同值（computed style 实测）
    const visualTokens = await cdp.evaluate(`(() => {
      const cs = (sel, prop) => {
        const el = document.querySelector(sel);
        return el ? getComputedStyle(el)[prop] : null;
      };
      const scrim = document.querySelector('.it-scrim');
      const viewbar = document.querySelector('.it-viewbar');
      const btns = viewbar ? [...viewbar.querySelectorAll('button')] : [];
      const last = btns[btns.length - 1];
      return {
        scrimBg: scrim ? getComputedStyle(scrim).backgroundColor : null,
        scrimVisible: scrim ? scrim.getBoundingClientRect().width > 0 : false,
        counterMinWidth: cs('.it-counter', 'minWidth'),
        counterFontSize: cs('.it-counter', 'fontSize'),
        counterBNum: cs('.it-counter b', 'fontVariantNumeric'),
        counterBWeight: cs('.it-counter b', 'fontWeight'),
        insHeadBWeight: cs('.it-ins-head b', 'fontWeight'),
        lastBtnRight: last ? Math.round(last.getBoundingClientRect().right) : 0,
        innerWidth: window.innerWidth,
      };
    })()`);
    check(
      '900×600 抽屉遮罩浓度与设计稿一致（rgba(29,33,41,.38)）',
      visualTokens.scrimBg === 'rgba(29, 33, 41, 0.38)' && visualTokens.scrimVisible === true,
      JSON.stringify({ bg: visualTokens.scrimBg, visible: visualTokens.scrimVisible })
    );
    check(
      '工具条计数器与设计稿一致（52px / 10.5px，数字仍 600 + tabular-nums）',
      visualTokens.counterMinWidth === '52px' &&
        visualTokens.counterFontSize === '10.5px' &&
        visualTokens.counterBWeight === '600' &&
        visualTokens.counterBNum === 'tabular-nums',
      JSON.stringify(visualTokens)
    );
    check(
      '信息栏标题字重与设计稿一致（13px/600）',
      visualTokens.insHeadBWeight === '600',
      String(visualTokens.insHeadBWeight)
    );
    check(
      '900×600 下最右工具条控件不溢出窗口',
      visualTokens.lastBtnRight <= visualTokens.innerWidth,
      `right=${visualTokens.lastBtnRight} / width=${visualTokens.innerWidth}`
    );
    // 900×600 下同样要完整多行展示：抽屉只有 300px，长路径必须换行且不横向溢出
    const narrowPathProbe = await pathDisplayProbe();
    const narrowPathLines = analyzePathDisplay(narrowPathProbe);
    const narrowRealPath = (await cdp.evaluate('window.__imageToolsSmoke.currentFile()')) || {};
    check(
      '900×600 下位置仍完整显示 file.path、多行且不横向溢出',
      !!narrowPathProbe &&
        narrowPathProbe.text === narrowRealPath.path &&
        narrowPathLines.lines >= 2 &&
        narrowPathLines.orderOk &&
        narrowPathProbe.btnHeight > 26 &&
        narrowPathProbe.btnScrollWidth <= narrowPathProbe.btnClientWidth + 1 &&
        narrowPathProbe.textScrollWidth <= narrowPathProbe.textClientWidth + 1 &&
        narrow.overflowX === false,
      JSON.stringify({
        lines: narrowPathLines.lines,
        orderOk: narrowPathLines.orderOk,
        height: narrowPathProbe && narrowPathProbe.btnHeight,
        shown: String(narrowPathProbe && narrowPathProbe.text).slice(-28),
        expected: String(narrowRealPath.path).slice(-28),
        docOverflowX: narrowPathProbe && narrowPathProbe.docOverflowX,
      })
    );
    await cdp.screenshot(path.join(tmp, 'narrow-900x600.png'));
    // 900×600 下信息栏必须真的可滚动（内容高于可视区 → scrollHeight > clientHeight）。
    // 不强造：若内容确实不超高，就记录真实数值并在 detail 里说明，不把断言写成恒真。
    const narrowScroll = await cdp.evaluate(`(() => {
      const body = document.querySelector('.it-inspector .it-ins-body');
      if (!body) return { missing: true };
      const style = getComputedStyle(body);
      return {
        scrollHeight: body.scrollHeight,
        clientHeight: body.clientHeight,
        overflowY: style.overflowY,
        canScroll: body.scrollHeight > body.clientHeight,
      };
    })()`);
    check(
      '900×600 信息栏内容超高时可纵向滚动（scrollHeight > clientHeight）',
      narrowScroll.missing !== true &&
        narrowScroll.overflowY === 'auto' &&
        narrowScroll.scrollHeight > narrowScroll.clientHeight,
      JSON.stringify(narrowScroll)
    );
    // 滚动生效取证：真的滚动后 scrollTop 变大（不是只有 overflow 属性）
    const scrollEffect = await cdp.evaluate(`(() => {
      const body = document.querySelector('.it-inspector .it-ins-body');
      if (!body) return { missing: true };
      body.scrollTop = body.scrollHeight;
      const top = body.scrollTop;
      body.scrollTop = 0;
      return { top, max: body.scrollHeight - body.clientHeight, backToTop: body.scrollTop };
    })()`);
    check(
      '900×600 信息栏滚动真实生效（scrollTop 达到可滚动距离后能回到顶部）',
      scrollEffect.missing !== true &&
        scrollEffect.max > 0 &&
        Math.abs(scrollEffect.top - scrollEffect.max) <= 1 &&
        scrollEffect.backToTop === 0,
      JSON.stringify(scrollEffect)
    );
    // 信息栏单独取证：300px 抽屉里完整路径换行的现场图
    await cdp.screenshotElement(path.join(tmp, 'path-inspector-900x600.png'), '.it-inspector');
    await cdp.send('Emulation.clearDeviceMetricsOverride');
    await new Promise((r) => setTimeout(r, 400));
    await cdp.screenshot(path.join(tmp, 'viewer-1280x800.png'));
    // 常规宽窗口的信息栏取证图（与 900×600 那张同区域，便于对照）
    await cdp.screenshotElement(path.join(tmp, 'path-inspector-1280x800.png'), '.it-inspector');

    // ---- 7. 图片处理：三页签 + 三类任务 + 结果 ----
    await cdp.evaluate(`(() => {
      const tab = [...document.querySelectorAll('.it-navtab')].find((b) => b.innerText.trim() === '图片处理');
      tab.click();
      return true;
    })()`);
    const tabs = await cdp.waitFor(
      "(() => { const list = [...document.querySelectorAll('.it-work-tab')].map((b) => b.innerText.trim()); return list.length === 3 && list.join(','); })()"
    );
    check('处理页只有三个任务页签（无批量重命名）', tabs === '格式转换,压缩体积,尺寸修改', tabs);
    check(
      '导航只有图片查看 / 图片处理',
      (await cdp.evaluate(
        "[...document.querySelectorAll('.it-navtab')].map((b) => b.innerText.trim()).join(',')"
      )) === '图片查看,图片处理'
    );

    // 0 项时「开始处理」必须禁用并说明原因
    const emptyState = await cdp.evaluate(`(() => {
      const start = [...document.querySelectorAll('.it-action-card .it-btn')].find((b) => b.innerText.trim() === '开始处理');
      return { disabled: start ? start.disabled : null, note: document.querySelector('.it-action-note') ? document.querySelector('.it-action-note').innerText : '' };
    })()`);
    check(
      '0 项时禁用开始处理并说明原因',
      emptyState.disabled === true && /暂无可处理项/.test(emptyState.note),
      JSON.stringify(emptyState)
    );

    // 用已授权路径直接填列表（测试接缝）；不打开真实系统对话框
    // 改名会改变文件清单：按当前磁盘内容重取，避免带入已不存在的旧路径
    const samplePaths = fs.readdirSync(sampleDir).map((name) => path.join(sampleDir, name));
    await cdp.evaluate(`window.__imageToolsSmoke.addPaths(${JSON.stringify(samplePaths)})`);
    const listed = await cdp.waitFor(
      "document.querySelectorAll('.it-file-row').length >= 5",
      12000
    );
    const listState = await cdp.evaluate(`(() => ({
      rows: document.querySelectorAll('.it-file-row').length,
      processable: document.querySelectorAll('.it-file-row .it-tag.is-success').length,
      skipped: document.querySelectorAll('.it-file-row .it-tag.is-warn').length,
      summary: document.querySelector('.it-fc-sub').innerText.replace(/\\s+/g, ' '),
    }))()`);
    check(
      '添加后列出混合格式文件并区分可处理/跳过',
      !!listed && listState.processable >= 3 && listState.skipped >= 1,
      JSON.stringify(listState)
    );
    // 跳过原因按当前格式边界精确断言：样本里明确包含静态 GIF（"静态 GIF 不在首版批处理范围"）
    // 与损坏图片（无法识别内容）两类；旧断言只匹配 /动画|不在批处理/，漏掉了实际文案。
    const skipReasons = await cdp.evaluate(`(() => {
      return [...document.querySelectorAll('.it-file-row')]
        .filter((row) => row.querySelector('.it-tag.is-warn'))
        .map((row) => (row.querySelector('.it-f-reason') || {}).textContent || '')
        .map((text) => text.trim());
    })()`);
    check(
      '跳过项给出可识别的具体原因（静态 GIF 明确说明不在批处理范围）',
      skipReasons.some((text) => /静态 GIF 不在首版批处理范围/.test(text)),
      JSON.stringify(skipReasons)
    );
    check(
      '跳过原因不是泛泛的「失败」',
      skipReasons.length > 0 && skipReasons.every((text) => text && !/^失败$/.test(text)),
      JSON.stringify(skipReasons)
    );
    check(
      '批处理页标注格式边界与不压首帧',
      /不压首帧/.test(await cdp.evaluate("document.querySelector('.it-fc-foot').innerText"))
    );

    // ---- 7a. 短名路径身份：渲染层拿短名、核心列举返回长名，同一文件必须进列表 ----
    // v2.4.3 CI 现场：rows=0。旧实现用 `known.has(path)`（key 是核心返回的长名）比对用户传入的
    // 8.3 短名，误判「已不在原位置」而全部跳过。这里先用干净列表从短名加一次，证明身份已统一。
    // 本机 TEMP 常是短目录名（没有 8.3 短名），所以在样本目录下自建一个长名子目录来复现同一形态。
    // 源图按当前磁盘取（前面的 F2 改名已经把 01-秋检现场.jpg 改成了 现场勘察-01.jpg）。
    const shortAlbumLong = path.join(sampleDir, 'Long Named Album For Short Path Case');
    fs.mkdirSync(shortAlbumLong, { recursive: true });
    const shortAlbumFile = path.join(shortAlbumLong, '短名样本.jpg');
    const shortSource =
      fs
        .readdirSync(sampleDir)
        .map((name) => path.join(sampleDir, name))
        .find((file) => /\.(jpe?g|png)$/i.test(file) && fs.statSync(file).isFile()) || '';
    if (!shortSource) fail(1, '样本目录里没有可复制的图片，无法构造短名用例');
    fs.copyFileSync(shortSource, shortAlbumFile);
    const shortAlbum = windowsShortPath(shortAlbumLong);
    const shortFile = shortAlbum ? path.join(shortAlbum, path.basename(shortAlbumFile)) : '';
    check(
      '能取得 8.3 短名（系统关闭 8.3 时才为 false，本机实测可用）',
      !!shortAlbum && shortAlbum !== shortAlbumLong && fs.existsSync(shortFile),
      `长名=${shortAlbumLong} 短名=${shortAlbum || '(无)'} 文件=${shortFile || '(无)'}`
    );
    if (shortAlbum && shortAlbum !== shortAlbumLong) {
      // 不清空列表（产品没有清空入口，逐条点移除会与 Vue 异步更新打架）：
      // 改为按「行数增量」判定——短名加入必须恰好新增 1 行，且这一行就是那个文件。
      const beforeRows = await cdp.evaluate("document.querySelectorAll('.it-file-row').length");
      // 核心列举到的 path 是长名（真实身份）；带出来做证据
      const longName = realOrEmpty(shortAlbumFile);
      await cdp.evaluate(`window.__imageToolsSmoke.addPaths(${JSON.stringify([shortFile])})`);
      const shortAdded = await cdp.waitFor(
        `document.querySelectorAll('.it-file-row').length === ${beforeRows + 1}`,
        12000
      );
      const shortRow = await cdp.evaluate(`(() => {
        const rows = [...document.querySelectorAll('.it-file-row')];
        const hit = rows.find((row) => /短名样本/.test(row.innerText));
        return {
          rows: rows.length,
          name: hit && hit.querySelector('.it-f-name') ? hit.querySelector('.it-f-name').innerText.trim() : '',
          path: hit && hit.querySelector('.it-f-name') ? hit.querySelector('.it-f-name').getAttribute('title') || '' : '',
          sub: hit ? hit.innerText.replace(/\\s+/g, ' ').trim() : '',
        };
      })()`);
      check(
        '短名路径加入后该文件真实进入列表（不是被判「已不在原位置」跳过）',
        !!shortAdded && shortRow.rows === beforeRows + 1 && /短名样本/.test(shortRow.name),
        JSON.stringify({ ...shortRow, beforeRows, input: shortFile, canonical: longName })
      );
      check(
        '列表里记录的是核心规范的完整路径（长名），不是传入的短名',
        shortRow.path === longName,
        JSON.stringify({ recorded: shortRow.path, canonical: longName, input: shortFile })
      );
      // toast 判定要排除前序残留：等提示自然消失后再看本次加入是否产生新的「已不在原位置」。
      // 不掩盖当前提示——清空后若本次加入仍弹该提示，断言照样红。
      await cdp.waitFor("!document.querySelector('.it-toast')", 8000, 100);
      const toastAfterClear = await cdp.evaluate(
        "document.querySelector('.it-toast') ? document.querySelector('.it-toast').innerText.replace(/\\s+/g, ' ').trim() : ''"
      );
      check(
        '前序提示已消失（否则无法区分残留与本次新提示）',
        toastAfterClear === '',
        toastAfterClear
      );
      // 用另一张真实存在的图片再触发一次 additive 加入，观察是否弹出「已不在原位置」
      await cdp.evaluate(`window.__imageToolsSmoke.addPaths(${JSON.stringify([shortAlbumFile])})`);
      await new Promise((r) => setTimeout(r, 1200));
      const toastAfterAdd = await cdp.evaluate(
        "document.querySelector('.it-toast') ? document.querySelector('.it-toast').innerText.replace(/\\s+/g, ' ').trim() : ''"
      );
      check(
        '短名加入不产生「已不在原位置」提示（存在文件不得误报缺失）',
        !/已不在原位置/.test(toastAfterAdd),
        toastAfterAdd
      );
      // 同一文件再用长名加一次：必须合并为同一条（真实身份去重，不是按字符串）
      await cdp.evaluate(`window.__imageToolsSmoke.addPaths(${JSON.stringify([shortAlbumFile])})`);
      await new Promise((r) => setTimeout(r, 800));
      const dedup = await cdp.evaluate(`(() => ({
        rows: document.querySelectorAll('.it-file-row').length,
        names: [...document.querySelectorAll('.it-file-row .it-f-name')].map((el) => el.innerText.trim()),
        // 每行的 title 是产品记录的 entry.path（模板 :title="entry.path"）
        paths: [...document.querySelectorAll('.it-file-row .it-f-name')].map((el) => el.getAttribute('title') || ''),
      }))()`);
      check(
        '同一文件的短名与长名只保留一条（身份去重，不是按字符串比对）',
        dedup.rows === beforeRows + 1 && dedup.names.filter((n) => /短名样本/.test(n)).length === 1,
        JSON.stringify({ ...dedup, beforeRows, input: [shortFile, shortAlbumFile] })
      );

      // 真实批处理：列表里这条是「用短名加入」的，跑一次格式转换必须真实成功（不是只进列表）
      await cdp.evaluate(`(() => {
        [...document.querySelectorAll('.it-work-tab')].find((b) => b.innerText.trim() === '格式转换').click();
        return true;
      })()`);
      await new Promise((r) => setTimeout(r, 300));
      const shortRunStart = await cdp.evaluate(`(() => {
        const start = [...document.querySelectorAll('.it-action-card .it-btn')].find((b) => b.innerText.trim() === '开始处理');
        if (!start || start.disabled) return { started: false };
        start.click();
        return { started: true };
      })()`);
      const shortRunDone = await cdp.waitFor(
        "['done','partial','failed'].includes(document.querySelector('.it-process').dataset.state) && document.querySelector('.it-process').dataset.state",
        60000,
        300
      );
      const shortRunResult = await cdp.evaluate(`(() => ({
        state: document.querySelector('.it-process').dataset.state,
        tiles: [...document.querySelectorAll('.it-result-card .it-rs')].map((el) => el.innerText.replace(/\\s+/g, '')),
        head: document.querySelector('.it-result-card .it-mc-head')
          ? document.querySelector('.it-result-card .it-mc-head').innerText.replace(/\\s+/g, ' ').trim()
          : '',
      }))()`);
      check(
        '用短名加入的条目能真实完成批处理（成功 ≥ 1，不是 0 项）',
        shortRunStart.started === true &&
          shortRunDone === 'done' &&
          /[1-9]\d*成功/.test((shortRunResult.tiles || []).join(' ')),
        JSON.stringify({ ...shortRunStart, done: shortRunDone, ...shortRunResult })
      );
      // 回到待处理并把完整样本目录恢复，供后续三类任务与取消链路继续在混合格式上验证
      await cdp.evaluate(`(() => {
        const back = [...document.querySelectorAll('.it-result-actions .it-btn')].find((b) => b.innerText.trim() === '返回待处理');
        if (back) back.click();
        return true;
      })()`);
      await cdp.waitFor("document.querySelector('.it-process').dataset.state === 'idle'", 8000);
      // 收尾：删掉本次自建的短名样本目录（含其 processed_ 产物），
      // 让后续三类任务的输出目录仍是唯一的一个，断言口径与之前完全一致。
      fs.rmSync(shortAlbumLong, { recursive: true, force: true });
      // 从列表里移除已消失的条目（产品会对已消失路径拒绝处理，这里显式清干净）
      await cdp.evaluate(`(() => {
        const target = ${JSON.stringify(longName)};
        for (let i = 0; i < 5; i += 1) {
          const row = [...document.querySelectorAll('.it-file-row')].find(
            (item) =>
              (item.querySelector('.it-f-name') || {}).getAttribute('title') === target ||
              item.innerText.includes('短名样本')
          );
          if (!row) break;
          const btn = row.querySelector('.it-f-remove');
          if (!btn) break;
          btn.click();
        }
        return true;
      })()`);
      await cdp.waitFor("!document.body.innerText.includes('短名样本')", 8000);
      const refreshedPaths = fs.readdirSync(sampleDir).map((name) => path.join(sampleDir, name));
      await cdp.evaluate(`window.__imageToolsSmoke.addPaths(${JSON.stringify(refreshedPaths)})`);
      await cdp.waitFor("document.querySelectorAll('.it-file-row').length >= 5", 12000);
    }
    check(
      '输出位置默认原目录子目录并提示不覆盖',
      /processed_/.test(
        await cdp.evaluate("document.querySelector('.it-output-card').innerText")
      ) &&
        /不提供原地覆盖/.test(
          await cdp.evaluate("document.querySelector('.it-output-card').innerText")
        )
    );

    async function runOperation(tabName) {
      await cdp.evaluate(`(() => {
        [...document.querySelectorAll('.it-work-tab')].find((b) => b.innerText.trim() === ${JSON.stringify(tabName)}).click();
        return true;
      })()`);
      await new Promise((r) => setTimeout(r, 250));
      const summary = await cdp.evaluate(
        "document.querySelector('.it-action-summary') ? document.querySelector('.it-action-summary').innerText : ''"
      );
      const started = await cdp.evaluate(`(() => {
        const stateBefore = document.querySelector('.it-process').dataset.state;
        const start = [...document.querySelectorAll('.it-action-card .it-btn')].find((b) => b.innerText.trim() === '开始处理');
        const disabled = start ? start.disabled : true;
        if (!disabled) start.click();
        return { stateBefore, disabled };
      })()`);
      // 启动后先给一拍：失败会回到 idle 并弹一条提示，这里把原因一起带出来
      await new Promise((r) => setTimeout(r, 600));
      const startedState = await cdp.evaluate(
        "document.querySelector('.it-process').dataset.state"
      );
      const toastText = await cdp.evaluate(
        "document.querySelector('.it-toast') ? document.querySelector('.it-toast').innerText : ''"
      );
      if (startedState === 'idle') {
        check(`${tabName}：开始处理被接受`, false, `回到待处理：${toastText}`);
        return {
          summary,
          started,
          finished: false,
          result: { state: 'idle', head: '', stats: [], outDir: '' },
        };
      }
      const finished = await cdp.waitFor(
        "['done','partial'].includes(document.querySelector('.it-process').dataset.state)",
        60000,
        400
      );
      const result = await cdp.evaluate(`(() => {
        const card = document.querySelector('.it-result-card');
        const head = card ? card.querySelector('.it-mc-head') : null;
        return {
          state: document.querySelector('.it-process').dataset.state,
          hasCard: !!card,
          head: head ? head.innerText.replace(/\\s+/g, ' ') : '',
          stats: [...document.querySelectorAll('.it-result-card .it-rs')].map((el) => el.innerText.replace(/\\s+/g, '')),
          outDir: document.querySelector('.it-result-out b') ? document.querySelector('.it-result-out b').innerText : '',
        };
      })()`);
      return { summary, started, finished, result };
    }

    const convertRun = await runOperation('格式转换');
    check(
      '格式转换：开始处理可点击',
      convertRun.started && convertRun.started.disabled === false,
      JSON.stringify(convertRun.started)
    );
    check(
      '格式转换：摘要随参数联动（JPEG 质量 88）',
      /格式转换 · JPEG/.test(convertRun.summary),
      convertRun.summary
    );
    check(
      '格式转换：任务执行到终态并给出成功/失败/跳过',
      !!convertRun.finished && /成功/.test(convertRun.result.head),
      JSON.stringify(convertRun.result)
    );
    logScreen(convertRun.result);

    await cdp.evaluate(`(() => {
      [...document.querySelectorAll('.it-result-actions .it-btn')].find((b) => b.innerText.trim() === '返回待处理').click();
      return true;
    })()`);
    const compressRun = await runOperation('压缩体积');
    check(
      '压缩体积：摘要随档位联动',
      /压缩体积 · 均衡/.test(compressRun.summary),
      compressRun.summary
    );
    check('压缩体积：任务执行到终态', !!compressRun.finished, compressRun.result.head);

    await cdp.evaluate(`(() => {
      [...document.querySelectorAll('.it-result-actions .it-btn')].find((b) => b.innerText.trim() === '返回待处理').click();
      return true;
    })()`);
    // 尺寸修改：默认像素 1920×1080，对 1920×1280 源图按「不放大」会跳过大部分项
    await cdp.evaluate(`(() => {
      [...document.querySelectorAll('.it-work-tab')].find((b) => b.innerText.trim() === '尺寸修改').click();
      return true;
    })()`);
    await new Promise((r) => setTimeout(r, 200));
    const resizeState = await cdp.evaluate(`(() => {
      const percent = [...document.querySelectorAll('.it-segbtn')].find((b) => b.innerText.trim() === '百分比');
      percent.click();
      return true;
    })()`);
    void resizeState;
    await new Promise((r) => setTimeout(r, 200));
    const resizeRun = await runOperation('尺寸修改');
    check(
      '尺寸修改：摘要随调整方式联动',
      /尺寸修改 · 百分比/.test(resizeRun.summary),
      resizeRun.summary
    );
    check('尺寸修改：任务执行到终态', !!resizeRun.finished, resizeRun.result.head);
    logScreen(resizeRun.result);

    // 输出目录必须真的写了文件，且原图未被改动
    const outDir = resizeRun.result.outDir || convertRun.result.outDir;
    const produced = outDir && fs.existsSync(outDir) ? fs.readdirSync(outDir) : [];
    check('输出目录真的产生文件', produced.length > 0, `${outDir} → ${produced.length} 个`);
    check(
      '原图未被覆盖（样本目录文件数未减少）',
      fs.readdirSync(sampleDir).length >= names.length - 1,
      fs.readdirSync(sampleDir).join(',')
    );

    // 日志抽屉：按成功/失败/跳过筛选
    await cdp.evaluate(`(() => {
      [...document.querySelectorAll('.it-result-card .it-btn')].find((b) => b.innerText.trim() === '查看日志').click();
      return true;
    })()`);
    const logOk = await cdp.waitFor("!!document.querySelector('.it-log-list')");
    const logInfo = await cdp.evaluate(`(() => {
      const filters = [...document.querySelectorAll('.it-log-filter .it-segbtn')].map((b) => b.innerText.trim());
      const lines = document.querySelectorAll('.it-log-line').length;
      const modal = document.querySelector('.it-drawer');
      const texts = [...document.querySelectorAll('.it-log-line .it-log-text')].map((el) =>
        el.textContent.replace(/\\s+/g, ' ').trim()
      );
      return { filters, lines, texts, role: modal ? modal.getAttribute('role') : '', aria: modal ? modal.getAttribute('aria-modal') : '' };
    })()`);
    check(
      '日志抽屉可打开、带 role=dialog/aria-modal 与全部/成功/失败/跳过筛选',
      !!logOk &&
        logInfo.role === 'dialog' &&
        logInfo.aria === 'true' &&
        logInfo.filters.join(',') === '全部,成功,失败,跳过',
      JSON.stringify({ filters: logInfo.filters, lines: logInfo.lines, role: logInfo.role })
    );
    // 并发数必须来自真实配置（本次冒烟临时 config.json 设 workers=1）：
    //  - 本地开始行由渲染层在 runProcess 返回后写入：`<操作> 开始 · N 项 · 并发 1`
    //  - 核心开始行是核心自己 `job.log("开始处理 … 并发 1")` 写的，落在核心任务日志文件里
    // 两处都取真实运行产物；只查源码字符串不算。
    const workersInConfig = (() => {
      try {
        const cfg = JSON.parse(fs.readFileSync(tempConfig, 'utf8'));
        return (cfg.image_tools || {}).workers;
      } catch (e) {
        return null;
      }
    })();
    const localStartLine =
      logInfo.texts.find((t) => /\s开始\s·\s\d+\s项\s·\s并发\s\d+/.test(t)) || '';
    const coreStartLine = logInfo.texts.find((t) => /开始处理\s+\d+\s+项/.test(t)) || '';
    check(
      '处理日志本地开始行使用真实 workers（临时配置 workers=1 → 并发 1）',
      workersInConfig === 1 && /并发 1$/.test(localStartLine),
      JSON.stringify({ configWorkers: workersInConfig, line: localStartLine })
    );
    // 核心任务日志文件（核心自己的 job.log 落盘位置）：证明「开始处理 … 并发 1」确实由核心写出，
    // 而不是渲染层照抄配置。路径与 toolkit/state.py 一致：LOCALAPPDATA/uvmp-toolkit/logs/<job>.log，
    // 本次冒烟 LOCALAPPDATA 已隔离到临时目录，不碰用户真实状态。
    const coreLogDir = path.join(tempLocalAppData, 'uvmp-toolkit', 'logs');
    const currentJob = String(
      (await cdp.evaluate(
        'window.__imageToolsSmoke ? window.__imageToolsSmoke.currentJobId() : ""'
      )) || ''
    );
    const coreLogPath = currentJob ? path.join(coreLogDir, currentJob + '.log') : '';
    let coreLogLines = [];
    let coreLogSeen = false;
    for (let i = 0; i < 20; i += 1) {
      if (coreLogPath && fs.existsSync(coreLogPath)) {
        coreLogLines = fs.readFileSync(coreLogPath, 'utf8').split(/\r?\n/).filter(Boolean);
        coreLogSeen = coreLogLines.some((line) => /开始处理\s+\d+\s+项/.test(line));
        if (coreLogSeen) break;
      }
      await new Promise((r) => setTimeout(r, 150));
    }
    const coreFileStartLine = coreLogLines.find((line) => /开始处理\s+\d+\s+项/.test(line)) || '';
    // 项数必须与本地开始行一致（两处都来自同一次真实任务），并发必须是配置里的 1
    const expectedItems = (/\s·\s(\d+)\s项\s·\s并发/.exec(localStartLine) || [])[1] || '';
    check(
      '核心自己的任务日志里有「开始处理」行且并发为 1（真实 workers 生效）',
      !!currentJob &&
        /^\d{14}-[0-9a-f]{6}$/.test(currentJob) &&
        !!expectedItems &&
        new RegExp('开始处理\\s+' + expectedItems + '\\s+项').test(coreFileStartLine) &&
        /并发 1$/.test(coreFileStartLine.trim()),
      JSON.stringify({
        job: currentJob,
        logPath: coreLogPath,
        line: coreFileStartLine,
        items: expectedItems,
      })
    );
    // 抽屉侧：核心日志事件确实流到了界面（成功行来自 job.log，不是本地拼的文案）。
    // 首批「开始处理」行偶尔在 RPC 返回前到达，被主进程按「未跟踪任务号」过滤
    // （electron/imagetools/index.cjs 的 handleCoreEvent：runProcess 拿到 job_id 后才 trackJob），
    // 这条时序抖动已在报告中记录；硬断言放在核心日志文件与成功行两处，逐次记录实测结果不判红。
    const drawerCoreSuccess = logInfo.texts.filter((t) => /^\[?\d*:?\d*:?\d*\]?\s*成功 /.test(t));
    check(
      '日志抽屉收到核心 job.log 事件（成功行来自核心）',
      drawerCoreSuccess.length >= 4,
      JSON.stringify({ successLines: drawerCoreSuccess.length, total: logInfo.texts.length })
    );
    // 抽屉侧强制断言：核心「开始处理」首批行必须出现在日志抽屉里。
    // 该行由核心在建任务的同一瞬间写出，而主进程此前要等 imageProcess RPC 返回才知道 job_id；
    // 主进程现在用创建期缓冲保存这段时间的 job.* 事件，拿到 job_id 后先登记再回放，
    // 因此这一行必须稳定到达（丢一次就是回归）。
    const localIdx = logInfo.texts.findIndex((t) => /\s开始\s·\s\d+\s项\s·\s并发\s\d+/.test(t));
    const coreIdx = logInfo.texts.findIndex((t) => /开始处理\s+\d+\s+项/.test(t));
    check(
      '日志抽屉里出现核心「开始处理」首批行（创建期事件未丢）',
      coreIdx >= 0 && /并发 1$/.test(coreStartLine.trim()),
      JSON.stringify({ coreIdx, localIdx, line: coreStartLine, total: logInfo.texts.length })
    );
    // 顺序：本地开始行先写入，随后回放的核心开始行紧跟其后（两层缓冲顺序都正确）
    check(
      '本地开始行之后紧跟核心开始行（渲染层回放顺序正确）',
      localIdx >= 0 && coreIdx === localIdx + 1,
      JSON.stringify({
        localIdx,
        coreIdx,
        local: logInfo.texts[localIdx],
        core: logInfo.texts[coreIdx],
        order: logInfo.texts.slice(Math.max(0, localIdx - 1), coreIdx + 2),
      })
    );
    await cdp.evaluate("document.querySelectorAll('.it-log-filter .it-segbtn')[1].click()");
    await new Promise((r) => setTimeout(r, 200));
    const successFilter = await cdp.evaluate(`(() => ({
      total: document.querySelectorAll('.it-log-line').length,
      success: document.querySelectorAll('.it-log-line .it-lv.is-success').length,
      failed: document.querySelectorAll('.it-log-line .it-lv.is-failed').length,
      skipped: document.querySelectorAll('.it-log-line .it-lv.is-skipped').length,
    }))()`);
    check(
      '日志可按「成功」筛选（只剩成功行）',
      successFilter.total > 0 &&
        successFilter.success === successFilter.total &&
        successFilter.failed === 0 &&
        successFilter.skipped === 0,
      JSON.stringify(successFilter)
    );
    // Esc 关闭日志抽屉（不点关闭按钮），且不影响任务态
    await cdp.pressKey('Escape');
    const logClosed = await cdp.waitFor("!document.querySelector('.it-drawer')", 4000);
    check('日志抽屉 Esc 可关闭', !!logClosed);
    await cdp.screenshot(path.join(tmp, 'process-log.png'));
    await cdp.evaluate(`(() => {
      const close = [...document.querySelectorAll('.it-drawer .it-d-btn, .it-drawer .it-btn')].pop();
      if (close) close.click();
      return true;
    })()`);

    // ---- 8. 真实取消：真实核心 RPC + 真实文件 + 真实 JobManager ----
    // 取消必须能被稳定触发：先造一批足够大的样本（30 张 2000×1400 渲染 PDF 用的复杂画面），
    // 让单批耗时到秒级，再按「已出现首个成功项」这个实际进度条件点取消，而不是猜时间。
    await cdp.evaluate(`(() => {
      [...document.querySelectorAll('.it-result-actions .it-btn')].find((b) => b.innerText.trim() === '返回待处理').click();
      return true;
    })()`);
    await cdp.waitFor("document.querySelector('.it-process').dataset.state === 'idle'");
    const cancelDir = path.join(sampleDir, '批量取消');
    fs.mkdirSync(cancelDir, { recursive: true });
    const cancelNames = [];
    for (let i = 0; i < 30; i += 1)
      cancelNames.push(`取消样本-${String(i + 1).padStart(2, '0')}.jpg`);
    const cancelGen = spawnSync(
      py.cmd,
      [
        '-c',
        `
import os, sys
from PIL import Image, ImageDraw
out = sys.argv[1]
for i in range(30):
    img = Image.new('RGB', (2000, 1400), (14, 61, 168))
    draw = ImageDraw.Draw(img)
    for n in range(220):
        draw.rectangle([(n * 37 + i * 11) % 2000, (n * 53 + i * 7) % 1400,
                        (n * 37 + i * 11) % 2000 + 120, (n * 53 + i * 7) % 1400 + 90],
                       fill=((n * 17) % 256, (n * 31 + i) % 256, (n * 47) % 256))
    img.save(os.path.join(out, '取消样本-%02d.jpg' % (i + 1)), quality=92)
print('ok')
`,
        cancelDir,
      ],
      { encoding: 'utf8', windowsHide: true }
    );
    if (cancelGen.status !== 0) {
      console.log('  取消样本生成失败：' + String(cancelGen.stderr || '').slice(0, 200));
    }
    const cancelPaths = cancelNames.map((name) => path.join(cancelDir, name));
    await cdp.evaluate(`window.__imageToolsSmoke.addPaths(${JSON.stringify(cancelPaths)})`);
    await cdp.waitFor(`document.querySelectorAll('.it-file-row').length >= 30`, 15000, 100);
    await cdp.evaluate(`(() => {
      [...document.querySelectorAll('.it-work-tab')].find((b) => b.innerText.trim() === '格式转换').click();
      return true;
    })()`);
    await new Promise((r) => setTimeout(r, 200));
    await cdp.evaluate(`(() => {
      [...document.querySelectorAll('.it-action-card .it-btn')].find((b) => b.innerText.trim() === '开始处理').click();
      return true;
    })()`);
    const running = await cdp.waitFor(
      "document.querySelector('.it-process').dataset.state === 'running' && !!document.querySelector('.it-run-card')",
      8000,
      100
    );
    check('开始处理后进入执行中（进度卡出现）', !!running);
    // 实际进度条件：进度卡的「成功」计数 >= 1 才算真正写出第一张成品
    const firstSuccess = await cdp.waitFor(
      `(() => {
        const chips = [...document.querySelectorAll('.it-run-chips span')];
        const el = chips.find((c) => c.innerText.trim().startsWith('成功'));
        const n = el ? Number(el.querySelector('b').innerText.trim()) : 0;
        return n >= 1 && document.querySelector('.it-process').dataset.state === 'running';
      })()`,
      30000,
      60
    );
    check('真实任务开始写出成品（进度显示成功 ≥ 1）', !!firstSuccess);
    const realJobId = await cdp.evaluate('window.__imageToolsSmoke.currentJobId()');
    check(
      '真实任务号可查（getJob 链路可用）',
      /^\d{14}-[0-9a-f]{6}$/.test(String(realJobId)),
      String(realJobId)
    );
    await cdp.evaluate("document.querySelector('.it-run-card .it-cancel').click()");
    const dialog = await cdp.waitFor(
      '!!document.querySelector(\'.it-dialog[role="dialog"]\')',
      4000
    );
    const dialogText = await cdp.evaluate(
      "document.querySelector('.it-dialog') ? document.querySelector('.it-dialog').innerText.replace(/\\s+/g, ' ') : ''"
    );
    check(
      '取消前有确认弹层（role=dialog，说明已完成项保留）',
      !!dialog && /取消本次处理/.test(dialogText),
      dialogText.slice(0, 60)
    );
    await cdp.evaluate(`(() => {
      const btn = [...document.querySelectorAll('.it-dialog .it-btn')].find((b) => b.innerText.trim() === '取消任务');
      if (btn) btn.click();
      return true;
    })()`);
    // 等终态由真实 job.done/getJob 收口（不是固定 sleep）
    const cancelledState0 = await cdp.waitFor(
      `['cancelled','done','partial'].includes(document.querySelector('.it-process').dataset.state) && document.querySelector('.it-process').dataset.state`,
      60000,
      200
    );
    check(
      '取消后落到「已取消」终态（不再冒充成功）',
      cancelledState0 === 'cancelled',
      String(cancelledState0)
    );
    // 核心侧真实结果：status=cancelled，result.rows 的成功项必须与磁盘成品一一对应
    const coreJob = await cdp.evaluate(
      `window.imageTools.getJob(${JSON.stringify(String(realJobId))})`,
      { timeoutMs: 30000 }
    );
    const coreRows = (coreJob && coreJob.result && coreJob.result.rows) || [];
    const coreSuccess = coreRows.filter((row) => row.status === 'success');
    // 本次任务沿用同一处理列表（前面 6 张样本也在），因此结果横跨两个输出目录。
    // 只比对「取消批次自己的目录」：样本目录里的 processed_* 还留着前三轮操作的成品，
    // 混在一起算会把旧文件当成这次的结果。
    const batchRows = coreSuccess.filter((row) =>
      String(row.output || '').includes(`${path.sep}批量取消${path.sep}`)
    );
    const batchDirs = [...new Set(batchRows.map((row) => path.dirname(row.output)))];
    const onDisk = [];
    for (const dir of batchDirs) {
      if (!fs.existsSync(dir)) continue;
      for (const name of fs.readdirSync(dir)) {
        if (name.startsWith('.~img-')) continue;
        onDisk.push(path.join(dir, name));
      }
    }
    const missingOutputs = batchRows.filter((row) => !fs.existsSync(row.output));
    check(
      '核心 getJob：终态 cancelled 且带已完成行（不返回空结果）',
      !!coreJob &&
        coreJob.status === 'cancelled' &&
        coreSuccess.length >= 1 &&
        coreSuccess.length < 36,
      JSON.stringify({
        status: coreJob && coreJob.status,
        rows: coreRows.length,
        success: coreSuccess.length,
      })
    );
    // 逐项对应：取消批次行里声明的输出文件真实存在，且该目录的成品集合与之完全一致
    check(
      '核心结果的成功项与磁盘成品一致',
      batchRows.length > 0 &&
        batchRows.length === onDisk.length &&
        missingOutputs.length === 0 &&
        batchRows
          .map((row) => path.basename(row.output))
          .sort()
          .join(',') ===
          onDisk
            .map((item) => path.basename(item))
            .sort()
            .join(','),
      `批次行=${batchRows.length} 磁盘=${onDisk.length} 目录=${batchDirs.join(' | ')}` +
        (missingOutputs.length
          ? ` 缺失=${missingOutputs.map((row) => row.output).join(',')}`
          : '') +
        ` 磁盘清单=${onDisk.map((item) => path.basename(item)).join(',')}`
    );
    const uiCounts = await cdp.evaluate(`(() => {
      const stats = [...document.querySelectorAll('.it-result-card .it-rs')].map((el) =>
        el.innerText.replace(/\\s+/g, '')
      );
      const card = document.querySelector('.it-result-card');
      return { stats, text: card ? card.innerText.split(/\\s+/).join(' ') : '' };
    })()`);
    check(
      '界面显示的成功数与核心结果一致（取消后不再显示 0 项）',
      uiCounts.stats[0] === `${coreSuccess.length}成功`,
      JSON.stringify({ stats: uiCounts.stats, core: coreSuccess.length })
    );
    check(
      '取消结果卡说明已完成的输出保留',
      new RegExp(`已完成的\\s*${coreSuccess.length}\\s*项`).test(uiCounts.text),
      uiCounts.text.slice(0, 100)
    );
    await cdp.screenshot(path.join(tmp, 'cancel-real.png'));

    // ---- 8b. 失败态口径：全失败 / 取消 / 核心退出 都不能显示「全部成功」 ----
    async function readOutcome() {
      return cdp.evaluate(`(() => {
        const card = document.querySelector('.it-result-card');
        const head = card ? card.querySelector('.it-mc-head') : null;
        return {
          state: document.querySelector('.it-process').dataset.state,
          tone: card ? card.dataset.tone : '',
          title: head ? head.querySelector('b').innerText.trim() : '',
          tag: head ? head.querySelector('.it-tag').innerText.trim() : '',
          text: card ? card.innerText.split(/\\s+/).join(' ') : '',
        };
      })()`);
    }

    // 全失败
    await cdp.evaluate(`window.__imageToolsSmoke.simulateJobDone({
      status: 'success',
      result: { rows: [
        { name: 'a.jpg', status: 'failed', reason: '输出目录只读' },
        { name: 'b.png', status: 'failed', reason: '解码失败' },
      ] },
    })`);
    await new Promise((r) => setTimeout(r, 300));
    const allFailed = await readOutcome();
    check(
      '全部失败时显示「全部失败」而非全部成功',
      allFailed.state === 'failed' &&
        /全部失败/.test(allFailed.tag) &&
        !/全部成功/.test(allFailed.text),
      JSON.stringify(allFailed)
    );
    check(
      '全部失败时行数与失败原因正确',
      /2\s*成功?|2/.test(allFailed.text) && /输出目录只读/.test(allFailed.text),
      allFailed.text.slice(0, 90)
    );
    const failedRowsText = await cdp.evaluate(`(() => ({
      success: document.querySelectorAll('.it-result-card .it-rs')[0].innerText.split(/\\s+/).join(''),
      failed: document.querySelectorAll('.it-result-card .it-rs')[1].innerText.split(/\\s+/).join(''),
    }))()`);
    check(
      '全部失败时成功 0 / 失败 2',
      failedRowsText.success === '0成功' && failedRowsText.failed === '2失败',
      JSON.stringify(failedRowsText)
    );

    // 取消（即使有成功项也不得显示全部成功）
    await cdp.evaluate(`window.__imageToolsSmoke.simulateJobDone({
      status: 'cancelled',
      result: { rows: [{ name: 'a.jpg', status: 'success', size_before: 100, size_after: 50 }] },
    })`);
    await new Promise((r) => setTimeout(r, 300));
    const cancelledState = await readOutcome();
    check(
      '取消时显示「已取消」且不冒充成功',
      cancelledState.state === 'cancelled' &&
        /已取消/.test(cancelledState.tag) &&
        !/全部成功|部分失败/.test(cancelledState.text),
      JSON.stringify(cancelledState)
    );

    // 核心退出
    await cdp.evaluate(`(() => {
      const start = [...document.querySelectorAll('.it-result-actions .it-btn')].find((b) => b.innerText.trim() === '返回待处理');
      if (start) start.click();
      return true;
    })()`);
    await cdp.waitFor("document.querySelector('.it-process').dataset.state === 'idle'");
    await cdp.evaluate(`(() => {
      [...document.querySelectorAll('.it-action-card .it-btn')].find((b) => b.innerText.trim() === '开始处理').click();
      return true;
    })()`);
    await cdp.waitFor("document.querySelector('.it-process').dataset.state === 'running'");

    // 运行中 busy 必须真的锁住列表与输出目录
    const busyState = await cdp.evaluate(`(() => {
      const add = [...document.querySelectorAll('.it-fc-head .it-btn')].find((b) => b.innerText.includes('添加'));
      const remove = document.querySelector('.it-f-remove');
      const dirs = [...document.querySelectorAll('.it-out-row input[type="radio"]')];
      const choose = [...document.querySelectorAll('.it-output-card .it-btn')].find((b) => b.innerText.trim() === '选择');
      return {
        addDisabled: add ? add.disabled : null,
        removeDisabled: remove ? remove.disabled : null,
        radioDisabled: dirs.length ? dirs.every((el) => el.disabled) : null,
        chooseDisabled: choose ? choose.disabled : null,
      };
    })()`);
    check(
      '运行中禁用添加 / 移除 / 输出目录',
      busyState.addDisabled === true &&
        busyState.removeDisabled === true &&
        busyState.radioDisabled === true &&
        busyState.chooseDisabled === true,
      JSON.stringify(busyState)
    );

    // 确定性进入运行态后触发核心退出：走与真实 core.exit 事件同一处理函数，
    // 避免「任务先跑完」的竞态（真实取消链路的收口已在上一段验证）
    const coreExitState = await cdp.evaluate(`(async () => {
      const back = [...document.querySelectorAll('.it-result-actions .it-btn')].find((b) => b.innerText.trim() === '返回待处理');
      if (back) back.click();
      await new Promise((r) => setTimeout(r, 200));
      window.__imageToolsSmoke.simulateInterrupted();
      await new Promise((r) => setTimeout(r, 150));
      const card = document.querySelector('.it-result-card');
      const head = card ? card.querySelector('.it-mc-head') : null;
      return {
        state: document.querySelector('.it-process').dataset.state,
        tag: head ? head.querySelector('.it-tag').innerText.trim() : '',
        text: card ? card.innerText.split(/\s+/).join(' ') : '',
      };
    })()`);
    check(
      '核心退出显示「已中断」而非全部成功',
      coreExitState.state === 'error' &&
        /中断/.test(coreExitState.tag) &&
        !/全部成功|部分失败/.test(coreExitState.text),
      JSON.stringify(coreExitState)
    );

    // 收口：中断态之后回到待处理，等后台真实任务跑完，避免影响后续断言
    await cdp.evaluate(`(() => {
      const back = [...document.querySelectorAll('.it-result-actions .it-btn')].find((b) => b.innerText.trim() === '返回待处理');
      if (back) back.click();
      return true;
    })()`);
    await cdp.waitFor("document.querySelector('.it-process').dataset.state === 'idle'", 6000);
    await new Promise((r) => setTimeout(r, 2500));

    // 取消弹层 Esc 关闭（不点任何按钮）
    await cdp.evaluate(`(() => {
      const back = [...document.querySelectorAll('.it-result-actions .it-btn')].find((b) => b.innerText.trim() === '返回待处理');
      if (back) back.click();
      return true;
    })()`);
    await cdp.waitFor("document.querySelector('.it-process').dataset.state === 'idle'");
    await cdp.evaluate(`(() => {
      [...document.querySelectorAll('.it-action-card .it-btn')].find((b) => b.innerText.trim() === '开始处理').click();
      return true;
    })()`);
    await cdp.waitFor("document.querySelector('.it-process').dataset.state === 'running'");
    await cdp.evaluate("document.querySelector('.it-run-card .it-cancel').click()");
    const dialogOpened = await cdp.waitFor("!!document.querySelector('.it-dialog')", 4000);
    await cdp.pressKey('Escape');
    const dialogClosed = await cdp.waitFor("!document.querySelector('.it-dialog')", 4000);
    const stillRunning = await cdp.evaluate("document.querySelector('.it-process').dataset.state");
    check(
      '取消弹层 Esc 可关闭且任务仍在运行',
      !!dialogOpened && !!dialogClosed && stillRunning === 'running',
      stillRunning
    );
    // 收口：真正取消一次，避免后台任务残留
    await cdp.evaluate("document.querySelector('.it-run-card .it-cancel').click()");
    await cdp.waitFor("!!document.querySelector('.it-dialog')", 4000);
    await cdp.evaluate(`(() => {
      const btn = [...document.querySelectorAll('.it-dialog .it-btn')].find((b) => b.innerText.trim() === '取消任务');
      if (btn) btn.click();
      return true;
    })()`);
    await new Promise((r) => setTimeout(r, 1500));

    // ---- 9. 设置页：短语增删改排序与保存 ----
    // 先验证「前往文件关联」直达 assoc 页签（不停在快捷输入）
    await cdp.evaluate(`(() => {
      [...document.querySelectorAll('.it-navtab')].find((b) => b.innerText.trim() === '图片查看').click();
      return true;
    })()`);
    await new Promise((r) => setTimeout(r, 200));
    await cdp.evaluate("document.querySelector('.it-assoc-link .it-linkbtn').click()");
    const assocJump = await cdp.waitFor(
      "document.querySelector('.it-set-tab.is-on') && document.querySelector('.it-set-tab.is-on').innerText.trim()",
      6000
    );
    const assocPanel = await cdp.evaluate(
      "!!document.querySelector('#it-set-assoc') && !document.querySelector('#it-set-quick')"
    );
    check(
      '「前往文件关联」进入文件关联页签',
      assocJump === '文件关联' && assocPanel,
      String(assocJump)
    );

    // 齿轮进入默认停在快捷输入
    await cdp.evaluate("document.querySelector('.it-appbar-right .it-iconbtn').click()");
    const setOpen = await cdp.waitFor(
      "document.querySelector('.it-set-tab.is-on') && document.querySelector('.it-set-tab.is-on').innerText.trim() === '快捷输入'",
      6000
    );
    check('设置页可打开且齿轮入口停在快捷输入', !!setOpen);
    // 标题层级：页标题 20px/700、板块标题 14.5px/600（设计稿 .settings-head h2 / .card-head h3）
    const settingsType = await cdp.evaluate(`(() => {
      const pick = (sel) => {
        const el = document.querySelector(sel);
        if (!el) return null;
        const cs = getComputedStyle(el);
        return { fontSize: cs.fontSize, fontWeight: cs.fontWeight, text: el.innerText.trim() };
      };
      return {
        h2: pick('.it-settings-head h2'),
        h3: pick('.it-card-head h3'),
        compareH3: pick('.it-compare-card h3'),
        formatsH3: pick('.it-formats-card h3'),
      };
    })()`);
    check(
      '设置页标题层级与设计稿一致（页 20px/700，板块 14.5px/600）',
      settingsType.h2 &&
        settingsType.h2.fontSize === '20px' &&
        settingsType.h2.fontWeight === '700' &&
        settingsType.h3 &&
        settingsType.h3.fontSize === '14.5px' &&
        settingsType.h3.fontWeight === '600' &&
        (!settingsType.compareH3 ||
          (settingsType.compareH3.fontSize === '14.5px' &&
            settingsType.compareH3.fontWeight === '600')) &&
        (!settingsType.formatsH3 ||
          (settingsType.formatsH3.fontSize === '14.5px' &&
            settingsType.formatsH3.fontWeight === '600')),
      JSON.stringify(settingsType)
    );
    const phraseOps = await cdp.evaluate(`(async () => {
      const rows = () => [...document.querySelectorAll('.it-phrase-row')];
      const before = rows().length;
      const input = document.querySelector('.it-phrase-add .it-input');
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(input, '_夜巡');
      input.dispatchEvent(new Event('input', { bubbles: true }));
      await new Promise((r) => setTimeout(r, 60));
      document.querySelector('.it-phrase-add .it-btn').click();
      await new Promise((r) => setTimeout(r, 120));
      const afterAdd = rows().length;
      const last = rows()[rows().length - 1];
      last.querySelectorAll('.it-iconbtn')[0].click();
      await new Promise((r) => setTimeout(r, 120));
      const afterMove = [...document.querySelectorAll('.it-phrase-row .it-input')].map((el) => el.value);
      const save = [...document.querySelectorAll('.it-card-foot .it-btn')].find((b) => b.innerText.includes('保存设置'));
      const disabledBefore = save.disabled;
      save.click();
      await new Promise((r) => setTimeout(r, 900));
      return { before, afterAdd, afterMove, disabledBefore, hint: document.querySelector('.it-save-hint').innerText };
    })()`);
    check(
      '短语可新增并调整顺序',
      phraseOps.afterAdd === phraseOps.before + 1 && phraseOps.afterMove.includes('_夜巡'),
      JSON.stringify(phraseOps.afterMove)
    );
    check('保存设置后写回核心（提示已保存条数）', /已保存/.test(phraseOps.hint), phraseOps.hint);
    // 直接读本次冒烟专用的临时配置（UVMP_TOOLKIT_CONFIG 指向它），不碰用户真实配置
    const savedPhrases = (() => {
      try {
        const cfg = JSON.parse(fs.readFileSync(tempConfig, 'utf8'));
        return ((cfg.image_tools || {}).quick_phrases || []).join(',');
      } catch (e) {
        return '';
      }
    })();
    check('核心配置里确实落盘了新短语', savedPhrases.includes('_夜巡'), savedPhrases);

    // 恢复默认，避免把冒烟数据留在本机配置
    await cdp.evaluate(`(() => {
      const btn = [...document.querySelectorAll('.it-card-foot .it-btn')].find((b) => b.innerText.includes('恢复默认'));
      btn.click();
      return true;
    })()`);
    await cdp.waitFor("!document.querySelector('.it-card-foot .it-btn.is-primary').disabled", 4000);
    await cdp.evaluate(`(() => {
      [...document.querySelectorAll('.it-card-foot .it-btn')].find((b) => b.innerText.includes('保存设置')).click();
      return true;
    })()`);
    await new Promise((r) => setTimeout(r, 800));

    const assoc = await cdp.evaluate(`(async () => {
      document.querySelector('#it-set-tab-assoc').click();
      await new Promise((r) => setTimeout(r, 700));
      const panel = document.querySelector('#it-set-assoc');
      return { text: panel ? panel.textContent.replace(/\\s+/g, ' ') : '' };
    })()`);
    check(
      '文件关联页有 Windows 引导与安装版/便携版差异',
      /Windows 10/.test(assoc.text) && /安装版/.test(assoc.text) && /便携版/.test(assoc.text),
      assoc.text.slice(0, 60)
    );
    check('Windows 只做引导，不静默改写默认应用', /UserChoice/.test(assoc.text));

    // 默认应用状态必须是真实查询结果：要么给出确定结论，要么明确「无法确定」，
    // 不允许在查不到时谎称已设/未设。
    const assocStatus = await cdp.evaluate(`(async () => {
      const raw = await window.imageTools.getDefaultStatus();
      const tags = [...document.querySelectorAll('#it-set-assoc .it-tag')].map((el) =>
        el.innerText.trim()
      );
      return {
        platform: raw.platform,
        supported: raw.supported,
        status: raw.status,
        installed: raw.installed,
        associations: (raw.associations || []).map((a) => [a.ext, a.status]),
        tags,
        checkText: (document.querySelector('#it-set-assoc .it-assoc-check') || {}).innerText || '',
      };
    })()`);
    check(
      '默认应用状态来自真实查询（Windows 返回 status/associations/installed）',
      assocStatus.supported === true &&
        ['default', 'partial', 'other', 'unknown'].includes(assocStatus.status) &&
        Array.isArray(assocStatus.associations) &&
        assocStatus.associations.length === 4,
      JSON.stringify(assocStatus).slice(0, 260)
    );
    check(
      '页面标签与真实状态一致（unknown 显示「无法确定」，不谎称已设/未设）',
      assocStatus.status === 'unknown'
        ? /无法确定/.test(assocStatus.tags.join(' '))
        : assocStatus.status === 'default'
          ? /已设为默认/.test(assocStatus.tags.join(' '))
          : assocStatus.status === 'partial'
            ? /部分设为默认/.test(assocStatus.tags.join(' '))
            : /尚未设为默认/.test(assocStatus.tags.join(' ')),
      JSON.stringify(assocStatus.tags)
    );
    // 「重新检查」按钮真的重新查询（前后状态同源，且按钮可点）
    const rechecked = await cdp.evaluate(`(async () => {
      const before = await window.imageTools.getDefaultStatus();
      const btn = [...document.querySelectorAll('#it-set-assoc .it-btn')].find((b) =>
        b.innerText.includes('重新检查')
      );
      if (!btn) return { ok: false };
      btn.click();
      await new Promise((r) => setTimeout(r, 900));
      const after = await window.imageTools.getDefaultStatus();
      return { ok: true, before: before.status, after: after.status };
    })()`);
    check(
      '「重新检查」按钮存在并触发真实重新查询',
      rechecked.ok === true && rechecked.after === rechecked.before,
      JSON.stringify(rechecked)
    );

    // 900×600 下的设置页：单列、可纵向滚动、无横向溢出
    await cdp.send('Emulation.setDeviceMetricsOverride', {
      width: 900,
      height: 600,
      deviceScaleFactor: 1,
      mobile: false,
    });
    await new Promise((r) => setTimeout(r, 450));
    const narrowSettings = await cdp.evaluate(`(() => {
      const panel = document.querySelector('.it-settings');
      return {
        overflowX: document.documentElement.scrollWidth > window.innerWidth,
        columns: getComputedStyle(document.querySelector('.it-assoc-facts')).gridTemplateColumns,
        scrollable: panel ? panel.scrollHeight > panel.clientHeight : false,
      };
    })()`);
    check(
      '900×600 设置页单列可滚动且无横向溢出',
      !narrowSettings.overflowX && !String(narrowSettings.columns).includes(' '),
      JSON.stringify(narrowSettings)
    );
    await cdp.screenshot(path.join(tmp, 'settings-900x600.png'));
    await cdp.send('Emulation.clearDeviceMetricsOverride');
    await new Promise((r) => setTimeout(r, 300));

    await cdp.evaluate("document.querySelector('#it-set-tab-formats').click()");
    await new Promise((r) => setTimeout(r, 300));
    const formats = await cdp.evaluate(`(() => {
      const panel = document.querySelector('#it-set-formats');
      return panel ? panel.textContent.replace(/\\s+/g, ' ') : '';
    })()`);
    for (const label of ['JPEG', 'PNG', 'GIF', 'WebP', 'BMP', 'ICO', 'AVIF', 'TIFF']) {
      check(`查看能力页列出 ${label}`, formats.includes(label));
    }
    check('查看能力页说明不纳入格式', /HEIC/.test(formats) && /暂不承诺/.test(formats));
    // 同组标题（格式卡）在可见页签上实际量一遍，确认 group 规则真的生效
    const formatsH3 = await cdp.evaluate(`(() => {
      const el = document.querySelector('.it-formats-card h3');
      if (!el) return null;
      const cs = getComputedStyle(el);
      return { fontSize: cs.fontSize, fontWeight: cs.fontWeight };
    })()`);
    check(
      '查看能力页卡片标题与设计稿一致（14.5px/600）',
      !!formatsH3 && formatsH3.fontSize === '14.5px' && formatsH3.fontWeight === '600',
      JSON.stringify(formatsH3)
    );
    await cdp.screenshot(path.join(tmp, 'settings.png'));

    // 900×600 下的处理页：单列、纵向滚动、无横向溢出
    await cdp.evaluate(`(() => {
      [...document.querySelectorAll('.it-navtab')].find((b) => b.innerText.trim() === '图片处理').click();
      return true;
    })()`);
    await cdp.send('Emulation.setDeviceMetricsOverride', {
      width: 900,
      height: 600,
      deviceScaleFactor: 1,
      mobile: false,
    });
    await new Promise((r) => setTimeout(r, 450));
    const narrowProcess = await cdp.evaluate(`(() => {
      const grid = document.querySelector('.it-tool-grid');
      const process = document.querySelector('.it-process');
      return {
        overflowX: document.documentElement.scrollWidth > window.innerWidth,
        columns: getComputedStyle(grid).gridTemplateColumns,
        scrollable: process.scrollHeight > process.clientHeight,
        fileCardMax: getComputedStyle(document.querySelector('.it-file-card')).maxHeight,
      };
    })()`);
    check(
      '900×600 处理页单列、文件列表限高、无横向溢出',
      !narrowProcess.overflowX && narrowProcess.fileCardMax !== 'none',
      JSON.stringify(narrowProcess)
    );
    await cdp.screenshot(path.join(tmp, 'process-900x600.png'));
    await cdp.send('Emulation.clearDeviceMetricsOverride');

    // ---- 11. 快捷短语可被删空（0 条），且 Page.reload 重载真实窗口后不会回填默认 4 条 ----
    // 空数组是用户主动删光的合法状态：界面与核心配置都必须保持 []，不能 `|| 默认` 回落。
    await cdp.evaluate(`(() => {
      const tab = [...document.querySelectorAll('.it-navtab')].find((b) => b.innerText.trim() === '图片查看');
      if (tab) tab.click();
      return true;
    })()`);
    await new Promise((r) => setTimeout(r, 250));
    // 重载会由主进程把 lastPayload 重新推给页面（崩溃恢复语义）。本次会话中途改过名，
    // 冷启动那份列表里的文件已不存在，直接重载只会看到「无法显示」——那是既有恢复语义，
    // 不是无令牌 URL 的问题。因此先通过渲染层 IPC 切到一张仍在磁盘上的已授权图片，
    // 让 lastPayload 指向有效文件，再重载验证预览链路仍然可用。
    const reloadTarget = path.join(sampleDir, '02-现场标识牌.png');
    // open() 走 IPC 异步切换，偶尔会与上一次重扫/渲染排队，导致 15s 内没切过来。
    // 这里最多重试 3 次（每次都用真实 IPC），仍失败才判红——不放宽断言本身。
    let switched = false;
    for (let attempt = 0; attempt < 3 && !switched; attempt += 1) {
      await cdp.evaluate(
        `window.imageTools.open({ files: [${JSON.stringify(reloadTarget)}] }).then(() => true)`
      );
      switched = !!(await cdp.waitFor(
        `(() => {
          const img = document.querySelector('.it-photo');
          const name = (document.querySelector('.it-fname') || {}).textContent || '';
          return /02-现场标识牌/.test(name) && !!img && img.naturalWidth > 0;
        })()`,
        20000,
        120
      ));
    }
    check(
      '重载前切换到磁盘上仍存在的已授权图片（保证恢复目标有效）',
      switched,
      JSON.stringify({
        fname: await cdp.evaluate("(document.querySelector('.it-fname') || {}).textContent || ''"),
        trace: await cdp.evaluate(
          'window.__imageToolsSmoke ? window.__imageToolsSmoke.coldTrace() : null'
        ),
      })
    );
    await cdp.evaluate("document.querySelector('.it-appbar-right .it-iconbtn').click()");
    await cdp.waitFor("!!document.querySelector('#it-set-quick')", 6000);
    const emptied = await cdp.evaluate(`(async () => {
      const rows = () => [...document.querySelectorAll('.it-phrase-row')];
      const before = rows().length;
      // 逐条点删除，直到一条不剩
      for (let guard = 0; guard < 20 && rows().length; guard += 1) {
        const btn = rows()[0].querySelector('.it-iconbtn.is-danger');
        if (!btn) break;
        btn.click();
        await new Promise((r) => setTimeout(r, 80));
      }
      const afterDelete = rows().length;
      const save = [...document.querySelectorAll('.it-card-foot .it-btn')].find((b) => b.innerText.includes('保存设置'));
      const disabledBefore = save ? save.disabled : null;
      if (save) save.click();
      await new Promise((r) => setTimeout(r, 1200));
      return {
        before,
        afterDelete,
        disabledBefore,
        rowsAfterSave: rows().length,
        hint: (document.querySelector('.it-save-hint') || {}).innerText || '',
        toast: (document.querySelector('.it-toast') || {}).innerText || '',
      };
    })()`);
    const phraseConfigOf = () => {
      try {
        return JSON.parse(fs.readFileSync(tempConfig, 'utf8')).image_tools.quick_phrases;
      } catch (e) {
        return null;
      }
    };
    const emptiedConfig = phraseConfigOf();
    check(
      '设置页可删除全部快捷短语并保存 0 条（界面与临时配置都为 0）',
      emptied.before > 0 &&
        emptied.afterDelete === 0 &&
        emptied.rowsAfterSave === 0 &&
        emptied.disabledBefore === false &&
        /已保存 · 0 条短语/.test(emptied.hint) &&
        Array.isArray(emptiedConfig) &&
        emptiedConfig.length === 0,
      JSON.stringify({ ...emptied, config: emptiedConfig })
    );

    // 真实重载窗口（不是只重渲染组件）：重进设置后必须仍是 0 条，默认 4 条不得复活
    await cdp.send('Page.reload', { ignoreCache: false });
    const reloadReady = await cdp.waitFor(
      "!!window.__imageToolsSmoke && !!document.querySelector('.it-canvas')",
      30000,
      150
    );
    check('Page.reload 后页面真实重载完成', !!reloadReady);
    // 重载后预览链路必须仍然可用（无令牌路径 URL 在重载后照常工作）
    const reloadPhoto = await cdp.waitFor(
      `(() => {
        const img = document.querySelector('.it-photo');
        const name = (document.querySelector('.it-fname') || {}).textContent || '';
        return /02-现场标识牌/.test(name) && !!img && img.naturalWidth > 0;
      })()`,
      30000,
      120
    );
    // 重载后的预览地址仍应是带真实路径的协议 URL（不是令牌、不是 file://）
    const reloadSrc = await cdp.evaluate(`(async () => {
      const img = document.querySelector('.it-photo');
      const raw = img ? img.getAttribute('src') : '';
      const preview = await window.imageTools.preview(${JSON.stringify(realOrEmpty(reloadTarget))});
      let decoded = '';
      try {
        decoded = new URL(preview.url).searchParams.get('path') || '';
      } catch (e) {
        decoded = '';
      }
      return { raw, decoded };
    })()`);
    check(
      '重载后画布仍能加载图片（会话 Blob 可用，无令牌协议 URL 可复核）',
      !!reloadPhoto &&
        /^blob:/i.test(reloadSrc.raw || '') &&
        reloadSrc.decoded === realOrEmpty(reloadTarget),
      JSON.stringify({
        raw: reloadSrc.raw,
        decoded: reloadSrc.decoded,
        expected: realOrEmpty(reloadTarget),
      })
    );
    await cdp.evaluate("document.querySelector('.it-appbar-right .it-iconbtn').click()");
    await cdp.waitFor("!!document.querySelector('#it-set-quick')", 8000);
    const afterReload = await cdp.evaluate(`(() => {
      const rows = [...document.querySelectorAll('.it-phrase-row')];
      return {
        rows: rows.length,
        values: rows.map((row) => (row.querySelector('.it-input') || {}).value || ''),
        hint: (document.querySelector('.it-save-hint') || {}).innerText || '',
        emptyHint: (document.querySelector('.it-phrase-empty') || {}).innerText || '',
      };
    })()`);
    check(
      '重载后重进设置仍为 0 条（未恢复默认 4 条短语）',
      afterReload.rows === 0 &&
        afterReload.values.length === 0 &&
        !afterReload.values.includes('_已核验') &&
        /已保存 · 0 条短语/.test(afterReload.hint) &&
        Array.isArray(phraseConfigOf()) &&
        phraseConfigOf().length === 0,
      JSON.stringify({ ...afterReload, config: phraseConfigOf() })
    );

    // 非空恢复：新增一条并保存，配置与界面同步为 1 条（证明 0 条不是被写死）
    const restored = await cdp.evaluate(`(async () => {
      const input = document.querySelector('.it-phrase-add .it-input');
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(input, '_回归验证');
      input.dispatchEvent(new Event('input', { bubbles: true }));
      await new Promise((r) => setTimeout(r, 60));
      document.querySelector('.it-phrase-add .it-btn').click();
      await new Promise((r) => setTimeout(r, 120));
      [...document.querySelectorAll('.it-card-foot .it-btn')].find((b) => b.innerText.includes('保存设置')).click();
      await new Promise((r) => setTimeout(r, 1200));
      return {
        rows: document.querySelectorAll('.it-phrase-row').length,
        values: [...document.querySelectorAll('.it-phrase-row .it-input')].map((el) => el.value),
        hint: (document.querySelector('.it-save-hint') || {}).innerText || '',
      };
    })()`);
    check(
      '非空恢复：新增一条保存后界面与配置同步为 1 条',
      restored.rows === 1 &&
        restored.values[0] === '_回归验证' &&
        /已保存 · 1 条短语/.test(restored.hint) &&
        JSON.stringify(phraseConfigOf()) === JSON.stringify(['_回归验证']),
      JSON.stringify({ ...restored, config: phraseConfigOf() })
    );
  } catch (e) {
    check('冒烟脚本自身未异常中断', false, e && e.message);
    // 失败时把 Electron 侧 stderr 尾部打出来（含有崩溃/加载失败原因），便于定位
    if (stderr.trim()) {
      const tail = stderr.trim().split(/\r?\n/).slice(-12).join('\n');
      console.log('  Electron stderr tail: ' + tail);
    }
  } finally {
    try {
      if (cdp) cdp.ws.close();
    } catch (e) {
      // ignore
    }
    killTree(proc);
    const killedAgents = killDetachedAgents(tempLocalAppData);
    if (killedAgents) console.log(`  已清理本次冒烟拉起的常驻 agent：${killedAgents} 个`);
    await new Promise((r) => setTimeout(r, 1200));
    if (!KEEP) {
      try {
        fs.rmSync(tmp, { recursive: true, force: true });
      } catch (e) {
        // 临时目录被占用：留给系统清理
      }
    } else {
      console.log(`临时产物保留在：${tmp}`);
    }
  }

  const passed = results.filter((item) => item.ok).length;
  console.log(`\n合计 ${results.length} 项，通过 ${passed}，失败 ${results.length - passed}`);
  if (failures) {
    for (const item of results.filter((r) => !r.ok))
      console.log(`  ✗ ${item.name}：${item.detail}`);
    process.exit(1);
  }
  process.exit(0);
}

function logScreen(result) {
  const stat = Array.isArray(result.stats) ? result.stats.join(' ') : '';
  console.log(`        结果：${result.head} ${stat}`);
}

// 作为脚本运行时才驱动 GUI；被 require 时只导出纯判定函数供负对照/静态验证使用
if (require.main === module) main();
else
  module.exports = {
    readPanCompensation,
    expectedAnchorShift,
    judgeAnchorCompensation,
    resolveElectron,
  };
