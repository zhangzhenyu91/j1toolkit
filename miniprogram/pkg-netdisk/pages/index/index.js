// 团队网盘 · 空间浏览主页（app_key netdisk；双端应用，后端为 OpenList 中转层，见 server/src/netdisk/）
// 结构（设计稿 design/小程序-团队网盘.html 屏①②③）：
//   空间分段（我的空间 / 公共区）+ ⋯ 菜单（分享管理 / 新建文件夹）+ 搜索（范围=当前空间）
//   + 面包屑 + 单卡多行文件列表（文件夹线性图标 / 类型字母块 / 图片缩略图）+ FAB 分片上传
// 说明：navbar 组件右上角为微信胶囊占位不可放可点内容，⋯ 菜单按惯例落在分段行右端
// 上传：init → FileSystemManager.readFile 按 chunk_size 切片、2 片并发 PUT（429/409 带 retry 退避 1~2s 重传，
//   每片最多 5 次）→ complete（长阻塞正常，timeout 120s；被中间层掐断时回落 status 轮询终态）；
//   进度以会话快照 received_bytes 为准（本地累计作下限）；0 字节文件 init 返回 instant 直接建成
// 预览：图片/视频 wx.previewMedia（inline 直链 + ?token= 鉴权，同目录媒体合集滑动）；
//   文档 wx.downloadFile（filePath 指定真名）→ wx.openDocument（DOC_EXTS 白名单同 filetransfer）；
//   其他类型仅下载并提示
// 权限：公共区 manageable=false 时操作面板隐藏重命名/删除（上传/新建文件夹全员可用）
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';
import { createTeamGate } from '../../../utils/teamgate';
import { pad, fmtSize, extOf, parseDate } from '../../../utils/util';
import config from '../../../config';
import { fileIconUrl } from '../../fileicon';

const API = '/api/v1/netdisk';
// 班组门控（storage netdisk_team_id）：仅复用其门控节奏与生效班组确定；公共区按班组隔离后
// 未分配班组不再拦截（spaces 不下发 public，隐藏分段只用我的空间），无切换器 UI
const teamGate = createTeamGate({ storageKey: 'netdisk_team_id' });

