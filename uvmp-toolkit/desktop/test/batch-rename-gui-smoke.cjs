// 批量重命名插件真实 GUI 端到端冒烟（本仓 Electron + ws/CDP，无新增依赖）。
//
// 覆盖：主窗口侧栏点击「批量重命名」→ 真实文件选择 IPC（仅系统对话框打桩，只返回临时样本）
// → 混合 JPG/DOCX/TXT 列表与实时预览 → 磁盘同名冲突不覆盖 → 二次确认 → 真实改名落盘
// → 结果态与日志 → 页内最近执行记录 → 执行历史「参数回填」→ 960×620 关键控件。
//
// 用法（在 uvmp-toolkit/desktop 下）：
//   node test/batch-rename-gui-smoke.cjs
//   node test/batch-rename-gui-smoke.cjs --keep   # 保留临时样本与截图供排查
//   UVMP_ELECTRON_EXE=<绝对路径的打包客户端> node test/batch-rename-gui-smoke.cjs
//     发版机只有打包客户端（源码 electron 二进制未下载）时用这条：跑的是真实打包壳 +
//     随包冻结核心，对话框打桩改走主进程调试端口。
//
// 退出码：0 通过；1 冒烟失败；2 环境不满足（缺 Electron / dist 未构建 / 无 Python）。
// 说明：配置、userData、运行状态全部隔离到系统临时目录；不联网、不写仓内文件、不装系统组件。
// 唯一打桩点是 electron.dialog.showOpenDialog（源码态经 NODE_OPTIONS 注入钩子，打包态经
// 主进程调试端口替换），只返回本次临时样本；其余全部走真实主进程、真实 preload IPC、
// 真实核心 RPC 与真实渲染层。

const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawn, spawnSync } = require('child_process');
const WebSocket = require('ws');

const DESKTOP = path.resolve(__dirname, '..');
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
// 与图片冒烟错开端口段，两个冒烟可并行
const PORT = 9510 + Math.floor(Math.random() * 40);
// 打包态打桩用的主进程调试端口（源码态不打桩，NODE_OPTIONS 已够用）
const INSPECT_PORT = 9610 + Math.floor(Math.random() * 40);
const KEEP = process.argv.slice(2).includes('--keep');

const results = [];
let failures = 0;
let cleanupOnExit = null;

