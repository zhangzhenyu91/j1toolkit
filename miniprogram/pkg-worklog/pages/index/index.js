// 出工日志 · 主页：日期条切换 / 视图开关（全部·仅看我）/ 日志卡片直改 / 日历选日（按日验证状态着色）
// 新建/编辑表单收进底部弹层（edit 二级页已取缔）；底部另有批量下载水印照片面板
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';

const WEEK = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];
const pad = (n) => (n < 10 ? `0${n}` : `${n}`);
const fmtDate = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
const parseDate = (s) => {
  const [y, m, d] = s.split('-').map(Number);
  return new Date(y, m - 1, d);
};

// 照片验证状态 → 展示（含义见《开发指南》7.2：date_verify/destination_verify 映射，日期优先）
const PHOTO_STATUS = {
  pending: { cls: 'ing', text: '验证中' },
  passed: { cls: 'ok', text: '通过' },
  date_mismatch: { cls: 'bad', text: '日期不符' },
  dest_mismatch: { cls: 'bad', text: '目的地不符' },
  failed: { cls: 'bad', text: '验证失败' },
};

// 记录验证状态角标（后端按 5 条规则实时计算，见开发指南 7.1）
const VERIFY_BADGE = {
  passed: { cls: 'green', text: '验证通过' },
  failed: { cls: 'red', text: '未通过' },
  exempt: { cls: 'gray', text: '免验证' },
};

