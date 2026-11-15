// 独立图片窗口服务：窗口生命周期、专用 IPC、路径授权与 shade-image 协议、系统默认应用引导。
// 与 screensaver 服务同一写法：不在顶层 require('electron')，依赖由 deps 注入，纯 node 可加载。
const path = require('path');
const fs = require('fs');
const { parseImageArgs, isViewablePath } = require('./argv.cjs');
const {
  VIEW_EXTENSIONS,
  createPathGuard,
  buildImageProtocolUrl,
  createImageProtocolHandler,
  isInside,
  resolveRealisticPath,
} = require('./path-guard.cjs');

const WINDOW_TITLE = '图片工具';
const WINDOW_WIDTH = 1280;
const WINDOW_HEIGHT = 800;
const MIN_WIDTH = 900;
const MIN_HEIGHT = 600;
// 图片窗口只放行这些 RPC：任务取消/查询与配置写入复用核心既有方法。
const ALLOWED_RPC = new Set([
  'imageList',
  'imageInspect',
  'imagePreview',
  'imageRename',
  'imageProcess',
  'cancelJob',
  'getJob',
  'getConfig',
  'saveConfig',
]);
const OPERATIONS = new Set(['convert', 'compress', 'resize']);
// 设计稿口径：最多 12 条短语、单条 40 字，不允许文件名非法字符。
const MAX_PHRASES = 12;
const MAX_PHRASE_LEN = 40;
const ILLEGAL_NAME_CHARS = /[\\/:*?"<>|\u0000-\u001f]/;
// Linux 只在用户点击后写入这三项默认（其余格式按需自行设置）。
const LINUX_MIMES = ['image/jpeg', 'image/png', 'image/webp'];
const DEFAULT_DESKTOP_FILE = 'uvmp-toolkit.desktop';
const XDG_TIMEOUT_MS = 5000;
// 核心冷启动的退避序列（毫秒）：合计约 20 秒，覆盖 Python 首次 spawn + Pillow 导入的常见耗时。
// 仅对「未就绪」类错误生效，内容类错误立即抛出不拖慢交互。
const CORE_RETRY_DELAYS = [
  150, 250, 400, 600, 800, 1000, 1200, 1500, 1800, 2000, 2500, 3000, 3500, 3500,
];
// Windows 文件关联：安装器（build/installer.nsh 的 UVMP_IMAGE_PROGID）写入的 ProgID 与
// 设置页重点检查的四个扩展名。只读 UserChoice，绝不写。
const WINDOWS_IMAGE_PROGID = 'uvmp-toolkit.image';
const WINDOWS_USERCHOICE_EXTS = ['.jpg', '.jpeg', '.png', '.webp'];

function electronDep(name) {
  return require('electron')[name];
}

// reg query 输出形如：
//   HKEY_CURRENT_USER\...\UserChoice
//       ProgId    REG_SZ    uvmp-toolkit.image
// 取最后一段 REG_SZ 值；REG_EXPAND_SZ 也认（值内容同语义）。
function parseRegProgId(stdout) {
  const text = String(stdout == null ? '' : stdout);
  const m = /^\s*ProgId\s+REG_(?:EXPAND_)?SZ\s+(\S.*?)\s*$/im.exec(text);
  return m ? m[1].trim() : '';
}

// reg.exe 的失败含义：ENOENT = 没有 reg.exe（极简镜像/非 Windows 容器）；
// 退出码 1 + "找不到"/"unable to find" = 键/值不存在（明确未知引用）；其余按访问失败处理。
function regFailureInfo(error) {
  const code = error && error.code;
  const message = String((error && error.message) || error || '');
  if (code === 'ENOENT') {
    return { missing: false, noReg: true, text: '找不到 reg.exe，无法读取注册表' };
  }
  if (/找不到|unable to find|not find|不存在/i.test(message)) {
    return { missing: true, noReg: false, text: 'UserChoice 键不存在' };
  }
  return { missing: false, noReg: false, text: message || '注册表读取失败' };
}

function describeRegFailure(error, ext) {
  const info = regFailureInfo(error);
  if (info.missing) return ext + '：UserChoice 键不存在（系统尚未记录默认应用）';
  return ext + '：' + info.text;
}

function badRequest(message) {
  const err = new Error(message);
  err.code = 'EINVAL';
  return err;
}

class ImageToolsService {
  constructor({ bridge, getBridge, deps = {}, platform, desktopFile, icon } = {}) {
    // bridge 在 core 重启时会换实例：优先用 getter，避免持有已停止的旧桥
    this.getBridge = getBridge || (() => bridge || null);
    this.deps = deps;
    this.platform = platform || process.platform;
    this.desktopFile = desktopFile || DEFAULT_DESKTOP_FILE;
    this.icon = icon || '';
    this.win = null;
    this.windowId = 0;
    this.pendingPayload = null;
    this.lastPayload = null;
    this.loaded = false;
    this.ipcRegistered = false;
    this.jobs = new Set();
    // 建任务期间的事件缓冲：核心在建任务的同一瞬间就写「开始处理」等日志，
    // 而主进程要等 imageProcess RPC 返回才知道 job_id。此窗口内的 job.* 事件先按序保存，
    // 拿到 job_id 后回放（见 runProcess），否则首批日志/进度/终态会被当成「未跟踪任务」丢掉。
    this.pendingJobEvents = null;
    this.pathGuard = createPathGuard();
  }

  _dep(name) {
    if (this.deps && this.deps[name]) return this.deps[name];
    // execFile 是 node 内置（Electron 的模块导出里没有），其余走 electron 主进程模块
    if (name === 'execFile') return require('child_process').execFile;
    return electronDep(name);
  }

  get protocolHandler() {
    return createImageProtocolHandler({ guard: this.pathGuard });
  }

  // ---------------- 窗口 ----------------
  _target() {
    const app = this._dep('app');
    const devUrl = !app.isPackaged ? String(process.env.ELECTRON_DEV_URL || '').trim() : '';
    if (devUrl) return { url: devUrl.replace(/\/+$/, '') + '/imagetools.html' };
    return { file: path.join(__dirname, '..', '..', 'dist', 'imagetools.html') };
  }

  _createWindow() {
    const BrowserWindow = this._dep('BrowserWindow');
    const win = new BrowserWindow({
      width: WINDOW_WIDTH,
      height: WINDOW_HEIGHT,
      minWidth: MIN_WIDTH,
      minHeight: MIN_HEIGHT,
      title: WINDOW_TITLE,
      icon: this.icon || undefined,
      // 与主窗口一致：无系统标题栏 + 48px 共享 TitleBar（不会出现双层标题栏）
      frame: false,
      // 外框/阴影/圆角/边缘拖拽交给 Windows：自绘 setShape 在 200% DPI 下会留下黑角且发虚
      thickFrame: true,
      resizable: true,
      show: false,
      backgroundColor: '#F5F7FA',
      webPreferences: {
        preload: path.join(__dirname, 'preload.cjs'),
        contextIsolation: true,
        nodeIntegration: false,
        // preload 只 require('electron')，可开沙箱；渲染层无法触碰 fs 与 node 内置模块
        sandbox: true,
        backgroundThrottling: false,
        webSecurity: false, // 与主窗口一致：本地 file:// 页面加载相对资源
      },
    });
    this.windowId += 1;
    if (typeof win.setMenuBarVisibility === 'function') win.setMenuBarVisibility(false);
    this.win = win;
    this.loaded = false;
    // 最大化状态同步给标题栏（与主窗口同通道语义，前缀不同以免互相串台）
    win.on('maximize', () => {
      if (!win.isDestroyed()) win.webContents.send('imagetools:max', true);
    });
    win.on('unmaximize', () => {
      if (!win.isDestroyed()) win.webContents.send('imagetools:max', false);
    });
    const target = this._target();
    if (target.url) win.loadURL(target.url);
    else win.loadFile(target.file);
    win.once('ready-to-show', () => {
      if (this.win !== win || win.isDestroyed()) return;
      win.show();
      win.focus();
    });
    win.webContents.on('did-finish-load', () => {
      if (this.win !== win) return;
      this.loaded = true;
      // 首帧用待开参数；渲染进程崩溃重载时用上一次的参数恢复，而不是清空当前查看
      const payload = this.pendingPayload || this.lastPayload || { files: [] };
      this.pendingPayload = null;
      this._sendOpen(payload);
    });
    win.on('closed', () => {
      // 旧窗被替换后其 closed 才到达：不得清掉新窗状态
      if (this.win !== win) return;
      this.win = null;
      this.loaded = false;
      this.pendingPayload = null;
      this.lastPayload = null;
      this.jobs.clear();
      // 建任务窗口被关窗打断：丢弃缓冲，避免关窗后仍把旧事件投给已销毁的页面
      this.pendingJobEvents = null;
      this.pathGuard.clear();
    });
    return win;
  }

  /**
   * 打开图片窗口（主进程入口：冷启动 argv/第二实例/主窗口）。
   * payload.files 中的文件按「父目录」授权——只有主进程可信来源才允许新授权。
   */
  open(payload = {}) {
    return this._open(payload, true);
  }

  /**
   * 渲染层请求打开：只允许在**已授权**的文件之间切换，不接受新路径授权，
   * 否则页面可通过 IPC 自行授权任意磁盘图片，绕过对话框。
   */
  reopen(payload = {}) {
    return this._open(payload, false);
  }

  _open(payload, authorize) {
    const files = authorize
      ? this._authorizeFiles((payload || {}).files)
      : this._authorizedFiles((payload || {}).files);
    this.lastPayload = { files };
    if (!this.win || this.win.isDestroyed()) {
      this.pendingPayload = { files };
      this._createWindow();
      return this.state();
    }
    this.pendingPayload = this.loaded ? null : { files };
    if (typeof this.win.isMinimized === 'function' && this.win.isMinimized()) this.win.restore();
    this.win.show();
    this.win.focus();
    if (this.loaded) this._sendOpen({ files });
    return this.state();
  }

  /** 主窗口/冷启动/第二实例入口：从 argv 之外的路径列表打开（过滤非法项，可空）。 */
  openPaths(paths) {
    return this.open({ files: Array.isArray(paths) ? paths : [] });
  }

  _authorizeFiles(list) {
    const out = [];
    const seen = new Set();
    for (const item of Array.isArray(list) ? list : []) {
      if (typeof item !== 'string' || !item.trim()) continue;
      if (!isViewablePath(item)) continue;
      const real = this.pathGuard.authorizeFile(item);
      if (!real || seen.has(real)) continue;
      seen.add(real);
      out.push(real);
    }
    return out;
  }

  _authorizedFiles(list) {
    const out = [];
    const seen = new Set();
    for (const item of Array.isArray(list) ? list : []) {
      if (typeof item !== 'string' || !item.trim()) continue;
      const real = this.pathGuard.check(item);
      if (!real || seen.has(real)) continue;
      seen.add(real);
      out.push(real);
    }
    return out;
  }

  _sendOpen(payload) {
    const win = this.win;
    if (!win || win.isDestroyed()) return;
    win.webContents.send('imagetools:open', payload);
  }

  state() {
    return { open: !!(this.win && !this.win.isDestroyed()), windowId: this.windowId };
  }

  // ---------------- 任务事件 ----------------
  trackJob(jobId) {
    if (!jobId) return;
    this.jobs.add(String(jobId));
  }

  isJobTracked(jobId) {
    return this.jobs.has(String(jobId || ''));
  }

  /** 核心事件转投：只转发本窗口发起的任务，以及核心退出。返回是否已投递。 */
  handleCoreEvent(payload) {
    const win = this.win;
    if (!win || win.isDestroyed() || !payload) return false;
    if (payload.event === 'core.exit') {
      win.webContents.send('imagetools:event', payload);
      return true;
    }
    if (!payload.job_id) return false;
    if (this.isJobTracked(payload.job_id)) {
      win.webContents.send('imagetools:event', payload);
      return true;
    }
    // 建任务窗口内：job_id 尚未进入 jobs 集合，先按到达顺序缓冲，等 runProcess 回放
    const pending = this.pendingJobEvents;
    if (pending && String(payload.event || '').startsWith('job.')) {
      pending.events.push(payload);
      return true;
    }
    return false;
  }

  // ---------------- IPC ----------------
  _isTrustedSender(event) {
    const win = this.win;
    if (!win || win.isDestroyed()) return false;
    return !!(event && event.sender === win.webContents);
  }

  _guard(fn) {
    return async (event, ...args) => {
      if (!this._isTrustedSender(event)) throw new Error('非法 IPC 来源');
      return fn(...args);
    };
  }

  // 核心冷启动（双击图片/文件关联）时常比窗口慢：Spawn Python + 导入 Pillow 可能十几秒。
  // 未就绪类错误按退避重试，内容类错误立即抛出；总尝试有界，核心真不可用时不会无限等待。
  _bridgeNotReady(error) {
    const text = String((error && error.message) || error || '');
    return /核心进程未启动|核心进程已启动失败|核心管道|核心调用超时|核心进程已退出/.test(text);
  }

  async _callBridge(method, params) {
    if (!ALLOWED_RPC.has(method)) throw badRequest('不支持的 RPC 方法: ' + method);
    const bridge = this.getBridge();
    if (!bridge) throw new Error('核心进程未启动');
    let lastError = null;
    for (let attempt = 0; attempt <= CORE_RETRY_DELAYS.length; attempt += 1) {
      try {
        return await bridge.call(method, params || {});
      } catch (e) {
        lastError = e;
        if (!this._bridgeNotReady(e) || attempt === CORE_RETRY_DELAYS.length) break;
        await this._retryWait(CORE_RETRY_DELAYS[attempt]);
      }
    }
    throw lastError;
  }

  // 退避等待（测试可替换为不等待的实现）
  _retryWait(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  _requireAuthorized(p) {
    const real = this.pathGuard.check(p);
    if (!real) throw new Error('未授权的图片路径');
    return real;
  }

  registerIpc() {
    if (this.ipcRegistered) return;
    this.ipcRegistered = true;
    const ipcMain = this._dep('ipcMain');
    const on = (channel, fn) => ipcMain.handle(channel, this._guard(fn));

    on('imagetools:open', (payload) => this.reopen(payload || {}));
    // 冷启动竞态兜底：主进程 did-finish-load 后立即推首帧，渲染层若订阅稍晚就会丢消息；
    // 页面挂载后可主动取回当前待开文件。
    on('imagetools:ready', () => ({ files: (this.lastPayload || { files: [] }).files.slice() }));
    on('imagetools:pickFiles', (filters) => this.pickFiles(filters));
    on('imagetools:pickDirectory', (defaultPath) => this.pickDirectory(defaultPath));
    on('imagetools:list', (p) => this.list(p));
    on('imagetools:inspect', (p) => this.inspect(p));
    on('imagetools:preview', (p, maxSize) => this.preview(p, maxSize));
    on('imagetools:rename', (p, name) => this.rename(p, name));
    on('imagetools:runProcess', (payload) => this.runProcess(payload));
    on('imagetools:cancelJob', (jobId) => this.cancelJob(jobId));
    on('imagetools:getJob', (jobId) => this.getJob(jobId));
    on('imagetools:getConfig', () => this.getConfig());
    on('imagetools:saveQuickPhrases', (phrases) => this.saveQuickPhrases(phrases));
    on('imagetools:showItem', (p) => this.showItem(p));
    on('imagetools:openDefaultSettings', () => this.openDefaultSettings());
    on('imagetools:setDefaultLinux', () => this.setDefaultLinux());
    on('imagetools:getDefaultStatus', () => this.getDefaultStatus());
    // 自定义标题栏（与主窗口 window.core 同名通道语义）
    on('imagetools:winMinimize', () => this.win && this.win.minimize());
    on('imagetools:winIsMax', () => !!(this.win && this.win.isMaximized()));
    on('imagetools:winToggleMax', () => {
      const win = this.win;
      if (!win || win.isDestroyed()) return;
      if (win.isMaximized()) win.unmaximize();
      else win.maximize();
    });
    on('imagetools:winClose', () => this.win && this.win.close());
    on('imagetools:appVersion', () => {
      const app = this._dep('app');
      return app.getVersion();
    });
  }

  // 文件/目录对话框：图片窗口自己的对话框，选中即授权
  async pickFiles(filters) {
    const dialog = this._dep('dialog');
    const list = Array.isArray(filters) && filters.length ? filters.slice() : [];
    list.push({ name: '图片文件', extensions: [...stripDots(VIEW_EXT_LIST)] });
    // 麒麟/UKUI 的扩展名过滤不可靠，兜底「所有文件」（与主窗口 pickFile 同口径）
    list.push({ name: '所有文件', extensions: ['*'] });
    const r = await dialog.showOpenDialog(this.win, {
      title: '打开图片',
      properties: ['openFile', 'multiSelections'],
      filters: list,
    });
    if (!r || r.canceled || !Array.isArray(r.filePaths) || !r.filePaths.length) return null;
    const files = this._authorizeFiles(r.filePaths);
    // 与主窗口 core.pickFiles 同口径：返回路径数组（取消返回 null）；目录用 list(files[0]) 取
    return files.length ? files : null;
  }

  async pickDirectory(defaultPath) {
    const dialog = this._dep('dialog');
    const r = await dialog.showOpenDialog(this.win, {
      title: '选择图片文件夹',
      properties: ['openDirectory'],
      defaultPath: defaultPath || undefined,
    });
    if (!r || r.canceled || !r.filePaths || !r.filePaths[0]) return null;
    const directory = this.pathGuard.authorizeDirectory(r.filePaths[0]);
    if (!directory) throw new Error('无法读取所选目录');
    let files = [];
    try {
      const res = await this._callBridge('imageList', { path: directory });
      files = (res && Array.isArray(res.files) && res.files) || [];
    } catch (e) {
      // 列表失败不吞掉目录授权：页面可自行提示重试
      files = [];
    }
    return { directory, files };
  }

  async list(p) {
    // 单图传文件，目录面板传目录（已授权目录本身或其子目录）
    const real = this.pathGuard.check(p);
    if (real) return this._callBridge('imageList', { path: real });
    const dir = this.pathGuard.checkDirectory(p) || this._authorizedDir(p);
    if (!dir) throw new Error('未授权的图片路径');
    return this._callBridge('imageList', { path: dir });
  }

  // 已授权目录及其子目录可列（看图时切目录不必重新选择），目录外一律拒绝
  _authorizedDir(p) {
    const text = String(p == null ? '' : p).trim();
    if (!text) return '';
    const resolved = resolveRealisticPath(text);
    if (!resolved) return '';
    const dirs = this.pathGuard.authorizedDirectories();
    if (!dirs.some((dir) => dir === resolved || isInside(dir, resolved))) return '';
    const st = (() => {
      try {
        return fs.statSync(resolved);
      } catch {
        return null;
      }
    })();
    return st && st.isDirectory() ? resolved : '';
  }

  async inspect(p) {
    const real = this._requireAuthorized(p);
    return this._callBridge('imageInspect', { path: real });
  }

  async preview(p, maxSize) {
    const real = this._requireAuthorized(p);
    const params = { path: real };
    const size = Number(maxSize);
    if (Number.isFinite(size) && size > 0) params.max_size = Math.min(8192, Math.round(size));
    const res = await this._callBridge('imagePreview', params);
    const out = Object.assign({}, res || {});
    // 核心可能返回原图或缓存预览路径：授权后把真实路径编码进协议 URL，
    // 渲染层拿到的 url 仍需每次请求由协议处理器重新复核，撤权后立即 404
    const target = typeof out.path === 'string' && out.path ? out.path : real;
    if (!target) throw new Error('预览失败');
    // 缓存预览在核心 state 目录，不在用户授权范围内：先精确授权该文件
    const authorized = this.pathGuard.check(target) || this.pathGuard.grantFile(target);
    if (!authorized) throw new Error('预览文件不可用');
    const url = buildImageProtocolUrl(authorized);
    if (!url) throw new Error('预览文件不可用');
    delete out.path;
    out.url = url;
    out.name = path.basename(real);
    out.ext = path.extname(real).toLowerCase();
    return out;
  }

  async rename(p, name) {
    const real = this._requireAuthorized(p);
    const base = String(name == null ? '' : name).trim();
    if (!base) throw badRequest('名称不能为空');
    if (ILLEGAL_NAME_CHARS.test(base)) throw badRequest('名称包含非法字符');
    if (base.length > 120) throw badRequest('名称过长');
    return this._callBridge('imageRename', { path: real, name: base });
  }

  async runProcess(payload) {
    const data = payload || {};
    const operation = String(data.operation || '');
    if (!OPERATIONS.has(operation)) throw badRequest('不支持的图片操作: ' + operation);
    const list = Array.isArray(data.files) ? data.files : [];
    if (!list.length) throw badRequest('请选择至少一张图片');
    const files = [];
    for (const item of list) files.push(this._requireAuthorized(item));
    const params = {
      operation,
      files,
      options: data.options && typeof data.options === 'object' ? data.options : {},
    };
    if (data.output_dir) params.output_dir = this._authorizeOutputDir(data.output_dir);
    // 同一窗口同时只允许一个建任务：两个 pending 并存就无法判定事件属于哪个任务。
    // 渲染层本身也串行（运行中禁止再次开始），这里显式拒绝，避免静默串台。
    if (this.pendingJobEvents) throw new Error('已有任务正在创建中，请等待当前任务开始');
    const pending = { events: [] };
    this.pendingJobEvents = pending;
    try {
      const res = await this._callBridge('imageProcess', params);
      const jobId = res && res.job_id ? String(res.job_id) : '';
      // 建任务期间窗口被关掉（pending 已作废）时，不能再登记任务号：
      // jobs 在关窗时已清空，重新登记会让迟到事件被投给下一个新窗口（跨窗口泄漏）。
      const target =
        this.pendingJobEvents === pending && this.win && !this.win.isDestroyed()
          ? this.win.webContents
          : null;
      if (jobId && target) {
        // 先登记再回放：回放途中的后续事件会走「已跟踪」直投分支，不会被重复缓冲
        this.trackJob(jobId);
        // 只投递本任务的事件，其他窗口/其他任务的事件丢弃
        for (const event of pending.events) {
          if (String(event.job_id) !== jobId) continue;
          if (!target.isDestroyed()) target.send('imagetools:event', event);
        }
      }
      return res;
    } finally {
      // 成功、失败、关窗都要清理：遗留会让下一轮误投旧事件或永久拒绝新建任务
      if (this.pendingJobEvents === pending) this.pendingJobEvents = null;
    }
  }

  // 输出目录只需落在已授权目录之内（可以是尚不存在的子目录）：不放开任意写盘位置。
  // 用「最深已存在祖先的 realpath + 剩余段」判断，授权目录内的符号链接逃逸同样拒绝。
  _authorizeOutputDir(raw) {
    const text = String(raw == null ? '' : raw).trim();
    if (!text) return '';
    const resolved = resolveRealisticPath(text);
    if (!resolved) throw new Error('未授权的输出目录');
    const dirs = this.pathGuard.authorizedDirectories();
    if (dirs.some((dir) => dir === resolved || isInside(dir, resolved))) return resolved;
    throw new Error('未授权的输出目录');
  }

  async cancelJob(jobId) {
    const id = String(jobId || '');
    if (!id || !this.isJobTracked(id)) throw new Error('未知任务');
    return this._callBridge('cancelJob', { job_id: id });
  }

  async getJob(jobId) {
    const id = String(jobId || '');
    if (!id || !this.isJobTracked(id)) throw new Error('未知任务');
    return this._callBridge('getJob', { job_id: id });
  }

  async getConfig() {
    const cfg = await this._callBridge('getConfig', {});
    const section = (cfg && cfg.image_tools) || {};
    return {
      quick_phrases: Array.isArray(section.quick_phrases) ? section.quick_phrases.slice() : [],
      preview_max: Number(section.preview_max) || 2560,
      workers: Number(section.workers) || 2,
    };
  }

  async saveQuickPhrases(phrases) {
    if (!Array.isArray(phrases)) throw badRequest('快捷短语必须是数组');
    if (phrases.length > MAX_PHRASES) throw badRequest('快捷短语最多 ' + MAX_PHRASES + ' 条');
    const cleaned = [];
    for (const item of phrases) {
      if (typeof item !== 'string') throw badRequest('快捷短语必须是文本');
      const text = item.trim();
      if (!text) throw badRequest('快捷短语不能为空');
      if (text.length > MAX_PHRASE_LEN) throw badRequest('单条短语不得超过 40 字');
      if (ILLEGAL_NAME_CHARS.test(text)) throw badRequest('快捷短语包含非法字符');
      if (!cleaned.includes(text)) cleaned.push(text);
    }
    await this._callBridge('saveConfig', { image_tools: { quick_phrases: cleaned } });
    return { quick_phrases: cleaned };
  }

  async showItem(p) {
    const shell = this._dep('shell');
    const real =
      this.pathGuard.check(p) || this.pathGuard.checkDirectory(p) || this._authorizedDir(p);
    if (!real) throw new Error('未授权的图片路径');
    shell.showItemInFolder(real);
    return { ok: true };
  }

  // Windows：只能打开系统默认应用设置并引导用户选择，不静默改写 UserChoice
  async openDefaultSettings() {
    if (this.platform !== 'win32') {
      return { ok: false, supported: false, reason: '当前系统不支持从本窗口打开默认应用设置' };
    }
    const shell = this._dep('shell');
    await shell.openExternal('ms-settings:defaultapps');
    return { ok: true, supported: true, managed: false };
  }

  // Linux：仅用户点击后写 XDG 默认项，逐项汇报结果（失败保留原值）
  async setDefaultLinux() {
    if (this.platform !== 'linux') {
      return {
        ok: false,
        supported: false,
        reason: '仅 Linux 支持写入 XDG 默认项',
        applied: [],
        failed: [],
      };
    }
    const applied = [];
    const failed = [];
    for (const mime of LINUX_MIMES) {
      try {
        await this._execFile('xdg-mime', ['default', this.desktopFile, mime]);
        applied.push(mime);
      } catch (e) {
        failed.push({ mime, error: (e && e.message) || String(e) });
      }
    }
    return { ok: failed.length === 0, supported: true, applied, failed };
  }

  /**
   * 系统默认应用真实状态。
   * - Windows：读 HKCU\...\FileExts\.<ext>\UserChoice /v ProgId，与安装器写入的
   *   uvmp-toolkit.image 比对；读不到（无 reg.exe / 键不存在 / 访问失败）一律 unknown + 原因，
   *   绝不把「查不到」说成「未设置」或「已设置」——设置页不能承诺做不到的事。
   * - Linux：xdg-mime query default 查三个 MIME。
   * - 其他平台：supported:false（无系统默认应用概念）。
   */
  async getDefaultStatus() {
    if (this.platform === 'win32') return this._windowsDefaultStatus();
    if (this.platform !== 'linux') {
      return {
        platform: this.platform,
        supported: false,
        managed: false,
        desktopFile: '',
        defaults: {},
        isDefault: {},
        status: 'unsupported',
        reason: '当前系统不支持查询默认应用',
        associations: [],
      };
    }
    const defaults = {};
    const isDefault = {};
    for (const mime of LINUX_MIMES) {
      let value = '';
      try {
        value = String(await this._execFile('xdg-mime', ['query', 'default', mime])).trim();
      } catch (e) {
        value = '';
      }
      defaults[mime] = value;
      isDefault[mime] = !!value && value === this.desktopFile;
    }
    const values = Object.values(isDefault);
    const isDefaultAll = values.length > 0 && values.every(Boolean);
    const anyDefault = values.some(Boolean);
    return {
      platform: 'linux',
      supported: true,
      managed: true,
      desktopFile: this.desktopFile,
      defaults,
      isDefault,
      // 与 Windows 同形，页面不必按平台分支判断
      status: isDefaultAll ? 'default' : anyDefault ? 'partial' : 'other',
      default: isDefaultAll,
      partial: !isDefaultAll && anyDefault,
      installed: true,
      progId: '',
      associations: Object.keys(defaults).map((mime) => ({
        ext: mime,
        progId: defaults[mime],
        status: isDefault[mime] ? 'default' : 'other',
        reason: '',
      })),
      reason: '',
    };
  }

  /** Windows：逐扩展名读 UserChoice\ProgId，并与安装器登记的 ProgID 比对。 */
  async _windowsDefaultStatus() {
    const associations = [];
    for (const ext of WINDOWS_USERCHOICE_EXTS) {
      associations.push(await this._queryUserChoice(ext));
    }
    const installed = await this._queryProgIdInstalled();
    const statuses = associations.map((a) => a.status);
    const defaults = associations.filter((a) => a.status === 'default').length;
    const unknown = statuses.filter((s) => s === 'unknown').length;

    let status = 'unknown';
    if (unknown === statuses.length) status = 'unknown';
    else if (defaults === statuses.length) status = 'default';
    else if (defaults > 0) status = 'partial';
    else if (unknown > 0) status = 'unknown';
    else status = 'other';

    const reason =
      status === 'unknown'
        ? associations
            .filter((a) => a.status === 'unknown' && a.reason)
            .map((a) => a.reason)
            .find(Boolean) || '无法读取注册表中的默认应用信息'
        : '';

    return {
      platform: 'win32',
      supported: true,
      managed: false, // UserChoice 受系统保护，本工具只读不改写
      desktopFile: '',
      // 与 Linux 同形的映射，便于页面沿用 isDefault/defaults 渲染
      defaults: Object.fromEntries(associations.map((a) => [a.ext, a.progId])),
      isDefault: Object.fromEntries(associations.map((a) => [a.ext, a.status === 'default'])),
      status,
      default: status === 'default',
      partial: status === 'partial',
      installed,
      progId: WINDOWS_IMAGE_PROGID,
      associations,
      reason,
    };
  }

  /** 读单个扩展名的 UserChoice\ProgId；返回 { ext, progId, status, reason }。 */
  async _queryUserChoice(ext) {
    const key =
      'HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Explorer\\FileExts\\' +
      ext +
      '\\UserChoice';
    try {
      const out = await this._execFile('reg', ['query', key, '/v', 'ProgId']);
      const progId = parseRegProgId(out);
      if (!progId) {
        return {
          ext,
          progId: '',
          status: 'unknown',
          reason: ext + '：UserChoice 里没有可读的 ProgId',
        };
      }
      return {
        ext,
        progId,
        status: progId === WINDOWS_IMAGE_PROGID ? 'default' : 'other',
        reason: '',
      };
    } catch (e) {
      return { ext, progId: '', status: 'unknown', reason: describeRegFailure(e, ext) };
    }
  }

  /**
   * ProgID 类是否已登记（区分安装版与便携版）。
   * true = 已登记；false = 明确未登记；null = 无法确定（无 reg.exe / 注册表读不了）。
   * reg.exe 的错误文案随系统语言变化（中文系统是 GBK，node 解不出来），
   * 因此「键不存在」用「父键能否读取」复核，不依赖文案匹配。
   */
  async _queryProgIdInstalled() {
    const key = 'HKCU\\Software\\Classes\\' + WINDOWS_IMAGE_PROGID;
    try {
      const out = await this._execFile('reg', ['query', key]);
      return String(out || '').includes(WINDOWS_IMAGE_PROGID) ? true : false;
    } catch (e) {
      const info = regFailureInfo(e);
      if (info.noReg) return null;
      if (info.missing) return false;
      // 文案认不出来时用父键探测：父键可读说明注册表本身没问题 → 该键确实没登记
      const readable = await this._registryReadable('HKCU\\Software\\Classes');
      return readable ? false : null;
    }
  }

  /** 注册表读取可用性探测（只读父键，失败不影响业务）。 */
  async _registryReadable(key) {
    try {
      await this._execFile('reg', ['query', key]);
      return true;
    } catch (e) {
      return regFailureInfo(e).missing ? true : false;
    }
  }

  _execFile(cmd, args) {
    const execFile = this._dep('execFile');
    return new Promise((resolve, reject) => {
      execFile(cmd, args, { timeout: XDG_TIMEOUT_MS, windowsHide: true }, (err, stdout) => {
        if (err) reject(err instanceof Error ? err : new Error(String(err)));
        else resolve(String(stdout == null ? '' : stdout));
      });
    });
  }

  /** 应用退出前关窗并撤销路径授权（before-quit 调用，必须幂等）。 */
  dispose() {
    const win = this.win;
    this.win = null;
    this.loaded = false;
    this.pendingPayload = null;
    this.jobs.clear();
    this.pendingJobEvents = null;
    this.pathGuard.clear();
    if (win && !win.isDestroyed()) win.close();
  }
}

// 对话框过滤器用的扩展名（去掉点号，与 Electron filters 格式一致）：与查看白名单同源
const VIEW_EXT_LIST = [...VIEW_EXTENSIONS].map((e) => e.replace(/^\./, ''));

function stripDots(list) {
  return list.map((e) => String(e).replace(/^\./, ''));
}

// 供 main.cjs 复用的入口（冷启动/第二实例解析）
module.exports = {
  ImageToolsService,
  parseImageArgs,
  isViewablePath,
  VIEW_EXTENSIONS,
  createPathGuard,
  buildImageProtocolUrl,
  createImageProtocolHandler,
  WINDOW_WIDTH,
  WINDOW_HEIGHT,
  MIN_WIDTH,
  MIN_HEIGHT,
  LINUX_MIMES,
};
