// 出工日志 · 汇总单生成（超管 / 班组管理员）：?type=task|fee|dispatch 三合一（原 tasksheet/feesheet/dispatchsheet 三页）
//   task     工作任务单：范围内出车卡片一卡一页合并为单个 docx（带 40901 生成前核验拦截）
//   fee      费用汇总：「人 × 日」费用矩阵 docx（一卡一行、一人一列、末尾合计；同带 40901 拦截）
//   dispatch 派车汇总：派车单号清单 xlsx（派车单号 + 用车日期两列，顺序与工作任务单一致 = 日期+卡片创建序；
//            无 40901 生成前核验——单号清单与照片核验无关）
// 均按日期范围生成并打开（单次最多 31 天）；班组口径同 manage 页：超管按主页切换器存下的 worklog_team_id 生效；班组管理员后端强制本班
import Toast from 'tdesign-miniprogram/toast/index';
import { BASE_URL } from '../../../config';
import { shareAppMessage } from '../../../utils/share';

// type → 接口路径 / 下载文件名前缀 / 标题与说明文案 / 是否拦截 40901（文件名口径同后端各自生成函数：单日带单日期，跨天带范围）
const SHEETS = {
  task: {
    title: '工作任务单',
    api: '/api/v1/worklog/task-sheet',
    fileName: '工作任务单',
    fileType: 'docx',
    verify40901: true,
    rangeTitle: '生成范围',
    rangeSub: '范围内全部出车卡片，一卡一页合并为一个 Word（单次最多 31 天）',
    hint: '字段口径：线路杆塔号＝本卡水印照片的施工内容；工作负责人＝排序最前的用车人（排序在「常用数据管理 → 人员」维护）；工作地点＝派车目的地；工作班成员＝用车人（空格间隔）；派车情况＝车牌号。未出车卡片不生成。',
    notFound: '该日期范围没有出车记录',
  },
  fee: {
    title: '费用汇总',
    api: '/api/v1/worklog/fee-sheet',
    fileName: '费用汇总',
    fileType: 'docx',
    verify40901: true,
    rangeTitle: '汇总范围',
    rangeSub: '范围内出车卡片一卡一行、用车人一人一列，末尾合计（单次最多 31 天）',
    hint: '字段口径：列＝范围内当过用车人的成员（按「常用数据管理 → 人员」点亮顺序排列，未出现者不列）；行＝每张出车卡片一行（日期升序）；单元格＝（当日伙食补助＋交通费）×1＝计算值，非用车人列留空；末尾合计行为各列之和。费用数据来自商旅同步，有未通过核验的记录需先在「汇总前核验」处理后再生成。',
    notFound: '该日期范围没有出车记录',
  },
  dispatch: {
    title: '派车汇总',
    api: '/api/v1/worklog/dispatch-sheet',
    fileName: '派车汇总',
    fileType: 'xlsx',
    verify40901: false,
    rangeTitle: '汇总范围',
    rangeSub: '范围内已填派车单号的出车卡片一单一行：派车单号 + 用车日期，顺序与工作任务单一致（单次最多 31 天）',
    hint: '字段口径：列＝派车单号、用车日期（＝卡片日志日期）；数据来自卡片「派车单号」字段——每日同步自动带入，也可点卡片车牌「修改派车」手工补填；仅已填单号的出车卡片出数（同步前日期的历史卡片无此字段）。',
    notFound: '该日期范围没有派车单记录',
  },
};

Page({
  data: {
    cfg: SHEETS.task, // 当前类型配置（onLoad 按 ?type 覆盖；wxml 标题/说明均取其字段）
    isAdmin: false,
    from: '', // 生成范围起（默认今天）
    to: '', // 生成范围止（默认今天）
    generating: false,
    calVisible: false, // range 日历展开态
    calValue: null, // 日历当前选中（[起, 止] 时间戳）
    minDate: 0, // 可选区间：去年今日 ~ 三个月后（同主页日历口径；不设置则组件默认今天起选不了历史日期）
    maxDate: 0,
  },

  async onLoad(query) {
    const cfg = SHEETS[(query && query.type) || ''] || SHEETS.task;
    this._cfg = cfg;
    this.setData({ cfg });
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

  // 生成前核验拦截（40901，仅 verify40901 的类型）：小弹窗二选一——「去核验」返回主页并打开汇总前核验面板；「仍要生成」管理员强制执行（force=1 重试）
  showVerifyFailures(res) {
    let ej = null;
    try {
      const path = res.filePath || res.tempFilePath;
      if (path) ej = JSON.parse(wx.getFileSystemManager().readFileSync(path, 'utf8'));
    } catch (e) { /* 非 JSON 响应忽略，走通用错误提示 */ }
    if (!ej || ej.code !== 40901) return false;
    wx.showModal({
      title: '存在未通过项',
      content: `${ej.message}。可先到「汇总前核验」逐项处理，或确认后直接生成。`,
      confirmText: '仍要生成',
      cancelText: '去核验',
      success: (m) => {
        if (m.confirm) {
          this.onGenerate(true); // 强制执行
          return;
        }
        if (m.cancel) {
          const pages = getCurrentPages();
          const prev = pages[pages.length - 2];
          if (prev && prev.onOpenReport) prev.onOpenReport(); // 主页打开汇总前核验面板
          wx.navigateBack();
        }
      },
    });
    return true;
  },

  // 生成并打开：二进制文档，wx.downloadFile 后 wx.openDocument 打开（同 manage 页模板下载链路）；force=强制执行跳过生成前核验
  onGenerate(force) {
    const { from, to } = this.data;
    if (!from || !to || this.data.generating) return;
    if (from > to) {
      this.toast('开始日期不能晚于结束日期');
      return;
    }
    const cfg = this._cfg;
    this.setData({ generating: true });
    wx.showLoading({ title: '正在生成…', mask: true });
    // 文件名口径同后端各类型生成函数：单日带单日期，跨天带范围
    const fileName = from === to ? `${cfg.fileName}-${from}.${cfg.fileType}` : `${cfg.fileName}-${from}至${to}.${cfg.fileType}`;
    wx.downloadFile({
      // bindtap 直绑时 force 为事件对象，仅确认按钮传来 true 才算强制执行
      url: `${BASE_URL}${cfg.api}?from=${from}&to=${to}${this._teamId ? `&team_id=${this._teamId}` : ''}${force === true ? '&force=1' : ''}`,
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      // 指定本地存储文件名，否则 openDocument 打开后显示的是随机临时文件名（乱码）
      filePath: `${wx.env.USER_DATA_PATH}/${fileName}`,
      success: (r) => {
        if (r.statusCode !== 200) {
          if (cfg.verify40901 && this.showVerifyFailures(r)) return;
          this.toast(r.statusCode === 404 ? cfg.notFound : '生成失败，请稍后重试');
          return;
        }
        wx.openDocument({
          filePath: r.filePath,
          fileType: cfg.fileType,
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
    return shareAppMessage(this, { app: 'work-log', title: this._cfg.title });
  },
});
