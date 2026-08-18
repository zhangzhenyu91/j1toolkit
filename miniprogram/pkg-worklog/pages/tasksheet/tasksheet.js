// 出工日志 · 工作任务单（超管 / 班组管理员）：按日期把当天全部出车卡片合并生成为单个 docx 并打开（一卡一页供打印）
// 班组口径同 manage 页：超管按主页切换器存下的 worklog_team_id 生效；班组管理员后端强制本班
import Toast from 'tdesign-miniprogram/toast/index';
import { BASE_URL } from '../../../config';
import { shareAppMessage } from '../../../utils/share';

Page({
  data: {
    isAdmin: false,
    date: '', // 生成日期（默认今天）
    generating: false,
  },

  async onLoad() {
    // 超管 / 班组管理员可访问（等启动自检完成再取角色；同 manage 页口径）
    await getApp().globalData.ready;
    const user = getApp().globalData.userInfo || wx.getStorageSync('userInfo') || {};
    if (user.role !== 'admin' && user.role !== 'team_admin') {
      this.toast('仅管理员可访问');
      setTimeout(() => wx.navigateBack(), 1200);
      return;
    }
    this._teamId = Number(wx.getStorageSync('worklog_team_id')) || 0;
    const now = new Date();
    const today = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`;
    this.setData({ isAdmin: true, date: today });
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  onDateChange(e) {
    this.setData({ date: e.detail.value });
  },

  // 生成并打开：二进制 docx，wx.downloadFile 后 wx.openDocument 打开（同 manage 页模板下载链路）
  onGenerate() {
    const date = this.data.date;
    if (!date || this.data.generating) return;
    this.setData({ generating: true });
    wx.showLoading({ title: '正在生成…', mask: true });
    wx.downloadFile({
      url: `${BASE_URL}/api/v1/worklog/task-sheet?date=${date}${this._teamId ? `&team_id=${this._teamId}` : ''}`,
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      // 指定本地存储文件名，否则 openDocument 打开后显示的是随机临时文件名（乱码）
      filePath: `${wx.env.USER_DATA_PATH}/工作任务单-${date}.docx`,
      success: (r) => {
        if (r.statusCode !== 200) {
          this.toast(r.statusCode === 404 ? '该日期没有出车记录' : '生成失败，请稍后重试');
          return;
        }
        wx.openDocument({
          filePath: r.filePath,
          fileType: 'docx',
          showMenu: true, // 右上角菜单可另存/转发
          fail: () => this.toast('该类型暂不支持打开'),
        });
      },
      fail: () => this.toast('网络异常，请检查网络后重试'),
      complete: () => {
        wx.hideLoading();
        this.setData({ generating: false });
      },
    });
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'work-log', title: '工作任务单' });
  },
});