// 照片字段 → 展示结构（卡片直改与表单面板共用，保持一份）
function mapPhoto(p) {
  const hasGeo = !!(p.lng && p.lat);
  return {
    id: p.id,
    url: p.url,
    members: p.members || [],
    workContent: p.work_content || '',
    status: PHOTO_STATUS[p.verify_status] || PHOTO_STATUS.failed,
    statusKey: p.verify_status, // failed 时显示「重新验证」按钮
    pending: p.verify_status === 'pending',
    hasGeo,
    geoLngLat: hasGeo ? `${p.lng},${p.lat}` : '',
    geoLatLng: hasGeo ? `${p.lat},${p.lng}` : '',
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
    scope: 'all', // 视图开关：all=全部 / mine=仅看我（后端按 nickname 匹配成员）
    list: [],
    loading: true,
    // 日历弹层
    calVisible: false,
    calValue: null,
    minDate: 0,
    maxDate: 0,
    // 照片人名点亮弹层（添加/修改复用）
    memberVisible: false,
    memberMode: 'add', // add=上传新照片 / edit=修改已有照片人名
    memberPhotoId: 0,
    memberEntryId: 0, // 当前操作的卡片 id
    candidates: [], // [{name, checked, disabled}]
    // ---------- 新建/编辑表单底部弹层（逻辑移植自已取缔的 edit 页） ----------
    formVisible: false,
    formId: 0, // 0=新建
    formDateStr: '',
    members: [], // meta 成员 + checked（点亮即用车人）
    patrol: '',
    vehicleId: -1, // -1 未出车（新建默认） / >0 车牌 id
    vehicleText: '',
    destId: 0, // 0 未选择
    destText: '',
    isNoVehicle: true,
    // 面板内联筛选下拉（手风琴互斥：'' 全关 / 'vehicle' / 'dest'）
    dropType: '',
    dropKeyword: '',
    dropList: [],
    // 键盘高度（textarea 不顶起整页，见开发指南键盘三件套）
    keyboardHeight: 0,
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
    // 下载面板改日期（range 日历；与下载面板互斥开合，避免叠层 z-index 冲突）
    dlCalVisible: false,
    dlCalValue: null,
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

  shiftDay(delta) {
    const d = parseDate(this.data.dateStr);
    d.setDate(d.getDate() + delta);
    this.applyDate(fmtDate(d));
    this.loadLogs();
  },

  // ---------- 视图开关（全部 / 仅看我） ----------

  onScopeChange(e) {
    const scope = e.currentTarget.dataset.scope;
    if (!scope || scope === this.data.scope) return;
    this.setData({ scope });
    // 口径变化：清空日历着色缓存并强制重拉当前月（mine 为个人口径）
    this._dayStatus = {};
    this._dayStatusMonths = {};
    this.loadLogs();
    this.loadDayStatus(this.data.dateStr.slice(0, 7), true);
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
          membersText: (e.members || []).map((m) => m.name).join('、'),
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

  // 日历着色数据缓存：this._dayStatus = { 'YYYY-MM-DD': 'passed'|'failed' }，按月去重拉取
  async loadDayStatus(month, force) {
    this._dayStatus = this._dayStatus || {};
    this._dayStatusMonths = this._dayStatusMonths || {};
    if (!force && this._dayStatusMonths[month]) {
      this.injectCalendarFormat();
      return;
    }
    try {
      const data = await request({ url: `/api/v1/worklog/day-status?month=${month}${this.scopeQuery()}` });
      Object.assign(this._dayStatus, (data && data.map) || {});
      this._dayStatusMonths[month] = true;
      this.injectCalendarFormat();
    } catch (err) {
      // 着色失败不阻塞选日，仅静默跳过
    }
  },

  // format 属性需注入函数：selectComponent 后 setData（每次换新的函数引用以触发组件重算）
  injectCalendarFormat() {
    const cal = this.selectComponent('#wl-calendar');
    if (cal) cal.setData({ format: (day) => this.formatCalDay(day) });
  },

  // 给当日有记录的日期单元格追加着色 className，「今天」追加醒目标记（样式见 index.wxss）
  formatCalDay(day) {
    const d = day.date;
    const key = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
    const cls = [];
    if (day.type !== 'disabled') {
      const st = this._dayStatus && this._dayStatus[key];
      if (st) cls.push(st === 'passed' ? 'wl-cal-passed' : 'wl-cal-failed');
      if (key === fmtDate(new Date())) cls.push('wl-cal-today');
    }
    if (!cls.length) return day;
    return { ...day, className: `${day.className || ''} ${cls.join(' ')}`.trim() };
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

  // 复制经纬度（经,纬 / 纬,经 两组；wx.setClipboardData 成功时微信会自弹「内容已复制」，不再重复提示）
  onCopyGeo(e) {
    const { text, has } = e.currentTarget.dataset;
    if (!has) return;
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

  // ---------- 新建/编辑表单底部弹层（时序沿用原 edit 页） ----------

  // meta：车牌/目的地/成员（下拉与点亮数据源），passGate 后加载
  async loadMeta() {
    try {
      const data = await request({ url: '/api/v1/worklog/meta' });
      this._vehicles = (data && data.vehicles) || [];
      this._destinations = (data && data.destinations) || [];
      this.setData({ members: ((data && data.members) || []).map((m) => ({ ...m, checked: false })) });
      // 面板先于 meta 打开时，用列表数据重填点亮态；否则仅刷新车牌/目的地文案
      if (this.data.formVisible && this.data.formId) this.backfillForm();
      else this.refreshDictText();
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
    this.openForm(0);
  },

  // 卡片「用车人」行/「巡视」行/车牌头部：打开同一面板并回填该卡数据
  onOpenForm(e) {
    this.openForm(Number(e.currentTarget.dataset.id) || 0);
  },

  openForm(id) {
    // 清掉上一轮可能残留的防抖保存
    if (this._saveTimer) {
      clearTimeout(this._saveTimer);
      this._saveTimer = null;
    }
    this._dirty = false;
    const base = {
      formVisible: true,
      formId: id,
      formDateStr: this.data.dateStr,
      dropType: '',
      dropKeyword: '',
      dropList: [],
      keyboardHeight: 0,
    };
    if (!id) {
      // 新建：默认未出车，人员全部未点亮
      this._lastPatrol = '';
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
    this._lastPatrol = entry.patrol;
    this.setData({
      ...base,
      patrol: entry.patrol,
      vehicleId: entry.vehicleId > 0 ? entry.vehicleId : -1,
      isNoVehicle: !(entry.vehicleId > 0),
      destId: entry.destId || 0,
      members: this.data.members.map((m) => ({ ...m, checked: entry.memberIds.includes(m.id) })),
    });
    this.refreshDictText();
  },

  onFormPatrolInput(e) {
    this._dirty = true;
    this.setData({ patrol: e.detail.value });
  },

  // 巡视内容失焦且值有变化时自动保存（已保存卡片）
  onFormPatrolBlur() {
    if (!this.data.formId) return;
    if (this.data.patrol === (this._lastPatrol || '')) return;
    this.autoSave();
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
    this._dirty = true;
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
    // 已保存卡片：车牌/目的地变化防抖自动保存（后端拒绝时 toast + 回填回滚）
    if (this.data.formId) this.scheduleAutoSave();
  },

  // ---------- 人员点亮 ----------

  onMemberTagChange(e) {
    const { index } = e.currentTarget.dataset;
    this._dirty = true;
    this.setData({ [`members[${index}].checked`]: e.detail.checked });
    if (!this.data.formId) {
      // 新建且已选车牌：点亮首个成员后立即自动创建卡片，转入已保存态
      if (this.data.vehicleId > 0 && this.data.members.some((m) => m.checked)) {
        this.autoCreate();
      }
      return;
    }
    this.scheduleAutoSave();
  },

  // ---------- 自动创建 / 自动保存 / 完成 ----------

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

  // 新建模式自动创建：点亮成员后立即落库（_autoCreating 防重入，失败保持新建态）
  async autoCreate() {
    if (this._autoCreating || this.data.formId) return;
    this._autoCreating = true;
    try {
      const data = await request({ url: '/api/v1/worklog/logs', method: 'POST', data: this.buildPayload() });
      this._lastPatrol = this.data.patrol;
      this.setData({ formId: (data && data.id) || 0 });
      this.toast('已自动保存');
      this.loadLogs(); // 下方卡片实时同步
      // 创建期间表单可能又有改动，防抖补一次保存
      this.scheduleAutoSave();
    } catch (err) {
      this.toast(err.message); // 保持新建态，可继续编辑或由「完成」重试
    } finally {
      this._autoCreating = false;
    }
  },

  // 已保存卡片：表单变化防抖 400ms 自动 PUT
  scheduleAutoSave() {
    if (!this.data.formId) return;
    if (this._saveTimer) clearTimeout(this._saveTimer);
    this._saveTimer = setTimeout(() => {
      this._saveTimer = null;
      this.autoSave();
    }, 400);
  },

  // _saving 防并发：保存进行中忽略新触发
  async autoSave() {
    if (!this.data.formId || this._saving) return;
    this._saving = true;
    try {
      await request({ url: `/api/v1/worklog/logs/${this.data.formId}`, method: 'PUT', data: this.buildPayload() });
      this._lastPatrol = this.data.patrol;
      await this.loadLogs(); // 下方卡片实时同步
    } catch (err) {
      this.toast(err.message);
      await this.loadLogs();
      this.backfillForm(); // 以最新 loadLogs 结果回填面板回滚
    } finally {
      this._saving = false;
    }
  },

  // 自动保存失败回滚：用列表中的该卡数据重填表单（卡片已消失则保持现状）
  backfillForm() {
    if (!this.data.formVisible) return;
    const entry = this.data.list.find((x) => x.id === this.data.formId);
    if (!entry) return;
    this._lastPatrol = entry.patrol;
    this.setData({
      patrol: entry.patrol,
      vehicleId: entry.vehicleId > 0 ? entry.vehicleId : -1,
      isNoVehicle: !(entry.vehicleId > 0),
      destId: entry.destId || 0,
      dropType: '',
      members: this.data.members.map((m) => ({ ...m, checked: entry.memberIds.includes(m.id) })),
    });
    this.refreshDictText();
  },

  // 底部「完成」：已保存→补发待保存并关闭；新建有输入→先创建再关闭；无操作→直接关闭（不建空卡）
  async onFormDone() {
    if (this.data.formId) {
      // 有待发的防抖保存则立即补发（autoSave 内部已成功/失败均 loadLogs）
      if (this._saveTimer) {
        clearTimeout(this._saveTimer);
        this._saveTimer = null;
        await this.autoSave();
      }
      this.setData({ formVisible: false, dropType: '', keyboardHeight: 0 });
      this.loadLogs();
      return;
    }
    if (!this._dirty) {
      this.setData({ formVisible: false, dropType: '', keyboardHeight: 0 });
      return;
    }
    if (this._autoCreating) {
      this.toast('正在自动保存，请稍候');
      return;
    }
    this._autoCreating = true;
    try {
      await request({ url: '/api/v1/worklog/logs', method: 'POST', data: this.buildPayload() });
      this.setData({ formVisible: false, dropType: '', keyboardHeight: 0 });
      this.loadLogs();
    } catch (err) {
      this.toast(err.message); // 失败留面板可重试
    } finally {
      this._autoCreating = false;
    }
  },

  // 遮罩关闭等同「完成」
  onFormVisibleChange(e) {
    if (!e.detail.visible && this.data.formVisible) this.onFormDone();
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

  // 添加照片：弹人名点亮层（候选 = 本卡用车人，已上传者置灰）
  onAddPhoto(e) {
    const { entryId } = e.currentTarget.dataset;
    const entry = this.data.list.find((x) => x.id === entryId);
    if (!entry || !entry.checks.length) {
      this.toast('本卡暂无用车人');
      return;
    }
    const used = this.usedPhotoNames(entry, 0);
    this.setData({
      memberVisible: true,
      memberMode: 'add',
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
    this.chooseAndUpload(names);
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

  async uploadPhoto(image, members) {
    wx.showLoading({ title: '正在上传…', mask: true });
    try {
      await request({
        url: `/api/v1/worklog/logs/${this.data.memberEntryId}/photos`,
        method: 'POST',
        data: { image, members },
        timeout: 60000,
      });
      wx.hideLoading();
      this.toast('已上传，验证中');
      this.loadLogs(); // 刷新后自动进入 pending 轮询
    } catch (err) {
      wx.hideLoading();
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
    wx.navigateTo({ url: '/pkg-worklog/pages/manage/manage' });
  },

  // ---------- 批量下载水印照片 ----------

  // 首次打开默认范围：当天 1~10 日 → 上月整月；11 日及以后 → 本月 1 号到今天
  defaultDlRange() {
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
    const { from, to } = this.defaultDlRange();
    this.setData({ dlVisible: true, dlFrom: from, dlTo: to });
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
      const list = (data && data.list) || [];
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
        dlRangeText: `${dlFrom} ~ ${dlTo}`,
        dlLoading: false,
      });
      this.recountDl();
    } catch (err) {
      this.setData({ dlLoading: false });
      this.toast(err.message);
    }
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
    this.setData({
      dlVisible: false,
      dlCalValue: [parseDate(dlFrom).getTime(), parseDate(dlTo).getTime()],
      dlCalVisible: true,
    });
  },

  // range 日历确认：e.detail.value 为两个时间戳
  onDlCalConfirm(e) {
    const value = e.detail.value;
    if (!Array.isArray(value) || value.length < 2) {
      this.toast('请选择起止日期');
      return;
    }
    this.setData({
      dlCalVisible: false,
      dlFrom: fmtDate(new Date(value[0])),
      dlTo: fmtDate(new Date(value[1])),
      dlVisible: true,
    });
    this.loadDlPhotos();
  },

  // 未选直接关闭日历：重开下载面板（保留原范围）
  onDlCalClose() {
    if (!this.data.dlCalVisible) return;
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
});