function check(name, ok, detail) {
  results.push({ name, ok: !!ok, detail: detail == null ? '' : String(detail) });
  if (!ok) failures += 1;
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}${detail == null ? '' : ` — ${detail}`}`);
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

// Windows 上 `python` 可能是 App Execution Alias，隔离 APPDATA 后解析不到运行时，
// 因此必须取绝对解释器路径（与图片冒烟同因）。
function resolvePython() {
  const probeArgs = [
    '-c',
    'import sys, site; print(sys.executable); print(site.getusersitepackages())',
  ];
  for (const candidate of [process.env.UVMP_PYTHON, 'python', 'python3']) {
    if (!candidate) continue;
    const probe = spawnSync(candidate, probeArgs, { encoding: 'utf8', windowsHide: true });
    if (probe.status !== 0) continue;
    const [exe, userSite] = String(probe.stdout).trim().split(/\r?\n/);
    if (exe && fs.existsSync(exe)) return { cmd: candidate, exe, userSite: userSite || '' };
  }
  return null;
}

// 主进程按需 require('electron') 时把对话框换成「返回本次样本」，其余对话框行为不受影响。
function writeDialogStub(file, payloadFile, markerFile, loadedMark) {
  const lines = [
    "const fs = require('fs');",
    "const Module = require('module');",
    "if (process.type !== 'browser') return;",
    `const payloadFile = ${JSON.stringify(payloadFile)};`,
    `const markerFile = ${JSON.stringify(markerFile)};`,
    'const origLoad = Module._load;',
    'let patched = false;',
    'Module._load = function (request, parent, isMain) {',
    '  const exported = origLoad.apply(this, arguments);',
    "  if (!patched && request === 'electron' && exported && exported.dialog) {",
    '    patched = true;',
    '    exported.dialog.showOpenDialog = async () => {',
    '      const payload = JSON.parse(fs.readFileSync(payloadFile, "utf8") || "{}");',
    '      fs.appendFileSync(markerFile, JSON.stringify({ at: Date.now(), files: payload.files || [] }) + "\\n");',
    '      return { canceled: false, filePaths: payload.files || [] };',
    '    };',
    `    fs.writeFileSync(${JSON.stringify(loadedMark)}, 'patched');`,
    '  }',
    '  return exported;',
    '};',
  ];
  fs.writeFileSync(file, lines.join('\n'));
}

// 打包态没有 NODE_OPTIONS 注入（Electron 打包壳直接忽略该变量，已实测），
// 改走主进程调试端口：连上 --inspect 的 node 目标，在主进程里替换同一个 dialog 对象。
// 打桩范围仍只限本次临时样本目录：样本外的路径一律丢弃并记入 marker，供断言核对。
async function patchDialogInMainProcess({ port, payloadFile, markerFile, loadedMark, sampleDir }) {
  const deadline = Date.now() + 40000;
  let target = null;
  while (Date.now() < deadline && !target) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
      target = list.find((item) => item.type === 'node' && item.webSocketDebuggerUrl) || null;
    } catch (e) {
      target = null;
    }
    if (!target) await new Promise((r) => setTimeout(r, 250));
  }
  if (!target) return { ok: false, reason: `主进程调试端口 ${port} 未就绪` };
  const inspect = new Cdp(target.webSocketDebuggerUrl);
  try {
    await inspect.ready();
    await inspect.send('Runtime.enable');
    const expression = `(() => {
      const electron = process.mainModule && process.mainModule.require('electron');
      const fsMod = process.mainModule && process.mainModule.require('fs');
      if (!electron || !electron.dialog || !fsMod) return 'no-electron';
      const payloadFile = ${JSON.stringify(payloadFile)};
      const markerFile = ${JSON.stringify(markerFile)};
      const root = ${JSON.stringify(path.resolve(sampleDir).toLowerCase())};
      electron.dialog.showOpenDialog = async () => {
        const payload = JSON.parse(fsMod.readFileSync(payloadFile, 'utf8') || '{}');
        const raw = payload.files || [];
        const files = raw.filter(
          (p) => typeof p === 'string' && p && path_is_inside(p, root)
        );
        fsMod.appendFileSync(
          markerFile,
          JSON.stringify({ at: Date.now(), files, dropped: raw.length - files.length }) + '\\n'
        );
        return { canceled: false, filePaths: files };
      };
      function path_is_inside(candidate, rootDir) {
        const resolved = String(candidate).replace(/\\\\/g, '/').toLowerCase();
        return resolved.startsWith(rootDir.replace(/\\\\/g, '/'));
      }
      return 'patched';
    })()`;
    const result = await inspect.evaluate(expression);
    if (result !== 'patched') return { ok: false, reason: `打桩返回 ${String(result)}` };
    // 与源码态 NODE_OPTIONS 钩子一致：替换成功即落标记（断言在点击「添加文件」之前就查它）
    fs.writeFileSync(loadedMark, 'patched');
    return { ok: true };
  } catch (e) {
    return { ok: false, reason: String((e && e.message) || e) };
  } finally {
    try {
      inspect.ws.close();
    } catch (e) {
      // 连接可能已断
    }
  }
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

  async sendRetry(method, params = {}, { attempts = 2, timeoutMs = 20000 } = {}) {
    let lastError = null;
    for (let i = 0; i < attempts; i += 1) {
      try {
        return await this.send(method, params, timeoutMs);
      } catch (e) {
        lastError = e;
        await new Promise((r) => setTimeout(r, 300));
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
      throw new Error(`页面求值异常：${text}\n  表达式：${String(expression).slice(0, 160)}`);
    }
    return result.result ? result.result.value : undefined;
  }

  // 渲染进程忙时单次 evaluate 可能整体超时：吞掉继续等到截止时间，到点仍不满足返回最后值。
  async waitFor(expression, timeoutMs = 15000, step = 200) {
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

  async screenshot(file) {
    const shot = await this.sendRetry(
      'Page.captureScreenshot',
      { format: 'png' },
      { attempts: 3, timeoutMs: 30000 }
    );
    fs.writeFileSync(file, Buffer.from(shot.data, 'base64'));
  }
}

async function listPages() {
  try {
    const res = await fetch(`http://127.0.0.1:${PORT}/json/list`);
    const list = await res.json();
    return list.filter(
      (item) => item.type === 'page' && /index\.html/.test(item.url) && item.webSocketDebuggerUrl
    );
  } catch (e) {
    return [];
  }
}

