// 出工日志 · 派车汇总（超管 / 班组管理员）：按日期范围生成派车单号清单 xlsx 并打开
// （派车单号 + 用车日期两列，顺序与工作任务单一致 = 日期+卡片创建序，单次最多 31 天；无 40901 生成前核验——单号清单与照片核验无关）
// 班组口径同 manage 页：超管按主页切换器存下的 worklog_team_id 生效；班组管理员后端强制本班
import Toast from 'tdesign-miniprogram/toast/index';
import { BASE_URL } from '../../../config';
import { shareAppMessage } from '../../../utils/share';

Page({
  data: {
    isAdmin: false,
    from: '', // 汇总范围起（默认今天）
    to: '', // 汇总范围止（默认今天）
    generating: false,
    calVisible: false, // range 日历展开态
    calValue: null, // 日历当前选中（[起, 止] 时间戳）
    minDate: 0, // 可选区间：去年今日 ~ 三个月后（同主页日历口径；不设置则组件默认今天起选不了历史日期）
    maxDate: 0,
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
    this.setData({
      isAdmin: true,
      from: today,
      to: today,
      minDate: new Date(now.getFullYear() - 1, now.getMonth(), now.getDate()).getTime(),
      maxDate: new Date(now.getFullYear(), now.getMonth() + 3, now.getDate()).getTime(),
    });
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  // 「改日期」：展开 range 日历（同主页批量下载面板口径；allow-same-day 支持单日）
  onOpenRange() {
    const { from, to } = this.data;
    this.setData({
      calVisible: true,
      calValue: [new Date(`${from}T00:00:00`).getTime(), new Date(`${to}T00:00:00`).getTime()],
    });
  },

  // range 日历确认：e.detail.value 为两个时间戳；回写范围
  onCalConfirm(e) {
    const value = e.detail.value;
    if (!Array.isArray(value) || value.length < 2) {
      this.toast('请选择起止日期');
      return;
    }
    const fmt = (t) => {
      const d = new Date(t);
      return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
    };
    this.setData({ from: fmt(value[0]), to: fmt(value[1]), calVisible: false });
  },

  onCalClose() {
    this.setData({ calVisible: false });
  },

  // 生成并打开：二进制 xlsx，wx.downloadFile 后 wx.openDocument 打开（同费用汇总页链路，fileType 换 xlsx）
  onGenerate() {
    const { from, to } = this.data;
    if (!from || !to || this.data.generating) return;
    if (from > to) {
      this.toast('开始日期不能晚于结束日期');
      return;
    }
    this.setData({ generating: true });
    wx.showLoading({ title: '正在生成…', mask: true });
    // 文件名口径同后端 dispatchSheetFileName：单日带单日期，跨天带范围
    const fileName = from === to ? `派车汇总-${from}.xlsx` : `派车汇总-${from}至${to}.xlsx`;
    wx.downloadFile({
      url: `${BASE_URL}/api/v1/worklog/dispatch-sheet?from=${from}&to=${to}${this._teamId ? `&team_id=${this._teamId}` : ''}`,
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      // 指定本地存储文件名，否则 openDocument 打开后显示的是随机临时文件名（乱码）
      filePath: `${wx.env.USER_DATA_PATH}/${fileName}`,
      success: (r) => {
        if (r.statusCode !== 200) {
          this.toast(r.statusCode === 404 ? '该日期范围没有派车单记录' : '生成失败，请稍后重试');
          return;
        }
        wx.openDocument({
          filePath: r.filePath,
          fileType: 'xlsx',
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
    return shareAppMessage(this, { app: 'work-log', title: '派车汇总' });
  },
});
