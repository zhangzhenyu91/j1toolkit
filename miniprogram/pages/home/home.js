// 首页 · 应用中心（小程序入口页：自动登录门控；「我的」为同页滑动面板，tab 切换左右滑动）
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../utils/request';
import { greeting, formatTime } from '../../utils/util';
import { shareAppMessage } from '../../utils/share';
import config from '../../config';

const WEEKDAYS = ['星期日', '星期一', '星期二', '星期三', '星期四', '星期五', '星期六'];

Page({
  data: {
    gate: false, // 门控是否已通过（未通过时显示启动加载页）
    tab: 'home', // 当前面板：home / me
    greeting: '',
    today: '',
    weekday: '',
    userInfo: {},
    avatarChar: '检',
    apps: [],
    appCount: 0, // 「我的」面板：可见应用数量
    appNames: [], // 「我的」面板：可见应用名称（我的权限弹窗）
    loading: true,
    notices: [], // 消息通知卡：最新 2 条（含 timeText 展示字段）
    noticeUnread: 0, // 未读通知数（0 时不显示角标）
    // 数据概览卡：上月通过/需补、本月通过/需补（出工日志 /stats 口径；null=未加载或无权限，显示 —）
    // 数字分级着色（Cls 字段）：通过=未通过数 0 绿 / ≤3 橙 / >3 红；需补=0 绿 / ≤3 橙 / >3 红
    statPrevPass: null,
    statPrevNeed: null,
    statCurPass: null,
    statCurNeed: null,
    statPrevPassCls: '',
    statPrevNeedCls: '',
    statCurPassCls: '',
    statCurNeedCls: '',
    hasWorklog: false, // 是否拥有出工日志权限（按 /app/list 是否含 work-log 判定；控制「绑定商旅」行显隐）
    // 「我的」面板「绑定商旅」行：三态文案（未绑定/已绑定/登录已过期）
    sgccNote: '未绑定',
    sgccExpired: false, // true=已绑定但商旅登录已过期（红字）
  },

  onLoad() {
    const now = new Date();
    this.setData({
      greeting: greeting(),
      today: `${now.getMonth() + 1}月${now.getDate()}日`,
      weekday: WEEKDAYS[now.getDay()],
    });

    // 已有 token（如刚从登录页跳转来）直接放行；
    // 否则等待启动自检（token 校验 + 静默微信登录）结果
    if (wx.getStorageSync('token')) {
      this.passGate();
      return;
    }
    getApp().globalData.ready.then((authed) => {
      if (authed) {
        this.passGate();
        return;
      }
      wx.reLaunch({ url: '/pages/login/login' });
    });
  },

  passGate() {
    if (this.data.gate) return;
    this.setData({ gate: true });
    this.loadProfile();
    this.loadApps();
    this.loadSgccStatus();
    this.loadNotices();
  },

  onShow() {
    if (this.data.gate) {
      this.loadProfile();
      this.loadApps();
      this.loadSgccStatus();
      this.loadNotices();
    }
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  // 刷新用户信息（本地缓存优先，后台同步最新）
  async loadProfile() {
    const cached = wx.getStorageSync('userInfo');
    if (cached) this.applyUser(cached);
    try {
      const user = await request({ url: '/api/v1/user/profile' });
      getApp().applyUser(user); // 同步缓存与"可静默微信登录"标记
      this.applyUser(user);
    } catch (err) {
      // 静默失败：保留缓存展示
    }
  },

  applyUser(user) {
    this.setData({
      userInfo: user,
      avatarChar: (user.nickname || user.username || '检').slice(0, 1),
    });
  },

  // 当前用户可见应用列表（首页面板宫格 + 「我的」面板数量/权限清单共用一次请求；
  // 宫格按 terminal 过滤：小程序只展示 双端/移动端 应用（PC 端应用仅网页端可见），
  // 数量/权限清单仍按完整列表统计）
  async loadApps() {
    this.setData({ loading: true });
    try {
      const data = await request({ url: '/api/v1/app/list' });
      const list = (data && data.list) || [];
      this.setData({
        apps: list.filter((item) => item.terminal !== 'pc'),
        appCount: list.length,
        appNames: list.map((item) => item.name),
        hasWorklog: list.some((item) => item.app_key === 'work-log'), // 「绑定商旅」行显隐（商旅打卡归属出工日志）
      });
      this.loadWorklogStats();
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ loading: false });
    }
  },

  // 数据概览卡：上月通过 / 上月需补 / 本月通过 / 本月需补（出工日志 /stats 口径，沿用 worklog_team_id 班组上下文）；
  // 通过 = 班组当月 verify_passed=passed 卡片数/全部卡片数；需补 = 我当月「未上传水印照片」的卡片条数；
  // 无出工日志权限、未分配班组或接口失败时静默降级，对应项显示 —
  // 分级着色：通过项按未通过数（total-passed）0 绿 / ≤3 橙 / >3 红；需补项 0 绿 / ≤3 橙 / >3 红
  statPassCls(passed, total) {
    const bad = Math.max(0, (Number(total) || 0) - (Number(passed) || 0));
    return bad === 0 ? 'stat-num-good' : bad <= 3 ? 'stat-num-warn' : 'stat-num-bad';
  },

  statNeedCls(n) {
    const c = Number(n) || 0;
    return c === 0 ? 'stat-num-good' : c <= 3 ? 'stat-num-warn' : 'stat-num-bad';
  },

  async loadWorklogStats() {
    if (!this.data.hasWorklog) {
      this.setData({ statPrevPass: null, statPrevNeed: null, statCurPass: null, statCurNeed: null });
      return;
    }
    const teamId = Number(wx.getStorageSync('worklog_team_id')) || 0; // 班组口径同 pkg-worklog
    const suffix = teamId ? `&team_id=${teamId}` : '';
    const now = new Date();
    const pad = (n) => String(n).padStart(2, '0');
    const curMonth = `${now.getFullYear()}-${pad(now.getMonth() + 1)}`;
    const prev = new Date(now.getFullYear(), now.getMonth() - 1, 1);
    const prevMonth = `${prev.getFullYear()}-${pad(prev.getMonth() + 1)}`;
    try {
      const a = await request({ url: `/api/v1/worklog/stats?month=${prevMonth}${suffix}` });
      const d = (a && a.data) || a || {};
      this.setData({
        statPrevPass: `${d.passed || 0}/${d.total || 0}`,
        statPrevPassCls: this.statPassCls(d.passed, d.total),
        statPrevNeed: d.needPhotos || 0,
        statPrevNeedCls: this.statNeedCls(d.needPhotos),
      });
    } catch (err) {
      // 静默失败：保留 —
    }
    try {
      const b = await request({ url: `/api/v1/worklog/stats?month=${curMonth}${suffix}` });
      const d = (b && b.data) || b || {};
      this.setData({
        statCurPass: `${d.passed || 0}/${d.total || 0}`,
        statCurPassCls: this.statPassCls(d.passed, d.total),
        statCurNeed: d.needPhotos || 0,
        statCurNeedCls: this.statNeedCls(d.needPhotos),
      });
    } catch (err) {
      // 静默失败：保留 —
    }
  },

  // 消息通知卡：未读数 + 最新 2 条；接口失败静默，不阻断首页
  async loadNotices() {
    try {
      const data = await request({ url: '/api/v1/notice/list?limit=2' });
      const items = (data && data.items) || [];
      this.setData({
        noticeUnread: (data && data.unread) || 0,
        notices: items.map((n) => ({ ...n, timeText: formatTime(n.createdAt) })),
      });
    } catch (err) {
      // 静默失败：保留现状
    }
  },

  // 进入消息通知列表
  goNotices() {
    wx.navigateTo({ url: '/pages/notice/list/list' });
  },

  // tab 切换（swiper 左右滑动动画）
  onTabSwitch(e) {
    this.setData({ tab: e.detail.key });
  },

  // 手指滑动 swiper 切换面板后同步底部导航高亮（点击 tab 触发的切换 current 已一致，无需处理）
  onSwiperChange(e) {
    const tab = e.detail.current === 1 ? 'me' : 'home';
    if (tab !== this.data.tab) this.setData({ tab });
  },

  // 进入应用（分包页面）；无小程序页面的应用（path 为空）
  // 统一提示前往 PC 端 Shade 壹匣，确认即复制网址
  onAppTap(e) {
    const { path, name } = e.currentTarget.dataset;
    if (!path) {
      Dialog.confirm({
        context: this,
        selector: '#t-dialog',
        title: name || '网页端应用',
        content: `请在 PC 端 Shade 壹匣 使用，网址：${config.BASE_URL}`,
        confirmBtn: '复制网址',
        cancelBtn: '知道了',
      }).then(() => {
        wx.setClipboardData({ data: config.BASE_URL });
      }).catch(() => {});
      return;
    }
    wx.navigateTo({
      url: path,
      fail: () => this.toast(`${name || '应用'}页面接入中`),
    });
  },

  onSoon() {
    this.toast('更多应用接入中，敬请期待');
  },

  /* ---------- 「我的」面板 ---------- */

  // 商旅打卡绑定状态刷新（三态：未绑定 / 已绑定 / 登录已过期）；
  // 商旅恒挂载，无出工日志权限 / 未分配班组时接口 403，静默降级为「未绑定」不报错
  async loadSgccStatus() {
    try {
      const teamId = Number(wx.getStorageSync('worklog_team_id')) || 0; // 班组口径同 pkg-worklog
      const data = await request({
        url: `/api/v1/sgcc/account${teamId ? `?team_id=${teamId}` : ''}`,
      });
      const bound = !!(data && data.bound);
      const expired = bound && Number(data.tokenStatus) !== 1;
      this.setData({
        sgccNote: bound ? (expired ? '登录已过期' : '已绑定') : '未绑定',
        sgccExpired: expired,
      });
    } catch (err) {
      this.setData({ sgccNote: '未绑定', sgccExpired: false });
    }
  },

  // 进入「绑定商旅」绑定页（pkg-worklog 分包）
  goSgccBind() {
    wx.navigateTo({ url: '/pkg-worklog/pages/sgccbind/sgccbind' });
  },

  // 我的权限：列出可见应用
  onPerms() {
    const content = this.data.appNames.length
      ? this.data.appNames.join('、')
      : '暂无可用应用，请联系管理员开通权限';
    Dialog.alert({
      context: this,
      selector: '#t-dialog',
      title: '我的权限',
      content,
      confirmBtn: '知道了',
    });
  },

  onAbout() {
    Dialog.alert({
      context: this,
      selector: '#t-dialog',
      title: '关于 Shade 壹匣',
      content: '版本号：v1.0.1 \n 应用权限申请联系 zzy',
      confirmBtn: '知道了',
    });
  },

  // 管理功能入口（仅管理员可见）
  goUsers() {
    wx.navigateTo({ url: '/pages/admin/users/users' });
  },

  goPerms() {
    wx.navigateTo({ url: '/pages/admin/perms/perms' });
  },

  goPush() {
    wx.navigateTo({ url: '/pages/admin/push/push' });
  },

  goTeams() {
    wx.navigateTo({ url: '/pages/admin/teams/teams' });
  },

  // 退出登录：确认后调用后端使 token 失效，清理本地登录态
  onLogout() {
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '退出登录',
      content: '确定要退出当前账号吗？',
      confirmBtn: '退出',
      cancelBtn: '取消',
    }).then(async () => {
      try {
        await request({ url: '/api/v1/auth/logout', method: 'POST' });
      } catch (err) {
        // 后端不可达也允许本地退出
      }
      wx.removeStorageSync('token');
      wx.removeStorageSync('userInfo');
      wx.removeStorageSync('canSilentWx'); // 清除"可静默微信登录"标记，避免下次冷启动被静默重新登录
      getApp().globalData.userInfo = null;
      wx.reLaunch({ url: '/pages/login/login' });
    }).catch(() => {});
  },

  onShareAppMessage() {
    return shareAppMessage(this);
  },
});
