// 首页 · 应用中心（小程序入口页：自动登录门控；「我的」为同页滑动面板，tab 切换左右滑动）
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../utils/request';
import { greeting, formatTime } from '../../utils/util';
import { shareAppMessage } from '../../utils/share';
import config from '../../config';
import { iconColors } from '../common/icon-theme';

const WEEKDAYS = ['星期日', '星期一', '星期二', '星期三', '星期四', '星期五', '星期六'];

Page({
  data: {
    gate: false, // 门控是否已通过（未通过时显示启动加载页）
    tab: 'home', // 当前面板：home / me
    isWide: false, // 600px 起首页导航由深蓝页首切换为浅色顶栏
    ico: iconColors('light'), // t-icon 图标色计算值（随系统主题刷新）
    greeting: '',
    today: '',
    weekday: '',
    userInfo: {},
    avatarChar: '检',
    apps: [],
    appCount: 0, // 「我的」面板：可见应用数量
    appNames: [], // 「我的」面板：可见应用名称（我的权限弹窗）
    loading: true,
    notices: [], // 消息通知卡：最新 3 条（含 timeText 展示字段）
    noticePreview: [], // 默认预览最新 1 条
    noticeExtra: [], // 展开后显示其余最多 2 条
    noticeRemaining: 0, // 默认预览之外的通知条数
    noticeExpanded: false,
    noticeLoading: true,
    noticeError: '',
    noticeUnread: 0, // 未读通知数（0 时不显示角标）
    // 数据概览条：上月通过/需补、本月通过/需补（出工日志 /stats 口径；null=未加载或无权限，显示 —）
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

  onLoad(options = {}) {
    const now = new Date();
    const info = wx.getWindowInfo ? wx.getWindowInfo() : wx.getSystemInfoSync();
    this.setData({
      tab: options.tab === 'me' ? 'me' : 'home',
      isWide: info.windowWidth >= 600,
      greeting: greeting(),
      today: `${now.getMonth() + 1}月${now.getDate()}日`,
      weekday: WEEKDAYS[now.getDay()],
      ico: iconColors(), // t-icon 图标色按系统主题计算
    });
    // 系统主题变化时刷新图标计算色
    this._onThemeChange = (res) => this.setData({ ico: iconColors(res && res.theme) });
    wx.onThemeChange(this._onThemeChange);
    this.bootGate();
  },

  // 页面卸载解除主题监听
  onUnload() {
    if (this._onThemeChange) {
      wx.offThemeChange(this._onThemeChange);
      this._onThemeChange = null;
    }
  },

  // 自动登录门控：等启动自检（token 校验 + 静默续期）完成后按本地 token 判定再进首页——
  // 避免拿过期 token 抢跑请求、与静默续期竞态。ready 的结果可能是登录前定下的（如刚从登录页跳转来），
  // 不直接采信，以本地 token 为准，保留登录后回首页通路
  async bootGate() {
    const ready = getApp().globalData.ready;
    if (ready && typeof ready.then === 'function') {
      try {
        await ready;
      } catch (e) {
        // 启动自检自身已容错，这里仅等待其完成，不据此放行/拦截
      }
    }
    if (wx.getStorageSync('token')) {
      this.passGate();
      return;
    }
    wx.reLaunch({ url: '/pages/login/login' });
  },

  passGate() {
    if (this.data.gate) return;
    this.setData({ gate: true });
    // 首个 onShow 可能先于门控到达（已记 _shown）也可能后到：两种顺序都只首拉一次，之后 onShow 才刷新
    if (this._shown) this.refresh();
  },

  // 首页数据统一拉取（用户信息 / 应用宫格 / 商旅绑定 / 消息通知）
  refresh() {
    this.loadProfile();
    this.loadApps();
    this.loadSgccStatus();
    this.loadNotices();
  },

  onResize() {
    const info = wx.getWindowInfo ? wx.getWindowInfo() : wx.getSystemInfoSync();
    const isWide = info.windowWidth >= 600;
    if (isWide !== this.data.isWide) this.setData({ isWide });
  },

  onShow() {
    this._shown = true;
    this.setData({ ico: iconColors() });
    if (!this.data.gate) return; // 门控未过（含启动自检未完成）：首拉由 passGate 负责
    this.refresh();
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
      const apps = list.filter((item) => item.terminal !== 'pc');
      getApp().globalData.apps = apps;
      wx.setStorageSync('wide_apps', apps);
      this.setData({
        apps,
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

  // 数据概览条：上月通过 / 上月需补 / 本月通过 / 本月需补（出工日志 /stats 口径，沿用 worklog_team_id 班组上下文）；
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
      this.setData({
        statPrevPass: null,
        statPrevNeed: null,
        statCurPass: null,
        statCurNeed: null,
      });
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

  // 通知展示派生：默认最新 1 条，展开显示其余最多 2 条
  noticeView(notices) {
    return {
      noticePreview: notices.slice(0, 1),
      noticeExtra: notices.slice(1, 3),
      noticeRemaining: Math.max(0, notices.length - 1),
    };
  },

  // 消息通知卡：未读数 + 最新 3 条；加载失败显示内联错误，不冒充空通知
  async loadNotices() {
    this.setData({ noticeLoading: true, noticeError: '' });
    try {
      const data = await request({ url: '/api/v1/notice/list?limit=3' });
      const items = (data && data.items) || [];
      const notices = items.slice(0, 3).map((n) => ({ ...n, timeText: formatTime(n.createdAt) }));
      this.setData({
        noticeUnread: (data && data.unread) || 0,
        notices,
        ...this.noticeView(notices),
        noticeExpanded: this.data.noticeExpanded && notices.length > 1,
        noticeLoading: false,
      });
    } catch (err) {
      this.setData({
        noticeLoading: false,
        noticeError: err.message || '通知加载失败，请重试',
      });
    }
  },

  onNoticeToggle() {
    if (!this.data.noticeRemaining) return;
    this.setData({ noticeExpanded: !this.data.noticeExpanded });
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
      })
        .then(() => {
          wx.setClipboardData({ data: config.BASE_URL });
        })
        .catch(() => {});
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
      content: '版本号：v1.22.1 \n 应用权限申请联系 zzy',
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
    })
      .then(async () => {
        try {
          await request({ url: '/api/v1/auth/logout', method: 'POST' });
        } catch (err) {
          // 后端不可达也允许本地退出
        }
        wx.removeStorageSync('token');
        wx.removeStorageSync('userInfo');
        wx.removeStorageSync('canSilentWx'); // 清除"可静默微信登录"标记，避免下次冷启动被静默重新登录
        wx.removeStorageSync('wide_apps');
        getApp().globalData.userInfo = null;
        getApp().globalData.apps = null;
        wx.reLaunch({ url: '/pages/login/login' });
      })
      .catch(() => {});
  },

  onShareAppMessage() {
    return shareAppMessage(this);
  },
});
