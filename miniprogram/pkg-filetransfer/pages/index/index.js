// 文件传输 · 设备列表 + 虚拟 U 盘上传/下载（移动端应用，app_key file-transfer）
// 列表数据实时代理自 GLKVM Cloud 平台（/api/v1/kvm/devices，kvm 或 file-transfer 任一权限）；
// 上传/下载经壹匣转发点（/api/v1/kvm/devices/{id}/push|files|download|delete|mount|status），平台链路直达设备
// 班组口径（屏九）：设备按生效班组过滤，本班组无设备时回退默认班组设备并显示黄色提示横幅（fallback）；
// 超管顶部切换器可切班组（storage filetransfer_team_id，全部请求带 team_id）；
// 非超管未分配班组 → 整页空态（屏十）
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';
import { createTeamGate } from '../../../utils/teamgate';
import { pad, fmtSize, extOf } from '../../../utils/util';
import config from '../../../config';

// 班组切换器 + 生效班组门控（storage filetransfer_team_id，全部请求带 team_id）
const teamGate = createTeamGate({ storageKey: 'filetransfer_team_id' });

// 平台设备状态 → 展示（与网页端 kvm.html 同口径）
const STATUS_MAP = {
  online: { key: 'online', text: '在线' },
  disabled: { key: 'disabled', text: '禁用' },
  offline: { key: 'offline', text: '离线' },
};

// 单文件大小上限 200MB（经壹匣内存中转，有意从严：后端上限 2GB；与弹层说明一致）
const MAX_FILE_SIZE = 200 * 1024 * 1024;

