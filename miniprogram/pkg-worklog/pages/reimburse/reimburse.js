// 出工日志 · 快捷报销（Web 端同口径：批量下载 + 工作任务单 / 费用汇总 / 派车汇总同页分区，全部直接可见）
// - 批量下载：日期范围内水印照片按月分组、勾选、PDF / PDF 存网盘 / 下载到相册
// - 三类汇总：共用同一日期范围，任务单/费用汇总带 40901 生成前核验拦截，派车汇总不拦截
// - 班组口径：超管按主页切换器 worklog_team_id；班组管理员后端强制本班
import Toast from 'tdesign-miniprogram/toast/index';
import { BASE_URL } from '../../../config';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

const pad = (n) => String(n).padStart(2, '0');
const fmtDate = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
const parseDate = (s) => {
  const [y, m, d] = s.split('-').map(Number);
  return new Date(y, m - 1, d);
};
const toDots = (iso) => (iso || '').replace(/-/g, '.');

const SHEET_SECTIONS = [
  {
    kind: 'task',
    title: '工作任务单',
    desc: '范围内全部出车卡片，一卡一页合并为一个 Word；单次最多 31 天。',
    fileType: 'docx',
    fileName: '工作任务单',
    verify: true,
    notFound: '该日期范围没有出车记录',
  },
  {
    kind: 'fee',
    title: '费用汇总',
    desc: '范围内出车卡片一卡一行、用车人一人一列，末尾合计；单次最多 31 天。',
    fileType: 'docx',
    fileName: '费用汇总',
    verify: true,
    notFound: '该日期范围没有出车记录',
  },
  {
    kind: 'dispatch',
    title: '派车汇总',
    desc: '范围内已填派车单号的出车卡片一单一行：派车单号 + 用车日期；单次最多 31 天。',
    fileType: 'xlsx',
    fileName: '派车汇总',
    verify: false,
    notFound: '该日期范围没有派车单记录',
  },
];

