// 出工日志 · 快捷报销（超管 / 班组管理员）：工作任务单 / 费用汇总 / 派车汇总同页分区，
// 每张表独立选择日期范围（不共用）；批量下载保留原入口，不在本页。
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
    calVisible: false,
    calValue: null,
    calFrom: '',
    calTo: '',
    calTarget: '',
    minDate: 0,
    maxDate: 0,
    // 每个分区独立日期范围与生成状态
    sheetSections: SHEET_SECTIONS.map((s) => ({ ...s })),
    sheetBusyKind: '',
    sheetSavingKind: '',
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
      sheetSections: SHEET_SECTIONS.map((s) => ({ ...s, from, to })),
      minDate: new Date(now.getFullYear() - 1, now.getMonth(), now.getDate()).getTime(),
      maxDate: new Date(now.getFullYear(), now.getMonth() + 3, now.getDate()).getTime(),
    });
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

  sectionOf(kind) {
    return this.data.sheetSections.find((s) => s.kind === kind);
  },

  /* ==================== 分区日期范围 ==================== */
  onOpenRange(e) {
    const target = e.currentTarget.dataset.target;
    const section = this.sectionOf(target);
    if (!section) return;
    this.setData({
      calVisible: true,
      calTarget: target,
      calFrom: section.from,
      calTo: section.to,
      calValue: [parseDate(section.from).getTime(), parseDate(section.to).getTime()],
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
    const target = this.data.calTarget;
    const sections = this.data.sheetSections.map((s) => (s.kind === target ? { ...s, from, to } : s));
    this.setData({ calVisible: false, sheetSections: sections });
  },

  onCalClose() {
    this.setData({ calVisible: false });
  },

  /* ==================== 三张汇总表 ==================== */
  onSheetGenerate(e) {
    const kind = e.currentTarget.dataset.kind;
    this.downloadSheet(kind, false);
  },

  downloadSheet(kind, force) {
    const cfg = this.sectionOf(kind);
    if (!cfg || this.data.sheetBusyKind || this.data.sheetSavingKind) return;
    const { from, to } = cfg;
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
    this._ndKind = kind;
    this.setData({ ndSaveVisible: true });
  },

  onNdDirConfirm(e) {
    const { dir = '', space = 'my' } = e.detail || {};
    this.setData({ ndSaveVisible: false });
    this.doSaveSheet(this._ndKind, dir, space, false);
  },

  onNdDirClose() {
    this.setData({ ndSaveVisible: false });
  },

  doSaveSheet(kind, dir, space, force) {
    const cfg = this.sectionOf(kind);
    if (!cfg || this.data.sheetSavingKind || this.data.sheetBusyKind) return;
    const { from, to } = cfg;
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
