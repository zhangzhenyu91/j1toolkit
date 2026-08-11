// 文件传输 · 设备列表 + 虚拟 U 盘上传/下载（移动端应用，app_key file-transfer）
// 列表数据实时代理自 GLKVM Cloud 平台（/api/v1/kvm/devices，kvm 或 file-transfer 任一权限）；
// 上传/下载经壹匣转发点（/api/v1/kvm/devices/{id}/push|files|download|mount|status），平台链路直达设备
// 班组口径（屏九）：设备按生效班组过滤，本班组无设备时回退默认班组设备并显示黄色提示横幅（fallback）；
// 超管顶部切换器可切班组（storage filetransfer_team_id，全部请求带 team_id）；
// 非超管未分配班组 → 整页空态（屏十）
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';
import config from '../../../config';

// 平台设备状态 → 展示（与网页端 kvm.html 同口径）
const STATUS_MAP = {
  online: { key: 'online', text: '在线' },
  disabled: { key: 'disabled', text: '禁用' },
  offline: { key: 'offline', text: '离线' },
};

// 单文件大小上限（经壹匣内存中转，与弹层说明一致）
const MAX_FILE_SIZE = 200 * 1024 * 1024;

const IMG_EXTS = ['jpg', 'jpeg', 'png', 'gif', 'bmp', 'webp'];
const VIDEO_EXTS = ['mp4', 'mov', 'm4v', 'avi', 'mkv'];
// wx.openDocument 可识别的文档类型（传 fileType 提高打开成功率）
const DOC_EXTS = ['doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'pdf'];

const extOf = (name) => {
  const i = (name || '').lastIndexOf('.');
  return i < 0 ? '' : name.slice(i + 1).toLowerCase();
};
const kindOf = (name) => {
  const ext = extOf(name);
  if (IMG_EXTS.includes(ext)) return 'image';
  if (VIDEO_EXTS.includes(ext)) return 'video';
  return 'file';
};
const iconOf = (name) => ({ image: 'file-image', video: 'video', file: 'file' }[kindOf(name)]);
const fmtSize = (n) => {
  if (n >= 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  if (n >= 1024) return `${Math.round(n / 1024)} KB`;
  return `${n} B`;
};

const pad = (n) => (n < 10 ? `0${n}` : `${n}`);
// unix 秒 → 'YYYY-MM-DD HH:mm'（同网页端 Shade.fmtDate(d, true)）
const fmtLast = (sec) => {
  const d = new Date(sec * 1000);
  if (Number.isNaN(d.getTime())) return '—';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
};

Page({
  data: {
    gate: false, // 门控（参照首页 gate 模式）
    list: [],
    loading: true, // 首屏加载中
    error: '', // 首屏加载失败文案（已有内容时失败仅 toast）
    // 班组设备回退提示（屏九）：本班组无设备时回退展示默认班组设备
    fallback: false,
    fallbackTeam: '',
    // 班组切换器（同 pkg-worklog 口径）：超管可点 chip 下拉切换；其余角色为静态班组名标签
    isAdmin: false,
    noTeam: false, // 非超管且未分配班组：整页空态（屏十），不发业务请求
    teamName: '',
    teamOptions: [], // [{id, name, on}]
    teamDropOpen: false,

    // 上传弹层
    upOpen: false,
    upDevice: {},
    upFiles: [], // [{name, size, sizeText, path, icon}]
    upTotalText: '0 B',
    uploading: false,
    upIndex: 0,

    // 下载弹层（dlState：checking 查询挂载状态 / shared 已挂载到目标计算机 / local 挂载在 KVM 显示文件列表）
    dlOpen: false,
    dlDevice: {},
    dlState: 'checking',
    dlFiles: [], // [{name, size, sizeText, icon}]
    dlLoading: false, // 「获取文件列表」执行中（断开共享并拉列表）
    downloading: '', // 正在下载的文件名
    mounting: false, // 正在挂载到目标计算机
  },

  onLoad() {
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
    this._role = user.role || 'user';
    this._teamId = 0; // 生效班组 id（仅超管经切换器指定；0=不带参数，后端落自己/默认班组）
    // 非超管且未分配班组：整页空态（屏十），不再发任何业务请求
    if (this._role !== 'admin' && !user.team) {
      this.setData({ gate: true, noTeam: true, loading: false });
      return;
    }
    this.setData({ gate: true, isAdmin: this._role === 'admin', teamName: user.team || '' });
    if (this._role === 'admin') {
      this.initTeams(user); // 超管先定生效班组，再加载设备
      return;
    }
    this.loadDevices('init');
    this.startTimer();
  },

  // ---------- 班组切换器（仅超管可切换，其余角色静态展示本班名） ----------

  // 超管：拉启用班组（/admin/teams 取 status=1）→ 生效班组（storage 优先 → 自己班组 → 第一个）→ 设备列表
  async initTeams(user) {
    let teams = [];
    try {
      const data = await request({ url: '/api/v1/admin/teams' });
      teams = ((data && data.list) || []).filter((t) => t.status === 1);
    } catch (err) {
      this.toast(err.message);
    }
    this._teams = teams;
    const saved = Number(wx.getStorageSync('filetransfer_team_id')) || 0;
    const cur = teams.find((t) => t.id === saved)
      || teams.find((t) => t.id === Number(user.team_id))
      || teams[0] || null;
    this.applyTeam(cur ? cur.id : 0, false);
    this.loadDevices('init');
    this.startTimer();
  },

  // 生效班组 query 片段（lead 为前导连接符；仅超管 _teamId>0 时携带，其余角色后端强制本班无需传）
  teamQuery(lead) {
    return this._teamId ? `${lead || '&'}team_id=${this._teamId}` : '';
  },

  // 生效班组 body 注入（POST JSON 用，口径同 teamQuery）
  teamBody(data) {
    return this._teamId ? Object.assign({}, data, { team_id: this._teamId }) : data;
  },

  // 记录当前生效班组并刷新切换器展示；switching=true 表示用户主动切换，重拉设备列表
  applyTeam(id, switching) {
    this._teamId = id;
    if (id) wx.setStorageSync('filetransfer_team_id', id);
    const cur = ((this._teams || []).find((t) => t.id === id)) || null;
    this.setData({
      teamName: cur ? cur.name : this.data.teamName,
      teamDropOpen: false,
      teamOptions: (this._teams || []).map((t) => ({ id: t.id, name: t.name, on: t.id === id })),
    });
    if (switching) this.loadDevices('manual');
  },

  onTeamChipTap() {
    if (!this.data.isAdmin || !(this._teams || []).length) return;
    this.setData({ teamDropOpen: !this.data.teamDropOpen });
  },

  onTeamDropClose() {
    if (this.data.teamDropOpen) this.setData({ teamDropOpen: false });
  },

  onTeamPick(e) {
    const id = Number(e.currentTarget.dataset.id);
    if (!id || id === this._teamId) {
      this.setData({ teamDropOpen: false });
      return;
    }
    this.applyTeam(id, true);
  },

  onShow() {
    if (!this.data.gate || this.data.noTeam) return;
    // 首屏由 passGate 触发 init，此处仅后续进场（切后台回来等）静默刷新
    if (this._loaded) this.loadDevices('auto');
    this.startTimer();
  },

  onHide() {
    this.clearTimer();
  },

  onUnload() {
    this.clearTimer();
  },

  startTimer() {
    this.clearTimer();
    this._timer = setInterval(() => this.loadDevices('auto'), 30000);
  },

  clearTimer() {
    if (this._timer) {
      clearInterval(this._timer);
      this._timer = null;
    }
  },

  onPullDownRefresh() {
    if (this.data.noTeam) {
      wx.stopPullDownRefresh();
      return;
    }
    this.loadDevices('manual').finally(() => wx.stopPullDownRefresh());
  },

  onRetry() {
    this.loadDevices('init');
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  // 加载模式：init 首屏/重试（显示加载中文案）；manual 下拉刷新；auto 定时/进场静默
  async loadDevices(mode) {
    if (mode === 'init') this.setData({ loading: true, error: '' });
    try {
      const data = await request({ url: `/api/v1/kvm/devices${this.teamQuery('?')}` });
      const list = (((data && data.items) || [])).map((d) => {
        const st = STATUS_MAP[d.status] || STATUS_MAP.offline;
        return {
          id: d.id,
          statusKey: st.key,
          statusText: st.text,
          group: d.deviceGroupName || '',
          name: d.description || d.ddns || `设备 #${d.id}`,
          ddns: d.ddns || '',
          ip: d.ip || '—',
          mac: d.mac || '—',
          last: d.status === 'online' ? '当前在线' : (d.connectedTime ? fmtLast(d.connectedTime) : '—'),
        };
      });
      this._loaded = true;
      this.setData({
        list,
        loading: false,
        error: '',
        // 班组设备回退提示（屏九）：本班组无设备时展示默认班组设备 + 黄色横幅
        fallback: !!(data && data.fallback),
        fallbackTeam: (data && data.fallback_team) || '',
      });
    } catch (err) {
      // 静默刷新失败不打断页面；已有内容时仅提示
      if (mode === 'auto' || this._loaded) {
        if (mode !== 'auto') this.toast(err.message);
      } else {
        this.setData({ loading: false, error: err.message || '设备列表加载失败' });
      }
    }
  },

  /* ==================== 上传 ==================== */

  onOpenUpload(e) {
    const dev = e.currentTarget.dataset.item;
    if (dev.statusKey !== 'online') {
      this.toast('设备离线，不可传输文件');
      return;
    }
    this.setData({ upOpen: true, upDevice: dev, upFiles: [], upTotalText: '0 B' });
  },

  onCloseUpload() {
    if (this.data.uploading) return; // 上传中不允许关
    this.setData({ upOpen: false });
  },

  onUpVisibleChange(e) {
    if (!e.detail.visible && !this.data.uploading) this.setData({ upOpen: false });
  },

  // 已添加列表汇总（合计大小）
  refreshUpFiles(upFiles) {
    const total = upFiles.reduce((sum, f) => sum + f.size, 0);
    this.setData({ upFiles, upTotalText: fmtSize(total) });
  },

  // 归一化加入待传列表（同名去重、超限剔除）
  addUpFiles(cands) {
    const upFiles = [...this.data.upFiles];
    let rejected = 0;
    for (const c of cands) {
      if (c.size > MAX_FILE_SIZE) {
        rejected += 1;
        continue;
      }
      const item = {
        name: c.name,
        size: c.size,
        sizeText: fmtSize(c.size),
        path: c.path,
        icon: iconOf(c.name),
      };
      const idx = upFiles.findIndex((f) => f.name === item.name);
      if (idx >= 0) upFiles.splice(idx, 1, item); // 同名替换
      else upFiles.push(item);
    }
    if (rejected) this.toast(`${rejected} 个文件超过 200MB 已剔除`);
    this.refreshUpFiles(upFiles);
  },

  // 从手机选择：图片 / 视频（微信无任意文件选择器，任意类型走聊天选取）
  // chooseMedia 只有临时路径（无原名），按相机命名习惯生成可读文件名
  onPickMedia() {
    wx.chooseMedia({
      count: 9,
      mediaType: ['image', 'video'],
      success: (res) => {
        const cands = (res.tempFiles || []).map((t, i) => {
          const ext = t.tempFilePath.split('.').pop() || (t.fileType === 'video' ? 'mp4' : 'jpg');
          const d = new Date();
          const ts = `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}` +
            `_${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
          return {
            name: `${t.fileType === 'video' ? 'VID' : 'IMG'}_${ts}${i ? `_${i}` : ''}.${ext}`,
            size: t.size,
            path: t.tempFilePath,
          };
        });
        this.addUpFiles(cands);
      },
    });
  },

  // 从聊天选取：任意类型文件
  onPickChat() {
    wx.chooseMessageFile({
      count: 9,
      type: 'file',
      success: (res) => {
        this.addUpFiles((res.tempFiles || []).map((t) => ({
          name: t.name,
          size: t.size,
          path: t.path,
        })));
      },
    });
  },

  onRemoveUpFile(e) {
    if (this.data.uploading) return;
    const upFiles = [...this.data.upFiles];
    upFiles.splice(e.currentTarget.dataset.index, 1);
    this.refreshUpFiles(upFiles);
  },

  // 单文件推送（wx.uploadFile 一次一个文件；
  // uploadFile 会把临时路径 basename 当 multipart 文件名，真实文件名走表单字段 filename）
  pushFile(deviceId, file) {
    return new Promise((resolve, reject) => {
      wx.uploadFile({
        url: `${config.BASE_URL}/api/v1/kvm/devices/${deviceId}/push`,
        filePath: file.path,
        name: 'files',
        formData: this._teamId ? { filename: file.name, team_id: this._teamId } : { filename: file.name },
        header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
        timeout: 120000,
        success(res) {
          let body = {};
          try { body = JSON.parse(res.data); } catch (e) { /* 保持空对象 */ }
          if (res.statusCode >= 200 && res.statusCode < 300 && body.code === 0) {
            resolve(body.data);
          } else {
            reject(new Error(body.message || `上传失败（${res.statusCode}）`));
          }
        },
        fail: () => reject(new Error('网络异常，请检查网络后重试')),
      });
    });
  },

  // 开始上传：逐文件推送（设备侧全程非共享），全部成功或部分成功后统一挂载一次
  async onUploadStart() {
    const { upFiles, upDevice, uploading } = this.data;
    if (!upFiles.length || uploading) return;
    this.setData({ uploading: true, upIndex: 0 });

    let okCount = 0;
    let failMsg = '';
    for (let i = 0; i < upFiles.length; i += 1) {
      this.setData({ upIndex: i + 1 });
      try {
        // eslint-disable-next-line no-await-in-loop
        await this.pushFile(upDevice.id, upFiles[i]);
        okCount += 1;
      } catch (err) {
        failMsg = `${upFiles[i].name}：${err.message}`;
        break; // 失败即中止，已传部分仍挂载
      }
    }

    if (okCount > 0) {
      try {
        await request({ url: `/api/v1/kvm/devices/${upDevice.id}/mount`, method: 'POST', data: this.teamBody({}), timeout: 60000 });
      } catch (err) {
        this.setData({ uploading: false });
        this.toast(`挂载失败：${err.message}（文件已在盘内，可重试挂载）`);
        return;
      }
    }

    this.setData({ uploading: false, upOpen: false, upFiles: [] });
    if (failMsg) {
      this.toast(`已传 ${okCount}/${upFiles.length}，失败：${failMsg}`);
    } else {
      this.toast(`已上传 ${okCount} 个文件并挂载到被控机`);
    }
  },

  /* ==================== 下载 ==================== */

  // 打开弹层不直接拉列表：先查挂载状态（设备 /status 被动查询，不切换状态）
  onOpenDownload(e) {
    const dev = e.currentTarget.dataset.item;
    if (dev.statusKey !== 'online') {
      this.toast('设备离线，不可传输文件');
      return;
    }
    this.setData({
      dlOpen: true, dlDevice: dev, dlFiles: [], dlLoading: false, dlState: 'checking',
    });
    this.checkDlState();
  },

  onCloseDownload() {
    this.setData({ dlOpen: false });
  },

  onDlVisibleChange(e) {
    if (!e.detail.visible) this.setData({ dlOpen: false });
  },

  // 挂载状态分流：已共享给目标计算机则先给「获取文件列表」按钮（列出会断开共享，由用户确认）；
  // 挂载在 KVM 本机则直接展示盘内文件（status 响应自带 files，无需二次调用）
  async checkDlState() {
    try {
      const data = await request({
        url: `/api/v1/kvm/devices/${this.data.dlDevice.id}/status${this.teamQuery('?')}`,
        timeout: 60000,
      });
      if (data && data.shared) {
        this.setData({ dlState: 'shared' });
      } else {
        this.setData({ dlFiles: this.mapDlFiles(data && data.files), dlState: 'local' });
      }
    } catch (err) {
      // 状态查询失败（如设备侧旧版无 /status）：回退直接拉列表，持续故障由列表报错带出
      this.loadDlFiles();
    }
  },

  mapDlFiles(files) {
    return (files || []).map((f) => ({
      name: f.name,
      size: f.size,
      sizeText: fmtSize(f.size),
      icon: iconOf(f.name),
    }));
  },

  // 「获取文件列表」（设备 /list：共享中先断开、挂载回 KVM，再返回列表）
  async loadDlFiles() {
    if (this.data.dlLoading) return;
    this.setData({ dlLoading: true });
    try {
      const data = await request({
        url: `/api/v1/kvm/devices/${this.data.dlDevice.id}/files${this.teamQuery('?')}`,
        timeout: 60000,
      });
      this.setData({ dlFiles: this.mapDlFiles(data && data.files), dlLoading: false, dlState: 'local' });
    } catch (err) {
      this.setData({ dlLoading: false, dlOpen: false });
      this.toast(err.message || '读取盘内文件失败');
    }
  },

  // 点按文件：下载 → 图片/视频存相册，其他 wx.openDocument 打开
  onDlFileTap(e) {
    const file = e.currentTarget.dataset.file;
    if (this.data.downloading) return;
    this.setData({ downloading: file.name });
    wx.downloadFile({
      url: `${config.BASE_URL}/api/v1/kvm/devices/${this.data.dlDevice.id}/download?name=${encodeURIComponent(file.name)}${this.teamQuery()}`,
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      timeout: 120000,
      // 指定本地存储文件名，否则 openDocument 打开后显示的是随机临时文件名（乱码）
      filePath: `${wx.env.USER_DATA_PATH}/${file.name}`,
      success: (res) => {
        if (res.statusCode !== 200) {
          this.toast(`下载失败（${res.statusCode}）`);
          return;
        }
        this.saveDownload(file, res.filePath);
      },
      fail: () => this.toast('网络异常，请检查网络后重试'),
      complete: () => this.setData({ downloading: '' }),
    });
  },

  saveDownload(file, tempFilePath) {
    const kind = kindOf(file.name);
    if (kind === 'image' || kind === 'video') {
      const save = kind === 'image' ? wx.saveImageToPhotosAlbum : wx.saveVideoToPhotosAlbum;
      save({
        filePath: tempFilePath,
        success: () => this.toast('已保存至相册'),
        fail: (err) => {
          if (err && /auth|deny/.test(err.errMsg || '')) {
            this.toast('请在设置中允许保存到相册');
          } else {
            this.toast('保存失败');
          }
        },
      });
      return;
    }
    const ext = extOf(file.name);
    wx.openDocument({
      filePath: tempFilePath,
      showMenu: true, // 右上角菜单可另存/转发
      ...(DOC_EXTS.includes(ext) ? { fileType: ext } : {}),
      fail: () => this.toast('该类型暂不支持打开'),
    });
  },

  // 删除盘内文件（图标小按钮 + 二次确认，同 Call Me 删除对话交互）
  onDeleteFile(e) {
    const { name, index } = e.currentTarget.dataset;
    if (this.data.downloading === name) {
      this.toast('该文件正在下载，请稍候');
      return;
    }
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '删除文件',
      content: `删除后不可恢复，确定删除「${name}」吗？`,
      confirmBtn: '删除',
      cancelBtn: '取消',
    }).then(async () => {
      try {
        const data = await request({
          url: `/api/v1/kvm/devices/${this.data.dlDevice.id}/delete`,
          method: 'POST',
          data: this.teamBody({ names: [name] }),
          timeout: 60000,
        });
        // deleted/missing 均视为已不在盘内，从列表移除
        const dlFiles = this.data.dlFiles.slice();
        dlFiles.splice(index, 1);
        this.setData({ dlFiles });
        this.toast((data && (data.deleted || []).includes(name)) ? '已删除' : '文件已不存在');
      } catch (err) {
        this.toast(err.message);
      }
    }).catch(() => {});
  },

  // 挂载 U 盘至目标计算机（被控机向盘内放入文件场景：挂载后弹层切到 shared 态，
  // 放好后点「获取文件列表」断开共享并读取新文件）
  async onMount() {
    const { dlDevice, mounting, downloading } = this.data;
    if (mounting) return;
    if (downloading) {
      this.toast('文件下载中，请稍候');
      return;
    }
    this.setData({ mounting: true });
    try {
      await request({ url: `/api/v1/kvm/devices/${dlDevice.id}/mount`, method: 'POST', data: this.teamBody({}), timeout: 60000 });
      this.setData({ dlState: 'shared' });
      this.toast('已挂载到目标计算机，可在该机向 U 盘放入文件');
    } catch (err) {
      this.toast(`挂载失败：${err.message}`);
    } finally {
      this.setData({ mounting: false });
    }
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'file-transfer', title: '文件传输' });
  },
});