Page({
  data: {
    isAdmin: false,
    from: '',
    to: '',
    calVisible: false,
    calValue: null,
    minDate: 0,
    maxDate: 0,
    // 批量下载
    photoLoading: false,
    photoGroups: [], // [{month,title,photos:[{id,url,log_date,day,selected}]}]
    photoUrls: [],
    photoTotal: 0,
    photoSelected: 0,
    photoAll: false,
    // PDF
    pdfBusy: false,
    // 汇总
    sheetSections: SHEET_SECTIONS,
    sheetBusyKind: '',
    sheetSavingKind: '',
    // 网盘目录选择（PDF / 汇总共用）
    ndSaveVisible: false,
  },

  async onLoad() {
    await getApp().globalData.ready;
    const user = getApp().globalData.userInfo || wx.getStorageSync('userInfo') || {};
    const isAdmin = user.role === 'admin' || user.role === 'team_admin';
    this._teamId = Number(wx.getStorageSync('worklog_team_id')) || 0;
    const now = new Date();
    const from = fmtDate(new Date(now.getFullYear(), now.getMonth(), 1));
    const to = fmtDate(new Date(now.getFullYear(), now.getMonth() + 1, 0));
    this.setData({
      isAdmin,
      from,
      to,
      minDate: new Date(now.getFullYear() - 1, now.getMonth(), now.getDate()).getTime(),
      maxDate: new Date(now.getFullYear(), now.getMonth() + 3, now.getDate()).getTime(),
    });
    this.loadPhotos();
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  teamQuery() {
    return this._teamId ? `&team_id=${this._teamId}` : '';
  },

  teamBody(extra) {
    return { ...extra, ...(this._teamId ? { team_id: this._teamId } : {}) };
  },

  /* ==================== 日期范围 ==================== */
  onOpenRange() {
    const { from, to } = this.data;
    this.setData({
      calVisible: true,
      calValue: [parseDate(from).getTime(), parseDate(to).getTime()],
    });
  },

  onCalConfirm(e) {
    const value = e.detail.value;
    if (!Array.isArray(value) || value.length < 2) {
      this.toast('请选择起止日期');
      return;
    }
    const from = fmtDate(new Date(value[0]));
    const to = fmtDate(new Date(value[1]));
    this.setData({ calVisible: false, from, to });
    this.loadPhotos();
  },

  onCalClose() {
    this.setData({ calVisible: false });
  },

  /* ==================== 批量下载 ==================== */
  async loadPhotos() {
    const { from, to } = this.data;
    if (!from || !to || this.data.photoLoading) return;
    if (from > to) {
      this.toast('开始日期不能晚于结束日期');
      return;
    }
    this.setData({ photoLoading: true });
    try {
      const data = await request({
        url: `/api/v1/worklog/photos?from=${from}&to=${to}${this.teamQuery()}`,
      });
      this._photoRaw = (data && data.list) || [];
      this.buildPhotoGroups();
    } catch (err) {
      this.toast(err.message || '照片加载失败');
    } finally {
      this.setData({ photoLoading: false });
    }
  },

  buildPhotoGroups() {
    const groups = [];
    const map = {};
    (this._photoRaw || []).forEach((p) => {
      if (!map[p.month]) {
        const [y, m] = p.month.split('-');
        map[p.month] = { month: p.month, title: `${Number(y)} 年 ${Number(m)} 月`, photos: [] };
        groups.push(map[p.month]);
      }
      map[p.month].photos.push({
        id: p.id,
        url: p.url,
        log_date: p.log_date,
        day: p.day,
        selected: true,
      });
    });
    this.setData({
      photoGroups: groups,
      photoUrls: (this._photoRaw || []).map((p) => p.url),
    });
    this.recountPhotos();
  },

  recountPhotos() {
    let total = 0;
    let selected = 0;
    this.data.photoGroups.forEach((g) =>
      g.photos.forEach((p) => {
        total += 1;
        if (p.selected) selected += 1;
      })
    );
    this.setData({
      photoTotal: total,
      photoSelected: selected,
      photoAll: total > 0 && selected === total,
    });
  },

  onPhotoToggle(e) {
    const { gi, pi } = e.currentTarget.dataset;
    const group = this.data.photoGroups[gi];
    if (!group || !group.photos[pi]) return;
    this.setData({
      [`photoGroups[${gi}].photos[${pi}].selected`]: !group.photos[pi].selected,
    });
    this.recountPhotos();
  },

  onPhotoToggleAll() {
    const target = !this.data.photoAll;
    const groups = this.data.photoGroups.map((g) => ({
      ...g,
      photos: g.photos.map((p) => ({ ...p, selected: target })),
    }));
    this.setData({ photoGroups: groups });
    this.recountPhotos();
  },

  onPreviewPhoto(e) {
    const { url } = e.currentTarget.dataset;
    if (!url) return;
    wx.previewImage({
      current: url,
      urls: this.data.photoUrls.length ? this.data.photoUrls : [url],
    });
  },

  ensureAlbumAuth() {
    return new Promise((resolve) => {
      wx.getSetting({
        success: (res) => {
          if (res.authSetting['scope.writePhotosAlbum']) {
            resolve(true);
            return;
          }
          wx.authorize({
            scope: 'scope.writePhotosAlbum',
            success: () => resolve(true),
            fail: () => resolve(false),
          });
        },
        fail: () => resolve(false),
      });
    });
  },

  saveToAlbum(filePath) {
    return new Promise((resolve, reject) => {
      wx.saveImageToPhotosAlbum({ filePath, success: resolve, fail: reject });
    });
  },

  dlFile(url) {
    return new Promise((resolve, reject) => {
      wx.downloadFile({
        url,
        success: (r) => (r.statusCode === 200 ? resolve(r.tempFilePath) : reject(new Error('下载失败'))),
        fail: reject,
      });
    });
  },

  // 下载选中照片：按 (log_date, id) 顺序保存到相册
  async onDownloadPhotos() {
    const picked = [];
    this.data.photoGroups.forEach((g) => g.photos.forEach((p) => picked.push(p)));
    const list = picked.filter((p) => p.selected);
    if (!list.length) return;
    list.sort((a, b) => (a.log_date < b.log_date ? -1 : a.log_date > b.log_date ? 1 : a.id - b.id));
    const authed = await this.ensureAlbumAuth();
    if (!authed) {
      this.toast('请在设置中允许保存到相册');
      return;
    }
    let saved = 0;
    wx.showLoading({ title: `保存中 0/${list.length}`, mask: true });
    for (let i = 0; i < list.length; i += 1) {
      wx.showLoading({ title: `保存中 ${i + 1}/${list.length}`, mask: true });
      try {
        const tempFilePath = await this.dlFile(list[i].url);
        await this.saveToAlbum(tempFilePath);
        saved += 1;
      } catch (e) {
        // 单张失败跳过，最终汇总实际成功数
      }
    }
    wx.hideLoading();
    this.toast(`已保存 ${saved} 张到相册`);
  },

  // 按日期范围全量生成 PDF（与勾选无关）
  onPdf() {
    const { from, to, pdfBusy } = this.data;
    if (pdfBusy) return;
    this.setData({ pdfBusy: true });
    wx.showLoading({ title: '正在生成 PDF…', mask: true });
    wx.downloadFile({
      url: `${BASE_URL}/api/v1/worklog/photos.pdf?from=${from}&to=${to}${this.teamQuery()}`,
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      timeout: 120000,
      filePath: `${wx.env.USER_DATA_PATH}/水印照片-${from}至${to}.pdf`,
      success: (r) => {
        if (r.statusCode !== 200) {
          this.toast(r.statusCode === 404 ? '该范围内暂无水印照片' : `生成失败（${r.statusCode}）`);
          return;
        }
        wx.openDocument({
          filePath: r.filePath,
          fileType: 'pdf',
          showMenu: true,
          fail: () => this.toast('该类型暂不支持打开'),
        });
      },
      fail: () => this.toast('网络异常，请检查网络后重试'),
      complete: () => {
        wx.hideLoading();
        this.setData({ pdfBusy: false });
      },
    });
  },

  // PDF / 汇总共用网盘目录选择
  onPdfSave() {
    if (this.data.pdfBusy) return;
    this._ndAction = 'pdf';
    this._ndKind = '';
    this.setData({ ndSaveVisible: true });
  },

  onNdDirConfirm(e) {
    const { dir = '', space = 'my' } = e.detail || {};
    this.setData({ ndSaveVisible: false });
    if (this._ndAction === 'pdf') {
      this.doSavePdf(dir, space);
      return;
    }
    this.doSaveSheet(this._ndKind, dir, space, false);
  },

  onNdDirClose() {
    this.setData({ ndSaveVisible: false });
  },

  doSavePdf(dir, space) {
    const { from, to, pdfBusy } = this.data;
    if (pdfBusy) return;
    this.setData({ pdfBusy: true });
    wx.showLoading({ title: '正在生成并存网盘…', mask: true });
    request({
      url: '/api/v1/worklog/photos-pdf/save-netdisk',
      method: 'POST',
      timeout: 120000,
      data: this.teamBody({ from, to, dir, space }),
    })
      .then((data) => {
        const failed = data && data.failed ? `，${data.failed} 张照片合成失败` : '';
        this.toast(`已保存到网盘：${(data && data.path) || ''}${failed}`);
      })
      .catch((err) => this.toast(err.message))
      .finally(() => {
        wx.hideLoading();
        this.setData({ pdfBusy: false });
      });
  },

  /* ==================== 三张汇总表 ==================== */
  sectionOf(kind) {
    return SHEET_SECTIONS.find((s) => s.kind === kind);
  },

  onSheetGenerate(e) {
    const kind = e.currentTarget.dataset.kind;
    this.downloadSheet(kind, false);
  },

  downloadSheet(kind, force) {
    const cfg = this.sectionOf(kind);
    if (!cfg || this.data.sheetBusyKind || this.data.sheetSavingKind) return;
    const { from, to } = this.data;
    if (!from || !to) return;
    if (from > to) {
      this.toast('开始日期不能晚于结束日期');
      return;
    }
    const fileName =
      from === to ? `${cfg.fileName}-${from}.${cfg.fileType}` : `${cfg.fileName}-${from}至${to}.${cfg.fileType}`;
    this.setData({ sheetBusyKind: kind });
    wx.showLoading({ title: '正在生成…', mask: true });
    wx.downloadFile({
      url: `${BASE_URL}/api/v1/worklog/${kind === 'task' ? 'task-sheet' : kind === 'fee' ? 'fee-sheet' : 'dispatch-sheet'}?from=${from}&to=${to}${this.teamQuery()}${force === true ? '&force=1' : ''}`,
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      timeout: 120000,
      filePath: `${wx.env.USER_DATA_PATH}/${fileName}`,
      success: (r) => {
        if (r.statusCode !== 200) {
          if (cfg.verify && this.showVerifyFailures(r, kind)) return;
          this.toast(r.statusCode === 404 ? cfg.notFound : '生成失败，请稍后重试');
          return;
        }
        wx.openDocument({
          filePath: r.filePath,
          fileType: cfg.fileType,
          showMenu: true,
          fail: () => this.toast('该类型暂不支持打开'),
        });
      },
      fail: () => this.toast('网络异常，请检查网络后重试'),
      complete: () => {
        wx.hideLoading();
        this.setData({ sheetBusyKind: '' });
      },
    });
  },

  showVerifyFailures(res, kind) {
    let ej = null;
    try {
      const path = res.filePath || res.tempFilePath;
      if (path) ej = JSON.parse(wx.getFileSystemManager().readFileSync(path, 'utf8'));
    } catch (e) {
      /* 非 JSON 响应忽略 */
    }
    if (!ej || ej.code !== 40901) return false;
    // 先释放生成锁，弹层确认后才能立即重试；complete 会再次清空并 hideLoading
    wx.hideLoading();
    this.setData({ sheetBusyKind: '' });
    wx.showModal({
      title: '存在未通过项',
      content: `${ej.message}。可先到「汇总前核验」逐项处理，或确认后直接生成。`,
      confirmText: '仍要生成',
      cancelText: '去核验',
      success: (m) => {
        if (m.confirm) {
          this.downloadSheet(kind, true);
          return;
        }
        if (m.cancel) this.goReport();
      },
    });
    return true;
  },

  onSheetSave(e) {
    const kind = e.currentTarget.dataset.kind;
    if (!kind || this.data.sheetSavingKind || this.data.sheetBusyKind) return;
    this._ndAction = 'sheet';
    this._ndKind = kind;
    this.setData({ ndSaveVisible: true });
  },

  doSaveSheet(kind, dir, space, force) {
    const cfg = this.sectionOf(kind);
    if (!cfg || this.data.sheetSavingKind) return;
    const { from, to } = this.data;
    if (!from || !to) return;
    this.setData({ sheetSavingKind: kind });
    wx.showLoading({ title: '正在生成并存网盘…', mask: true });
    request({
      url: '/api/v1/worklog/sheet/save-netdisk',
      method: 'POST',
      timeout: 120000,
      data: this.teamBody({
        sheet: kind,
        from,
        to,
        dir,
        space,
        ...(force === true ? { force: 1 } : {}),
      }),
    })
      .then((data) => {
        this.toast(`已保存到网盘：${(data && data.path) || ''}`);
      })
      .catch((err) => {
        if (err && err.code === 40901) {
          // 释放保存锁，弹层确认后才能立即 force 重试
          wx.hideLoading();
          this.setData({ sheetSavingKind: '' });
          wx.showModal({
            title: '存在未通过项',
            content: `${err.message}。可先到「汇总前核验」逐项处理，或确认后直接生成并存网盘。`,
            confirmText: '仍要生成',
            cancelText: '去核验',
            success: (m) => {
              if (m.confirm) this.doSaveSheet(kind, dir, space, true);
              else if (m.cancel) this.goReport();
            },
          });
          return;
        }
        this.toast(err.message || '保存失败');
      })
      .finally(() => {
        wx.hideLoading();
        this.setData({ sheetSavingKind: '' });
      });
  },

  goReport() {
    const pages = getCurrentPages();
    const prev = pages[pages.length - 2];
    if (prev && prev.onOpenReport) prev.onOpenReport();
    wx.navigateBack();
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'work-log', title: '快捷报销' });
  },
});
