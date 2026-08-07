// 文件传输 · 设备列表 + 虚拟 U 盘上传/下载（移动端应用，app_key file-transfer）
// 列表数据实时代理自 GLKVM Cloud 平台（/api/v1/kvm/devices，kvm 或 file-transfer 任一权限）；
// 上传/下载经壹匣转发点（/api/v1/kvm/devices/{id}/push|files|download|mount），平台链路直达设备
import Toast from 'tdesign-miniprogram/toast/index';
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

    // 上传弹层
    upOpen: false,
    upDevice: {},
    upFiles: [], // [{name, size, sizeText, path, icon}]
    upTotalText: '0 B',
    uploading: false,
    upIndex: 0,

    // 下载弹层
    dlOpen: false,
    dlDevice: {},
    dlFiles: [], // [{name, size, sizeText, icon}]
    dlLoading: false,
    downloading: '', // 正在下载的文件名
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
    this.setData({ gate: true });
    this.loadDevices('init');
    this.startTimer();
  },

  onShow() {
    if (!this.data.gate) return;
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
      const data = await request({ url: '/api/v1/kvm/devices' });
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
      this.setData({ list, loading: false, error: '' });
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
        formData: { filename: file.name },
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
        await request({ url: `/api/v1/kvm/devices/${upDevice.id}/mount`, method: 'POST', timeout: 60000 });
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

  onOpenDownload(e) {
    const dev = e.currentTarget.dataset.item;
    if (dev.statusKey !== 'online') {
      this.toast('设备离线，不可传输文件');
      return;
    }
    this.setData({ dlOpen: true, dlDevice: dev, dlFiles: [], dlLoading: true });
    this.loadDlFiles();
  },

  onCloseDownload() {
    this.setData({ dlOpen: false });
  },

  onDlVisibleChange(e) {
    if (!e.detail.visible) this.setData({ dlOpen: false });
  },

  async loadDlFiles() {
    try {
      const data = await request({
        url: `/api/v1/kvm/devices/${this.data.dlDevice.id}/files`,
        timeout: 60000,
      });
      const dlFiles = ((data && data.files) || []).map((f) => ({
        name: f.name,
        size: f.size,
        sizeText: fmtSize(f.size),
        icon: iconOf(f.name),
      }));
      this.setData({ dlFiles, dlLoading: false });
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
      url: `${config.BASE_URL}/api/v1/kvm/devices/${this.data.dlDevice.id}/download?name=${encodeURIComponent(file.name)}`,
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      timeout: 120000,
      success: (res) => {
        if (res.statusCode !== 200) {
          this.toast(`下载失败（${res.statusCode}）`);
          return;
        }
        this.saveDownload(file, this.renameTemp(res.tempFilePath, file.name));
      },
      fail: () => this.toast('网络异常，请检查网络后重试'),
      complete: () => this.setData({ downloading: '' }),
    });
  },

  // downloadFile 的临时路径名是随机的，重命名为真实文件名再打开/保存
  renameTemp(tempPath, name) {
    const fsm = wx.getFileSystemManager();
    const target = `${wx.env.USER_DATA_PATH}/${name}`;
    try { fsm.unlinkSync(target); } catch (e) { /* 目标不存在则忽略 */ }
    try {
      fsm.renameSync(tempPath, target);
      return target;
    } catch (e) {
      return tempPath; // 重命名失败退回临时路径
    }
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

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'file-transfer', title: '文件传输' });
  },
});