async function waitMainPage(deadline) {
  while (Date.now() < deadline) {
    const pages = await listPages();
    if (pages.length) {
      // 冷启动可能重建窗口：等页面集合在连续两次采样中稳定后再绑定
      const firstId = pages[0].id;
      await new Promise((r) => setTimeout(r, 350));
      const again = await listPages();
      const stable = again.find((item) => item.id === firstId) || again[0];
      if (stable) return stable;
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  return null;
}

// 冒烟只拉起 core 子进程；万一有脱离进程树的残留（本项目核心会 detached 拉起 agent），
// 按「命令行包含本次临时目录」精确清理，绝不碰用户真实进程。
function killTempPython(tempDir) {
  if (process.platform !== 'win32' || !tempDir) return 0;
  const script = [
    `$needle = '${tempDir.replace(/'/g, "''")}'`,
    'Get-CimInstance Win32_Process -Filter "Name like \'%python%\'" |',
    '  Where-Object { $_.CommandLine -and $_.CommandLine.Contains($needle) } |',
    '  ForEach-Object { try { Stop-Process -Id $_.ProcessId -Force -ErrorAction Stop; $_.ProcessId } catch {} }',
  ].join(' ');
  const r = spawnSync('powershell', ['-NoProfile', '-Command', script], {
    encoding: 'utf8',
    windowsHide: true,
    timeout: 30000,
  });
  return String(r.stdout || '')
    .split(/\r?\n/)
    .filter((line) => line.trim()).length;
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

function makeSamples(dir) {
  const files = {
    'IMG_2043.JPG': 'jpeg-bytes-for-rename-only',
    '缺陷记录.docx': 'docx-bytes',
    '巡检说明.txt': 'xun-jian-original',
    // 预置同名目标：执行时绝不能被覆盖
    '2026秋检_巡检说明.txt': 'KEEP-ORIGINAL',
  };
  for (const [name, body] of Object.entries(files)) {
    fs.writeFileSync(path.join(dir, name), body);
  }
  return files;
}

// 页面侧：一次取回预览态的可断言快照
const PREVIEW_SNAPSHOT = `(() => {
  const text = (el) => (el ? el.innerText.replace(/\\s+/g, ' ').trim() : '');
  const rows = [...document.querySelectorAll('.pv-row')].map((row) => ({
    tag: text(row.querySelector('.tag')),
    tone: row.classList.contains('is-conflict') ? 'conflict' : '',
    old: text(row.querySelector('.nm')),
    fresh: text(row.querySelector('.new-name')),
    reason: text(row.querySelector('.reason')),
  }));
  const sum = (sel) => {
    const el = document.querySelector(sel);
    return el ? Number(text(el)) : null;
  };
  const run = document.querySelector('.rename-runbar .t-button');
  return {
    rows,
    total: sum('.list-foot b'),
    can: sum('.run-sum .it.ok b'),
    skip: sum('.run-sum .it.skip b'),
    same: sum('.run-sum .it.same b'),
    runDisabled: run ? run.disabled : null,
    runText: text(run),
    source: text(document.querySelector('.rename-source')),
    summary: text(document.querySelector('.rename-card .card-head .right')),
  };
})()`;

async function main() {
  const electron = resolveElectron();
  if (electron.error) fail(2, electron.error);
  if (!fs.existsSync(electron.exe)) fail(2, `找不到 Electron：${electron.exe}`);
  if (!fs.existsSync(path.join(DESKTOP, 'dist', 'index.html'))) {
    fail(2, '缺少 dist/index.html，请先 npm run build');
  }
  const py = resolvePython();
  if (!py) fail(2, '没有可用的 Python 解释器，核心无法启动');
  console.log(`客户端：${electron.exe}${electron.packaged ? '（打包态）' : '（源码态）'}`);

  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'batch-gui-'));
  const sampleDir = path.join(tmp, '样本');
  fs.mkdirSync(sampleDir);
  const files = makeSamples(sampleDir);
  // 配置与运行状态全部隔离：绝不写用户真实 config.json / 任务历史 / 状态目录
  const tempConfig = path.join(tmp, 'config.json');
  fs.copyFileSync(path.join(DESKTOP, '..', 'toolkit', 'config.example.json'), tempConfig);
  const tempAppData = path.join(tmp, 'AppData');
  const tempLocalAppData = path.join(tmp, 'LocalAppData');
  fs.mkdirSync(tempAppData);
  fs.mkdirSync(tempLocalAppData);

  const payloadFile = path.join(tmp, 'picker-payload.json');
  const markerFile = path.join(tmp, 'picker-calls.jsonl');
  const loadedMark = path.join(tmp, 'picker-stub-loaded.txt');
  const stubFile = path.join(tmp, 'dialog-stub.cjs');
  const picked = ['IMG_2043.JPG', '缺陷记录.docx', '巡检说明.txt'].map((name) =>
    path.join(sampleDir, name)
  );
  fs.writeFileSync(payloadFile, JSON.stringify({ files: picked }));
  // 源码态用 NODE_OPTIONS 注入钩子；打包态该变量被忽略，改为连主进程调试端口打桩。
  if (!electron.packaged) writeDialogStub(stubFile, payloadFile, markerFile, loadedMark);
  console.log(`临时目录：${tmp}\n样本：${picked.join(' | ')}`);

  const launchArgs = [
    ...electron.args,
    `--remote-debugging-port=${PORT}`,
    '--remote-allow-origins=*',
  ];
  if (electron.packaged) launchArgs.push(`--inspect=${INSPECT_PORT}`);
  const proc = spawn(electron.exe, launchArgs, {
    cwd: DESKTOP,
    env: Object.assign({}, process.env, {
      // 只打桩系统文件对话框，返回临时样本（保留调用方已有的 NODE_OPTIONS）。
      // 打包态不打这条：Electron 打包壳完全忽略 NODE_OPTIONS（已实测），打桩由下面的主进程调试端口完成。
      NODE_OPTIONS: electron.packaged
        ? process.env.NODE_OPTIONS
        : [process.env.NODE_OPTIONS, `--require "${stubFile.replace(/\\/g, '/')}"`]
            .filter(Boolean)
            .join(' '),
      UVMP_PYTHON: py.exe,
      UVMP_TOOLKIT_CONFIG: tempConfig,
      LOCALAPPDATA: tempLocalAppData,
      APPDATA: tempAppData,
      // 绕过单实例锁；IMAGE 变体关掉主窗口 CI 自检（否则自检跑完 process.exit(0) 会掐断本脚本）
      UVMP_CI_SMOKE: '1',
      UVMP_CI_IMAGE_SMOKE: '1',
      PYTHONPATH: [py.userSite, process.env.PYTHONPATH].filter(Boolean).join(path.delimiter),
    }),
    stdio: ['ignore', 'pipe', 'pipe'],
    windowsHide: true,
  });
  let stderr = '';
  proc.stderr.on('data', (chunk) => {
    stderr += String(chunk);
    if (stderr.length > 8000) stderr = stderr.slice(-8000);
  });

  cleanupOnExit = () => {
    killTree(proc);
    killTempPython(tmp);
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
    const page = await waitMainPage(Date.now() + 40000);
    if (!page) fail(1, `主窗口没有出现（stderr 尾部：${stderr.slice(-600)}）`);
    cdp = new Cdp(page.webSocketDebuggerUrl);
    await cdp.ready();
    await cdp.send('Runtime.enable');
    await cdp.send('Page.enable');

    // 打包态：主进程起来后再替换 dialog（与源码态 NODE_OPTIONS 钩子等价，范围同样只限临时样本）
    if (electron.packaged) {
      const patch = await patchDialogInMainProcess({
        port: INSPECT_PORT,
        payloadFile,
        markerFile,
        loadedMark,
        sampleDir,
      });
      check('打包态主进程对话框打桩成功', patch.ok === true, patch.ok ? '' : patch.reason);
    }

    // ---- 1. 主窗口就绪，侧栏点击「批量重命名」 ----
    const navReady = await cdp.waitFor(
      `(() => { const b = [...document.querySelectorAll('.nav-item')].find((el) => el.innerText.includes('批量重命名')); return b && document.querySelector('#app').childElementCount > 0 ? b.innerText.trim() : false; })()`,
      40000
    );
    check('主窗口渲染完成，侧栏有「批量重命名」入口', !!navReady, String(navReady));
    check('系统文件对话框已按临时样本打桩', fs.existsSync(loadedMark));

    await cdp.evaluate(`(() => {
      [...document.querySelectorAll('.nav-item')].find((el) => el.innerText.includes('批量重命名')).click();
      return true;
    })()`);
    const entered = await cdp.waitFor(
      `(() => {
        const page = document.querySelector('.rename-page');
        const head = document.querySelector('.page-header, .page-head, header');
        return page && document.body.innerText.includes('批量重命名') ? { hasPage: true, empty: !!document.querySelector('.empty-block') } : false;
      })()`,
      15000
    );
    check(
      '点击入口进入插件页（空列表提示）',
      !!entered && entered.hasPage === true && entered.empty === true,
      JSON.stringify(entered)
    );

    // ---- 2. 真实 IPC 选择混合文件（JPG/DOCX/TXT）----
    await cdp.evaluate(`(() => {
      const btn = [...document.querySelectorAll('.src-acts .t-button')].find((b) => b.innerText.trim() === '添加文件');
      btn.click();
      return true;
    })()`);
    const pickedState = await cdp.waitFor(
      `(() => {
        const rows = document.querySelectorAll('.pv-row');
        return rows.length === 3 ? {
          rows: rows.length,
          metas: [...document.querySelectorAll('.pv-row .nm-meta')].map((el) => el.innerText.trim()),
          source: document.querySelector('.rename-source').innerText.replace(/\\s+/g, ' ').trim(),
        } : false;
      })()`,
      25000
    );
    const pickerCalls = fs
      .readFileSync(markerFile, 'utf8')
      .split(/\r?\n/)
      .filter((line) => line.trim())
      .map((line) => JSON.parse(line));
    check(
      '真实的文件选择 IPC 返回 3 个混合文件并进入列表',
      !!pickedState && pickedState.rows === 3 && /3 个文件/.test(pickedState.source),
      JSON.stringify(pickedState) + ` | picker=${JSON.stringify(pickerCalls)}`
    );
    check(
      '列表区分 JPG / DOCX / TXT 三种类型，且未越出临时样本目录',
      !!pickedState &&
        /JPG · 图片/.test(pickedState.metas.join(' ')) &&
        /DOCX · 文档/.test(pickedState.metas.join(' ')) &&
        /TXT · 文本/.test(pickedState.metas.join(' ')) &&
        pickedState.rows === 3,
      pickedState && pickedState.metas.join(' | ')
    );
    const pickerPaths = pickerCalls.flatMap((call) => call.files || []);
    check(
      '文件选择只来自打桩的临时样本（一次调用、3 条路径都在临时目录）',
      pickerCalls.length === 1 &&
        pickerPaths.length === 3 &&
        pickerPaths.every((p) => path.resolve(p).startsWith(path.resolve(sampleDir))),
      JSON.stringify(pickerPaths)
    );

    // ---- 3. 规则联动：查找替换 + 前缀，预览实时刷新 ----
    async function setInputByPlaceholder(placeholder, value) {
      return cdp.evaluate(`(() => {
        const input = document.querySelector('input[placeholder=${JSON.stringify(placeholder)}]');
        if (!input) return false;
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
        setter.call(input, ${JSON.stringify(value)});
        input.dispatchEvent(new Event('input', { bubbles: true }));
        return true;
      })()`);
    }
    check('找到「查找内容」输入框', await setInputByPlaceholder('查找内容', 'IMG_'));
    check('找到「替换为」输入框', await setInputByPlaceholder('替换为（可留空）', '现场巡检_'));
    check('找到「前缀」输入框', await setInputByPlaceholder('例如 2026秋检_', '2026秋检_'));

    const previewState = await cdp.waitFor(
      `(() => {
        const snap = ${PREVIEW_SNAPSHOT};
        return snap.rows.length === 3 && snap.rows.every((row) => row.fresh && row.fresh !== '等待预检') && snap.can !== null ? snap : false;
      })()`,
      20000
    );
    check(
      '预览实时算出新名称与可执行数量（可改名 2 / 跳过 1）',
      !!previewState &&
        previewState.can === 2 &&
        previewState.skip === 1 &&
        previewState.same === 0,
      previewState &&
        JSON.stringify({ can: previewState.can, skip: previewState.skip, same: previewState.same })
    );
    check(
      '规则摘要是「1 组替换 · 前缀」',
      !!previewState && previewState.summary === '1 组替换 · 前缀',
      previewState && previewState.summary
    );
    const previewRows = (previewState && previewState.rows) || [];
    check(
      '磁盘同名项就地标注「磁盘已有」并保持原名（不覆盖）',
      previewRows.some(
        (row) =>
          row.tag === '磁盘已有' &&
          row.reason.includes('已存在') &&
          row.old.includes('巡检说明') &&
          row.tone === 'conflict'
      ),
      JSON.stringify(previewRows)
    );
    check(
      '其余两项给出预期新名（IMG_ 替换 + 统一前缀）',
      previewRows.some((row) => row.fresh.includes('现场巡检_2043.JPG')) &&
        previewRows.some((row) => row.fresh.includes('2026秋检_缺陷记录.docx')),
      JSON.stringify(previewRows.map((row) => row.fresh))
    );
    check(
      '执行按钮数量与可改名数一致（2），无规则误触发执行',
      !!previewState &&
        previewState.runDisabled === false &&
        previewState.runText.includes('执行重命名') &&
        previewState.runText.includes('2'),
      previewState && previewState.runText
    );
    await cdp.screenshot(path.join(tmp, 'preview-1160x780.png'));

    // ---- 3b. 960×620（主窗口最小尺寸）关键控件：在可执行预览态下量 ----
    const before = await cdp.evaluate('({ w: window.innerWidth, h: window.innerHeight })');
    await cdp.evaluate('(() => { window.resizeTo(960, 620); return true; })()');
    await new Promise((r) => setTimeout(r, 1200));
    const resized = await cdp.evaluate('({ w: window.innerWidth, h: window.innerHeight })');
    let sizeMode = `原生 resizeTo ${before.w}x${before.h} → ${resized.w}x${resized.h}`;
    if (resized.w > 1000) {
      // 原生 resizeTo 被策略拦下时退回内容区模拟，断言的仍是同一套布局
      await cdp.send('Emulation.setDeviceMetricsOverride', {
        width: 960,
        height: 620,
        deviceScaleFactor: 1,
        mobile: false,
      });
      await new Promise((r) => setTimeout(r, 500));
      const emu = await cdp.evaluate('({ w: window.innerWidth, h: window.innerHeight })');
      sizeMode += `（原生被拦，改用内容区 960×620：${emu.w}x${emu.h}）`;
    }
    const narrow = await cdp.evaluate(`(() => {
      const text = (el) => (el ? el.innerText.replace(/\\s+/g, ' ').trim() : '');
      const visible = (el) => {
        if (!el) return false;
        const rect = el.getBoundingClientRect();
        const style = getComputedStyle(el);
        return rect.width > 0 && rect.height > 0 && style.visibility !== 'hidden' && style.display !== 'none';
      };
      const inViewport = (el) => {
        if (!visible(el)) return false;
        const rect = el.getBoundingClientRect();
        return rect.top >= -1 && rect.bottom <= window.innerHeight + 1 && rect.left >= -1 && rect.right <= window.innerWidth + 1;
      };
      const runBtn = document.querySelector('.rename-runbar .t-button');
      const addBtn = [...document.querySelectorAll('.src-acts .t-button')].find((b) => b.innerText.trim() === '添加文件');
      const folderBtn = [...document.querySelectorAll('.src-acts .t-button')].find((b) => b.innerText.trim() === '选择文件夹');
      const work = document.querySelector('.rename-work');
      return {
        width: window.innerWidth,
        height: window.innerHeight,
        overflowX: document.documentElement.scrollWidth > window.innerWidth,
        columns: work ? getComputedStyle(work).gridTemplateColumns : '',
        runVisible: inViewport(runBtn),
        runText: text(runBtn),
        addVisible: inViewport(addBtn),
        folderVisible: inViewport(folderBtn),
        sections: [...document.querySelectorAll('.rename-rules .rsec')].map((el) => text(el.querySelector('.rsec-head b'))),
        rulesVisible: inViewport(document.querySelector('.rename-rules')),
        rows: document.querySelectorAll('.pv-row').length,
        firstRowVisible: inViewport(document.querySelector('.pv-row')),
        runbarInViewport: inViewport(document.querySelector('.rename-runbar')),
        summaryVisible: text(document.querySelector('.rename-card .card-head .right')),
      };
    })()`);
    check(
      '960×620：执行按钮、来源操作、规则面板与列表全部可见',
      !!narrow &&
        narrow.width <= 1000 &&
        narrow.runVisible === true &&
        narrow.addVisible === true &&
        narrow.folderVisible === true &&
        narrow.rulesVisible === true &&
        narrow.firstRowVisible === true &&
        narrow.rows === 3,
      `${sizeMode} | ${JSON.stringify(narrow)}`
    );
    check(
      '960×620：四段规则完整保留、执行条可点且不被挤出视口',
      !!narrow &&
        narrow.sections.join(',') === '查找替换,前后缀,统一主名 + 序号,冲突处理' &&
        narrow.runbarInViewport === true &&
        /执行重命名 2$/.test(narrow.runText),
      narrow && `${narrow.sections.join(',')} | ${narrow.runText}`
    );
    check(
      '960×620：无横向溢出（规则列收窄到 268px）',
      !!narrow && narrow.overflowX === false && !/340px/.test(narrow.columns),
      narrow && `columns=${narrow.columns} overflowX=${narrow.overflowX}`
    );
    await cdp.screenshot(path.join(tmp, 'preview-960x620.png'));

    await cdp.send('Emulation.clearDeviceMetricsOverride');
    await new Promise((r) => setTimeout(r, 400));
    await cdp.evaluate('(() => { window.resizeTo(1160, 780); return true; })()');
    await new Promise((r) => setTimeout(r, 800));
    const wide = await cdp.evaluate(
      '({ w: window.innerWidth, overflowX: document.documentElement.scrollWidth > window.innerWidth })'
    );
    check(
      '恢复默认窗口后无横向溢出',
      wide.w > 1000 && wide.overflowX === false,
      JSON.stringify(wide)
    );
    await cdp.screenshot(path.join(tmp, 'final-1160x780.png'));

    // ---- 4. 二次确认弹层 ----
    await cdp.evaluate(`(() => {
      document.querySelector('.rename-runbar .t-button').click();
      return true;
    })()`);
    const confirmState = await cdp.waitFor(
      `(() => {
        const dlg = document.querySelector('.t-dialog');
        if (!dlg) return false;
        const btn = [...dlg.querySelectorAll('.t-button')].find((b) => b.innerText.includes('确认重命名'));
        return btn ? { text: dlg.innerText.replace(/\\s+/g, ' ').trim(), btn: btn.innerText.replace(/\\s+/g, ' ').trim() } : false;
      })()`,
      8000
    );
    check(
      '执行前弹出二次确认（写明数量、不覆盖、不可撤销）',
      !!confirmState &&
        /2\s*个文件将被重命名/.test(confirmState.text) &&
        /不会覆盖/.test(confirmState.text) &&
        /不提供撤销/.test(confirmState.text),
      confirmState && confirmState.text.slice(0, 180)
    );

    // ---- 5. 真实执行：改名落盘 ----
    await cdp.evaluate(`(() => {
      const dlg = document.querySelector('.t-dialog');
      [...dlg.querySelectorAll('.t-button')].find((b) => b.innerText.includes('确认重命名')).click();
      return true;
    })()`);
    const resultView = await cdp.waitFor(
      `(() => {
        const head = document.querySelector('.res-head');
        if (!head) return false;
        const tiles = [...document.querySelectorAll('.res-tiles .tile')].map((tile) => tile.innerText.replace(/\\s+/g, ' ').trim());
        return {
          heading: head.querySelector('h2') ? head.querySelector('h2').innerText.trim() : '',
          tiles,
          rows: [...document.querySelectorAll('.res-row')].map((row) => row.innerText.replace(/\\s+/g, ' ').trim()),
          outcome: document.querySelector('.result-head') ? document.querySelector('.result-head').innerText.replace(/\\s+/g, ' ').trim() : '',
          summary: document.querySelector('.result-summary') ? document.querySelector('.result-summary').innerText.trim() : '',
          artifact: [...document.querySelectorAll('.result-head .t-button')].map((b) => b.innerText.trim()),
        };
      })()`,
      60000,
      300
    );
    check(
      '任务执行到结果态并给出终态标题',
      !!resultView && /重命名完成/.test(resultView.heading),
      resultView && JSON.stringify(resultView.heading)
    );
    check(
      '结果计数正确（成功 2 / 失败 0 / 跳过 1 / 未变化 0）',
      !!resultView &&
        /^2 成功$/.test(resultView.tiles[0] || '') &&
        /^0 失败$/.test(resultView.tiles[1] || '') &&
        /^1 跳过$/.test(resultView.tiles[2] || '') &&
        /^0 未变化$/.test(resultView.tiles[3] || ''),
      resultView && resultView.tiles.join(' | ')
    );
    check(
      '结果摘要与产物目录入口（打开原目录）',
      !!resultView &&
        /成功 2/.test(resultView.summary) &&
        /跳过 1/.test(resultView.summary) &&
        resultView.artifact.includes('打开原目录'),
      resultView && `${resultView.summary} | ${resultView.artifact.join(',')}`
    );
    check(
      '结果列表逐项给出状态、原名与目标名',
      !!resultView &&
        resultView.rows.length === 3 &&
        resultView.rows.some((row) => row.includes('成功') && row.includes('现场巡检_2043.JPG')) &&
        resultView.rows.some((row) => row.includes('跳过') && row.includes('巡检说明.txt')),
      resultView && JSON.stringify(resultView.rows)
    );

    // TaskResult 日志：确认成功/跳过写入执行日志（textContent：隐藏时 innerText 为空）
    await cdp.evaluate(`(() => {
      const el = document.querySelector('.logview[data-log-expanded="1"]');
      const visible = el && el.offsetParent !== null;
      const toggle = [...document.querySelectorAll('.log-toggle')].find((b) => b.innerText.includes('排错日志'));
      if (!visible && toggle) toggle.click();
      return true;
    })()`);
    await new Promise((r) => setTimeout(r, 300));
    const logText = await cdp.evaluate(
      `(() => { const el = document.querySelector('.logview[data-log-expanded="1"]'); return el ? el.textContent : ''; })()`
    );
    check(
      '执行日志记录预检、成功与跳过原因',
      /预检完成：共 3 项/.test(logText) &&
        /重命名完成：成功 2，失败 0/.test(logText) &&
        /已存在/.test(logText),
      String(logText).slice(-240)
    );

    const dirNow = fs.readdirSync(sampleDir);
    check(
      '磁盘上确实完成改名（新名存在、原名消失）',
      dirNow.includes('2026秋检_现场巡检_2043.JPG') &&
        dirNow.includes('2026秋检_缺陷记录.docx') &&
        !dirNow.includes('IMG_2043.JPG') &&
        !dirNow.includes('缺陷记录.docx'),
      dirNow.join(',')
    );
    const keptTarget = path.join(sampleDir, '2026秋检_巡检说明.txt');
    const keptBody = fs.existsSync(keptTarget) ? fs.readFileSync(keptTarget, 'utf8') : '(缺失)';
    check(
      '冲突项绝不覆盖：预置同名目标内容保持 KEEP-ORIGINAL，源文件仍在',
      keptBody === files['2026秋检_巡检说明.txt'] &&
        dirNow.includes('巡检说明.txt') &&
        fs.readFileSync(path.join(sampleDir, '巡检说明.txt'), 'utf8') === files['巡检说明.txt'],
      `目标内容=${keptBody}`
    );
    check(
      '文件总数不变（改名不外溢、不产生临时残留）',
      dirNow.length === Object.keys(files).length,
      `${dirNow.length} 个：${dirNow.join(',')}`
    );
    await cdp.screenshot(path.join(tmp, 'result-1160x780.png'));

    // ---- 6. 页内最近执行记录回填 ----
    const historyCard = await cdp.waitFor(
      `(() => {
        const card = [...document.querySelectorAll('.t-card')].find((el) => el.innerText.includes('最近执行记录'));
        if (!card) return false;
        const rows = [...card.querySelectorAll('tbody tr')].map((tr) => tr.innerText.replace(/\\s+/g, ' ').trim());
        return rows.length ? { rows, text: card.innerText.replace(/\\s+/g, ' ').trim() } : false;
      })()`,
      15000
    );
    check(
      '页内「最近执行记录」回填本次结果（成功 2，跳过 1）',
      !!historyCard &&
        historyCard.rows.length >= 1 &&
        /成功 2/.test(historyCard.rows[0]) &&
        /跳过 1/.test(historyCard.rows[0]),
      historyCard && historyCard.rows.join(' || ')
    );

    // 结果态同样要在最小窗口下完整：结果页随壳层纵向滚动，滚到底部后操作脚与历史卡必须可见
    await cdp.evaluate('(() => { window.resizeTo(960, 620); return true; })()');
    await new Promise((r) => setTimeout(r, 1200));
    const RESULT_GEOMETRY = `(() => {
      const inViewport = (el) => {
        if (!el) return false;
        const rect = el.getBoundingClientRect();
        return rect.width > 0 && rect.height > 0 && rect.top >= -1 && rect.bottom <= window.innerHeight + 1;
      };
      const btn = (label) => [...document.querySelectorAll('.res-foot .t-button')].find((b) => b.innerText.trim() === label);
      const scroller = document.querySelector('.page-scroll');
      return {
        width: window.innerWidth,
        overflowX: document.documentElement.scrollWidth > window.innerWidth,
        scrollTop: scroller ? scroller.scrollTop : -1,
        scrollable: scroller ? scroller.scrollHeight > scroller.clientHeight : false,
        scrollHeight: scroller ? scroller.scrollHeight : 0,
        clientHeight: scroller ? scroller.clientHeight : 0,
        headVisible: inViewport(document.querySelector('.res-head')),
        tiles: [...document.querySelectorAll('.res-tiles .tile')].filter(inViewport).length,
        rows: document.querySelectorAll('.res-row').length,
        firstRowVisible: inViewport(document.querySelector('.res-row')),
        filtersVisible: inViewport(document.querySelector('.res-filters')),
        finishVisible: inViewport(btn('完成')),
        backVisible: inViewport(btn('返回预览')),
        historyVisible: inViewport([...document.querySelectorAll('.t-card')].find((el) => el.innerText.includes('最近执行记录'))),
        historyRows: document.querySelectorAll('.t-card tbody tr').length,
      };
    })()`;
    await cdp.evaluate(
      "(() => { const s = document.querySelector('.page-scroll'); if (s) s.scrollTop = 0; return true; })()"
    );
    await new Promise((r) => setTimeout(r, 400));
    const resultTop = await cdp.evaluate(RESULT_GEOMETRY);
    await cdp.evaluate(
      "(() => { const s = document.querySelector('.page-scroll'); if (s) s.scrollTop = s.scrollHeight; return true; })()"
    );
    await new Promise((r) => setTimeout(r, 400));
    const resultBottom = await cdp.evaluate(RESULT_GEOMETRY);
    check(
      '960×620 结果态：顶部见计数卡与筛选，滚到底见结果表、「完成/返回预览」与历史卡，无横向溢出',
      !!resultTop &&
        !!resultBottom &&
        resultTop.width <= 1000 &&
        resultTop.overflowX === false &&
        resultTop.headVisible === true &&
        resultTop.tiles === 5 &&
        resultTop.rows === 3 &&
        resultTop.filtersVisible === true &&
        resultBottom.overflowX === false &&
        resultBottom.firstRowVisible === true &&
        resultBottom.finishVisible === true &&
        resultBottom.backVisible === true &&
        resultBottom.historyVisible === true &&
        resultBottom.historyRows >= 1,
      JSON.stringify({ top: resultTop, bottom: resultBottom })
    );
    await cdp.screenshot(path.join(tmp, 'result-960x620.png'));
    await cdp.evaluate('(() => { window.resizeTo(1160, 780); return true; })()');
    await new Promise((r) => setTimeout(r, 900));

    // ---- 7. 执行历史 →「参数回填」回到插件页 ----
    await cdp.evaluate(`(() => {
      [...document.querySelectorAll('.nav-item')].find((el) => el.innerText.includes('执行历史')).click();
      return true;
    })()`);
    const historyPage = await cdp.waitFor(
      `(() => {
        const rows = [...document.querySelectorAll('.history-row')];
        const target = rows.find((row) => row.innerText.includes('批量重命名'));
        if (!target) return false;
        return { total: rows.length, row: target.innerText.replace(/\\s+/g, ' ').trim() };
      })()`,
      20000
    );
    check(
      '执行历史页出现本次批量重命名记录',
      !!historyPage && /成功 2/.test(historyPage.row),
      historyPage && historyPage.row.slice(0, 160)
    );
    await cdp.evaluate(`(() => {
      const row = [...document.querySelectorAll('.history-row')].find((el) => el.innerText.includes('批量重命名'));
      [...row.querySelectorAll('button')].find((b) => b.innerText.includes('参数回填')).click();
      return true;
    })()`);
    const restored = await cdp.waitFor(
      `(() => {
        const notice = document.querySelector('.rename-restore');
        const prefix = document.querySelector('input[placeholder="例如 2026秋检_"]');
        const rows = document.querySelectorAll('.pv-row').length;
        if (!notice) return false;
        return {
          notice: notice.innerText.replace(/\\s+/g, ' ').trim(),
          prefix: prefix ? prefix.value : '',
          rows,
          inPreview: !!document.querySelector('.rename-page.is-preview'),
          running: !!document.querySelector('.progress-card'),
        };
      })()`,
      20000
    );
    check(
      '参数回填回到插件页：恢复规则与文件、只提示不自动执行',
      !!restored &&
        /已回填/.test(restored.notice) &&
        /不会自动执行/.test(restored.notice) &&
        restored.prefix === '2026秋检_' &&
        restored.rows === 3 &&
        restored.inPreview === true &&
        restored.running === false,
      restored && JSON.stringify(restored)
    );
    await cdp.screenshot(path.join(tmp, 'restored-preview.png'));
  } catch (e) {
    check('冒烟脚本自身未异常中断', false, e && e.message);
    if (stderr.trim()) {
      const tail = stderr.trim().split(/\r?\n/).slice(-12).join('\n');
      console.log('  Electron stderr tail: ' + tail);
    }
  } finally {
    try {
      if (cdp) cdp.ws.close();
    } catch (e) {
      // 连接可能已断
    }
    killTree(proc);
    const killed = killTempPython(tmp);
    if (killed) console.log(`  已清理本次冒烟拉起的残留核心进程：${killed} 个`);
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

main();
