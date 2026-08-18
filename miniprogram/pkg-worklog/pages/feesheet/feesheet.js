// 出工日志 · 费用汇总（超管 / 班组管理员）：按日期范围生成「人 × 日」费用矩阵 docx 并打开（一卡一行、一人一列、末尾合计，单次最多 31 天）
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

  // 生成前核验拦截（40901）：错误响应 JSON 已随下载落盘，解析出未通过清单弹窗提示（同工作任务单页口径）
  showVerifyFailures(res) {
    let ej = null;
    try {
      const path = res.filePath || res.tempFilePath;
      if (path) ej = JSON.parse(wx.getFileSystemManager().readFileSync(path, 'utf8'));
    } catch (e) { /* 非 JSON 响应忽略，走通用错误提示 */ }
    const failures = ej && ej.code === 40901 && ej.data && ej.data.failures;
    if (!failures || !failures.length) return false;
    const lines = failures.slice(0, 5).map((f) => {
      const d = `${Number(f.log_date.slice(5, 7))}月${Number(f.log_date.slice(8, 10))}日`;
      return `${d} ${f.plate_no || '未出车'}（${(f.members || []).join('、')}）：${(f.reasons || []).slice(0, 2).join('；')}`;
    });
    if (failures.length > 5) lines.push(`……等 ${failures.length} 条`);
    wx.showModal({
      title: '存在未通过项',
      content: `${ej.message}：\n${lines.join('\n')}\n可到「汇总前核验」查看处理`,
      showCancel: false,
      confirmText: '知道了',
    });
    return true;
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
    // 文件名口径同后端 feeFileName：单日带单日期，跨天带范围
    const fileName = from === to ? `费用汇总-${from}.docx` : `费用汇总-${from}至${to}.docx`;
    wx.downloadFile({
      url: `${BASE_URL}/api/v1/worklog/fee-sheet?from=${from}&to=${to}${this._teamId ? `&team_id=${this._teamId}` : ''}`,
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      // 指定本地存储文件名，否则 openDocument 打开后显示的是随机临时文件名（乱码）
      filePath: `${wx.env.USER_DATA_PATH}/${fileName}`,
      success: (r) => {
        if (r.statusCode !== 200) {
          if (this.showVerifyFailures(r)) return;
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
    return shareAppMessage(this, { app: 'work-log', title: '费用汇总' });
  },
});
