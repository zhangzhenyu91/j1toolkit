// 出工日志 · 主页：日期条切换 / 视图开关（全部·仅看我）/ 日志卡片直改 / 日历选日（按日验证状态着色）
// 新建与「改派车/用车人」共用底部表单弹层（仅「保 存」提交，无实时保存；改派车保存前弹内网派车单同步警告）；
// 巡视内容点卡片主块单独弹层修改（带快捷输入）；底部另有批量下载水印照片面板与验证不通过报告面板
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

const WEEK = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];
const pad = (n) => (n < 10 ? `0${n}` : `${n}`);
const fmtDate = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
// 水印拍摄时间格式：2026.07.30 11:02（与今日水印相机样式一致）
const fmtWmTime = (d) =>
  `${d.getFullYear()}.${pad(d.getMonth() + 1)}.${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
// 随机时分：10:00-12:00（不含 12:00）内随机，历史带入与无历史预填共用此口径
const randWmHm = () => `${pad(10 + Math.floor(Math.random() * 2))}:${pad(Math.floor(Math.random() * 60))}`;

// 防伪码字符集：14 位大写字母+数字，去 0/O、1/I 等易混淆字符（与服务端校验规则一致）
const CODE_CHARS = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789';
const genAntiCode = () => {
  let out = '';
  for (let i = 0; i < 14; i += 1) out += CODE_CHARS.charAt(Math.floor(Math.random() * CODE_CHARS.length));
  return out;
};
const parseDate = (s) => {
  const [y, m, d] = s.split('-').map(Number);
  return new Date(y, m - 1, d);
};

// 日历按日着色缓存（模块级）与 format 回调
// 注意：t-calendar 的 format 是函数型属性，setData / wxml 绑定传函数在微信下都会被剥离，
// 只能直接写组件实例的 cal.base.format（见 recolorCalendar）；着色数据写入本缓存后手动重算
const DAY_STATUS = {};

function calFormat(day) {
  if (day.type === 'disabled') return day;
  const d = day.date;
  const key = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  const cls = [];
  const st = DAY_STATUS[key];
  if (st) cls.push(st === 'passed' ? 'wl-cal-passed' : 'wl-cal-failed');
  if (key === fmtDate(new Date())) cls.push('wl-cal-today');
  if (!cls.length) return day;
  return { ...day, className: `${day.className || ''} ${cls.join(' ')}`.trim() };
}

// 照片验证状态 → 展示（逐项判定：date_verify/destination_verify 任一 'false' 即该项不符，见《开发指南》7.2）
// 记录验证状态角标（后端按 6 条规则实时计算，见开发指南 7.1）
const VERIFY_BADGE = {
  passed: { cls: 'green', text: '验证通过' },
  failed: { cls: 'red', text: '未通过' },
  exempt: { cls: 'gray', text: '免验证' },
};

// 照片字段 → 展示结构（右侧八项：验证情况/人员/施工内容/拍摄时间/天气/地点/经度/纬度）
function mapPhoto(p) {
  // 验证情况：pending=验证中 / failed=验证失败（可重试）/ 完成态逐项列出未通过项
  let verify;
  if (p.verify_status === 'pending') {
    verify = { cls: 'ing', text: '验证中' };
  } else if (p.verify_status === 'failed') {
    verify = { cls: 'bad', text: '验证失败' };
  } else {
    const bad = [];
    // 新数据读 date_ok/dest_ok；历史数据（NULL）回退到旧状态值判定
    const dateBad = p.date_ok === 0 || (p.date_ok == null && p.verify_status === 'date_mismatch');
    const destBad = p.dest_ok === 0 || (p.dest_ok == null && p.verify_status === 'dest_mismatch');
    if (dateBad) bad.push('日期不符');
    if (destBad) bad.push('地点不符');
    verify = bad.length ? { cls: 'bad', text: bad.join('、') } : { cls: 'ok', text: '核验通过' };
  }
  return {
    id: p.id,
    url: p.url,
    members: p.members || [],
    workContent: p.work_content || '',
    verify,
    statusKey: p.verify_status, // failed 时显示「重新验证」按钮
    pending: p.verify_status === 'pending', // 轮询依据
    shotTime: p.shot_time || '',
    weather: p.weather || '',
    location: p.location || '',
    lng: p.lng || '',
    lat: p.lat || '',
  };
}

Page({
  data: {
    gate: false, // 门控（参照首页 gate 模式）
    dateStr: '', // 当前日期 YYYY-MM-DD
    dateText: '',
    weekText: '',
    isToday: false,
    isAdmin: false,
    fabOpen: false, // 右下悬浮主钮展开态（＋/×；展开项：数据管理·仅 admin / 批量下载 / 查看报告 / 新建日志）
    scope: 'all', // 视图开关：all=全部 / mine=仅看我（后端按 nickname 匹配成员）
    list: [],
    loading: true,
    flashId: 0, // 报告定位后高亮中的卡片 id（约 1.6s 后消退）
    dayAnim: '', // 日期切换平移动效：'' / from-right / from-left
    // 日历弹层
    calVisible: false,
    calValue: null,
    minDate: 0,
    maxDate: 0,
    // 照片人名点亮弹层（添加/修改复用）
    memberVisible: false,
    memberMode: 'add', // add=上传新照片 / edit=修改已有照片人名
    memberAction: 'raw', // memberMode=add 时的二选一：raw=选择水印照片上传 / wm=选照片并添加水印
    memberPhotoId: 0,
    memberEntryId: 0, // 当前操作的卡片 id
    candidates: [], // [{name, checked, disabled}]
    // 添加照片二选一弹层（自绘，替代 t-action-sheet）
    addSheetVisible: false,
    wmSourceType: 'album', // 加水印流程的照片来源（camera=拍摄 / album=相册，由人名层按钮决定）
    // ---------- 「选择照片并添加水印」字段编辑弹层 ----------
    wmVisible: false,
    wmPhotoPath: '', // 用户所选原图临时路径
    wmNames: [], // 人名点亮层确认的人名
    wmForm: { content: '', time: '', weather: '', location: '', lng: '', lat: '' },
    quickInputs: ['110kV', '220kV', 'Ⅰ', 'Ⅱ', '线巡视'], // 快捷输入，点击追加到内容末尾（水印施工内容与巡视内容共用）
    wmCode: '', // 防伪码（自动生成，用户不可编辑）
    wmUploading: false,
    // ---------- 4:3 裁剪层（加水印流程：拍摄必裁；相册非 4:3 才裁，横拍锁 4:3 / 纵拍锁 3:4） ----------
    cropVisible: false,
    cropSrc: '', // 待裁原图临时路径
    cropLandscape: true, // true=横向 4:3 / false=纵向 3:4
    cropFrameW: 0, // 取景框尺寸（px）
    cropFrameH: 0,
    cropViewW: 0, // 图片 cover 适配后的显示尺寸（px，未缩放）
    cropViewH: 0,
    cropX: 0, // movable-view 位置（px；缩放原点为视图中心，事件值存于 _cropX/_cropY/_cropScale）
    cropY: 0,
    cropScale: 1,
    cropExporting: false,
    // ---------- 新建/改派车表单底部弹层（仅「保 存」提交，无实时保存） ----------
    formVisible: false,
    formId: 0, // 0=新建
    formDateStr: '',
    members: [], // meta 成员 + checked（点亮即用车人）
    patrol: '', // 面板不展示；改派车提交时原样带上（PUT 全量替换）
    vehicleId: -1, // -1 未出车（新建默认） / >0 车牌 id
    vehicleText: '',
    destId: 0, // 0 未选择
    destText: '',
    isNoVehicle: true,
    formSaving: false, // 保存按钮防连点
    // 面板内联筛选下拉（手风琴互斥：'' 全关 / 'vehicle' / 'dest'）
    dropType: '',
    dropKeyword: '',
    dropList: [],
    // 键盘高度（textarea 不顶起整页，见开发指南键盘三件套）
    keyboardHeight: 0,
    // ---------- 巡视内容修改弹层（点卡片巡视内容主块弹出，「保 存」才提交） ----------
    patrolVisible: false,
    patrolEntryId: 0, // 当前修改的卡片 id
    patrolDraft: '', // 编辑中的巡视内容
    patrolSaving: false,
    // ---------- 批量下载水印照片面板 ----------
    dlVisible: false,
    dlFrom: '',
    dlTo: '',
    dlRangeText: '',
    dlGroups: [], // [{month, title, photos:[{id,url,log_date,day,selected}]}]
    dlUrls: [], // 当前范围全部照片（预览用，后端已按日期+上传序排列）
    dlTotal: 0,
    dlSelected: 0,
    dlAllChecked: false,
    dlLoading: false,
    // 下载面板改日期（range 日历；与下载/报告面板互斥开合，避免叠层 z-index 冲突；_rangeCalFor 标记回开对象）
    dlCalVisible: false,
    dlCalValue: null,
    // ---------- 验证不通过报告面板 ----------
    rpVisible: false,
    rpFrom: '',
    rpTo: '',
    rpRangeText: '',
    rpGroups: [], // [{date, title, items:[{id, plateText, membersText, reasons}]}]
    rpTotal: 0,
    rpLoading: false,
  },

  onLoad() {
    const now = new Date();
    this.setData({
      minDate: new Date(now.getFullYear() - 1, now.getMonth(), now.getDate()).getTime(),
      maxDate: new Date(now.getFullYear(), now.getMonth() + 3, now.getDate()).getTime(),
    });
    this.applyDate(fmtDate(now));

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
    this._myName = user.nickname || ''; // 「仅看我」匹配成员名用（同后端 scope=mine 口径）
    this.setData({ gate: true, isAdmin: user.role === 'admin' });
    this.loadMeta();
    this.loadLogs();
  },

  onShow() {
    if (this.data.gate) this.loadLogs();
  },

  onHide() {
    this.clearPoll();
  },

  onUnload() {
    this.clearPoll();
    if (this._flashTimer) {
      clearTimeout(this._flashTimer);
      this._flashTimer = null;
    }
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  // 切换当前日期并联动日期条文案
  applyDate(dateStr) {
    const d = parseDate(dateStr);
    this.setData({
      dateStr,
      dateText: `${d.getMonth() + 1}月${d.getDate()}日`,
      weekText: WEEK[d.getDay()],
      isToday: dateStr === fmtDate(new Date()),
      calValue: d.getTime(),
    });
  },

  onPrevDay() {
    this.shiftDay(-1);
  },

  onNextDay() {
    this.shiftDay(1);
  },

  // 两段式平移动效：当前内容先沿滑动方向移出，加载后新内容从对侧滑入
  async shiftDay(delta) {
    if (this._daySwitching) return; // 连滑防抖
    this._daySwitching = true;
    try {
      // 出场：后一天向左移出、前一天向右移出
      this.setData({ dayAnim: delta > 0 ? 'out-left' : 'out-right' });
      await new Promise((r) => setTimeout(r, 80));
      const d = parseDate(this.data.dateStr);
      d.setDate(d.getDate() + delta);
      this.applyDate(fmtDate(d));
      await this.loadLogs();
      this.playDayAnim(delta);
    } finally {
      this._daySwitching = false;
    }
  },

  // 入场：后一天从右侧滑入、前一天从左侧滑入（先清空再 nextTick 重放，保证连切也触发）
  playDayAnim(delta) {
    this.setData({ dayAnim: '' });
    wx.nextTick(() => {
      this.setData({ dayAnim: delta > 0 ? 'from-right' : 'from-left' });
    });
  },

  // ---------- 左右滑动切换日期 ----------
  // 横向位移 ≥60px 且明显横向（|dx| > 2|dy|）才触发，不影响纵向滚动与点按
  onTouchStart(e) {
    const t = e.touches[0];
    this._touch = { x: t.clientX, y: t.clientY };
  },

  onTouchEnd(e) {
    if (!this._touch) return;
    const t = e.changedTouches[0];
    const dx = t.clientX - this._touch.x;
    const dy = t.clientY - this._touch.y;
    this._touch = null;
    if (Math.abs(dx) >= 60 && Math.abs(dx) > Math.abs(dy) * 2) {
      this.shiftDay(dx < 0 ? 1 : -1); // 左滑后一天，右滑前一天
    }
  },

  // ---------- 视图开关（全部 / 仅看我） ----------

  onScopeChange(e) {
    const scope = e.currentTarget.dataset.scope;
    if (!scope || scope === this.data.scope) return;
    this.setData({ scope });
    // 口径变化：清空日历着色缓存并强制重拉当前月（mine 为个人口径）
    Object.keys(DAY_STATUS).forEach((k) => delete DAY_STATUS[k]);
    this._dayStatusMonths = {};
    this.loadLogs();
    this.loadDayStatus(this.data.dateStr.slice(0, 7), true);
    // 批量下载 / 查看报告面板的「仅看我」已收拢到本开关，面板打开时随动刷新
    if (this.data.dlVisible) this.buildDlGroups();
    if (this.data.rpVisible) this.loadReport();
  },

  // scope=mine 时请求追加个人口径参数
  scopeQuery() {
    return this.data.scope === 'mine' ? '&scope=mine' : '';
  },

  // 当日日志卡片列表
  async loadLogs() {
    this.clearPoll();
    this.setData({ loading: true });
    try {
      const data = await request({ url: `/api/v1/worklog/logs?date=${this.data.dateStr}${this.scopeQuery()}` });
      const list = ((data && data.list) || []).map((e) => {
        const photos = (e.photos || []).map(mapPhoto);
        return {
          id: e.id,
          hasVehicle: !!e.vehicle_id,
          plateText: e.vehicle_id ? e.plate_no : '未出车',
          badge: VERIFY_BADGE[e.verify_passed] || VERIFY_BADGE.failed,
          failReasons: e.verify_reasons || [], // 未通过明细（角标为「未通过」时逐行展示）
          patrolText: e.patrol_content || '—',
          checks: (e.members || []).map((m) => ({ mid: m.id, name: m.name, checked: !!m.checked })),
          photos,
          photoUrls: photos.map((p) => p.url),
          // 无照片且无派车时不显示水印照片区
          showPhotos: !!e.vehicle_id || photos.length > 0,
          // 表单面板回填用的原始字段
          patrol: e.patrol_content || '',
          vehicleId: e.vehicle_id || 0,
          destId: e.destination_id || 0,
          memberIds: (e.members || []).map((m) => m.member_id),
        };
      });
      this.setData({ list });
      this.schedulePoll(list);
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ loading: false });
    }
  },

  // 有照片处于「验证中」时 3 秒后自动刷新（Dify 异步回写）
  schedulePoll(list) {
    const hasPending = list.some((e) => e.photos.some((p) => p.pending));
    if (!hasPending) return;
    this._pollTimer = setTimeout(() => this.loadLogs(), 3000);
  },

  clearPoll() {
    if (this._pollTimer) {
      clearTimeout(this._pollTimer);
      this._pollTimer = null;
    }
  },

  // ---------- 日历选日（按日着色：绿=全部通过 / 红=有未通过） ----------

  onOpenCalendar() {
    this.setData({ calVisible: true });
    // 打开时强制刷新当前月的着色数据
    this.loadDayStatus(this.data.dateStr.slice(0, 7), true);
  },

  onCalClose() {
    this.setData({ calVisible: false });
  },

  // 未设确认按钮：单选点日期即触发 change 并自动关闭
  onCalChange(e) {
    const value = e.detail.value;
    if (!value) return;
    this.setData({ calVisible: false });
    this.applyDate(fmtDate(new Date(value)));
    this.loadLogs();
  },

  // 月份切换（switch-mode=month 的翻月箭头）：拉取该月着色数据
  onCalPanelChange(e) {
    const { year, month } = e.detail;
    this.loadDayStatus(`${year}-${pad(month)}`);
  },

  // 日历着色数据：按月去重拉取，写入模块级缓存（DAY_STATUS）后手动重算日历
  async loadDayStatus(month, force) {
    this._dayStatusMonths = this._dayStatusMonths || {};
    if (!force && this._dayStatusMonths[month]) {
      this.recolorCalendar();
      return;
    }
    try {
      const data = await request({ url: `/api/v1/worklog/day-status?month=${month}${this.scopeQuery()}` });
      Object.assign(DAY_STATUS, (data && data.map) || {});
      this._dayStatusMonths[month] = true;
      this.recolorCalendar();
    } catch (err) {
      // 着色失败不阻塞选日，仅静默跳过
    }
  },

  // 强制日历重算：format 函数经 setData / wxml 绑定传递在微信下不可靠（会被剥离），
  // 直接写入组件内部 TCalendar 实例的 format（纯 JS 引用，无序列化），再手动重算。
  // 注意 switch-mode="month" 时网格渲染的是 currentMonth（由 months 推导），
  // 只 calcMonths 不够，必须再 updateCurrentMonth——否则点过日期才着色
  recolorCalendar() {
    const cal = this.selectComponent('#wl-calendar');
    if (!cal || !cal.base || typeof cal.calcMonths !== 'function') return;
    cal.base.format = calFormat;
    cal.calcMonths();
    if (typeof cal.updateCurrentMonth === 'function') cal.updateCurrentMonth();
  },

  // ---------- 卡片交互 ----------

  // 预览水印照片
  // 验证失败 → 重新验证（重置为验证中并异步重调 Dify，随后轮询刷新）
  async onRetryVerify(e) {
    const { pid } = e.currentTarget.dataset;
    try {
      await request({ url: `/api/v1/worklog/photos/${pid}/verify`, method: 'POST' });
      this.toast('已重新提交验证');
      this.loadLogs();
    } catch (err) {
      this.toast(err.message);
    }
  },

  onPreview(e) {
    const { url, urls } = e.currentTarget.dataset;
    wx.previewImage({ current: url, urls });
  },

  // 复制施工内容（系统自弹「内容已复制」，不再重复提示）
  onCopyWc(e) {
    const { text } = e.currentTarget.dataset;
    if (!text) return;
    wx.setClipboardData({ data: text });
  },

  // 打卡 chips 直接切换：成功本地取反并刷新角标，失败 toast 并回滚
  async onCheckToggle(e) {
    const { entryId, mid, index } = e.currentTarget.dataset;
    try {
      const data = await request({
        url: `/api/v1/worklog/logs/${entryId}/members/${mid}/check`,
        method: 'PUT',
      });
      const ei = this.data.list.findIndex((x) => x.id === entryId);
      if (ei >= 0) {
        this.setData({ [`list[${ei}].checks[${index}].checked`]: !!(data && data.checked) });
      }
      this.loadLogs(); // 刷新 verify_passed 角标
    } catch (err) {
      this.toast(err.message);
      this.loadLogs(); // 回滚展示
    }
  },

  // ---------- 新建/改派车表单底部弹层 ----------

  // meta：车牌/目的地/成员（下拉与点亮数据源），passGate 后加载
  async loadMeta() {
    try {
      const data = await request({ url: '/api/v1/worklog/meta' });
      this._vehicles = (data && data.vehicles) || [];
      this._destinations = (data && data.destinations) || [];
      this.setData({ members: ((data && data.members) || []).map((m) => ({ ...m, checked: false })) });
      this.refreshDictText();
    } catch (err) {
      this.toast(err.message);
    }
  },

  // 依据当前 vehicleId/destId 刷新展示文案（meta 或回填后调用）
  refreshDictText() {
    const { vehicleId, destId } = this.data;
    let { vehicleText, destText } = this.data;
    if (vehicleId === -1) vehicleText = '未出车';
    else if (vehicleId > 0 && this._vehicles) {
      const v = this._vehicles.find((x) => x.id === vehicleId);
      vehicleText = v ? v.plate_no : vehicleText;
    }
    if (destId > 0 && this._destinations) {
      const d = this._destinations.find((x) => x.id === destId);
      destText = d ? d.name : destText;
    }
    this.setData({ vehicleText, destText });
  },

  // 「＋ 新建日志」：不再跳页，打开表单底部弹层（默认未出车）
  onCreate() {
    this.setData({ fabOpen: false });
    this.openForm(0);
  },

  // ---------- 右下悬浮主钮（speed dial 展开/收起） ----------
  onFabToggle() {
    this.setData({ fabOpen: !this.data.fabOpen });
  },

  // 卡片车牌头部：打开改派车面板并回填该卡数据（派车情况/用车人；巡视内容不在此修改）
  onOpenForm(e) {
    this.openForm(Number(e.currentTarget.dataset.id) || 0);
  },

  openForm(id) {
    const base = {
      formVisible: true,
      formId: id,
      formDateStr: this.data.dateStr,
      dropType: '',
      dropKeyword: '',
      dropList: [],
      keyboardHeight: 0,
      formSaving: false,
    };
    if (!id) {
      // 新建：默认未出车，人员全部未点亮
      this.setData({
        ...base,
        patrol: '',
        vehicleId: -1,
        vehicleText: '未出车',
        destId: 0,
        destText: '',
        isNoVehicle: true,
        members: this.data.members.map((m) => ({ ...m, checked: false })),
      });
      return;
    }
    const entry = this.data.list.find((x) => x.id === id);
    if (!entry) {
      this.toast('日志不存在或已被删除');
      return;
    }
    this.setData({
      ...base,
      patrol: entry.patrol, // 面板不展示；保存时原样带上（PUT 全量替换）
      vehicleId: entry.vehicleId > 0 ? entry.vehicleId : -1,
      isNoVehicle: !(entry.vehicleId > 0),
      destId: entry.destId || 0,
      members: this.data.members.map((m) => ({ ...m, checked: entry.memberIds.includes(m.id) })),
    });
    this.refreshDictText();
  },

  onKeyboardHeight(e) {
    const h = e.detail.height || 0;
    this.setData({ keyboardHeight: h > 0 ? h : 0 });
  },

  // ---------- 面板内联筛选下拉（车牌/目的地，选中即收起） ----------

  onToggleVehicleDrop() {
    this.toggleDrop('vehicle');
  },

  onToggleDestDrop() {
    if (this.data.isNoVehicle) return; // 未出车不可选
    this.toggleDrop('dest');
  },

  toggleDrop(type) {
    if (this.data.dropType === type) {
      this.setData({ dropType: '' });
      return;
    }
    this.setData({ dropType: type, dropKeyword: '' });
    this.buildDropList();
  },

  onDropInput(e) {
    this.setData({ dropKeyword: e.detail.value });
    this.buildDropList();
  },

  // 顶部筛选输入框实时过滤；车牌列表末位固定「未出车」
  buildDropList() {
    const { dropType, dropKeyword, vehicleId, destId } = this.data;
    const kw = (dropKeyword || '').trim();
    let list = [];
    if (dropType === 'vehicle') {
      list = (this._vehicles || [])
        .filter((v) => !kw || v.plate_no.includes(kw))
        .map((v) => ({ id: v.id, name: v.plate_no, selected: v.id === vehicleId }));
      if (!kw || '未出车'.includes(kw)) {
        list.push({ id: -1, name: '未出车', none: true, selected: vehicleId === -1 });
      }
    } else {
      list = (this._destinations || [])
        .filter((d) => !kw || d.name.includes(kw))
        .map((d) => ({ id: d.id, name: d.name, selected: d.id === destId }));
    }
    this.setData({ dropList: list });
  },

  onDropSelect(e) {
    const id = Number(e.currentTarget.dataset.id);
    if (this.data.dropType === 'vehicle') {
      // 选中「未出车」：清空目的地与人员选择，人员段联动隐藏
      const isNoVehicle = id === -1;
      const patch = {
        vehicleId: id,
        isNoVehicle,
        dropType: '',
      };
      if (isNoVehicle) {
        patch.destId = 0;
        patch.destText = '';
        patch.members = this.data.members.map((m) => ({ ...m, checked: false }));
      }
      this.setData(patch);
      this.refreshDictText();
    } else {
      this.setData({ destId: id, dropType: '' });
      this.refreshDictText();
    }
  },

  // ---------- 人员点亮 ----------

  onMemberTagChange(e) {
    const { index } = e.currentTarget.dataset;
    this.setData({ [`members[${index}].checked`]: e.detail.checked });
  },

  // ---------- 保存（无实时保存：仅「保 存」按钮提交，遮罩关闭 = 放弃修改） ----------

  buildPayload() {
    const { formDateStr, patrol, vehicleId, destId, members } = this.data;
    return {
      log_date: formDateStr,
      patrol_content: patrol,
      vehicle_id: vehicleId > 0 ? vehicleId : null,
      destination_id: vehicleId > 0 && destId > 0 ? destId : null,
      member_ids: vehicleId > 0 ? members.filter((m) => m.checked).map((m) => m.id) : [],
    };
  },

  // 底部「保 存」：新建→直接创建；改派车→先弹内网派车单同步警告，确认后保存
  onFormSave() {
    if (this.data.formSaving) return;
    if (!this.data.formId) {
      this.saveForm();
      return;
    }
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '保存派车修改',
      content: '请确保内网派车单同步修改！',
      confirmBtn: '我确认已修改',
      cancelBtn: '取消',
    })
      .then(() => this.saveForm())
      .catch(() => {});
  },

  // 实际提交：新建 POST / 改派车 PUT（全量字段，巡视内容沿用原值）；成功关闭并刷新，失败留面板可重试
  async saveForm() {
    if (this.data.formSaving) return;
    this.setData({ formSaving: true });
    try {
      if (this.data.formId) {
        await request({ url: `/api/v1/worklog/logs/${this.data.formId}`, method: 'PUT', data: this.buildPayload() });
      } else {
        await request({ url: '/api/v1/worklog/logs', method: 'POST', data: this.buildPayload() });
      }
      this.setData({ formVisible: false, formSaving: false, dropType: '', keyboardHeight: 0 });
      this.loadLogs();
    } catch (err) {
      this.setData({ formSaving: false });
      this.toast(err.message);
    }
  },

  // 遮罩关闭 = 放弃修改（不提交）
  onFormVisibleChange(e) {
    if (!e.detail.visible && this.data.formVisible) {
      this.setData({ formVisible: false, dropType: '', keyboardHeight: 0 });
    }
  },

  // ---------- 巡视内容修改弹层（「保 存」才提交，无实时保存） ----------

  // 卡片「巡视内容」主块：打开弹层并回填当前内容
  onOpenPatrol(e) {
    const id = Number(e.currentTarget.dataset.id) || 0;
    const entry = this.data.list.find((x) => x.id === id);
    if (!entry) {
      this.toast('日志不存在或已被删除');
      return;
    }
    this.setData({
      patrolVisible: true,
      patrolEntryId: id,
      patrolDraft: entry.patrol,
      patrolSaving: false,
      keyboardHeight: 0,
    });
  },

  onPatrolInput(e) {
    this.setData({ patrolDraft: e.detail.value });
  },

  // 快捷输入（与水印施工内容一致）：点击将字符追加到当前内容末尾
  onPatrolQuickInput(e) {
    const { text } = e.currentTarget.dataset;
    if (!text) return;
    this.setData({ patrolDraft: this.data.patrolDraft + text });
  },

  onPatrolCancel() {
    this.setData({ patrolVisible: false, keyboardHeight: 0 });
  },

  onPatrolVisibleChange(e) {
    if (!e.detail.visible && this.data.patrolVisible) {
      this.setData({ patrolVisible: false, keyboardHeight: 0 });
    }
  },

  // 保存巡视内容：PUT 全量字段（车牌/目的地/用车人取保存时卡片最新值，仅替换巡视内容），失败留弹层可重试
  async onPatrolSave() {
    if (this.data.patrolSaving) return;
    const entry = this.data.list.find((x) => x.id === this.data.patrolEntryId);
    if (!entry) {
      this.toast('日志不存在或已被删除');
      this.setData({ patrolVisible: false, keyboardHeight: 0 });
      return;
    }
    this.setData({ patrolSaving: true });
    try {
      await request({
        url: `/api/v1/worklog/logs/${entry.id}`,
        method: 'PUT',
        data: {
          patrol_content: this.data.patrolDraft,
          vehicle_id: entry.vehicleId > 0 ? entry.vehicleId : null,
          destination_id: entry.vehicleId > 0 && entry.destId > 0 ? entry.destId : null,
          member_ids: entry.vehicleId > 0 ? entry.memberIds : [],
        },
      });
      this.setData({ patrolVisible: false, patrolSaving: false, keyboardHeight: 0 });
      this.loadLogs();
    } catch (err) {
      this.setData({ patrolSaving: false });
      this.toast(err.message);
    }
  },

  // ---------- 水印照片（卡片直接改） ----------

  // 已被占用人名（每人限一张；excludePid 为当前正在修改的照片）
  usedPhotoNames(entry, excludePid) {
    const used = new Set();
    ((entry && entry.photos) || []).forEach((p) => {
      if (excludePid && p.id === excludePid) return;
      (p.members || []).forEach((n) => used.add(n));
    });
    return used;
  },

  // 添加照片：先弹二选一（①选择水印照片上传 ②拍摄/选择照片并添加水印），再进人名点亮层
  onAddPhoto(e) {
    const { entryId } = e.currentTarget.dataset;
    const entry = this.data.list.find((x) => x.id === entryId);
    if (!entry || !entry.checks.length) {
      this.toast('本卡暂无用车人');
      return;
    }
    this._pendingPhotoEntryId = entryId;
    this.setData({ addSheetVisible: true });
  },

  onAddSheetVisibleChange(e) {
    if (!e.detail.visible) this.setData({ addSheetVisible: false });
  },

  onAddSheetCancel() {
    this.setData({ addSheetVisible: false });
  },

  onAddSheetRaw() {
    this.setData({ addSheetVisible: false });
    this.openMemberPicker(this._pendingPhotoEntryId, 'raw');
  },

  onAddSheetWm() {
    this.setData({ addSheetVisible: false });
    this.openMemberPicker(this._pendingPhotoEntryId, 'wm');
  },

  // 人名点亮层（候选 = 本卡用车人，已上传者置灰）；action: raw=直接上传 / wm=加水印上传
  openMemberPicker(entryId, action) {
    const entry = this.data.list.find((x) => x.id === entryId);
    if (!entry) return;
    const used = this.usedPhotoNames(entry, 0);
    this.setData({
      memberVisible: true,
      memberMode: 'add',
      memberAction: action,
      memberPhotoId: 0,
      memberEntryId: entryId,
      candidates: entry.checks.map((m) => ({
        name: m.name,
        checked: false,
        disabled: used.has(m.name),
      })),
    });
  },

  // 修改已有照片人名：复用弹层（当前人名保持点亮，被其他照片占用者置灰）
  onPhotoMembers(e) {
    const { entryId, pid, names } = e.currentTarget.dataset;
    const entry = this.data.list.find((x) => x.id === entryId);
    if (!entry) return;
    const used = this.usedPhotoNames(entry, pid);
    this.setData({
      memberVisible: true,
      memberMode: 'edit',
      memberPhotoId: pid,
      memberEntryId: entryId,
      candidates: entry.checks.map((m) => ({
        name: m.name,
        checked: (names || []).includes(m.name),
        disabled: used.has(m.name),
      })),
    });
  },

  onMemberVisibleChange(e) {
    if (!e.detail.visible) this.setData({ memberVisible: false });
  },

  onMemberCancel() {
    this.setData({ memberVisible: false });
  },

  onCandidateChange(e) {
    const { index } = e.currentTarget.dataset;
    this.setData({ [`candidates[${index}].checked`]: e.detail.checked });
  },

  // 弹层确认：edit=提交人名修改；add=关闭后进相册选片上传
  async onMemberConfirm() {
    const names = this.data.candidates.filter((c) => c.checked && !c.disabled).map((c) => c.name);
    if (!names.length) {
      this.toast('请选择照片所属人名');
      return;
    }
    if (this.data.memberMode === 'edit') {
      try {
        await request({
          url: `/api/v1/worklog/photos/${this.data.memberPhotoId}/members`,
          method: 'PUT',
          data: { members: names },
        });
        this.setData({ memberVisible: false });
        this.toast('人名已修改');
        this.loadLogs();
      } catch (err) {
        this.toast(err.message);
      }
      return;
    }
    this.setData({ memberVisible: false });
    if (this.data.memberAction === 'wm') {
      this.setData({ wmSourceType: 'album' });
      this.choosePhotoForWm(names);
    } else {
      this.chooseAndUpload(names);
    }
  },

  // 人名层「拍摄」：点亮人名后直接调相机（加水印流程）
  onMemberShoot() {
    const names = this.data.candidates.filter((c) => c.checked && !c.disabled).map((c) => c.name);
    if (!names.length) {
      this.toast('请选择照片所属人名');
      return;
    }
    this.setData({ memberVisible: false, wmSourceType: 'camera' });
    this.choosePhotoForWm(names);
  },

  // ---------- 「选择照片并添加水印」：选片 →（按需 4:3 裁剪）→ 编辑字段 → 服务端加水印上传 ----------

  // 按来源取图（拍摄/相册）后判定是否进 4:3 裁剪层（不限 sizeType：原图/压缩图均可，由用户选）
  choosePhotoForWm(names) {
    wx.chooseMedia({
      count: 1,
      mediaType: ['image'],
      sourceType: [this.data.wmSourceType === 'camera' ? 'camera' : 'album'],
      success: (res) => {
        const path = res.tempFiles[0].tempFilePath;
        wx.getImageInfo({
          src: path,
          success: (info) => this.afterPickWmPhoto(path, names, info || {}),
          fail: () => this.afterPickWmPhoto(path, names, {}),
        });
      },
    });
  },

  // 取图后分流：拍摄一律进裁剪；相册已为 4:3/3:4（±0.02 容差）则免裁直进表单。
  // EXIF 旋转 90/270° 时显示宽高互换；取信息失败按已是 4:3 处理（保持旧流程）
  afterPickWmPhoto(path, names, info) {
    const rotated = ['left', 'right', 'left-mirrored', 'right-mirrored'].indexOf(info.orientation) >= 0;
    const dispW = rotated ? info.height : info.width;
    const dispH = rotated ? info.width : info.height;
    const ratio = dispW && dispH ? dispW / dispH : 4 / 3;
    const is43 = Math.abs(ratio - 4 / 3) <= 0.02 || Math.abs(ratio - 3 / 4) <= 0.02;
    if (this.data.wmSourceType === 'album' && is43) {
      this.proceedWmForm(path, names);
      return;
    }
    this.openWmCrop(path, names, dispW || 4, dispH || 3, info.orientation || '');
  },

  // 裁剪完成/免裁：记录照片路径与人名、生成防伪码，进字段编辑弹层
  proceedWmForm(path, names) {
    this.setData({ wmPhotoPath: path, wmNames: names, wmCode: genAntiCode() });
    this.prefillWmForm();
  },

  // ---------- 4:3 裁剪层 ----------
  // 交互：movable-view 拖拽 + 双指缩放（scale 原点为视图中心）；取景框锁定 4:3（横）/ 3:4（纵）
  // 导出：离屏 type=2d canvas 按可视区重绘裁出（createImage 解码应用 EXIF；个别机型未应用时手动旋转兜底）

  // 打开裁剪层：取景框横向顶满屏宽、纵向受高度限制；图片按 cover 适配并居中
  openWmCrop(path, names, dispW, dispH, orientation) {
    const win = wx.getWindowInfo ? wx.getWindowInfo() : wx.getSystemInfoSync();
    const maxW = win.windowWidth - 48;
    const maxH = Math.max(win.windowHeight - 260, 200); // 标题/提示/按钮预留
    let fw;
    let fh;
    if (dispW >= dispH) {
      fw = maxW;
      fh = (fw * 3) / 4;
    } else {
      fh = Math.min((maxW * 4) / 3, maxH);
      fw = (fh * 3) / 4;
    }
    const k = Math.max(fw / dispW, fh / dispH); // cover 适配
    const vw = dispW * k;
    const vh = dispH * k;
    const x = (fw - vw) / 2;
    const y = (fh - vh) / 2;
    this._cropNames = names;
    this._cropOrientation = orientation;
    this._cropDispW = dispW;
    this._cropDispH = dispH;
    this._cropX = x;
    this._cropY = y;
    this._cropScale = 1;
    this.setData({
      cropVisible: true,
      cropSrc: path,
      cropLandscape: dispW >= dispH,
      cropFrameW: fw,
      cropFrameH: fh,
      cropViewW: vw,
      cropViewH: vh,
      cropX: x,
      cropY: y,
      cropScale: 1,
      cropExporting: false,
    });
  },

  onCropMove(e) {
    this._cropX = e.detail.x;
    this._cropY = e.detail.y;
  },

  onCropScale(e) {
    this._cropX = e.detail.x;
    this._cropY = e.detail.y;
    this._cropScale = e.detail.scale;
  },

  onCropCancel() {
    this.setData({ cropVisible: false, cropExporting: false });
  },

  onCropVisibleChange(e) {
    if (!e.detail.visible) this.setData({ cropVisible: false });
  },

  // 确认裁剪：可视区换算到图片像素 → 离屏 canvas 重绘导出（长边压到 2560 内）→ 进字段编辑弹层
  onCropConfirm() {
    if (this.data.cropExporting) return;
    this.setData({ cropExporting: true });
    const { cropFrameW: fw, cropFrameH: fh, cropViewW: vw, cropViewH: vh } = this.data;
    const s = this._cropScale || 1;
    const dispW = this._cropDispW || vw;
    const dispH = this._cropDispH || vh;
    // movable-view 缩放原点为中心：取景框左/上缘在图片显示坐标中的位置
    const left = (vw * s) / 2 - (this._cropX + vw / 2);
    const top = (vh * s) / 2 - (this._cropY + vh / 2);
    const sx = Math.max(0, (left * dispW) / (vw * s));
    const sy = Math.max(0, (top * dispH) / (vh * s));
    const sw = Math.min(dispW - sx, (fw * dispW) / (vw * s));
    const sh = Math.min(dispH - sy, (fh * dispH) / (vh * s));
    const outK = Math.min(1, 2560 / Math.max(sw, sh));
    const ow = Math.round(sw * outK);
    const oh = Math.round(sh * outK);
    wx.createSelectorQuery()
      .select('#wmCropCanvas')
      .fields({ node: true })
      .exec((res) => {
        const canvas = res && res[0] && res[0].node;
        if (!canvas) {
          this.setData({ cropExporting: false });
          this.toast('裁剪失败，请重试');
          return;
        }
        canvas.width = ow;
        canvas.height = oh;
        const ctx = canvas.getContext('2d');
        const img = canvas.createImage();
        img.onload = () => {
          ctx.fillStyle = '#000000';
          ctx.fillRect(0, 0, ow, oh); // jpg 无透明通道，兜黑底
          // EXIF 兜底：解码后宽高未按 EXIF 互换（个别机型）时按 orientation 手动旋转
          const swapped = ['left', 'right', 'left-mirrored', 'right-mirrored'].indexOf(this._cropOrientation) >= 0;
          const dimsMatch = Math.abs(img.width - dispW) <= 2 && Math.abs(img.height - dispH) <= 2;
          if (swapped && !dimsMatch) {
            this.drawCropRotated(ctx, img, sx, sy, sw, sh, ow, oh);
          } else {
            ctx.drawImage(img, sx, sy, sw, sh, 0, 0, ow, oh);
          }
          wx.canvasToTempFilePath({
            canvas,
            fileType: 'jpg',
            quality: 0.92,
            success: (r) => {
              // 导出期间用户已取消：丢弃结果，不再进字段编辑弹层
              if (!this.data.cropVisible) return;
              this.setData({ cropVisible: false, cropExporting: false });
              this.proceedWmForm(r.tempFilePath, this._cropNames);
            },
            fail: () => {
              this.setData({ cropExporting: false });
              this.toast('裁剪失败，请重试');
            },
          });
        };
        img.onerror = () => {
          this.setData({ cropExporting: false });
          this.toast('图片读取失败');
        };
        img.src = this.data.cropSrc;
      });
  },

  // EXIF 90/270° 手动旋转兜底：sx/sy/sw/sh 为显示坐标系裁剪框，换算到底图原始坐标后旋转绘制
  drawCropRotated(ctx, img, sx, sy, sw, sh, ow, oh) {
    if (this._cropOrientation === 'left' || this._cropOrientation === 'left-mirrored') {
      ctx.translate(0, oh);
      ctx.rotate(-Math.PI / 2);
      ctx.drawImage(img, img.width - sy - sh, sx, sh, sw, 0, 0, oh, ow);
    } else {
      // right / right-mirrored
      ctx.translate(ow, 0);
      ctx.rotate(Math.PI / 2);
      ctx.drawImage(img, sy, img.height - sx - sw, sh, sw, 0, 0, oh, ow);
    }
  },

  // 历史带入的拍摄时间：保留日期，时分随机为 10:00-12:00 内且不与历史值相同
  randomWmTime(shotTime) {
    const m = /^(\d{4}\.\d{2}\.\d{2})\s+(\d{1,2}):(\d{2})$/.exec(String(shotTime || '').trim());
    const datePart = m ? m[1] : fmtWmTime(new Date()).split(' ')[0];
    const oldHm = m ? `${m[2]}:${m[3]}` : '';
    let hm = oldHm;
    while (hm === oldHm) hm = randWmHm();
    return `${datePart} ${hm}`;
  },

  // 字段预填：有历史水印照片 → 带入其字段（经纬度随机偏移 ≤500m，拍摄时间随机化为 10:00-12:00，避免完全一致）；
  // 无历史 → 施工内容留空、经纬度/地点/天气按当前定位取值（腾讯地图）；
  //          拍摄时间当日取当前时间，非当日仅能确定日期 → 取记录日期 10:00-12:00 内随机时间
  prefillWmForm() {
    const entry = this.data.list.find((x) => x.id === this.data.memberEntryId);
    const photos = (entry && entry.photos) || [];
    const history = photos.filter((p) => p.shotTime || p.workContent || p.location || p.lng || p.lat);
    const last = history[history.length - 1];
    if (last) {
      const lng = parseFloat(last.lng);
      const lat = parseFloat(last.lat);
      const jittered = Number.isFinite(lng) && Number.isFinite(lat) ? this.jitterCoord(lng, lat) : { lng: '', lat: '' };
      this.setData({
        wmVisible: true,
        wmForm: {
          content: last.workContent || '',
          time: this.randomWmTime(last.shotTime),
          weather: last.weather || '',
          location: last.location || '',
          lng: jittered.lng,
          lat: jittered.lat,
        },
      });
      return;
    }
    // 无历史：拍摄时间当日取当前时间，非当日取记录日期 10:00-12:00 内随机时间；经纬度/地点/天气均按当前定位取值
    const time = this.data.dateStr === fmtDate(new Date())
      ? fmtWmTime(new Date())
      : `${this.data.dateStr.replace(/-/g, '.')} ${randWmHm()}`;
    this.setData({
      wmVisible: true,
      wmForm: { content: '', time, weather: '', location: '', lng: '', lat: '' },
    });
    this.fillWmByLocation();
  },

  // 当前定位取值：经纬度直接填；地点/天气调后端 /geo（腾讯地图）。授权被拒或失败均留空手填（失败原因打控制台）
  fillWmByLocation() {
    wx.getLocation({
      type: 'gcj02',
      success: (loc) => {
        if (!this.data.wmVisible) return; // 弹层已关则不再回填
        this.setData({
          'wmForm.lng': loc.longitude.toFixed(6),
          'wmForm.lat': loc.latitude.toFixed(6),
        });
        request({ url: `/api/v1/worklog/geo?lng=${loc.longitude}&lat=${loc.latitude}`, timeout: 10000 })
          .then((r) => {
            if (!this.data.wmVisible) return;
            this.setData({
              'wmForm.weather': this.data.wmForm.weather || (r && r.weather) || '',
              'wmForm.location': this.data.wmForm.location || (r && r.location) || '',
            });
          })
          .catch((err) => console.error('[出工日志] /geo 地点天气获取失败（留空手填）：', err));
      },
      fail: (err) => console.error('[出工日志] wx.getLocation 定位失败（留空手填）：', err),
    });
  },

  // 经纬度随机偏移：半径 ≤400m（对 500m 上限留余量），角度随机
  jitterCoord(lng, lat) {
    const r = Math.random() * 400;
    const a = Math.random() * Math.PI * 2;
    const dLat = (r * Math.sin(a)) / 111320;
    const cosLat = Math.cos((lat * Math.PI) / 180);
    const dLng = (r * Math.cos(a)) / (111320 * (Math.abs(cosLat) > 1e-6 ? cosLat : 1e-6));
    return { lng: (lng + dLng).toFixed(6), lat: (lat + dLat).toFixed(6) };
  },

  onWmInput(e) {
    const { field } = e.currentTarget.dataset;
    this.setData({ [`wmForm.${field}`]: e.detail.value });
  },

  // 施工内容快捷输入：点击将字符追加到当前输入内容末尾（不超出 textarea 的 maxlength 500）
  onWmQuickInput(e) {
    const { text } = e.currentTarget.dataset;
    if (!text) return;
    const content = (this.data.wmForm.content + text).slice(0, 500);
    this.setData({ 'wmForm.content': content });
  },

  onWmCodeRefresh() {
    this.setData({ wmCode: genAntiCode() });
  },

  onWmCancel() {
    this.setData({ wmVisible: false });
  },

  onWmVisibleChange(e) {
    if (!e.detail.visible) this.setData({ wmVisible: false });
  },

  // 经纬度补方向后缀：只填数字时自动补 °E/°N（已带符号则原样）
  withDegSuffix(v, suffix) {
    const s = String(v || '').trim();
    if (!s) return '';
    return /[°NSEWnsew]/.test(s) ? s : `${s}${suffix}`;
  },

  // 确认：取 EXIF 方向 → 原图 base64 → 连同字段上传（服务端加水印）
  onWmConfirm() {
    if (this.data.wmUploading) return;
    this.setData({ wmUploading: true });
    wx.getImageInfo({
      src: this.data.wmPhotoPath,
      success: (info) => this.readAndUploadWm((info && info.orientation) || ''),
      fail: () => this.readAndUploadWm(''),
    });
  },

  readAndUploadWm(orientation) {
    const f = this.data.wmForm;
    const wm = {
      content: f.content,
      time: f.time,
      weather: f.weather,
      location: f.location,
      longitude: this.withDegSuffix(f.lng, '°E'),
      latitude: this.withDegSuffix(f.lat, '°N'),
      antiCode: this.data.wmCode,
      orientation,
    };
    wx.getFileSystemManager().readFile({
      filePath: this.data.wmPhotoPath,
      encoding: 'base64',
      success: (r) => {
        const ext = (this.data.wmPhotoPath.split('.').pop() || 'jpeg').toLowerCase();
        const mime = ext === 'png' ? 'png' : 'jpeg';
        this.uploadPhoto(`data:image/${mime};base64,${r.data}`, this.data.wmNames, wm);
      },
      fail: () => {
        this.setData({ wmUploading: false });
        this.toast('图片读取失败');
      },
    });
  },

  // 相册选片 → base64 → 上传（沿用 Call Me 聊天图片先例）
  chooseAndUpload(names) {
    wx.chooseMedia({
      count: 1,
      mediaType: ['image'],
      sourceType: ['album'],
      success: (res) => {
        const path = res.tempFiles[0].tempFilePath;
        wx.getFileSystemManager().readFile({
          filePath: path,
          encoding: 'base64',
          success: (r) => {
            const ext = (path.split('.').pop() || 'jpeg').toLowerCase();
            const mime = ext === 'png' ? 'png' : 'jpeg';
            this.uploadPhoto(`data:image/${mime};base64,${r.data}`, names);
          },
          fail: () => this.toast('图片读取失败'),
        });
      },
    });
  },

  // 上传：wm 存在时走「加水印上传」（服务端渲染水印），否则为原「水印照片上传」
  async uploadPhoto(image, members, wm) {
    wx.showLoading({ title: wm ? '正在加水印上传…' : '正在上传…', mask: true });
    try {
      await request({
        url: `/api/v1/worklog/logs/${this.data.memberEntryId}/photos`,
        method: 'POST',
        data: wm ? { image, members, wm } : { image, members },
        timeout: 120000,
      });
      wx.hideLoading();
      this.setData({ wmVisible: false, wmUploading: false });
      this.toast('已上传，验证中');
      this.loadLogs(); // 刷新后自动进入 pending 轮询
    } catch (err) {
      wx.hideLoading();
      this.setData({ wmUploading: false });
      this.toast(err.message);
    }
  },

  onDeletePhoto(e) {
    const { pid } = e.currentTarget.dataset;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '删除照片',
      content: '删除后不可恢复，确定删除该水印照片吗？',
      confirmBtn: '删除',
      cancelBtn: '取消',
    })
      .then(async () => {
        try {
          await request({ url: `/api/v1/worklog/photos/${pid}`, method: 'DELETE' });
          this.toast('已删除');
          this.loadLogs();
        } catch (err) {
          this.toast(err.message);
        }
      })
      .catch(() => {});
  },

  onDelete(e) {
    const { id } = e.currentTarget.dataset;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '删除日志',
      content: '删除后不可恢复，卡片内照片一并删除，确定删除吗？',
      confirmBtn: '删除',
      cancelBtn: '取消',
    })
      .then(async () => {
        try {
          await request({ url: `/api/v1/worklog/logs/${id}`, method: 'DELETE' });
          this.toast('已删除');
          this.loadLogs();
        } catch (err) {
          this.toast(err.message);
        }
      })
      .catch(() => {});
  },

  onManage() {
    this.setData({ fabOpen: false });
    wx.navigateTo({ url: '/pkg-worklog/pages/manage/manage' });
  },

  // ---------- 批量下载水印照片 ----------

  // 首次打开默认范围：当天 1~10 日 → 上月整月；11 日及以后 → 本月 1 号到今天（批量下载 / 查看报告共用）
  defaultRange() {
    const now = new Date();
    let from;
    let to;
    if (now.getDate() <= 10) {
      from = new Date(now.getFullYear(), now.getMonth() - 1, 1);
      to = new Date(now.getFullYear(), now.getMonth(), 0); // 上月最后一天
    } else {
      from = new Date(now.getFullYear(), now.getMonth(), 1);
      to = now;
    }
    return { from: fmtDate(from), to: fmtDate(to) };
  },

  onOpenDownload() {
    const { from, to } = this.defaultRange();
    this.setData({ dlVisible: true, dlFrom: from, dlTo: to, fabOpen: false });
    this.loadDlPhotos();
  },

  onCloseDownload() {
    this.setData({ dlVisible: false });
  },

  onDlVisibleChange(e) {
    if (!e.detail.visible && this.data.dlVisible) this.setData({ dlVisible: false });
  },

  // 拉取范围内照片并按 month 分组（后端已按日期+上传序排列，遇序分组即月份升序）
  async loadDlPhotos() {
    this.setData({ dlLoading: true });
    try {
      const { dlFrom, dlTo } = this.data;
      const data = await request({ url: `/api/v1/worklog/photos?from=${dlFrom}&to=${dlTo}` });
      this._dlRaw = (data && data.list) || [];
      this.buildDlGroups();
      this.setData({ dlRangeText: `${dlFrom} ~ ${dlTo}`, dlLoading: false });
    } catch (err) {
      this.setData({ dlLoading: false });
      this.toast(err.message);
    }
  },

  // 按当前视图开关（scope=mine 时仅含自己名字的照片）把原始列表组装为月份分组
  buildDlGroups() {
    let list = this._dlRaw || [];
    if (this.data.scope === 'mine' && this._myName) {
      list = list.filter((p) => (p.members || []).includes(this._myName));
    }
    const groups = [];
    const groupMap = {};
    list.forEach((p) => {
      if (!groupMap[p.month]) {
        const [y, m] = p.month.split('-');
        groupMap[p.month] = { month: p.month, title: `${Number(y)} 年 ${Number(m)} 月`, photos: [] };
        groups.push(groupMap[p.month]);
      }
      groupMap[p.month].photos.push({
        id: p.id,
        url: p.url,
        log_date: p.log_date,
        day: p.day, // 后端字段，从 1 开始
        selected: true, // 默认全选，点圈可反选
      });
    });
    this.setData({
      dlGroups: groups,
      dlUrls: list.map((p) => p.url),
    });
    this.recountDl();
  },

  // 已选计数 / 全选态
  recountDl() {
    let total = 0;
    let selected = 0;
    this.data.dlGroups.forEach((g) =>
      g.photos.forEach((p) => {
        total += 1;
        if (p.selected) selected += 1;
      })
    );
    this.setData({ dlTotal: total, dlSelected: selected, dlAllChecked: total > 0 && selected === total });
  },

  // 点缩略图预览（urls 为当前范围全部照片，按顺序）
  onDlPreview(e) {
    const { url } = e.currentTarget.dataset;
    if (!this.data.dlUrls.length) return;
    wx.previewImage({ current: url, urls: this.data.dlUrls });
  },

  // 点选择圈切换单张（wxml 用 catchtap 防穿透触发预览）
  onDlToggle(e) {
    const { gi, pi } = e.currentTarget.dataset;
    this.setData({ [`dlGroups[${gi}].photos[${pi}].selected`]: !this.data.dlGroups[gi].photos[pi].selected });
    this.recountDl();
  },

  // 底部「全选」切换
  onDlToggleAll() {
    const target = !this.data.dlAllChecked;
    const groups = this.data.dlGroups.map((g) => ({
      ...g,
      photos: g.photos.map((p) => ({ ...p, selected: target })),
    }));
    this.setData({ dlGroups: groups });
    this.recountDl();
  },

  // 「改日期」：先关下载面板再开 range 日历（两弹层互斥，规避叠层 z-index 冲突），选完重开
  onDlChangeDate() {
    const { dlFrom, dlTo } = this.data;
    this._rangeCalFor = 'dl'; // range 日历与报告面板共用，标记回开对象
    this.setData({
      dlVisible: false,
      dlCalValue: [parseDate(dlFrom).getTime(), parseDate(dlTo).getTime()],
      dlCalVisible: true,
    });
  },

  // range 日历确认：e.detail.value 为两个时间戳；按 _rangeCalFor 回写并重开来源面板
  onDlCalConfirm(e) {
    const value = e.detail.value;
    if (!Array.isArray(value) || value.length < 2) {
      this.toast('请选择起止日期');
      return;
    }
    const from = fmtDate(new Date(value[0]));
    const to = fmtDate(new Date(value[1]));
    if (this._rangeCalFor === 'rp') {
      this.setData({ dlCalVisible: false, rpFrom: from, rpTo: to, rpVisible: true });
      this.loadReport();
      return;
    }
    this.setData({ dlCalVisible: false, dlFrom: from, dlTo: to, dlVisible: true });
    this.loadDlPhotos();
  },

  // 未选直接关闭日历：重开来源面板（保留原范围）
  onDlCalClose() {
    if (!this.data.dlCalVisible) return;
    if (this._rangeCalFor === 'rp') {
      this.setData({ dlCalVisible: false, rpVisible: true });
      return;
    }
    this.setData({ dlCalVisible: false, dlVisible: true });
  },

  // 相册授权：先 getSetting，未授权走 authorize，被拒绝返回 false（由调用方 toast 提示）
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

  dlFile(url) {
    return new Promise((resolve, reject) => {
      wx.downloadFile({
        url,
        success: (r) => (r.statusCode === 200 ? resolve(r.tempFilePath) : reject(new Error('下载失败'))),
        fail: reject,
      });
    });
  },

  saveToAlbum(filePath) {
    return new Promise((resolve, reject) => {
      wx.saveImageToPhotosAlbum({ filePath, success: resolve, fail: reject });
    });
  },

  // 下载：选中项按 (log_date, id) 排序保证从 1 号开始顺序保存，串行下载逐张存入相册
  async onDlDownload() {
    const picked = [];
    this.data.dlGroups.forEach((g) => g.photos.forEach((p) => picked.push(p)));
    const list = picked.filter((p) => p.selected);
    if (!list.length) return;
    list.sort((a, b) => (a.log_date < b.log_date ? -1 : a.log_date > b.log_date ? 1 : a.id - b.id));

    const authed = await this.ensureAlbumAuth();
    if (!authed) {
      this.toast('请在设置中允许保存到相册');
      return;
    }

    // 注意：COS 域名需配置为小程序 downloadFile 合法域名（部署侧事项，见开发指南）
    let saved = 0;
    wx.showLoading({ title: `保存中 0/${list.length}`, mask: true });
    for (let i = 0; i < list.length; i += 1) {
      wx.showLoading({ title: `保存中 ${i + 1}/${list.length}`, mask: true });
      try {
        // 个别失败跳过继续，最终 toast 实际成功数
        const tempFilePath = await this.dlFile(list[i].url);
        await this.saveToAlbum(tempFilePath);
        saved += 1;
      } catch (err) {
        // 单张失败忽略，继续下一张
      }
    }
    wx.hideLoading();
    this.toast(`已保存 ${saved} 张到相册`);
  },

  // ---------- 验证不通过报告 ----------

  onOpenReport() {
    const { from, to } = this.defaultRange();
    this.setData({ rpVisible: true, rpFrom: from, rpTo: to, fabOpen: false });
    this.loadReport();
  },

  onCloseReport() {
    this.setData({ rpVisible: false });
  },

  onRpVisibleChange(e) {
    if (!e.detail.visible && this.data.rpVisible) this.setData({ rpVisible: false });
  },

  // 拉取范围内不通过记录并按日期分组（后端已按日期+卡片序排列，遇序分组即日期升序；仅看我随主页视图开关）
  async loadReport() {
    this.setData({ rpLoading: true });
    try {
      const { rpFrom, rpTo } = this.data;
      const data = await request({ url: `/api/v1/worklog/report?from=${rpFrom}&to=${rpTo}${this.scopeQuery()}` });
      const list = (data && data.list) || [];
      const groups = [];
      const groupMap = {};
      list.forEach((e) => {
        if (!groupMap[e.log_date]) {
          const d = parseDate(e.log_date);
          groupMap[e.log_date] = {
            date: e.log_date,
            title: `${d.getMonth() + 1}月${d.getDate()}日 ${WEEK[d.getDay()]}`,
            items: [],
          };
          groups.push(groupMap[e.log_date]);
        }
        groupMap[e.log_date].items.push({
          id: e.id,
          date: e.log_date, // 点击定位用：跳到该卡片所在日期
          plateText: e.plate_no,
          membersText: (e.members || []).join('、'),
          reasons: e.reasons || [],
        });
      });
      this.setData({
        rpGroups: groups,
        rpTotal: list.length,
        rpRangeText: `${rpFrom} ~ ${rpTo}`,
        rpLoading: false,
      });
    } catch (err) {
      this.setData({ rpLoading: false });
      this.toast(err.message);
    }
  },

  // 「改日期」：与下载面板共用 range 日历（先关报告面板，_rangeCalFor='rp' 选完重开）
  onRpChangeDate() {
    const { rpFrom, rpTo } = this.data;
    this._rangeCalFor = 'rp';
    this.setData({
      rpVisible: false,
      dlCalValue: [parseDate(rpFrom).getTime(), parseDate(rpTo).getTime()],
      dlCalVisible: true,
    });
  },

  // 点报告记录：关闭报告面板并跳到该日对应卡片（滚动定位 + 短暂高亮）
  async onRpItemTap(e) {
    const { id, date } = e.currentTarget.dataset;
    if (!id || !date) return;
    this.setData({ rpVisible: false });
    this.applyDate(date);
    await this.loadLogs();
    this.scrollToCard(Number(id));
  },

  // 滚动到指定卡片并闪烁高亮；当前视图口径下无此卡（如「仅看我」未含该记录）时提示
  scrollToCard(id) {
    if (!this.data.list.some((x) => x.id === id)) {
      this.toast('当前视图下无该卡片，请切换到「全部」查看');
      return;
    }
    this.setData({ flashId: id });
    wx.nextTick(() => {
      const q = wx.createSelectorQuery().in(this);
      q.select(`#logcard-${id}`).boundingClientRect();
      q.selectViewport().scrollOffset();
      q.exec((res) => {
        const rect = res && res[0];
        const scroll = res && res[1];
        if (!rect || !scroll) return;
        const top = rect.top + scroll.scrollTop - 12; // 卡片距顶 12px 留白
        wx.pageScrollTo({ scrollTop: Math.max(top, 0), duration: 300 });
      });
    });
    if (this._flashTimer) clearTimeout(this._flashTimer);
    this._flashTimer = setTimeout(() => this.setData({ flashId: 0 }), 1600);
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'work-log', title: '出工日志' });
  },
});
