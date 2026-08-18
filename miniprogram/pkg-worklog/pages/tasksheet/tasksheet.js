// 出工日志 · 工作任务单（超管 / 班组管理员）：按日期范围把出车卡片合并生成为单个 docx 并打开（一卡一页供打印，单次最多 31 天）
// 班组口径同 manage 页：超管按主页切换器存下的 worklog_team_id 生效；班组管理员后端强制本班
import Toast from 'tdesign-miniprogram/toast/index';
import { BASE_URL } from '../../../config';
import { shareAppMessage } from '../../../utils/share';

Page({
  data: {
    isAdmin: false,
    from: '', // 生成范围起（默认今天）
    to: '', // 生成范围止（默认今天）
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
    this.setData({ isAdmin: true, from: today, to: today });
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  onFromChange(e) {
    this.setData({ from: e.detail.value });
  },

  onToChange(e) {
    this.setData({ to: e.detail.value });
  },

  // 生成并打开：二进制 docx，wx.downloadFile 后 wx.openDocument 打开（同 manage 页模板下载链路）
  onGenerate() {
    const { from, to } = this.data;
    if (!from || !to || this.data.generating) return;
    if (from > to) {
      this.toast('开始日期不能晚于结束日期');
      return;
    }
    this.setData({ generating: true });
    wx.showLoading({ title: '正在生成…', mask: true });
    // 文件名口径同后端 sheetFileName：单日带单日期，跨天带范围
    const fileName = from === to ? `工作任务单-${from}.docx` : `工作任务单-${from}至${to}.docx`;
    wx.downloadFile({
      url: `${BASE_URL}/api/v1/worklog/task-sheet?from=${from}&to=${to}${this._teamId ? `&team_id=${this._teamId}` : ''}`,
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      // 指定本地存储文件名，否则 openDocument 打开后显示的是随机临时文件名（乱码）
      filePath: `${wx.env.USER_DATA_PATH}/${fileName}`,
      success: (r) => {
        if (r.statusCode !== 200) {
          this.toast(r.statusCode === 404 ? '该日期范围没有出车记录' : '生成失败，请稍后重试');
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