const IMG_EXTS = ['jpg', 'jpeg', 'png', 'gif', 'bmp', 'webp'];
const VIDEO_EXTS = ['mp4', 'mov', 'm4v', 'avi', 'mkv'];
// wx.openDocument 可识别的文档类型（传 fileType 提高打开成功率）
const DOC_EXTS = ['doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'pdf'];

const kindOf = (name) => {
  const ext = extOf(name);
  if (IMG_EXTS.includes(ext)) return 'image';
  if (VIDEO_EXTS.includes(ext)) return 'video';
  return 'file';
};
const iconOf = (name) => ({ image: 'file-image', video: 'video', file: 'file' }[kindOf(name)]);

// unix 秒 → 'YYYY-MM-DD HH:mm'（同网页端 Shade.fmtDate(d, true)）
const fmtLast = (sec) => {
  const d = new Date(sec * 1000);
  if (Number.isNaN(d.getTime())) return '—';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
};

Page({
  behaviors: [teamGate],
  data: {
    gate: false, // 门控（参照首页 gate 模式）
    list: [],
    loading: true, // 首屏加载中
    error: '', // 首屏加载失败文案（已有内容时失败仅 toast）
    // 班组设备回退提示（屏九）：本班组无设备时回退展示默认班组设备
    fallback: false,
    fallbackTeam: '',
    // 班组切换器数据（isAdmin/noTeam/teamName/teamOptions/teamDropOpen）由 teamgate behavior 提供；
    // 非超管未分配班组 → noTeam 整页空态（屏十），不发业务请求

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
    dlFiles: [], // [{name, size, sizeText, icon, checked}]（checked 为多选态勾选）
    dlLoading: false, // 「获取文件列表」执行中（断开共享并拉列表）
    downloading: '', // 正在下载的文件名
    mounting: false, // 正在挂载到目标计算机
    // 下载弹层多选模式（行首圆形勾选标；点按行 = toggle 选择；底部操作条 全选/下载所选/存网盘）
    dlMulti: false,
    dlSel: [], // 已选文件名（与 dlFiles.checked 同步）
    dlBusy: false, // 批量下载/批量存网盘进行中（互斥：禁止重复触发与单文件操作）
    // 网盘联动（「从网盘选择」/「存网盘」入口；未开通 40301 / 未配置 50301 探测失败即隐藏）
    ndOk: false,
    ndSpaces: [{ key: 'my', name: '我的空间', label: '我的空间' }],
    ndHasPublic: false,
    savingNd: '', // 正在存网盘的设备文件名
    // 存网盘目录选择（nd-dirpicker 共享组件；单文件与批量共用，names 为待存设备文件名列表）
    ndSave: { open: false, names: [] },
    // 网盘文件选择弹层（文件夹可进入，仅文件可勾选，多选）
    ndPicker: { open: false, space: 'my', dir: '/', items: [], loading: false, error: '', selectedList: [], saving: false },
    ndCrumbs: [],
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
    // 门控与生效班组确定由 teamgate behavior 完成（非超管未分配班组 → noTeam 空态，不再加载）
    this.passTeamGate(user, () => {
      this.loadDevices('init');
      this.startTimer();
      this.probeNetdisk();
    });
  },

  // 超管主动切换班组后重拉设备列表（teamgate behavior 回调）
  onTeamSwitched() {
    this.loadDevices('manual');
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
          maintaining: Boolean(d.maintaining), // 维护锁（每日派车单同步窗口，后端锁定期间操作返回 423）
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
    if (dev.maintaining) {
      this.toast('设备维护中，暂不可用');
      return;
    }
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
  // uploadFile 会把临时路径 basename 当 multipart 文件名，真实文件名走表单字段 filename；
  // formData 值官方要求字符串，team_id 须 String 化）
  pushFile(deviceId, file) {
    return new Promise((resolve, reject) => {
      wx.uploadFile({
        url: `${config.BASE_URL}/api/v1/kvm/devices/${deviceId}/push`,
        filePath: file.path,
        name: 'files',
        formData: this._teamId ? { filename: file.name, team_id: String(this._teamId) } : { filename: file.name },
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
    if (dev.maintaining) {
      this.toast('设备维护中，暂不可用');
      return;
    }
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
    this.setData({ dlOpen: false, dlMulti: false, dlSel: [] });
  },

  onDlVisibleChange(e) {
    if (!e.detail.visible) this.setData({ dlOpen: false, dlMulti: false, dlSel: [] });
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
      checked: false, // 多选态勾选
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
      this.setData({ dlFiles: this.mapDlFiles(data && data.files), dlLoading: false, dlState: 'local', dlSel: [] });
    } catch (err) {
      this.setData({ dlLoading: false, dlOpen: false });
      this.toast(err.message || '读取盘内文件失败');
    }
  },

  /* ---------- 下载弹层多选模式（行首圆形勾选标；底部操作条 全选/下载所选/存网盘） ---------- */

  // 多选开关（进入/退出均清空已选；批量处理或单文件下载中不可切换）
  onDlMultiToggle() {
    if (this.data.dlBusy || this.data.downloading) return;
    const dlMulti = !this.data.dlMulti;
    const patch = { dlMulti, dlSel: [] };
    if (!dlMulti) patch.dlFiles = this.data.dlFiles.map((f) => ({ ...f, checked: false }));
    this.setData(patch);
  },

  // 全选 / 取消全选
  onDlSelAll() {
    if (this.data.dlBusy || !this.data.dlFiles.length) return;
    const all = this.data.dlSel.length === this.data.dlFiles.length;
    const dlFiles = this.data.dlFiles.map((f) => ({ ...f, checked: !all }));
    this.setData({ dlFiles, dlSel: dlFiles.filter((f) => f.checked).map((f) => f.name) });
  },

  // 批量下载：顺序逐个执行既有 wx.downloadFile → saveDownload 流程（静默），结束 toast 汇总
  async onDlBatchDownload() {
    if (this.data.dlBusy || this.data.downloading) return;
    const names = this.data.dlSel.slice();
    if (!names.length) return;
    this.setData({ dlBusy: true });
    let ok = 0;
    let fail = 0;
    for (let i = 0; i < names.length; i += 1) {
      const file = this.data.dlFiles.find((f) => f.name === names[i]);
      if (!file) continue; // 列表已变化（如被删除）：跳过不计成败
      this.setData({ downloading: names[i] }); // 行内「下载中…」逐文件复用单下载展示位
      // eslint-disable-next-line no-await-in-loop
      const done = await this.downloadAndSave(file);
      if (done) ok += 1;
      else fail += 1;
    }
    this.setData({
      downloading: '',
      dlBusy: false,
      dlSel: [],
      dlFiles: this.data.dlFiles.map((f) => ({ ...f, checked: false })),
    });
    this.toast(fail ? `成功 ${ok} 失败 ${fail}` : `已下载 ${ok} 个文件`);
  },

  // 批量下载单文件（静默，resolve true/false；保存口径与 saveDownload 一致）
  downloadAndSave(file) {
    return new Promise((resolve) => {
      wx.downloadFile({
        url: `${config.BASE_URL}/api/v1/kvm/devices/${this.data.dlDevice.id}/download?name=${encodeURIComponent(file.name)}${this.teamQuery()}`,
        header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
        timeout: 120000,
        filePath: `${wx.env.USER_DATA_PATH}/${file.name}`,
        success: (res) => {
          if (res.statusCode !== 200) {
            resolve(false);
            return;
          }
          this.saveDownload(file, res.filePath, { quiet: true }).then(resolve);
        },
        fail: () => resolve(false),
      });
    });
  },

  // 点按文件：多选态 = toggle 选择；单选态 = 下载 → 图片/视频存相册，其他 wx.openDocument 打开
  onDlFileTap(e) {
    const file = e.currentTarget.dataset.file;
    if (this.data.dlMulti) {
      if (this.data.dlBusy) return;
      const index = this.data.dlFiles.findIndex((f) => f.name === file.name);
      if (index < 0) return;
      this.setData({ [`dlFiles[${index}].checked`]: !this.data.dlFiles[index].checked });
      this.setData({ dlSel: this.data.dlFiles.filter((f) => f.checked).map((f) => f.name) });
      return;
    }
    if (this.data.downloading || this.data.dlBusy) return;
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

  // 下载落地：图片/视频存相册，其余 wx.openDocument 打开；返回 Promise（true=成功），
  // opts.quiet 为批量静默模式（不逐个 toast，成败由批量调用方汇总）
  saveDownload(file, tempFilePath, opts) {
    const quiet = !!(opts && opts.quiet);
    const kind = kindOf(file.name);
    if (kind === 'image' || kind === 'video') {
      return new Promise((resolve) => {
        const save = kind === 'image' ? wx.saveImageToPhotosAlbum : wx.saveVideoToPhotosAlbum;
        save({
          filePath: tempFilePath,
          success: () => {
            if (!quiet) this.toast('已保存至相册');
            resolve(true);
          },
          fail: (err) => {
            if (!quiet) {
              if (err && /auth|deny/.test(err.errMsg || '')) {
                this.toast('请在设置中允许保存到相册');
              } else {
                this.toast('保存失败');
              }
            }
            resolve(false);
          },
        });
      });
    }
    const ext = extOf(file.name);
    return new Promise((resolve) => {
      wx.openDocument({
        filePath: tempFilePath,
        showMenu: true, // 右上角菜单可另存/转发
        ...(DOC_EXTS.includes(ext) ? { fileType: ext } : {}),
        success: () => resolve(true),
        fail: () => {
          if (!quiet) this.toast('该类型暂不支持打开');
          resolve(false);
        },
      });
    });
  },

  // 删除盘内文件（图标小按钮 + 二次确认，同 Call Me 删除对话交互）
  onDeleteFile(e) {
    const { name, index } = e.currentTarget.dataset;
    if (this.data.dlBusy) {
      this.toast('批量处理中，请稍候');
      return;
    }
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
        // deleted/missing 均视为已不在盘内，从列表移除（多选已选同步剔除）
        const dlFiles = this.data.dlFiles.slice();
        dlFiles.splice(index, 1);
        this.setData({ dlFiles, dlSel: this.data.dlSel.filter((n) => n !== name) });
        this.toast((data && (data.deleted || []).includes(name)) ? '已删除' : '文件已不存在');
      } catch (err) {
        this.toast(err.message);
      }
    }).catch(() => {});
  },

  // 挂载 U 盘至目标计算机（被控机向盘内放入文件场景：挂载后弹层切到 shared 态，
  // 放好后点「获取文件列表」断开共享并读取新文件）
  async onMount() {
    const { dlDevice, mounting, downloading, dlBusy } = this.data;
    if (mounting) return;
    if (downloading || dlBusy) {
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

  /* ==================== 网盘联动（从网盘拉取到设备 / 设备文件存网盘） ==================== */

  // 网盘可用性探测：成功缓存空间分段（公共区带班组名，选择弹层默认选中公共区，同网盘主页口径）；
  // 失败（40301 未开通 / 50301 未配置 / 网络异常）即隐藏两个入口
  probeNetdisk() {
    request({ url: '/api/v1/netdisk/spaces', timeout: 10000 })
      .then((data) => {
        const raw = (data && data.spaces) || [];
        const pub = raw.find((s) => s.key === 'public');
        this.setData({
          ndOk: true,
          ndHasPublic: !!pub,
          ndSpaces: (raw.length ? raw : [{ key: 'my', name: '我的空间' }]).map((s) => ({
            ...s,
            label: s.key === 'public' && s.team ? `${s.name} · ${s.team}` : s.name,
          })),
        });
      })
      .catch(() => this.setData({ ndOk: false }));
  },

  onOpenNdPicker() {
    this.setData({
      ndPicker: {
        open: true,
        space: this.data.ndHasPublic ? 'public' : 'my',
        dir: '/',
        items: [], loading: true, error: '', selectedList: [], saving: false,
      },
    });
    this.loadNdList();
  },

  onNdCancel() {
    if (this.data.ndPicker.saving) return;
    this.setData({ 'ndPicker.open': false });
  },

  // 拉取中不允许遮罩关闭
  onNdVisibleChange(e) {
    if (e.detail.visible) return;
    if (this.data.ndPicker.saving) {
      this.setData({ 'ndPicker.open': true });
      return;
    }
    if (this.data.ndPicker.open) this.setData({ 'ndPicker.open': false });
  },

  // 面包屑：空间名 / 各级目录（父级可点）
  buildNdCrumbs() {
    const p = this.data.ndPicker;
    const cur = this.data.ndSpaces.find((s) => s.key === p.space);
    const segs = p.dir.split('/').filter(Boolean);
    const crumbs = [{ name: cur ? cur.name : p.space, path: '/', last: segs.length === 0 }];
    segs.forEach((s, i) => {
      crumbs.push({ name: s, path: `/${segs.slice(0, i + 1).join('/')}`, last: i === segs.length - 1 });
    });
    this.setData({ ndCrumbs: crumbs });
  },

  // 当前目录列表（文件夹在前；文件行勾选态按 selectedList 回标，跨目录选择保留）
  async loadNdList() {
    const p = this.data.ndPicker;
    this.setData({ 'ndPicker.loading': true, 'ndPicker.error': '' });
    this.buildNdCrumbs();
    try {
      const data = await request({ url: '/api/v1/netdisk/list', method: 'POST', data: { space: p.space, path: p.dir } });
      const sel = this.data.ndPicker.selectedList;
      const rows = ((data && data.items) || []).map((o) => ({
        name: o.name,
        isDir: !!o.is_dir,
        icon: o.is_dir ? 'folder' : iconOf(o.name), // 复用本页图标口径（file/file-image/video）
        sizeText: fmtSize(o.size || 0),
        fullPath: p.dir === '/' ? `/${o.name}` : `${p.dir}/${o.name}`,
        checked: !o.is_dir && sel.indexOf(p.dir === '/' ? `/${o.name}` : `${p.dir}/${o.name}`) >= 0,
      }));
      this.setData({
        'ndPicker.items': rows.filter((r) => r.isDir).concat(rows.filter((r) => !r.isDir)),
        'ndPicker.loading': false,
      });
    } catch (err) {
      this.setData({ 'ndPicker.loading': false, 'ndPicker.error': err.message || '目录加载失败' });
    }
  },

  onNdSpaceTap(e) {
    const key = e.currentTarget.dataset.key;
    if (!key || key === this.data.ndPicker.space) return;
    // 切换空间清空已选（paths 属于单一 space）
    this.setData({ 'ndPicker.space': key, 'ndPicker.dir': '/', 'ndPicker.items': [], 'ndPicker.selectedList': [] });
    this.loadNdList();
  },

  onNdCrumbTap(e) {
    const path = e.currentTarget.dataset.path;
    if (path === this.data.ndPicker.dir) return;
    this.setData({ 'ndPicker.dir': path, 'ndPicker.items': [] });
    this.loadNdList();
  },

  // 文件夹进入（同空间内跨目录选择保留）；文件勾选/取消勾选
  onNdRowTap(e) {
    const { item, index } = e.currentTarget.dataset;
    if (item.isDir) {
      this.setData({ 'ndPicker.dir': item.fullPath, 'ndPicker.items': [] });
      this.loadNdList();
      return;
    }
    const sel = this.data.ndPicker.selectedList.slice();
    const i = sel.indexOf(item.fullPath);
    const on = i < 0;
    if (on) sel.push(item.fullPath);
    else sel.splice(i, 1);
    this.setData({ 'ndPicker.selectedList': sel, [`ndPicker.items[${index}].checked`]: on });
  },

  // 确认：所选网盘文件推送到当前上传目标设备（paths 为空间内相对路径数组，1-20 个；
  // 响应 data.items 为逐项结果 {name, ok, error?}，toast 用返回 message 汇总文案）
  onNdConfirm() {
    const p = this.data.ndPicker;
    const dev = this.data.upDevice;
    if (!p.selectedList.length || p.saving || !dev.id) return;
    this.setData({ 'ndPicker.saving': true });
    request({
      url: `/api/v1/kvm/devices/${dev.id}/pull-from-netdisk`,
      method: 'POST',
      timeout: 120000,
      withMessage: true,
      data: this.teamBody({ space: p.space, paths: p.selectedList }),
    }).then((res) => {
      this.setData({ 'ndPicker.open': false, upOpen: false, upFiles: [] });
      this.toast((res && res.message) || `已推送 ${p.selectedList.length} 个文件到设备`);
    }).catch((err) => this.toast(err.message))
      .finally(() => this.setData({ 'ndPicker.saving': false }));
  },

  // 设备文件存网盘（下载弹层行内按钮，单文件）：先弹网盘目录选择（nd-dirpicker），confirm 后带 dir 转存
  onSaveToNetdisk(e) {
    const { name } = e.currentTarget.dataset;
    if (!name || !this.data.dlDevice.id || this.data.savingNd || this.data.dlBusy) return;
    this.setData({ ndSave: { open: true, names: [name] } });
  },

  // 批量存网盘（多选操作条）：同样先弹目录选择
  onDlBatchSave() {
    if (this.data.dlBusy || this.data.savingNd) return;
    const names = this.data.dlSel.slice();
    if (!names.length) return;
    this.setData({ ndSave: { open: true, names } });
  },

  onNdSaveDirClose() {
    this.setData({ 'ndSave.open': false });
  },

  // 目录选择确认：顺序循环调 save-to-netdisk（body {name, dir} 带生效班组），结束 toast 汇总
  async onNdSaveDirConfirm(e) {
    const dir = (e.detail && e.detail.dir) || '';
    const names = (this.data.ndSave.names || []).slice();
    const dev = this.data.dlDevice;
    this.setData({ 'ndSave.open': false, 'ndSave.names': [] });
    if (!names.length || !dev.id || this.data.savingNd) return;
    this.setData({ dlBusy: true });
    let ok = 0;
    let fail = 0;
    let failMsg = '';
    let lastPath = '';
    for (let i = 0; i < names.length; i += 1) {
      this.setData({ savingNd: names[i] });
      try {
        // eslint-disable-next-line no-await-in-loop
        const data = await request({
          url: `/api/v1/kvm/devices/${dev.id}/save-to-netdisk`,
          method: 'POST',
          timeout: 120000,
          data: this.teamBody({ name: names[i], dir }),
        });
        ok += 1;
        lastPath = (data && data.path) || lastPath;
      } catch (err) {
        fail += 1;
        failMsg = err.message;
      }
    }
    this.setData({ savingNd: '', dlBusy: false });
    if (names.length === 1) {
      // 单文件保持原回显：成功 toast 返回路径，失败 toast 后端原文
      this.toast(ok ? `已保存到网盘：${lastPath}` : (failMsg || '保存失败'));
    } else {
      this.toast(fail ? `成功 ${ok} 失败 ${fail}：${failMsg}` : `已保存 ${ok} 个文件到网盘`);
    }
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'file-transfer', title: '文件传输' });
  },
});