const IMG_EXTS = ['jpg', 'jpeg', 'png', 'gif', 'bmp', 'webp'];
const VIDEO_EXTS = ['mp4', 'mov', 'm4v'];
// wx.openDocument 可识别的文档类型（传 fileType 提高打开成功率，同 filetransfer 口径）
const DOC_EXTS = ['doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'pdf'];
// 压缩包类型（操作面板显示「查看压缩包 / 解压到当前目录」）
const ARCHIVE_EXTS = ['zip', 'rar', '7z', 'tar', 'gz', 'tgz', 'bz2', 'xz'];
// 提取码字符集：去掉易混淆的 I/L/O/0/1（后端校验 ^[A-Z0-9]{4}$）
const CODE_CHARS = 'ABCDEFGHJKMNPQRSTUVWXYZ23456789';
const EXPIRE_OPTS = [{ days: 7, text: '7 天' }, { days: 30, text: '30 天' }, { days: 0, text: '永久' }];
const CHUNK_CONCURRENCY = 2; // 单文件分片并发数
const CHUNK_MAX_TRY = 5; // 单分片最多尝试次数（429/409 retry 退避重传）
const SEARCH_PER_PAGE = 30;

const CANCELLED = new Error('已取消');
CANCELLED.cancelled = true;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const joinPath = (base, name) => (base === '/' ? `/${name}` : `${base}/${name}`);
// 提取码：4 位字母数字（自动生成，可刷新）
const genCode = () => Array.from({ length: 4 }, () => CODE_CHARS[Math.floor(Math.random() * CODE_CHARS.length)]).join('');
// 时间 → 'MM.DD'（OpenList modified 为 ISO 串；解析失败给占位）
const fmtDay = (input) => {
  const d = parseDate(input);
  return d ? `${pad(d.getMonth() + 1)}.${pad(d.getDate())}` : '—';
};

Page({
  behaviors: [teamGate],
  data: {
    gate: false, // 门控（参照首页 gate 模式）
    // 班组数据由 teamgate behavior 提供；公共区按班组隔离：未分配班组时 spaces 不下发 public，
    // 不再整页拦截（noTeam 放行），仅隐藏公共区分段、只用我的空间
    spaces: [{ key: 'my', name: '我的空间', label: '我的空间' }],
    hasPublic: false, // 是否下发公共区（决定空间分段显隐；拉取 spaces 后确定）
    publicTeam: '', // 公共区所属班组名（分段文案「公共区 · 班组名」）
    space: 'my',
    spaceName: '我的空间',
    maxUploadMb: 0, // 单文件上限（MB；0=尚未取到，不做前端拦截）
    path: '/', // 当前目录（相对空间根）
    crumbs: [], // 面包屑 [{name, path, last}]
    items: [], // 文件行（mapRow 后结构；文件夹在前）
    uploads: [], // 内联上传行 [{id,name,sizeText,sentText,pct,merging}]
    manageable: true, // 当前空间可整理（重命名/删除入口；公共区按班管/超管）
    loading: true,
    error: '', // 首屏加载失败文案（已有内容时失败仅 toast）
    menuOpen: false, // ⋯ 菜单（分享管理 / 新建文件夹）
    // 搜索态（范围=当前空间）
    searching: false,
    kw: '',
    searchItems: [],
    searchState: 'idle', // idle / loading / done
    searchTotal: 0,
    // 文件操作面板（底部弹层）
    sheet: { open: false, item: null },
    // 名称输入弹层（新建文件夹 / 重命名共用）
    nameSheet: { open: false, mode: 'mkdir', value: '', target: null, saving: false },
    keyboardHeight: 0,
    // 创建分享弹层（屏③）
    shareSheet: { open: false, item: null, days: 7, code: '', saving: false },
    expireOpts: EXPIRE_OPTS,
    // 目标位置选择弹层（移动 / 复制；srcSpace/srcDir 为打开时源位置快照）
    picker: {
      open: false, mode: 'move', item: null,
      srcSpace: '', srcDir: '/', space: 'my', dir: '/',
      dirs: [], loading: false, error: '', saving: false,
    },
    pickerCrumbs: [],
  },

  onLoad() {
    this._tasks = {}; // 进行中的上传任务（id → task）
    this._alive = true;
    this._maxBytes = 0;

    // gate 兜底：首页宫格已做权限过滤，此处仅保证登录态就绪后再加载
    if (wx.getStorageSync('token')) {
      this.passGate();
      return;
    }
    getApp().globalData.ready.then((authed) => {
      if (authed) {
        this.passGate();
        return;
      }
      wx.navigateBack({
        fail: () => wx.reLaunch({ url: '/pages/home/home' }),
      });
    });
  },

  passGate() {
    if (this.data.gate) return;
    const user = getApp().globalData.userInfo || wx.getStorageSync('userInfo') || {};
    // 班组门控由 teamgate behavior 完成；未分配班组（返回 false）不再整页拦截：
    // 放行加载（spaces 不下发 public 项，仅显示我的空间）
    if (this.passTeamGate(user, () => this.loadSpaces()) === false) {
      this.setData({ noTeam: false });
      this.loadSpaces();
    }
  },

  onShow() {
    // 切回页面时静默刷新一次（他端可能改动过目录）
    if (this.data.gate && this._loaded && !this.data.searching) {
      this.loadList('quiet');
    }
  },

  onUnload() {
    this._alive = false;
    if (this._searchTimer) {
      clearTimeout(this._searchTimer);
      this._searchTimer = null;
    }
    // 页面销毁后请求回调无处落地：进行中上传统一取消（abort 由后端清理会话）
    Object.keys(this._tasks || {}).forEach((id) => {
      const task = this._tasks[id];
      task.cancelled = true;
      if (task.uploadId) {
        request({ url: `${API}/upload/abort`, method: 'POST', data: { upload_id: task.uploadId } }).catch(() => {});
      }
    });
  },

  onPullDownRefresh() {
    this.loadList('quiet').finally(() => wx.stopPullDownRefresh());
  },

  onRetry() {
    this.loadList('init');
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  onExpired() {
    wx.removeStorageSync('token');
    wx.removeStorageSync('userInfo');
    wx.reLaunch({ url: '/pages/login/login' });
  },

  /* ==================== 空间 / 目录 ==================== */

  // 空间配置（分段显隐与单文件上限）→ 面包屑 → 首屏列表
  // 公共区按班组隔离：未分配班组时 spaces 只有 my（隐藏分段）；有公共区时默认选中公共区
  async loadSpaces() {
    try {
      const data = await request({ url: `${API}/spaces` });
      const raw = (data && data.spaces) || [];
      const pub = raw.find((s) => s.key === 'public');
      const hasPublic = !!pub;
      // 分段文案：公共区带班组名（「公共区 · 检修一班」，超长由 wxss 省略号截断）
      const spaces = (raw.length ? raw : [{ key: 'my', name: '我的空间' }]).map((s) => ({
        ...s,
        label: s.key === 'public' && s.team ? `${s.name} · ${s.team}` : s.name,
      }));
      this.setData({
        spaces,
        hasPublic,
        publicTeam: (pub && pub.team) || '',
        space: hasPublic ? 'public' : 'my',
        maxUploadMb: (data && data.max_upload_mb) || 0,
      });
      this._maxBytes = this.data.maxUploadMb * 1024 * 1024;
    } catch (err) {
      this.toast(err.message);
      this.setData({ hasPublic: false, space: 'my' });
    }
    this.buildCrumbs();
    this.loadList('init');
  },

  // 面包屑：空间名 / 各级路径（父级可点，末级当前加粗）
  buildCrumbs() {
    const cur = this.data.spaces.find((s) => s.key === this.data.space);
    const spaceName = cur ? cur.name : this.data.space;
    const segs = this.data.path.split('/').filter(Boolean);
    const crumbs = [{ name: spaceName, path: '/', last: segs.length === 0 }];
    segs.forEach((s, i) => {
      crumbs.push({ name: s, path: `/${segs.slice(0, i + 1).join('/')}`, last: i === segs.length - 1 });
    });
    this.setData({ crumbs, spaceName });
  },

  onSpaceTap(e) {
    const key = e.currentTarget.dataset.key;
    if (!key || key === this.data.space) return;
    this.setData({ space: key, path: '/', items: [], error: '', searching: false, kw: '', searchItems: [], searchState: 'idle' });
    this.buildCrumbs();
    this.loadList('init');
  },

  enterDir(path) {
    if (path === this.data.path) return;
    this.setData({ path, items: [], error: '' });
    this.buildCrumbs();
    this.loadList('init');
  },

  onCrumbTap(e) {
    this.enterDir(e.currentTarget.dataset.path);
  },

  // 加载模式：init 首屏/重试（骨架屏）；quiet 下拉/进场/操作后静默（保留旧列表）
  async loadList(mode) {
    if (mode === 'init') this.setData({ loading: true, error: '' });
    try {
      const data = await request({
        url: `${API}/list`,
        method: 'POST',
        data: { space: this.data.space, path: this.data.path },
      });
      this._loaded = true;
      this.setData({
        items: this.mapItems((data && data.items) || []),
        manageable: !!(data && data.manageable),
        loading: false,
        error: '',
      });
    } catch (err) {
      // 首屏失败给错误重试态；已有内容时静默失败仅提示
      if (mode === 'init' && !this._loaded) this.setData({ loading: false, error: err.message || '列表加载失败' });
      else this.toast(err.message);
    }
  },

  // OpenList 条目 → 展示行（文件夹在前，保持各自相对顺序）
  mapItems(items) {
    const token = wx.getStorageSync('token');
    const rows = items.map((o) => this.mapRow(o, joinPath(this.data.path, o.name), token, false));
    return rows.filter((r) => r.isDir).concat(rows.filter((r) => !r.isDir));
  },

  mapRow(o, fullPath, token, fromSearch) {
    const ext = extOf(o.name);
    const isDir = !!o.is_dir;
    const isImg = !isDir && IMG_EXTS.includes(ext);
    const media = isImg || (!isDir && VIDEO_EXTS.includes(ext));
    return {
      name: o.name,
      isDir,
      ext,
      isImg,
      media,
      isArchive: !isDir && ARCHIVE_EXTS.includes(ext),
      letter: isDir ? '' : (ext ? ext.slice(0, 4).toUpperCase() : 'FILE'),
      // 真实文件类型图标（/assets/filetypes/，与 Web 同源）；iconFail 时回退字母块/线性文件夹
      icon: fileIconUrl(o.name, isDir),
      iconFail: false,
      thumb: isImg ? this.inlineUrl(fullPath, token) : '',
      thumbFail: false,
      sizeText: isDir ? '文件夹' : fmtSize(o.size),
      sub: isDir ? `${fmtDay(o.modified)} 更新` : `${fmtSize(o.size)} · ${fmtDay(o.modified)}`,
      fullPath,
      fromSearch: !!fromSearch,
      parent: o.parent || '',
    };
  },

  // 内联直链（<image> 缩略图 / previewMedia 用；?token= 由后端 tokenQuery 映射为鉴权头）
  inlineUrl(fullPath, token) {
    const tk = token === undefined ? wx.getStorageSync('token') : token;
    return `${config.BASE_URL}${API}/download?space=${this.data.space}&path=${encodeURIComponent(fullPath)}&disposition=inline&token=${encodeURIComponent(tk || '')}`;
  },

  attachmentUrl(fullPath) {
    return `${config.BASE_URL}${API}/download?space=${this.data.space}&path=${encodeURIComponent(fullPath)}&disposition=attachment`;
  },

  // 缩略图加载失败回退字母块（图片文件）/ 类型图标加载失败回退字母块或线性文件夹
  onThumbErr(e) {
    const { list, index } = e.currentTarget.dataset;
    this.setData({ [`${list}[${index}].thumbFail`]: true });
  },

  onIconErr(e) {
    const { list, index } = e.currentTarget.dataset;
    // list 为 setData 路径前缀（items / searchItems / picker.dirs / shareSheet.item）
    if (index === undefined || index === '') this.setData({ [`${list}.iconFail`]: true });
    else this.setData({ [`${list}[${index}].iconFail`]: true });
  },

  /* ==================== ⋯ 菜单（分享管理 / 新建文件夹） ==================== */

  onMenuTap() {
    this.setData({ menuOpen: !this.data.menuOpen });
  },

  onMenuClose() {
    if (this.data.menuOpen) this.setData({ menuOpen: false });
  },

  onMenuShares() {
    this.setData({ menuOpen: false });
    wx.navigateTo({ url: '/pkg-netdisk/pages/shares/shares' });
  },

  onMenuMkdir() {
    this.setData({
      menuOpen: false,
      keyboardHeight: 0,
      nameSheet: { open: true, mode: 'mkdir', value: '', target: null, saving: false },
    });
  },

  /* ==================== 搜索（范围=当前空间） ==================== */

  onSearchTap() {
    this.setData({ searching: true });
  },

  onSearchInput(e) {
    this.setData({ kw: e.detail.value });
    if (this._searchTimer) clearTimeout(this._searchTimer);
    this._searchTimer = setTimeout(() => this.doSearch(), 400);
  },

  onSearchConfirm() {
    if (this._searchTimer) clearTimeout(this._searchTimer);
    this.doSearch();
  },

  doSearch() {
    const kw = (this.data.kw || '').trim();
    if (!kw) {
      this.setData({ searchItems: [], searchState: 'idle', searchTotal: 0 });
      return;
    }
    this.setData({ searchState: 'loading' });
    request({
      url: `${API}/search`,
      method: 'POST',
      data: { space: this.data.space, keywords: kw, page: 1, per_page: SEARCH_PER_PAGE },
    }).then((data) => {
      const token = wx.getStorageSync('token');
      const items = ((data && data.items) || []).map((o) => {
        const fullPath = joinPath(o.parent || '/', o.name);
        const row = this.mapRow(o, fullPath, token, true);
        row.sub = `${row.sizeText} · 所在目录：${o.parent || '/'}`;
        return row;
      });
      this.setData({ searchItems: items, searchTotal: (data && data.total) || 0, searchState: 'done' });
    }).catch((err) => {
      this.toast(err.message);
      this.setData({ searchItems: [], searchTotal: 0, searchState: 'done' });
    });
  },

  onSearchCancel() {
    if (this._searchTimer) clearTimeout(this._searchTimer);
    this.setData({ searching: false, kw: '', searchItems: [], searchState: 'idle', searchTotal: 0 });
  },

  // 搜索结果点行：文件夹退出搜索态进入该目录；文件就地预览
  onSearchRowTap(e) {
    const item = e.currentTarget.dataset.item;
    if (item.isDir) {
      this.onSearchCancel();
      this.enterDir(item.fullPath);
      return;
    }
    this.previewFile(item);
  },

  /* ==================== 行交互：进入 / 预览 ==================== */

  onRowTap(e) {
    const item = e.currentTarget.dataset.item;
    if (item.isDir) {
      this.enterDir(item.fullPath);
      return;
    }
    this.previewFile(item);
  },

  // 文件预览：图片/视频 previewMedia（inline 直链）；文档下载后 openDocument；其他仅下载
  previewFile(item) {
    if (item.media) {
      // 目录浏览时同目录媒体合集滑动预览；搜索结果仅预览单项
      const pool = item.fromSearch ? [item] : this.data.items.filter((r) => r.media);
      const sources = pool.map((r) => ({
        url: this.inlineUrl(r.fullPath),
        type: IMG_EXTS.includes(r.ext) ? 'image' : 'video',
      }));
      wx.previewMedia({
        sources,
        current: Math.max(0, pool.findIndex((r) => r.fullPath === item.fullPath)),
      });
      return;
    }
    if (DOC_EXTS.includes(item.ext)) {
      this.downloadFile(item, 'preview');
      return;
    }
    this.downloadFile(item, 'only');
  },

  // 下载（wx.downloadFile 需自带 Authorization；filePath 指定真名避免 openDocument 显示乱码临时名）
  // mode：preview 下载后打开；save 操作面板「下载」（媒体存相册 / 文档打开 / 其他提示）；only 仅下载
  downloadFile(item, mode) {
    wx.showLoading({ title: '下载中…', mask: true });
    wx.downloadFile({
      url: this.attachmentUrl(item.fullPath),
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      timeout: 120000,
      filePath: `${wx.env.USER_DATA_PATH}/${item.name}`,
      success: (res) => {
        if (res.statusCode === 401) {
          this.onExpired();
          return;
        }
        if (res.statusCode !== 200) {
          this.toast(`下载失败（${res.statusCode}）`);
          return;
        }
        this.afterDownload(item, res.filePath, mode);
      },
      fail: () => this.toast('网络异常，请检查网络后重试'),
      complete: () => wx.hideLoading(),
    });
  },

  afterDownload(item, filePath, mode) {
    // 「下载」操作：图片/视频保存至相册
    if (mode === 'save' && item.media) {
      const save = IMG_EXTS.includes(item.ext) ? wx.saveImageToPhotosAlbum : wx.saveVideoToPhotosAlbum;
      save({
        filePath,
        success: () => this.toast('已保存至相册'),
        fail: (err) => {
          if (err && /auth|deny/.test(err.errMsg || '')) this.toast('请在设置中允许保存到相册');
          else this.toast('保存失败');
        },
      });
      return;
    }
    if (mode === 'preview' || (mode === 'save' && DOC_EXTS.includes(item.ext))) {
      wx.openDocument({
        filePath,
        fileType: item.ext,
        showMenu: true, // 右上角菜单可另存/转发
        fail: () => this.toast('该类型暂不支持打开'),
      });
      return;
    }
    this.toast(mode === 'only' ? '暂不支持预览，文件已下载' : '文件已下载');
  },

  /* ==================== 文件操作面板（底部弹层） ==================== */

  onRowMore(e) {
    this.setData({ sheet: { open: true, item: e.currentTarget.dataset.item } });
  },

  closeSheet() {
    this.setData({ 'sheet.open': false });
  },

  onSheetVisibleChange(e) {
    if (!e.detail.visible) this.closeSheet();
  },

  onOpPreview() {
    const item = this.data.sheet.item;
    this.closeSheet();
    if (item) this.previewFile(item);
  },

  onOpDownload() {
    const item = this.data.sheet.item;
    this.closeSheet();
    if (item) this.downloadFile(item, 'save');
  },

  onOpShare() {
    const item = this.data.sheet.item;
    this.closeSheet();
    if (item) this.setData({ shareSheet: { open: true, item, days: 7, code: genCode(), saving: false } });
  },

  onOpRename() {
    const item = this.data.sheet.item;
    this.closeSheet();
    if (item) {
      this.setData({
        keyboardHeight: 0,
        nameSheet: { open: true, mode: 'rename', value: item.name, target: item, saving: false },
      });
    }
  },

  onOpDelete() {
    const item = this.data.sheet.item;
    this.closeSheet();
    if (!item) return;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: item.isDir ? '删除文件夹' : '删除文件',
      content: item.isDir
        ? `「${item.name}」内全部内容将一并删除，该操作不可恢复。`
        : `删除后不可恢复，确定删除「${item.name}」吗？`,
      confirmBtn: '确认删除',
      cancelBtn: '取消',
    }).then(() => {
      request({ url: `${API}/remove`, method: 'POST', data: { space: this.data.space, paths: [item.fullPath] } })
        .then(() => {
          this.toast('已删除');
          this.loadList('quiet');
        })
        .catch((err) => this.toast(err.message));
    }).catch(() => {});
  },

  /* ==================== 压缩包（查看 / 解压到当前目录） ==================== */

  // 查看压缩包：跳转包内浏览页（加密包在该页弹密码层）
  onOpArchiveView() {
    const item = this.data.sheet.item;
    this.closeSheet();
    if (!item) return;
    wx.navigateTo({
      url: `/pkg-netdisk/pages/archive/archive?space=${encodeURIComponent(this.data.space)}` +
        `&path=${encodeURIComponent(item.fullPath)}&name=${encodeURIComponent(item.name)}`,
    });
  },

  // 解压到当前目录（dst_dir=当前路径，put_into_new_dir 默认 true 即以压缩包名建新目录；耗时较长先注明）
  onOpDecompress() {
    const item = this.data.sheet.item;
    this.closeSheet();
    if (!item) return;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '解压到当前目录',
      content: `将「${item.name}」解压到当前目录下的同名文件夹；压缩包较大时耗时较长，请耐心等待。`,
      confirmBtn: '开始解压',
      cancelBtn: '取消',
    }).then(() => {
      wx.showLoading({ title: '解压中…', mask: true });
      request({
        url: `${API}/archive/decompress`,
        method: 'POST',
        timeout: 120000,
        data: { space: this.data.space, path: item.fullPath, dst_dir: this.data.path, put_into_new_dir: true },
      })
        .then(() => {
          this.toast('已解压');
          this.loadList('quiet');
        })
        .catch((err) => this.toast(err.message))
        .finally(() => wx.hideLoading());
    }).catch(() => {});
  },

  /* ==================== 移动 / 复制（目标位置选择弹层） ==================== */

  onOpMove() {
    this.openPicker('move');
  },

  onOpCopy() {
    this.openPicker('copy');
  },

  // 打开目标位置选择：默认当前空间根目录；源位置取打开时快照（sheet 条目必在当前目录）
  openPicker(mode) {
    const item = this.data.sheet.item;
    this.closeSheet();
    if (!item) return;
    this.setData({
      picker: {
        open: true, mode, item,
        srcSpace: this.data.space, srcDir: this.data.path,
        space: this.data.space, dir: '/',
        dirs: [], loading: false, error: '', saving: false,
      },
    });
    this.loadPickerDirs();
  },

  // 目标侧目录列表（只列文件夹）
  async loadPickerDirs() {
    const p = this.data.picker;
    this.setData({ 'picker.loading': true, 'picker.error': '' });
    this.buildPickerCrumbs();
    try {
      const data = await request({ url: `${API}/list`, method: 'POST', data: { space: p.space, path: p.dir } });
      const dirs = ((data && data.items) || []).filter((o) => o.is_dir)
        .map((o) => ({ name: o.name, icon: fileIconUrl(o.name, true), iconFail: false }));
      this.setData({ 'picker.dirs': dirs, 'picker.loading': false });
    } catch (err) {
      this.setData({ 'picker.loading': false, 'picker.error': err.message || '目录加载失败' });
    }
  },

  buildPickerCrumbs() {
    const p = this.data.picker;
    const cur = this.data.spaces.find((s) => s.key === p.space);
    const segs = p.dir.split('/').filter(Boolean);
    const crumbs = [{ name: cur ? cur.name : p.space, path: '/', last: segs.length === 0 }];
    segs.forEach((s, i) => {
      crumbs.push({ name: s, path: `/${segs.slice(0, i + 1).join('/')}`, last: i === segs.length - 1 });
    });
    this.setData({ pickerCrumbs: crumbs });
  },

  onPickerSpaceTap(e) {
    const key = e.currentTarget.dataset.key;
    if (!key || key === this.data.picker.space) return;
    this.setData({ 'picker.space': key, 'picker.dir': '/', 'picker.dirs': [] });
    this.loadPickerDirs();
  },

  onPickerCrumbTap(e) {
    const path = e.currentTarget.dataset.path;
    if (path === this.data.picker.dir) return;
    this.setData({ 'picker.dir': path, 'picker.dirs': [] });
    this.loadPickerDirs();
  },

  onPickerDirTap(e) {
    this.setData({ 'picker.dir': joinPath(this.data.picker.dir, e.currentTarget.dataset.name), 'picker.dirs': [] });
    this.loadPickerDirs();
  },

  onPickerCancel() {
    if (this.data.picker.saving) return;
    this.setData({ 'picker.open': false });
  },

  onPickerVisibleChange(e) {
    if (e.detail.visible) return;
    if (this.data.picker.saving) {
      this.setData({ 'picker.open': true });
      return;
    }
    if (this.data.picker.open) this.setData({ 'picker.open': false });
  },

  onPickerConfirm() {
    const p = this.data.picker;
    if (!p.item || p.saving || p.loading || p.error) return;
    if (p.space === p.srcSpace && p.dir === p.srcDir) {
      this.toast('源位置与目标位置相同');
      return;
    }
    // 文件夹不能移动/复制到自身内部
    if (p.item.isDir && p.space === p.srcSpace
      && (p.dir === p.item.fullPath || p.dir.startsWith(`${p.item.fullPath}/`))) {
      this.toast('目标位置在源文件夹内部');
      return;
    }
    const move = p.mode === 'move';
    this.setData({ 'picker.saving': true });
    request({
      url: `${API}/${p.mode}`,
      method: 'POST',
      timeout: 120000,
      data: {
        src_space: p.srcSpace, src_dir: p.srcDir,
        dst_space: p.space, dst_dir: p.dir,
        names: [p.item.name],
      },
    }).then(() => {
      this.setData({ 'picker.open': false });
      this.toast(move ? '已移动' : '已复制');
      this.loadList('quiet');
    }).catch((err) => this.toast(err.message))
      .finally(() => this.setData({ 'picker.saving': false }));
  },

  /* ==================== 名称输入弹层（新建文件夹 / 重命名） ==================== */

  onNameInput(e) {
    this.setData({ 'nameSheet.value': e.detail.value });
  },

  onKeyboardHeight(e) {
    const h = e.detail.height || 0;
    this.setData({ keyboardHeight: h > 0 ? h : 0 });
  },

  onNameCancel() {
    if (this.data.nameSheet.saving) return;
    wx.hideKeyboard(); // 关层前收起 hold-keyboard 残留键盘
    this.setData({ 'nameSheet.open': false, keyboardHeight: 0 });
  },

  // 提交中不允许遮罩关闭
  onNameVisibleChange(e) {
    if (e.detail.visible) return;
    if (this.data.nameSheet.saving) {
      this.setData({ 'nameSheet.open': true });
      return;
    }
    if (this.data.nameSheet.open) {
      wx.hideKeyboard();
      this.setData({ 'nameSheet.open': false, keyboardHeight: 0 });
    }
  },

  onNameSave() {
    const ns = this.data.nameSheet;
    if (ns.saving) return;
    const name = (ns.value || '').trim();
    if (!name) {
      this.toast('请输入名称');
      return;
    }
    if (/[/\\\r\n]/.test(name) || name === '.' || name === '..') {
      this.toast('名称不能包含斜杠或纯点');
      return;
    }
    const mkdir = ns.mode === 'mkdir';
    wx.hideKeyboard();
    this.setData({ 'nameSheet.saving': true });
    request({
      url: `${API}/${mkdir ? 'mkdir' : 'rename'}`,
      method: 'POST',
      data: mkdir
        ? { space: this.data.space, path: this.data.path, name }
        : { space: this.data.space, path: ns.target.fullPath, name },
    }).then(() => {
      this.setData({ 'nameSheet.open': false, keyboardHeight: 0 });
      this.toast(mkdir ? '已创建' : '已重命名');
      this.loadList('quiet');
    }).catch((err) => this.toast(err.message))
      .finally(() => this.setData({ 'nameSheet.saving': false }));
  },

  /* ==================== 创建分享弹层（屏③） ==================== */

  onShareDaysTap(e) {
    this.setData({ 'shareSheet.days': Number(e.currentTarget.dataset.days) });
  },

  onShareCodeRefresh() {
    this.setData({ 'shareSheet.code': genCode() });
  },

  onShareCancel() {
    if (this.data.shareSheet.saving) return;
    this.setData({ 'shareSheet.open': false });
  },

  onShareVisibleChange(e) {
    if (e.detail.visible) return;
    if (this.data.shareSheet.saving) {
      this.setData({ 'shareSheet.open': true });
      return;
    }
    if (this.data.shareSheet.open) this.setData({ 'shareSheet.open': false });
  },

  onShareCreate() {
    const ss = this.data.shareSheet;
    if (ss.saving || !ss.item) return;
    this.setData({ 'shareSheet.saving': true });
    request({
      url: `${API}/shares`,
      method: 'POST',
      data: { space: this.data.space, paths: [ss.item.fullPath], password: ss.code, expire_days: ss.days },
    }).then((data) => {
      this.setData({ shareSheet: { open: false, item: null, days: 7, code: '', saving: false } });
      const link = `${config.BASE_URL}/share.html#/s/${data.share_id}`;
      const expireText = ss.days > 0
        ? `\n有效期至 ${fmtDay(new Date(Date.now() + ss.days * 86400000))}`
        : '\n永久有效';
      // 成功 Dialog：展示链接 + 提取码，确认键复制到剪贴板
      Dialog.confirm({
        context: this,
        selector: '#t-dialog',
        title: '分享已创建',
        content: `链接：${link}\n提取码：${ss.code}${expireText}`,
        confirmBtn: '复制链接',
        cancelBtn: '关闭',
      }).then(() => {
        wx.setClipboardData({ data: `${link}\n提取码：${ss.code}` });
      }).catch(() => {});
    }).catch((err) => this.toast(err.message))
      .finally(() => this.setData({ 'shareSheet.saving': false }));
  },

  /* ==================== 分片上传（FAB → 聊天选取 → init/chunk/complete） ==================== */

  onPickFiles() {
    wx.chooseMessageFile({
      count: 9,
      type: 'file',
      success: (res) => this.startUploads(res.tempFiles || []),
    });
  },

  startUploads(files) {
    if (!files.length) return;
    const ok = [];
    let skipped = 0;
    files.forEach((f) => {
      // 单文件超上限前端直接拦截（后端 41301 兜底）
      if (this._maxBytes && f.size > this._maxBytes) {
        skipped += 1;
        return;
      }
      ok.push({ name: f.name, size: f.size, path: f.path });
    });
    if (skipped) this.toast(`${skipped} 个文件超过单文件上限（${this.data.maxUploadMb}MB）已跳过`);
    if (ok.length) this.runQueue(ok);
  },

  // 逐文件上传（文件间串行，片内并发 2）；全部结束后统一汇总提示
  async runQueue(files) {
    let done = 0;
    let failMsg = '';
    for (let i = 0; i < files.length; i += 1) {
      try {
        // eslint-disable-next-line no-await-in-loop
        await this.uploadOne(files[i]);
        done += 1;
        this.loadList('quiet'); // 每完成一个即刷新列表（静默，不动骨架屏）
      } catch (err) {
        if (err && err.cancelled) continue; // 用户主动取消：行已移除，不计失败
        failMsg = `${files[i].name}：${err.message}`;
      }
    }
    if (failMsg) this.toast(`已传 ${done}/${files.length}，失败：${failMsg}`);
    else if (done) this.toast(done === 1 ? '已上传' : `已上传 ${done} 个文件`);
  },

  async uploadOne(file) {
    const id = `up_${Date.now()}_${Math.floor(Math.random() * 100000)}`;
    const task = {
      id,
      name: file.name,
      size: file.size,
      path: file.path,
      space: this.data.space, // 目标空间/目录取开始上传时快照，中途切目录不影响
      dir: this.data.path,
      uploadId: '',
      cancelled: false,
      merging: false,
      doneBytes: 0,
    };
    this._tasks[id] = task;
    this.pushUploadRow(task);
    try {
      const init = await request({
        url: `${API}/upload/init`,
        method: 'POST',
        timeout: 60000,
        data: { space: task.space, path: task.dir, name: file.name, size: file.size },
      });
      if (init && init.instant) {
        // 0 字节文件：后端已直接建成
        this.removeUploadRow(id);
        return;
      }
      task.uploadId = init.upload_id;
      const chunkSize = init.chunk_size;
      const total = init.total_chunks;
      // 断线续传：received 区间已覆盖的分片直接跳过并计入进度
      const pending = [];
      for (let i = 0; i < total; i += 1) {
        const start = i * chunkSize;
        const end = Math.min(start + chunkSize, file.size) - 1;
        const covered = ((init && init.received) || []).some((rg) => rg && rg[0] <= start && rg[1] >= end);
        if (covered) task.doneBytes += end - start + 1;
        else pending.push(i);
      }
      this.updateUploadRow(task);
      // 2 片并发：游标取片；429/409 retry 在 uploadChunk 内退避重传
      let cursor = 0;
      const worker = async () => {
        while (cursor < pending.length) {
          if (task.cancelled) throw CANCELLED;
          const idx = pending[cursor];
          cursor += 1;
          // eslint-disable-next-line no-await-in-loop
          await this.uploadChunk(task, idx, chunkSize);
          if (task.cancelled) throw CANCELLED;
        }
      };
      const workers = [];
      for (let w = 0; w < CHUNK_CONCURRENCY; w += 1) workers.push(worker());
      await Promise.all(workers);
      await this.completeUpload(task);
      this.removeUploadRow(id);
    } catch (err) {
      this.removeUploadRow(id);
      if (err && err.cancelled) throw err;
      // 失败兜底：尝试 abort 会话，避免残留分片占用空间
      if (task.uploadId) {
        request({ url: `${API}/upload/abort`, method: 'POST', data: { upload_id: task.uploadId } }).catch(() => {});
      }
      throw err;
    } finally {
      delete this._tasks[id];
    }
  },

  // 分片读取：按 position/length 切出 ArrayBuffer
  readSlice(filePath, position, length) {
    return new Promise((resolve, reject) => {
      wx.getFileSystemManager().readFile({
        filePath,
        position,
        length,
        success: (res) => resolve(res.data),
        fail: () => reject(new Error('读取文件分片失败')),
      });
    });
  },

  // 传一片（裸字节；JSON 信封 data 为会话快照；429/409 且 data.retry 标记由调用方退避重传）
  putChunk(uploadId, idx, buf) {
    return new Promise((resolve, reject) => {
      wx.request({
        url: `${config.BASE_URL}${API}/upload/chunk`,
        method: 'PUT',
        data: buf,
        timeout: 120000,
        header: {
          Authorization: `Bearer ${wx.getStorageSync('token')}`,
          'X-Upload-Id': uploadId,
          'X-Chunk-Index': String(idx),
          'Content-Type': 'application/octet-stream',
        },
        success: (res) => {
          const body = res.data || {};
          if (res.statusCode >= 200 && res.statusCode < 300 && body.code === 0) {
            resolve(body.data);
            return;
          }
          if (res.statusCode === 401) {
            this.onExpired();
            reject(new Error('登录已过期，请重新登录'));
            return;
          }
          const err = new Error(body.message || `分片上传失败（${res.statusCode}）`);
          if ((res.statusCode === 429 || res.statusCode === 409) && body.data && body.data.retry) err.retry = true;
          reject(err);
        },
        fail: () => reject(new Error('网络异常，请检查网络后重试')),
      });
    });
  },

  async uploadChunk(task, idx, chunkSize) {
    const start = idx * chunkSize;
    const len = Math.min(chunkSize, task.size - start);
    const buf = await this.readSlice(task.path, start, len);
    for (let attempt = 0; attempt < CHUNK_MAX_TRY; attempt += 1) {
      if (task.cancelled) throw CANCELLED;
      try {
        // eslint-disable-next-line no-await-in-loop
        const snap = await this.putChunk(task.uploadId, idx, buf);
        task.doneBytes += len;
        // 进度以会话快照为准（并发他片已计入），本地累计只作下限
        if (snap && typeof snap.received_bytes === 'number') {
          task.doneBytes = Math.max(task.doneBytes, snap.received_bytes);
        }
        task.doneBytes = Math.min(task.doneBytes, task.size);
        this.updateUploadRow(task);
        return;
      } catch (err) {
        if (err && err.retry && attempt < CHUNK_MAX_TRY - 1) {
          // eslint-disable-next-line no-await-in-loop
          await sleep(1000 + Math.random() * 1000); // 退避 1~2s 重传本片
          continue;
        }
        throw err;
      }
    }
  },

  // 合并：长阻塞正常（timeout 120s）；请求被中间层掐断时回落 status 轮询终态
  async completeUpload(task) {
    task.merging = true;
    this.updateUploadRow(task);
    try {
      await request({ url: `${API}/upload/complete`, method: 'POST', data: { upload_id: task.uploadId }, timeout: 120000 });
      return;
    } catch (err) {
      for (let i = 0; i < 20; i += 1) {
        // eslint-disable-next-line no-await-in-loop
        await sleep(3000);
        if (task.cancelled) throw CANCELLED;
        try {
          // eslint-disable-next-line no-await-in-loop
          const st = await request({ url: `${API}/upload/status?upload_id=${encodeURIComponent(task.uploadId)}` });
          if (st && st.state === 'completed') return;
        } catch (perr) { /* 查询失败继续等 */ }
      }
      throw new Error('合并状态确认超时，请稍后刷新确认');
    }
  },

  onCancelUpload(e) {
    const id = e.currentTarget.dataset.id;
    const task = this._tasks[id];
    if (task) {
      task.cancelled = true;
      if (task.uploadId) {
        request({ url: `${API}/upload/abort`, method: 'POST', data: { upload_id: task.uploadId } }).catch(() => {});
      }
    }
    this.removeUploadRow(id);
    this.toast('已取消上传');
  },

  /* ==================== 上传行（列表顶部内联） ==================== */

  pushUploadRow(task) {
    if (!this._alive) return;
    const row = { id: task.id, name: task.name, sizeText: fmtSize(task.size), sentText: '0 B', pct: 0, merging: false };
    this.setData({ uploads: [row].concat(this.data.uploads) });
  },

  updateUploadRow(task) {
    if (!this._alive) return;
    const pct = task.size ? Math.min(100, Math.floor((task.doneBytes / task.size) * 100)) : 100;
    const uploads = this.data.uploads.map((r) => (
      r.id === task.id ? { ...r, sentText: fmtSize(task.doneBytes), pct, merging: !!task.merging } : r
    ));
    this.setData({ uploads });
  },

  removeUploadRow(id) {
    if (!this._alive) return;
    this.setData({ uploads: this.data.uploads.filter((r) => r.id !== id) });
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'netdisk', title: '团队网盘' });
  },
});
