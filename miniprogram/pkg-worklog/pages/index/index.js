// 出工日志 · 主页：日期条切换 / 视图开关（全部·仅看我）/ 日志卡片直改 / 日历选日（按日验证状态着色）
// 新建与「改派车/用车人」共用底部表单弹层（仅「保 存」提交，无实时保存；改派车保存前弹内网派车单同步警告）；
// 跨班日志（仅超管）：悬浮钮「跨班日志」新建 / 点跨班卡车牌头编辑均走 cross 模式（/logs/cross/*，全启用班组
// 字典按名称选择，归属班组可选）；跨班卡车牌行加「跨班」徽章，非超管不可改派车 / 删除，他班跨班卡不显示「从商旅同步」；
// 巡视内容点卡片主块单独弹层修改（带快捷输入）；备注（文字+附件传 COS）点「备 注」按钮或备注块弹层编辑；
// 底部另有批量下载水印照片面板与「汇总前核验」面板（按月列未通过记录，默认当月、可翻月）
// 商旅打卡扩展（见《开发指南》第十五节）：卡片「商旅打卡」区每人开始/结束两枚 chip
// （未打卡橙虚线 / 已打卡绿勾 / 未绑定灰锁三态）；打卡确认弹层（定位/同记录同 seq 带入/杆塔带入 + 商旅备注）、
// 费用弹层（首次开始打卡自动弹，仅伙食补助/交通费可编辑）、添加照片三选（非水印直传免验证）、人名点亮层商旅登录态置灰、
// 照片区非水印展示（同款式渲染，仅无验证信息）与商旅同步标（失败重试）、卡片操作区「从商旅同步」（按日手动拉取，不一致以商旅为准覆盖）
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { BASE_URL } from '../../../config';
import { shareAppMessage } from '../../../utils/share';
import { pad } from '../../../utils/util';
import { createWmPhoto, genAntiCode, fmtWmTime } from '../../../utils/wmphoto';
import { createTowerCascade } from '../../../utils/tower';

// 水印照片链路共享（选片分流 + 4:3 裁剪层 + 水印字段公共件 + 相册保存；/api/v1/worklog/geo 地点天气）
const wmPhoto = createWmPhoto({ geoBase: '/api/v1/worklog', logTag: '出工日志' });
// 杆塔三级级联共享（缓存按生效班组 id 隔离，避免串班组的旧缓存——切换班组后 applyTeam 已清 towerRows；
// 接口拼生效班组 teamQuery）
const towerCascade = createTowerCascade({
  towersUrl: (page) => `/api/v1/worklog/towers${page.teamQuery('?')}`,
  cacheKey: (page) => `worklog_towers_${page._teamId || 0}`,
  logTag: '出工日志',
});

const WEEK = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];
const fmtDate = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
// 随机时分：10:00-12:00（不含 12:00）内随机，历史带入与无历史预填共用此口径
const randWmHm = () => `${pad(10 + Math.floor(Math.random() * 2))}:${pad(Math.floor(Math.random() * 60))}`;

const parseDate = (s) => {
  const [y, m, d] = s.split('-').map(Number);
  return new Date(y, m - 1, d);
};

// ---------- 商旅打卡展示辅助 ----------
// 打卡时间 → HH:mm（clock_time 为 DATETIME，JSON 输出 ISO 串时按本地时区取时分；兼容 'YYYY-MM-DD HH:mm:ss' 直取）
const fmtClockHm = (t) => {
  const s = String(t || '');
  if (!s) return '';
  if (s.indexOf('T') > 0) {
    const d = new Date(s);
    return Number.isNaN(d.getTime()) ? '' : `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }
  const m = /(\d{1,2}):(\d{2})/.exec(s);
  return m ? `${pad(Number(m[1]))}:${m[2]}` : '';
};
// 打卡地点短文案：去「中国」前缀与省级段，保留 市/区/县 + 街道/路段（如 中国山西省吕梁市汾阳市西河街道英雄北路 → 汾阳市西河街道英雄北路）
const shortPosition = (p) => {
  let s = String(p || '').replace(/^中国/, '').replace(/^[^省]{1,12}省/, '');
  const m = /^[^市]{1,12}市(.+)$/.exec(s);
  if (m && /[市区县]/.test(m[1])) s = m[1]; // 前段为地级市时去除（剩余仍含 市/区/县 才判为地级段）
  return s;
};
// 工时展示：纯数字补 h 后缀（商旅原文为字符串，可能已带单位）
const fmtWorkHours = (w) => {
  const s = String(w || '').trim();
  if (!s) return '';
  return /^\d+(\.\d+)?$/.test(s) ? `${s}h` : s;
};
// 打卡备注默认值（仅作商旅 remarks 提交，与记录卡片备注无关）
const CLOCK_REMARKS_DEFAULT = '110kV及220kV输电线路巡视';

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
  // 着色三态：failed 红 / remark 黄（通过但有备注）/ passed 绿
  if (st) cls.push(st === 'passed' ? 'wl-cal-passed' : st === 'remark' ? 'wl-cal-remark' : 'wl-cal-failed');
  if (key === fmtDate(new Date())) cls.push('wl-cal-today');
  if (!cls.length) return day;
  return { ...day, className: `${day.className || ''} ${cls.join(' ')}`.trim() };
}

// 照片验证状态 → 展示（逐项判定：date_ok/dest_ok 任一 0 即该项不符；历史数据回退旧状态值，见《开发指南》7.2）
// 记录验证状态角标（后端实时计算；规则为 7 条 a~g（含 f 费用达标、g 打卡地点含目的地），见开发指南 7.1 与第十五节）
const VERIFY_BADGE = {
  passed: { cls: 'green', text: '验证通过' },
  failed: { cls: 'red', text: '未通过' },
  exempt: { cls: 'gray', text: '免验证' },
};

// 照片单操作任务（上传/删除/改人名/resync）kind → 卡片进度条文案（任务化异步执行，进度条样式同从商旅同步卡片锁）
const PHOTO_OP_TEXT = {
  upload: '上传同步商旅中',
  delete: '解除商旅关联中',
  members: '人名同步商旅中',
  resync: '商旅重新同步中',
};

// 照片单操作任务完成 toast（kind → 文案；upload 的水印/非水印分支在 pollCardOp 内细分）
const PHOTO_OP_OK_TEXT = {
  upload: '已上传，验证中',
  delete: '已删除',
  members: '人名已修改',
  resync: '同步成功',
};

// 照片字段 → 展示结构（右侧八项：验证情况/人员/施工内容/拍摄时间/天气/地点/经度/纬度）
// 非水印照片（is_watermark=0）：与水印照片同款式渲染，但不做验证也不显示验证状态/施工内容/时间地点行，仅存档并同步商旅费用照片
function mapPhoto(p) {
  const isPlain = p.is_watermark === 0;
  // 验证情况：pending=验证中 / failed=验证失败（可重试）/ 完成态逐项列出未通过项（非水印照片无此信息，不渲染）
  let verify = null;
  if (!isPlain) {
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
  }
  return {
    id: p.id,
    url: p.url,
    members: p.members || [],
    workContent: p.work_content || '',
    verify,
    statusKey: p.verify_status, // failed 时显示「重新验证」按钮
    editable: !isPlain && p.verify_status !== 'pending', // 验证状态可点击进手动修正（识别中不可改）
    pending: !isPlain && p.verify_status === 'pending', // 轮询依据（非水印 skipped 不进轮询）
    shotTime: p.shot_time || '',
    weather: p.weather || '',
    location: p.location || '',
    lng: p.lng || '',
    lat: p.lat || '',
    // 商旅打卡扩展：非水印标记 + 商旅费用照片同步态（0 未同步 / 1 已同步 / 2 同步失败可重试）
    isPlain,
    sgccSynced: p.sgcc_synced || 0,
  };
}

Page({
  behaviors: [wmPhoto, towerCascade],
  data: {
    gate: false, // 门控（参照首页 gate 模式）
    dateStr: '', // 当前日期 YYYY-MM-DD
    dateText: '',
    weekText: '',
    isToday: false,
    isAdmin: false,
    canManage: false, // 「数据管理」入口可见性：超管 / 班组管理员
    // 班组切换器（屏五）：超管可点 chip 下拉切换生效班组；其余角色为静态班组名标签
    noTeam: false, // 非超管且未分配班组：整页空态（屏十），不发业务请求
    teamName: '', // 切换器 chip 展示名
    teamOptions: [], // 超管下拉选项 [{id, name, on}]
    teamDropOpen: false,
    fabOpen: false, // 右下悬浮主钮展开态（＋/×；展开项：数据管理 / 工作任务单 / 费用汇总 / 派车汇总 / 派车对齐 / 批量从商旅同步（均超管与班组管理员）/ 批量下载 / 汇总前核验 / 跨班日志（仅超管）/ 新建日志）
    scope: 'all', // 视图开关：all=全部 / mine=仅看我（后端按 nickname 匹配成员）
    list: [],
    loading: true,
    flashId: 0, // 报告定位后高亮中的卡片 id（约 1.6s 后消退）
    // 切日 swiper（三窗格预渲染 前/今/后 一天；落定后窗口平移并复位中间格）
    win: [], // [{dateStr, list, ready}]；ready=false 为预拉中的加载占位格
    swiperCurrent: 1, // 恒定位中间格（当前日）
    intoView: '', // 中间格滚区 scroll-into-view 锚点（报告定位滚动用）
    // 日历弹层
    calVisible: false,
    calValue: null,
    minDate: 0,
    maxDate: 0,
    calAnim: '', // 滑动切换月份动效（落在 t-class）：'' / wl-cal-from-right / wl-cal-from-left
    // 照片人名点亮弹层（添加/修改复用）
    memberVisible: false,
    memberMode: 'add', // add=上传新照片 / edit=修改已有照片人名
    memberAction: 'raw', // memberMode=add 时的三选一：raw=选择水印照片上传 / wm=选照片并添加水印 / plain=非水印照片直传
    memberPhotoId: 0,
    memberEntryId: 0, // 当前操作的卡片 id
    candidates: [], // [{name, checked, disabled, note}]（note：已上传 / 登录过期 / 未绑定）
    memberNote: '', // 层内说明（随方式变化）
    memberOffTip: '', // 商旅登录过期提醒条（有过期候选人时显示）
    memberSaving: false, // 人名修改提交中（防连点；改人名需同步等待商旅结果，耗时较长）
    // 添加照片方式三选弹层（自绘，替代 t-action-sheet）
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
    // 杆塔级联与 4:3 裁剪层数据（wmTowerPicked/tower*/crop*）由 tower / wmphoto behavior 提供
    // ---------- 新建/改派车表单底部弹层（仅「保 存」提交，无实时保存） ----------
    formVisible: false,
    formId: 0, // 0=新建
    formDateStr: '',
    formCross: false, // 跨班日志模式（仅超管；归属班组可选，车牌/目的地/人员按名称选择，无字典 id）
    crossTeamId: 0, // 跨班日志归属班组 id
    crossTeamText: '', // 归属班组展示名
    members: [], // meta 成员 + checked（点亮即用车人）+ disabled（当日已在其他卡片，置灰不可点亮）
    memberUsedTip: false, // 有成员被当日其他卡片占用时，人员选择提示「灰色 = 当日已在其他卡片」
    patrol: '', // 面板不展示；改派车提交时原样带上（PUT 全量替换）
    vehicleId: -1, // -1 未出车（新建默认） / >0 车牌 id
    vehicleText: '',
    destId: 0, // 0 未选择
    destText: '',
    orderNo: '', // 派车单号（可空；未出车禁用，保存时服务端强制置空）
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
    // ---------- 备注编辑弹层（点「备 注」按钮或备注块弹出；附件保存时才上传 COS） ----------
    rmkVisible: false,
    rmkEntryId: 0,
    rmkDraft: '', // 编辑中的备注文字
    rmkFiles: [], // [{type,name,url?,cos_key?,size?,tempFilePath?,preview,key,isNew}]
    rmkMedia: [], // rmkFiles 中 image/video（缩略图区展示）
    rmkDocs: [], // rmkFiles 中 doc（文件条区展示）
    rmkSaving: false,
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
    pdfBusy: false, // PDF 生成中（下载为 PDF / PDF 存网盘共用锁）
    ndSaveVisible: false, // 网盘目录选择器（nd-dirpicker；PDF 存网盘选目录）
    // 下载面板改日期（range 日历；与下载面板互斥开合，避免叠层 z-index 冲突）
    dlCalVisible: false,
    dlCalValue: null,
    // ---------- 汇总前核验面板（原「验证报告」；按月列未通过记录，默认当月、可翻月不看未来；数据取 /worklog/report） ----------
    rpVisible: false,
    rpMonth: '', // 当前查看月份 YYYY-MM（打开面板时默认当月）
    rpMonthText: '', // 「2026 年 8 月」
    rpMonthAtCur: true, // 已是当月：翻月「下一月」置灰
    rpLoading: false,
    rpIssues: [], // [{id, logDate, dateText, plateText, membersText, reasons}]
    rpOkText: '', // 绿色通过态文案（其余 N 条全部通过 / 当月无未通过记录）
    // 批量从商旅同步面板（悬浮钮入口；改区段 range 日历与面板互斥开合，同下载面板口径）
    bsVisible: false,
    bsFrom: '', // 同步区段起 YYYY-MM-DD（默认当月 1 日）
    bsTo: '', // 止（默认今天）
    bsLoading: false,
    bsOkCount: 0, // 区段内成功记录条数（仅汇总展示）
    bsFails: [], // 失败明细 [{id, dateText, memberName, typeText, detail}]
    sgCalVisible: false,
    sgCalValue: null,
    // 批量从商旅同步：全页进度遮罩（本班组有批量拉取进行中时任何人进入出工日志均被覆盖，完成后解锁；轮询 GET /sgcc/sync/active）
    sgSyncVisible: false,
    sgSyncPct: 0,
    // ---------- 水印信息手动修正弹层（Dify 识别出错时用；保存后按记录日期与目的地重新核验） ----------
    wmEditVisible: false,
    wmEditPhotoId: 0,
    wmEditDraft: { content: '', time: '', weather: '', location: '', lng: '', lat: '' },
    wmEditSaving: false,
    // ---------- 商旅打卡 · 打卡确认弹层（开始/结束/更新共用） ----------
    ckVisible: false,
    ckTitle: '', // 「开始打卡 · 姓名」/「结束打卡 · 姓名」/「更新打卡地点 · 姓名」
    ckNote: '', // 层内说明（机型口径 / 同记录带入提示）
    ckEntryId: 0,
    ckMemberId: 0,
    ckMemberName: '',
    ckSeq: 1, // 1 开始打卡 / 2 结束打卡
    ckAction: 'mark', // mark=打卡 / update=更新打卡地点
    ckPosition: '', // 完整地址串（「中国」前缀，商旅口径）
    ckLng: '',
    ckLat: '',
    ckCityCode: '',
    ckCityName: '',
    ckLocSub: '', // 位置卡副行（经纬度 + 来源标注）
    ckLocating: false, // 定位中（位置行显示占位文案）
    ckRemarks: '', // 打卡备注（默认 CLOCK_REMARKS_DEFAULT）
    ckSaving: false,
    // ---------- 商旅打卡 · 费用信息弹层 ----------
    feeVisible: false,
    feeMemberId: 0,
    feeMemberName: '',
    feeLoading: false,
    feeFood: '', // 伙食补助（输入框字符串）
    feeTransit: '', // 交通费
    feeFoodFocus: false, // 伙食补助输入框焦点（受控：hold-keyboard 下关层需同步收键盘）
    feeTransitFocus: false, // 交通费输入框焦点（同上）
    feeSaving: false,
    feeStdFood: '', // 当日适用标准伙食（空=无标准数据，不显示「按标准填入」行）
    feeStdTransit: '', // 当日适用标准交通费
    feeStdText: '', // 标准文案（如「当日适用标准：伙食40/交通0（驻地）」）
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
    this._role = user.role || 'user';
    this._teamId = 0; // 生效班组 id（仅超管经切换器指定；0=不带参数，后端落自己/默认班组）
    this._myTeamId = Number(user.team_id) || 0; // 自己所属班组 id（非超管的生效班组；他班跨班卡「从商旅同步」显隐用，取值 _teamId || _myTeamId）
    this._dayLists = {}; // 邻日预拉缓存（key `${date}|${scope}|${team}` → 映射后的卡片列表；按窗格裁剪）
    this._inflightDays = {}; // 进行中的按日请求（key 同上，并发去重）
    this._daySwitching = false; // 切日落定处理中（防抖）
    this._syncJobs = {}; // 从商旅同步进行中任务（key 卡片 id → {jobId, pct}；mapLogList 据此给卡片挂进度条）
    this._photoOps = {}; // 照片单操作进行中任务（key 卡片 id → {opId, kind, pct, spectator?, wm?}；同上挂条）
    this._jobTimers = {}; // 进度轮询定时器（key 任务 id）
    // 非超管且未分配班组：整页空态（屏十），不再发任何业务请求
    if (this._role !== 'admin' && !user.team) {
      this.setData({ gate: true, noTeam: true, loading: false });
      return;
    }
    this.setData({
      gate: true,
      isAdmin: this._role === 'admin',
      canManage: this._role === 'admin' || this._role === 'team_admin',
      teamName: user.team || '',
    });
    if (this._role === 'admin') {
      this.initTeams(user); // 超管先定生效班组，再整页加载
      return;
    }
    this.loadMeta();
    this.loadLogs();
  },

  // ---------- 班组切换器（屏五；仅超管可切换，其余角色静态展示本班名） ----------

  // 超管：拉启用班组（/admin/teams 取 status=1）→ 生效班组（storage 优先 → 自己班组 → 第一个）→ 整页数据
  async initTeams(user) {
    let teams = [];
    try {
      const data = await request({ url: '/api/v1/admin/teams' });
      teams = ((data && data.list) || []).filter((t) => t.status === 1);
    } catch (err) {
      this.toast(err.message);
    }
    this._teams = teams;
    const saved = Number(wx.getStorageSync('worklog_team_id')) || 0;
    const cur = teams.find((t) => t.id === saved)
      || teams.find((t) => t.id === Number(user.team_id))
      || teams[0] || null;
    this.applyTeam(cur ? cur.id : 0, false);
    this.loadMeta();
    this.loadLogs();
  },

  // 生效班组 query 片段（lead 为前导连接符；仅超管 _teamId>0 时携带，其余角色后端强制本班无需传）
  teamQuery(lead) {
    return this._teamId ? `${lead || '&'}team_id=${this._teamId}` : '';
  },

  // 生效班组 body 注入（POST/PUT JSON 用，口径同 teamQuery）
  teamBody(data) {
    return this._teamId ? Object.assign({}, data, { team_id: this._teamId }) : data;
  },

  // 记录当前生效班组并刷新切换器展示；switching=true 表示用户主动切换，整页口径随之重拉
  applyTeam(id, switching) {
    this._teamId = id;
    if (id) wx.setStorageSync('worklog_team_id', id);
    const cur = ((this._teams || []).find((t) => t.id === id)) || null;
    this.setData({
      teamName: cur ? cur.name : this.data.teamName,
      teamDropOpen: false,
      teamOptions: (this._teams || []).map((t) => ({ id: t.id, name: t.name, on: t.id === id })),
    });
    if (!switching) return;
    // 切换班组 = 整页口径变化：清日历着色缓存、杆塔坐标缓存与邻日预拉缓存、重置切日窗格，重拉全部数据（含开着的面板）
    Object.keys(DAY_STATUS).forEach((k) => delete DAY_STATUS[k]);
    this._dayStatusMonths = {};
    this._dayLists = {};
    this.setData({ towerRows: null, win: [] });
    this.loadMeta();
    this.loadLogs();
    this.loadDayStatus(this.data.dateStr.slice(0, 7), true);
    if (this.data.dlVisible) this.loadDlPhotos();
  },

  onTeamChipTap() {
    if (!this.data.isAdmin || !(this._teams || []).length) return;
    this.setData({ teamDropOpen: !this.data.teamDropOpen });
  },

  onTeamDropClose() {
    if (this.data.teamDropOpen) this.setData({ teamDropOpen: false });
  },

  onTeamPick(e) {
    const id = Number(e.currentTarget.dataset.id);
    if (!id || id === this._teamId) {
      this.setData({ teamDropOpen: false });
      return;
    }
    this.applyTeam(id, true);
  },

  onShow() {
    if (this.data.gate && !this.data.noTeam) this.loadLogs();
    this._lockCheckedAt = 0; // 切回页面强制探测一次全局锁（批量同步进行中立即出遮罩）
    if (this.data.gate && !this.data.noTeam) this.checkSgccLock();
    if (this.data.gate && !this.data.noTeam) this.checkSgccExpired();
  },

  // 商旅账号过期提醒：有成员登录过期时弹窗（指纹=当日日期+过期成员 id 串，同日同名单不重复弹；
  // 名单变化或跨天再弹——既覆盖「每次进入提醒」，又避免页面内子页返回骚扰）
  async checkSgccExpired() {
    try {
      const data = await request({ url: `/api/v1/sgcc/expired${this.teamQuery('?')}` });
      const list = (data && data.list) || [];
      if (!list.length) return;
      const now = new Date();
      const day = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`;
      const key = `${day}:${list.map((x) => x.memberId || x.mobile).sort().join(',')}`;
      if (this._expiredAlertKey === key) return;
      this._expiredAlertKey = key;
      const names = list.map((x) => x.memberName || x.mobile).join('、');
      Dialog.alert({
        context: this,
        selector: '#t-dialog',
        title: '商旅账号过期提醒',
        content: `${names} 的商旅账号已过期，需重新绑定后恢复打卡与同步。`,
        confirmBtn: '知道了',
      });
    } catch (err) {
      // 探测失败静默（商旅恒挂载，失败多为网络异常，不打断进入）
    }
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
    if (this._pullTimer) { // 「从商旅同步」的 8 秒静默刷新定时器
      clearTimeout(this._pullTimer);
      this._pullTimer = null;
    }
    Object.keys(this._jobTimers || {}).forEach((k) => clearTimeout(this._jobTimers[k])); // 同步进度轮询全部停止
    this._jobTimers = {};
    if (this._lockTimer) { // 全局锁遮罩轮询
      clearTimeout(this._lockTimer);
      this._lockTimer = null;
    }
    if (this._dailyTimer) { // 每日核查锁轮询
      clearTimeout(this._dailyTimer);
      this._dailyTimer = null;
    }
    this._dailySync = null;
    this._unloaded = true; // 页面已卸载：照片任务轮询（pollCardOp/pollSpectatorOps）据此退出，不再发请求/setData
    this._photoOps = {};
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
    this.nudgeSwiper(0); // 定到左格（前一天），原生滑动画
  },

  onNextDay() {
    this.nudgeSwiper(2); // 定到右格（后一天）
  },

  // 日期条按钮：把 swiper 定到目标格，落定与手势同走 onDaySwiperFinish
  nudgeSwiper(pos) {
    if (this._daySwitching || this.data.swiperCurrent !== 1 || this.data.win.length !== 3) return;
    this.setData({ swiperCurrent: pos });
  },

  // 切日落定（手势/按钮把 swiper 定到左/右格；current=1 为回弹未切日，忽略）：
  // 窗口平移到新当前日并复位中间格（中间格内容即用户所见，无感切换），再预拉新邻日
  async onDaySwiperFinish(e) {
    const pos = e.detail.current;
    if (pos === 1 || this._daySwitching) return;
    const target = this.data.win[pos];
    if (!target) return;
    this._daySwitching = true;
    // 日期条随落定更新（新当前日 = 目标格日期）
    this.applyDate(target.dateStr);
    // 目标日列表：预拉通常已命中；未命中则等拉取（该格为加载占位，落定即填充）
    let list = this._dayLists[this.dayKey(target.dateStr)];
    if (!list) {
      try {
        list = await this.fetchDayList(target.dateStr);
      } catch (err) {
        this.toast(err.message);
        list = [];
      }
    }
    const d = parseDate(target.dateStr);
    const prev = new Date(d.getTime());
    prev.setDate(prev.getDate() - 1);
    const next = new Date(d.getTime());
    next.setDate(next.getDate() + 1);
    const prevDs = fmtDate(prev);
    const nextDs = fmtDate(next);
    const prevList = this._dayLists[this.dayKey(prevDs)] || null;
    const nextList = this._dayLists[this.dayKey(nextDs)] || null;
    this.setData({
      list,
      win: [
        { dateStr: prevDs, list: prevList || [], ready: !!prevList },
        { dateStr: target.dateStr, list, ready: true },
        { dateStr: nextDs, list: nextList || [], ready: !!nextList },
      ],
      swiperCurrent: 1,
      intoView: '',
    });
    this._daySwitching = false;
    this.pruneDayLists();
    this.prefetchDays();
  },

  // 预拉缓存 key（口径=日期+视图+班组）
  dayKey(ds) {
    return `${ds}|${this.data.scope}|${this._teamId || 0}`;
  },

  // 拉取并映射某天日志列表（带缓存与并发去重；缓存由 pruneDayLists 按窗格裁剪）
  fetchDayList(ds) {
    const ck = this.dayKey(ds);
    if (this._dayLists[ck]) return Promise.resolve(this._dayLists[ck]);
    if (this._inflightDays[ck]) return this._inflightDays[ck];
    const p = request({ url: `/api/v1/worklog/logs?date=${ds}${this.scopeQuery()}${this.teamQuery()}` })
      .then((data) => {
        const list = this.mapLogList(data);
        this._dayLists[ck] = list;
        return list;
      })
      .finally(() => { delete this._inflightDays[ck]; });
    this._inflightDays[ck] = p;
    return p;
  },

  // 静默预拉窗格中未就绪的邻日（首屏两侧 / 切日后的新邻日），拉到即填充对应格
  prefetchDays() {
    (this.data.win || []).forEach((w) => {
      if (w.ready) return;
      this.fetchDayList(w.dateStr).then((list) => {
        const idx = (this.data.win || []).findIndex((x) => x.dateStr === w.dateStr);
        if (idx >= 0 && !this.data.win[idx].ready) {
          this.setData({ [`win[${idx}].list`]: list, [`win[${idx}].ready`]: true });
        }
      }).catch(() => {});
    });
  },

  // 预拉缓存裁剪：只留当前窗格三天（防多日连滑堆积）
  pruneDayLists() {
    const keep = {};
    (this.data.win || []).forEach((w) => { keep[this.dayKey(w.dateStr)] = 1; });
    Object.keys(this._dayLists).forEach((k) => { if (!keep[k]) delete this._dayLists[k]; });
  },

  // 首屏/口径变化后建窗格（当前日已加载；两侧占位，由 prefetchDays 填充）
  buildWin() {
    const d = parseDate(this.data.dateStr);
    const prev = new Date(d.getTime());
    prev.setDate(prev.getDate() - 1);
    const next = new Date(d.getTime());
    next.setDate(next.getDate() + 1);
    this.setData({
      win: [
        { dateStr: fmtDate(prev), list: [], ready: false },
        { dateStr: this.data.dateStr, list: this.data.list, ready: true },
        { dateStr: fmtDate(next), list: [], ready: false },
      ],
      swiperCurrent: 1,
      intoView: '',
    });
    this.prefetchDays();
  },

  // 中间格（当前日）列表同步；win 未建时仅更新镜像 list
  setCurPane(list) {
    const patch = { list, loading: false };
    if (this.data.win.length === 3) {
      patch['win[1].list'] = list;
      patch['win[1].ready'] = true;
    }
    this.setData(patch);
  },

  // ---------- 视图开关（全部 / 仅看我） ----------

  onScopeChange(e) {
    const scope = e.currentTarget.dataset.scope;
    if (!scope || scope === this.data.scope) return;
    this.setData({ scope });
    // 口径变化：清空日历着色缓存与邻日预拉缓存、重置切日窗格并强制重拉当前月（mine 为个人口径）
    Object.keys(DAY_STATUS).forEach((k) => delete DAY_STATUS[k]);
    this._dayStatusMonths = {};
    this._dayLists = {};
    this.setData({ win: [] });
    this.loadLogs();
    this.loadDayStatus(this.data.dateStr.slice(0, 7), true);
    // 批量下载面板的「仅看我」已收拢到本开关，面板打开时随动刷新；汇总前核验由 loadLogs 内重建
    if (this.data.dlVisible) this.buildDlGroups();
  },

  // scope=mine 时请求追加个人口径参数
  scopeQuery() {
    return this.data.scope === 'mine' ? '&scope=mine' : '';
  },

  // 当日日志卡片列表（始终网络重拉保证新鲜；完成后写缓存、同步中间窗格并预拉邻日；首屏顺带建窗格）
  async loadLogs() {
    this.clearPoll();
    this.setData({ loading: true });
    try {
      const data = await request({ url: `/api/v1/worklog/logs?date=${this.data.dateStr}${this.scopeQuery()}${this.teamQuery()}` });
      const list = this.mapLogList(data);
      this._dayLists[this.dayKey(this.data.dateStr)] = list;
      this.setCurPane(list);
      // 核验签名（各卡验证状态 + 备注有无）变化时强刷当月日历色点——覆盖编辑操作与轮询中异步验证落定两条路径
      // （签名单色点口径：verify_passed 与备注；逐条原因变化不触发，卡片本身已随列表重渲染）
      const sig = ((data && data.list) || [])
        .map((e) => `${e.id}:${e.verify_passed}:${String(e.remark || '').trim() ? 1 : 0}:${(e.remark_files || []).length}`)
        .join('|');
      if (this._verifySig !== undefined && sig !== this._verifySig) {
        this.loadDayStatus(this.data.dateStr.slice(0, 7), true);
      }
      this._verifySig = sig;
      this.schedulePoll(list);
      if (this.data.rpVisible) this.loadReport(); // 汇总前核验面板开着时随列表重查（按月口径）
      this.checkSgccLock(); // 批量从商旅同步全局锁探测（10s 节流）
      if (this.data.win.length) this.prefetchDays();
      else this.buildWin();
    } catch (err) {
      this.toast(err.message);
      this.setData({ loading: false });
    }
  },

  // 接口日志列表 → 卡片展示结构
  mapLogList(data) {
    return ((data && data.list) || []).map((e) => {
      const photos = (e.photos || []).map(mapPhoto);
      const remarkFiles = (e.remark_files || []).map((f) => ({ ...f }));
      const clockRaw = e.clockinMap || {}; // {memberId: {1:{detailId,time,position,workHours}, 2:{...}}}
      const members = e.members || [];
      // 商旅打卡区渲染前提：派车卡且有用车人（商旅恒挂载，sgccBound 恒随成员下发；无用车人的未出车卡不渲染）
      const showClock = !!e.vehicle_id && members.some((m) => m.sgccBound !== undefined);
      // 打卡仅当日开放（含更新，当日口径与后端 40041 一致）；非当日打卡 chip 置锁、副文案提示；费用修改不受限
      const ckReadonly = e.log_date !== fmtDate(new Date());
      // 同步进度：照片单操作任务（_photoOps：上传/删除/改人名/resync）与本卡手动同步（_syncJobs）优先；
      // 否则每晚定时核查进行中时当日出车卡同挂进度条（_dailySync）
      const photoOp = (this._photoOps || {})[e.id];
      const syncJob = (this._syncJobs || {})[e.id];
      const dailyHit = !photoOp && !syncJob && this._dailySync && e.log_date === fmtDate(new Date()) && !!e.vehicle_id;
      return {
        id: e.id,
        syncing: !!photoOp || !!syncJob || dailyHit,
        syncPct: photoOp ? photoOp.pct : (syncJob ? syncJob.pct : (dailyHit ? this._dailySync.pct : 0)),
        syncText: photoOp ? PHOTO_OP_TEXT[photoOp.kind] : '从商旅同步中',
        hasVehicle: !!e.vehicle_id,
        plateText: e.vehicle_id ? e.plate_no : '未出车',
        crossTeam: !!e.cross_team, // 跨班日志（仅超管可改派车 / 删除，卡片头部加「跨班」徽章）
        teamId: Number(e.team_id) || 0, // 归属班组 id（cross 编辑回填 / 商旅同步显隐用）
        badge: VERIFY_BADGE[e.verify_passed] || VERIFY_BADGE.failed,
        failReasons: e.verify_reasons || [], // 未通过明细（角标为「未通过」时逐行展示）
        patrolText: e.patrol_content || '—',
        checks: members.map((m) => ({
          mid: m.id,
          memberId: m.member_id, // 打卡/费用接口口径（worklog_member.id）
          name: m.name,
          checked: !!m.checked,
          sgccBound: m.sgccBound !== false, // 未绑定商旅 → 打卡 chip 置锁
          sgccTokenStatus: m.sgccTokenStatus == null ? 1 : m.sgccTokenStatus, // 0=登录过期（人名点亮层置灰；打卡仍交由后端 40021 拦截）
        })),
        clockRaw,
        clockRows: showClock ? this.buildClockRows(members, clockRaw, ckReadonly) : [],
        showClock,
        feeStd: e.feeStd || null, // 当日费用适用标准（商旅开启时后端装配 {foodFee,transitFee,scope}，费用弹层「按标准填入」用）
        // 他班跨班卡：不渲染「从商旅同步」（仅归属班可发起同步，后端按 entry.team_id 校验）
        syncHidden: !!e.cross_team && Number(e.team_id) !== ((this._teamId || this._myTeamId) || 0),
        ckReadonly,
        photos,
        photoUrls: photos.map((p) => p.url),
        // 备注（文字 + 附件；均为空即 hasRemark=false，卡片不渲染备注块）
        remark: e.remark || '',
        remarkFiles,
        remarkMedia: remarkFiles.filter((f) => f.type === 'image' || f.type === 'video'),
        remarkDocs: remarkFiles.filter((f) => f.type === 'doc'),
        hasRemark: !!(e.remark || remarkFiles.length),
        // 无照片且无派车时不显示水印照片区
        showPhotos: !!e.vehicle_id || photos.length > 0,
        // 表单面板回填用的原始字段
        patrol: e.patrol_content || '',
        vehicleId: e.vehicle_id || 0,
        destId: e.destination_id || 0,
        destText: e.destination_name || '', // cross 编辑模式按名称回填
        orderNo: e.dispatch_order_no || '', // 派车单号回填
        memberIds: (e.members || []).map((m) => m.member_id),
      };
    });
  },

  // 商旅打卡区行数据：每个用车人一行（状态小字 + 开始/结束两枚 chip 三态）
  // chip 三态：done=已打卡（绿底勾+时间+地点+「更新」，非当日不显示更新）/ todo=未打卡（橙虚线，同记录已有他人同 seq 打卡时副文案提示带入）/ lock=未绑定或非当日（灰锁禁用）
  buildClockRows(members, clockRaw, readonly) {
    // 同 seq 已有他人打卡（与 openClockSheet 带入规则一致：开始/结束不互相带入）
    const seqClocked = (seq) => Object.keys(clockRaw).some((k) => clockRaw[k] && clockRaw[k][String(seq)] && clockRaw[k][String(seq)].position);
    const slot = (s, seq, bound) => {
      const title = seq === 1 ? '开始打卡' : '结束打卡';
      if (s) {
        return {
          cls: 'done',
          icon: 'check',
          iconColor: '#00B42A',
          title: `${seq === 1 ? '开始' : '结束'} ${fmtClockHm(s.time)}`,
          sub: shortPosition(s.position) || '地点未知',
          done: true,
          upd: !readonly, // 非当日不允许更新地点
        };
      }
      if (readonly) {
        return { cls: 'lock', icon: 'lock-on', iconColor: '#C9CDD4', title, sub: '仅当日可打卡', done: false, upd: false };
      }
      if (!bound) {
        return { cls: 'lock', icon: 'lock-on', iconColor: '#C9CDD4', title, sub: '引导本人至「我的」页绑定', done: false, upd: false };
      }
      return { cls: 'todo', icon: 'time', iconColor: '#0E3DA8', title, sub: seqClocked(seq) ? '点击打卡 · 带入同记录定位' : '点击打卡', done: false, upd: false };
    };
    return members.map((m) => {
      const bound = m.sgccBound !== false;
      const c = clockRaw[m.member_id] || {};
      const s1 = c['1'];
      const s2 = c['2'];
      // 行首状态小字：工时（两次打满）/ 还差结束打卡 / 未绑定商旅；未开始打卡不显示
      let statText = '';
      let statCls = '';
      let statIcon = '';
      let statIconColor = '';
      if (!bound) {
        statText = '未绑定商旅';
        statCls = 'red';
        statIcon = 'lock-on';
        statIconColor = '#F53F3F';
      } else if (s1 && s2) {
        const wh = fmtWorkHours((s2 && s2.workHours) || (s1 && s1.workHours));
        statText = wh ? `工时 ${wh}` : '已完成两次打卡';
        statCls = 'ok';
        statIcon = 'check';
        statIconColor = '#00B42A';
      } else if (s1 && !s2) {
        statText = '还差结束打卡';
      }
      return {
        memberId: m.member_id,
        name: m.name,
        bound,
        statText,
        statCls,
        statIcon,
        statIconColor,
        seq1: slot(s1, 1, bound),
        seq2: slot(s2, 2, bound),
      };
    });
  },

  // 有照片处于「验证中」时 3 秒后自动刷新（Dify 异步回写；非水印 skipped 不触发）
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
    // 跨日跳转：重置切日窗格（loadLogs 后以新日期为中心重建）
    this.setData({ calVisible: false, win: [] });
    this.applyDate(fmtDate(new Date(value)));
    this.loadLogs();
  },

  // 月份切换（switch-mode=month 的翻月箭头）：拉取该月着色数据
  onCalPanelChange(e) {
    const { year, month } = e.detail;
    this.loadDayStatus(`${year}-${pad(month)}`);
  },

  // ---------- 日历左右滑动切换月份（与页面切日同口径手势，带平移进入动画） ----------

  onCalTouchStart(e) {
    const t = e.touches[0];
    this._calTouch = { x: t.clientX, y: t.clientY };
  },

  onCalTouchEnd(e) {
    if (!this._calTouch) return;
    const t = e.changedTouches[0];
    const dx = t.clientX - this._calTouch.x;
    const dy = t.clientY - this._calTouch.y;
    this._calTouch = null;
    if (Math.abs(dx) >= 60 && Math.abs(dx) > Math.abs(dy) * 2) {
      this.switchCalMonth(dx < 0 ? 1 : -1); // 左滑下一月，右滑上一月
    }
  },

  // 与翻月箭头同口径（getCurrentDate/calcCurrentMonth 均为组件方法）：越出 minDate/maxDate 当月不切换；
  // 切换后按新月份拉着色数据（等同 panel-change 链路），新网格按滑动方向平移进入
  switchCalMonth(delta) {
    if (this._calSwitching) return; // 动画期间连滑防抖
    const cal = this.selectComponent('#wl-calendar');
    if (!cal || typeof cal.getCurrentDate !== 'function') return;
    const cur = new Date(cal.getCurrentDate());
    const target = new Date(cur.getFullYear(), cur.getMonth() + delta, 1);
    const min = new Date(this.data.minDate);
    const max = new Date(this.data.maxDate);
    if (target < new Date(min.getFullYear(), min.getMonth(), 1)) return;
    if (target > new Date(max.getFullYear(), max.getMonth(), 1)) return;
    this._calSwitching = true;
    cal.calcCurrentMonth(target.getTime());
    this.loadDayStatus(`${target.getFullYear()}-${pad(target.getMonth() + 1)}`);
    // 先清空再 nextTick 重放，保证连切同向也重新触发动画（同切日 swiper 复位模式）
    this.setData({ calAnim: '' });
    wx.nextTick(() => {
      this.setData({ calAnim: delta > 0 ? 'wl-cal-from-right' : 'wl-cal-from-left' });
    });
    setTimeout(() => {
      this._calSwitching = false;
    }, 280);
  },

  // 日历着色数据：按月去重拉取，写入模块级缓存（DAY_STATUS）后手动重算日历
  async loadDayStatus(month, force) {
    this._dayStatusMonths = this._dayStatusMonths || {};
    if (!force && this._dayStatusMonths[month]) {
      this.recolorCalendar();
      return;
    }
    try {
      const data = await request({ url: `/api/v1/worklog/day-status?month=${month}${this.scopeQuery()}${this.teamQuery()}` });
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

  // 验证失败 → 重新验证（重置为验证中并异步重调 Dify，随后轮询刷新）
  async onRetryVerify(e) {
    const { pid } = e.currentTarget.dataset;
    try {
      await request({ url: `/api/v1/worklog/photos/${pid}/verify`, method: 'POST', data: this.teamBody({}) });
      this.toast('已重新提交验证');
      this.loadLogs();
    } catch (err) {
      this.toast(err.message);
    }
  },

  // 预览水印照片
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

  // ---------- 商旅打卡 · 打卡确认弹层（开始/结束/更新共用） ----------

  // 点打卡 chip：未绑定 toast 引导；已打卡进「更新打卡地点」，未打卡进「开始/结束打卡」
  onClockTap(e) {
    const { entryId, memberId, name, seq, bound, done, today } = e.currentTarget.dataset;
    if (today === false || today === 'false') {
      this.toast('仅当日日期可打卡');
      return;
    }
    if (!bound) {
      this.toast('该用车人未绑定商旅，请引导其本人在「我的 → 绑定商旅」绑定');
      return;
    }
    this.openClockSheet(Number(entryId), Number(memberId), name, Number(seq) === 2 ? 2 : 1, done ? 'update' : 'mark');
  },

  // 打开弹层：位置优先级 = 同记录他人同 seq 打卡带入 → 当前定位逆编码。
  // 带入为**整套带入**（地址串+坐标+城市编码/城市名，保持地址与坐标城市对应；不取本机定位）；
  // 开始/结束不互相带入：seq=1 仅在同记录已有他人的「开始打卡」时带入，seq=2 仅在已有他人的「结束打卡」时带入，否则走当前定位；
  // 更新打卡遵循同一带入逻辑——视为该次打卡未打，不带入本人既有地点，带入同记录其他已打卡人（同 seq 第一个），无他人打卡则走当前定位；
  // 地址串支持手动输入（手输后 geo 回填不覆盖；提交时经 /sgcc/geo 正向解析出坐标与城市信息，采用规范地址串）
  openClockSheet(entryId, memberId, name, seq, action) {
    const entry = this.data.list.find((x) => x.id === entryId);
    if (!entry) {
      this.toast('日志不存在或已被删除');
      return;
    }
    // 同记录已打卡人（按用车人顺序找第一个同 seq 有位置的他人）：带入其地址串并标注
    let first = null;
    for (const m of entry.checks) {
      if (m.memberId === memberId) continue; // 仅带入他人的打卡地点
      const c = entry.clockRaw[m.memberId];
      if (c && c[String(seq)] && c[String(seq)].position) {
        first = { name: m.name, ...c[String(seq)] };
        break;
      }
    }
    const title = action === 'update' ? `更新打卡地点 · ${name}` : `${seq === 1 ? '开始' : '结束'}打卡 · ${name}`;
    let note = `提交时使用 ${name} 绑定的机型；打卡即提交商旅并双写本地`;
    let position = '';
    let locTag = '';
    if (first) {
      position = first.position;
      locTag = '同记录已打卡人定位';
      note = `本记录 ${first.name} 已于 ${fmtClockHm(first.time)} 打卡，地点已自动带入；提交时使用 ${name} 绑定的机型`;
    }
    if (action === 'update') {
      // 更新：仅改地点；带入逻辑同上（不带入本人既有地点）
      note = `仅修改打卡地点；${note}`;
    }
    this._ckLocTag = locTag; // 定位回调覆盖位置时清掉带入标注
    this._ckManualPos = false; // 手动输入标记：手输地址后 geo 回填不覆盖（重新定位/选杆塔时清除）
    this.setData({
      ckVisible: true,
      ckTitle: title,
      ckNote: note,
      ckEntryId: entryId,
      ckMemberId: memberId,
      ckMemberName: name,
      ckSeq: seq,
      ckAction: action,
      ckPosition: position,
      // 带入他人打卡时同步带入全套定位信息（坐标+城市编码/城市名），保持地址与坐标城市对应
      ckLng: (first && first.lng) || '',
      ckLat: (first && first.lat) || '',
      ckCityCode: (first && first.cityCode) || '',
      ckCityName: (first && first.cityName) || '',
      ckLocSub: '',
      ckLocating: !(first && first.lng && first.lat),
      ckRemarks: CLOCK_REMARKS_DEFAULT,
      ckSaving: false,
      keyboardHeight: 0,
    });
    if (first && first.lng && first.lat) {
      // 带入全套定位：不取本机坐标；带入缺城市信息（历史同步数据）时按带入坐标逆编码补齐（不覆盖地址串）
      this.setCkLocSub();
      if (!first.cityCode || !first.cityName) this.ckGeo(first.lng, first.lat, locTag, true);
    } else {
      this.ckLocate(!position); // 带入地址缺坐标时本机定位补坐标，逆编码仅补城市、不覆盖位置文案
    }
  },

  // 本机定位：成功拿经纬度；随后必调 /sgcc/geo 逆编码——needGeo=true 时填充完整地址串，
  // 带入他人地址串（needGeo=false）时只补城市编码/城市名、不覆盖带入的位置文案（否则上游报「打卡城市为空」）
  ckLocate(needGeo) {
    wx.getLocation({
      type: 'gcj02',
      success: (loc) => {
        if (!this.data.ckVisible) return; // 弹层已关则不再回填
        const lng = loc.longitude.toFixed(6);
        const lat = loc.latitude.toFixed(6);
        this.setData({ ckLng: lng, ckLat: lat, ckLocating: false });
        this.setCkLocSub();
        this.ckGeo(lng, lat, needGeo ? '当前定位' : this._ckLocTag, !needGeo);
      },
      fail: (err) => {
        console.error('[出工日志] 打卡定位失败（可重新定位或选择杆塔）：', err);
        if (!this.data.ckVisible) return;
        this.setData({ ckLocating: false });
        this.setCkLocSub();
      },
    });
  },

  // 位置卡副行：经纬度 + 来源标注 / 失败提示
  setCkLocSub(tag) {
    const { ckLng, ckLat } = this.data;
    const t = tag !== undefined ? tag : this._ckLocTag;
    let sub = '';
    if (ckLng && ckLat) sub = `${ckLng}, ${ckLat}${t ? ` · ${t}` : ''}`;
    else if (!this.data.ckLocating) sub = '定位失败，可点「重新定位」或「选择杆塔带入坐标」';
    this.setData({ ckLocSub: sub });
  },

  // 高德逆编码（/sgcc/geo）：经纬度 → 完整地址串 + 城市编码；keepPosition=true（带入他人地址串）时只补城市编码/城市名，
  // 不覆盖带入的位置文案；空串=未配置（提交时由服务端兜底，失败 message 直弹）
  ckGeo(lng, lat, tag, keepPosition) {
    request({ url: `/api/v1/sgcc/geo?lng=${lng}&lat=${lat}${this.teamQuery()}`, timeout: 10000 })
      .then((r) => {
        if (!this.data.ckVisible) return;
        if (this.data.ckLng !== lng || this.data.ckLat !== lat) return; // 坐标已变（重新定位/选杆塔），旧响应丢弃
        this._ckLocTag = tag;
        const patch = {
          ckCityCode: (r && r.cityCode) || '',
          ckCityName: (r && r.cityName) || '',
        };
        // keepPosition（带入他人地址串）或用户已手动输入地址时，不覆盖位置文案，只补城市编码/城市名
        if (!keepPosition && !this._ckManualPos) patch.ckPosition = (r && r.position) || '';
        this.setData(patch);
        this.setCkLocSub(tag);
      })
      .catch((err) => console.error('[出工日志] /sgcc/geo 逆编码失败（提交时由服务端兜底）：', err));
  },

  // 「重新定位」：重走定位 + 逆编码（覆盖带入/手输地址）
  onCkRelocate() {
    this._ckLocTag = '当前定位';
    this._ckManualPos = false;
    this.setData({ ckLocating: true, ckPosition: '', ckLocSub: '' });
    this.ckLocate(true);
  },

  // 手动输入地点：置手动标记（geo 回填不覆盖手输文案；城市编码仍按本机坐标逆编码补齐）
  onCkPositionInput(e) {
    this._ckManualPos = true;
    this._ckLocTag = '手动输入';
    this.setData({ ckPosition: e.detail.value });
    this.setCkLocSub();
  },

  // 「选择杆塔带入坐标」：复用杆塔三级级联弹层（_towerFor=ck 时确定回调写入本层；打开逻辑共享）
  onCkOpenTower() {
    this._towerFor = 'ck';
    this.resetTowerState(); // 清空三级选择态（towerRows 坐标缓存保留）
    this.openTowerCascade();
  },

  onCkRemarkInput(e) {
    this.setData({ ckRemarks: e.detail.value });
  },

  onCkCancel() {
    wx.hideKeyboard(); // 关层前收起 hold-keyboard 残留键盘
    this.setData({ ckVisible: false, keyboardHeight: 0 });
  },

  onCkVisibleChange(e) {
    if (!e.detail.visible && this.data.ckVisible) {
      wx.hideKeyboard(); // 遮罩关闭同步收起 hold-keyboard 残留键盘
      this.setData({ ckVisible: false, keyboardHeight: 0 });
    }
  },

  // 确认打卡：POST /sgcc/clockin（mark：seq=1|2；update：更新地点）。失败 message 直弹；
  // 40037 已打过 → 关层刷新（数据以服务端为准）。成功后首次「开始打卡」（含代他人打卡）自动弹该成员费用层
  async onCkConfirm() {
    if (this.data.ckSaving) return;
    wx.hideKeyboard(); // 确认打卡前收起 hold-keyboard 残留键盘
    const { ckEntryId, ckMemberId, ckMemberName, ckSeq, ckAction, ckRemarks } = this.data;
    let { ckPosition, ckLng, ckLat, ckCityCode, ckCityName } = this.data;
    this.setData({ ckSaving: true });
    // 手输地址：先正向解析出坐标与城市信息（position 采用解析后的规范地址串；region 带本机城市缩小范围）
    if (this._ckManualPos && ckPosition.trim()) {
      try {
        const r = await request({
          url: `/api/v1/sgcc/geo?address=${encodeURIComponent(ckPosition.trim())}${ckCityName ? `&region=${encodeURIComponent(ckCityName)}` : ''}${this.teamQuery()}`,
          timeout: 10000,
        });
        if (!r || !r.longitude || !r.latitude) {
          this.setData({ ckSaving: false });
          this.toast('无法识别该地址，请检查输入或改用定位/杆塔');
          return;
        }
        ckPosition = r.position || ckPosition.trim();
        ckLng = r.longitude;
        ckLat = r.latitude;
        ckCityCode = r.cityCode || '';
        ckCityName = r.cityName || '';
      } catch (err) {
        this.setData({ ckSaving: false });
        this.toast(err.message || '地址解析失败，请重试');
        return;
      }
    }
    if (!ckLng || !ckLat) {
      this.setData({ ckSaving: false });
      this.toast('请先完成定位（重新定位或选择杆塔带入坐标）');
      return;
    }
    wx.showLoading({ title: '正在打卡…', mask: true });
    try {
      await request({
        url: '/api/v1/sgcc/clockin',
        method: 'POST',
        data: this.teamBody({
          entry_id: ckEntryId,
          member_id: ckMemberId,
          seq: ckSeq,
          action: ckAction,
          position: ckPosition,
          longitude: ckLng,
          latitude: ckLat,
          cityCode: ckCityCode,
          cityName: ckCityName,
          remarks: ckRemarks || CLOCK_REMARKS_DEFAULT,
        }),
        timeout: 60000,
      });
      wx.hideLoading();
      this.setData({ ckVisible: false, ckSaving: false, keyboardHeight: 0 });
      this.toast('打卡成功');
      this.loadLogs();
      // 首次「开始打卡」成功（40037 已拦截重复，成功即首次）→ 自动打开该成员费用弹层（代他人打卡 = 代填费用）
      if (ckAction === 'mark' && ckSeq === 1) {
        this.openFee(ckMemberId, ckMemberName, ckEntryId);
      }
    } catch (err) {
      wx.hideLoading();
      this.setData({ ckSaving: false });
      this.toast(err.message);
      if (err.code === 40037) {
        this.setData({ ckVisible: false, keyboardHeight: 0 });
        this.loadLogs();
      }
    }
  },

  // ---------- 商旅打卡 · 费用信息弹层（首次开始打卡自动弹 + 打卡区「费用」入口） ----------

  // 打卡区人名行末「费用」入口
  onOpenFee(e) {
    const { memberId, name, entryId } = e.currentTarget.dataset;
    this.openFee(Number(memberId), name, Number(entryId) || 0);
  },

  // 打开并加载：本地费用摘要优先，无值时从商旅模板 id=10 组件解析 {foodFee, arrive}（弹层仅伙食补助/交通费两项可编辑）
  // entryId 定位卡片取当日适用费用标准（feeStd），供「按标准填入」一键预填
  async openFee(memberId, name, entryId) {
    const entry = (this.data.list || []).find((x) => x.id === entryId) || null;
    const std = entry && entry.feeStd && typeof entry.feeStd.foodFee === 'number' ? entry.feeStd : null;
    this.setData({
      feeVisible: true,
      feeMemberId: memberId,
      feeMemberName: name,
      feeLoading: true,
      feeFood: '',
      feeTransit: '',
      feeFoodFocus: false,
      feeTransitFocus: false,
      feeSaving: false,
      keyboardHeight: 0,
      feeStdFood: std ? String(std.foodFee) : '',
      feeStdTransit: std ? String(std.transitFee) : '',
      feeStdText: std
        ? `当日适用标准：伙食${std.foodFee}/交通${std.transitFee}（${std.scope === 1 ? '市外' : (std.scope === 2 ? '驻地' : '市内')}）`
        : '',
    });
    wx.showLoading({ title: '费用信息加载中…', mask: true }); // 单步操作：转圈等待动画
    try {
      const data = await request({
        url: `/api/v1/sgcc/fee?member_id=${memberId}&date=${this.data.dateStr}${this.teamQuery()}`,
        timeout: 30000,
      });
      wx.hideLoading();
      if (!this.data.feeVisible || this.data.feeMemberId !== memberId) return; // 层已关或已换人，丢弃
      const local = (data && data.local) || null;
      const comps = (data && data.clockTemplate && data.clockTemplate.dtComponentList) || [];
      // 补助明细组件（id=10）：value 为 JSON 字符串 {foodFee, arrive}
      let tplFood = '';
      let tplArrive = '';
      const c10 = comps.find((c) => c.id === 10);
      if (c10 && c10.value) {
        try {
          const v = JSON.parse(c10.value);
          tplFood = v.foodFee;
          tplArrive = v.arrive;
        } catch (e) { /* 模板值异常按无值处理 */ }
      }
      const num = (v) => {
        const n = Number(v);
        return Number.isFinite(n) ? String(n) : '';
      };
      this.setData({
        feeLoading: false,
        feeFood: local ? num(local.food_fee) : num(tplFood) || '0',
        feeTransit: local ? num(local.transit_fee) : num(tplArrive) || '0',
        feeAlloc: (data && data.costAlloc) || null, // 成本分配展示（成本中心/编码，只读）
      });
    } catch (err) {
      wx.hideLoading();
      if (!this.data.feeVisible) return;
      this.setData({ feeLoading: false });
      this.toast(err.message);
    }
  },

  onFeeInput(e) {
    const { field } = e.currentTarget.dataset;
    this.setData({ [field]: e.detail.value });
  },

  // 按当日适用标准一键填入伙食/交通（标准随卡片 feeStd 装配，含市外/驻地口径）
  onFeeFillStd() {
    if (!this.data.feeStdText) return;
    this.setData({ feeFood: this.data.feeStdFood, feeTransit: this.data.feeStdTransit });
  },

  // 输入框焦点受控同步（data-field=feeFood/feeTransit → 焦点键 feeFoodFocus/feeTransitFocus）：
  // hold-keyboard 下点页面不收键盘，关层时须把焦点键置 false 才能收起键盘
  onFeeFocus(e) {
    this.setData({ [`${e.currentTarget.dataset.field}Focus`]: true });
  },

  onFeeBlur(e) {
    this.setData({ [`${e.currentTarget.dataset.field}Focus`]: false });
  },

  // 关层统一先失焦收键盘（iOS type=digit 数字键盘无完成键，否则键盘滞留无法关闭）
  closeFee(extra) {
    this.setData({
      feeVisible: false,
      keyboardHeight: 0,
      feeFoodFocus: false,
      feeTransitFocus: false,
      ...extra,
    });
  },

  onFeeCancel() {
    this.closeFee();
  },

  onFeeVisibleChange(e) {
    if (!e.detail.visible && this.data.feeVisible) this.closeFee();
  },

  // 保存费用信息：POST /sgcc/fee（本层仅伙食补助/交通费两项可编辑）
  async onFeeSave() {
    if (this.data.feeSaving) return;
    this.setData({ feeSaving: true });
    wx.showLoading({ title: '正在保存费用…', mask: true });
    try {
      await request({
        url: '/api/v1/sgcc/fee',
        method: 'POST',
        data: this.teamBody({
          member_id: this.data.feeMemberId,
          date: this.data.dateStr,
          foodFee: Number(this.data.feeFood) || 0,
          transitFee: Number(this.data.feeTransit) || 0,
        }),
        timeout: 60000,
      });
      wx.hideLoading();
      this.closeFee({ feeSaving: false });
      this.toast('保存成功');
    } catch (err) {
      wx.hideLoading();
      this.setData({ feeSaving: false });
      this.toast(err.message);
    }
  },

  // ---------- 商旅打卡 · 照片同步失败重试 ----------

  // 重试同步：任务化异步执行（后端登记后立即返回 opId，商旅重传在后台完成），
  // 本卡挂进度条（退出页面不影响完成），结果由 pollCardOp 收尾
  async onResyncPhoto(e) {
    const { pid } = e.currentTarget.dataset;
    try {
      const data = await request({ url: `/api/v1/sgcc/photos/${pid}/resync`, method: 'POST', data: this.teamBody({}), timeout: 120000 });
      const entry = (this.data.list || []).find((x) => (x.photos || []).some((p) => String(p.id) === String(pid)));
      if (entry && data && data.opId) {
        this.toast('已发起重新同步');
        this.startCardOp(entry.id, data.opId, 'resync');
        return;
      }
      this.loadLogs(); // 兜底：未拿到 opId 时按原口径刷新
    } catch (err) {
      this.toast(err.message);
    }
  },

  // ---------- 商旅打卡 · 从商旅同步（卡片操作区入口，替代原悬浮钮「同步核查」） ----------

  // 轮询同步任务进度（1.5s 间隔；任务不存在 / 过期按完成收尾；连续网络失败超 20 次放弃轮询，数据随下次刷新呈现）。
  // onTick(pct) 逐次回调，正常完成时先补 100 再 resolve；resolve 值 = 任务终态（含 failed/failMsg 供收尾区分成败，放弃轮询为 null）
  pollSyncJob(jobId, onTick) {
    return new Promise((resolve) => {
      let fails = 0;
      const tick = async () => {
        let p = null;
        try {
          p = await request({ url: `/api/v1/sgcc/sync/progress?job_id=${encodeURIComponent(jobId)}${this.teamQuery()}` });
        } catch (err) { /* 网络抖动：计数后继续 */ }
        if (!p) {
          fails += 1;
          if (fails <= 20) {
            this._jobTimers[jobId] = setTimeout(tick, 1500);
            return;
          }
          delete this._jobTimers[jobId];
          resolve(null); // 放弃轮询按完成收尾（不补 100，保留当前进度字样直至遮罩关闭）
          return;
        }
        fails = 0;
        if (!p.finished) {
          onTick(p.total ? Math.min(99, Math.round((p.done / p.total) * 100)) : 0);
          this._jobTimers[jobId] = setTimeout(tick, 1500);
          return;
        }
        delete this._jobTimers[jobId];
        onTick(100);
        resolve(p);
      };
      tick();
    });
  },

  // 卡片级同步进度：登记任务 → 卡片挂进度条（三窗格与邻日缓存同步补丁，切日 / 刷新不丢）→ 完成后刷新本日列表
  async startCardSync(entryId, jobId, date) {
    this._syncJobs[entryId] = { jobId, pct: 0 };
    this.patchCardSync(entryId, 0, true);
    const fin = await this.pollSyncJob(jobId, (pct) => {
      if (this._syncJobs[entryId]) this._syncJobs[entryId].pct = pct;
      this.patchCardSync(entryId, pct, true);
    });
    delete this._syncJobs[entryId];
    this.patchCardSync(entryId, 0, false); // 清标志（缓存内旧对象一并复位，完成态由 loadLogs 重拉呈现）
    if (date) delete this._dayLists[this.dayKey(date)]; // 该日缓存已过时（切日窗格重建时强制重拉）
    // 任务终态区分 全部成功 / 部分失败 / 整体失败（failed ≥ total；failMsg 为服务端友好口径；fin=null 为放弃轮询按原口径）
    const failed = fin && Number(fin.failed) > 0 ? Number(fin.failed) : 0;
    if (failed && failed >= Number(fin.total || 0)) this.toast(`本卡同步失败：${fin.failMsg || '请稍后重试'}`);
    else if (failed) this.toast(`本卡已同步，${failed} 人失败（详见核查记录）`);
    else this.toast('本卡已从商旅同步');
    this.loadLogs();
  },

  // 卡片进度条补丁：镜像 list + 切日窗格三格 + 邻日预拉缓存内的同 id 卡片一并改（on=true 挂条 / false 摘除）
  patchCardSync(entryId, pct, on, text) {
    const update = {};
    const applyTo = (list, path) => {
      (list || []).forEach((c, ci) => {
        if (c.id !== entryId) return;
        update[`${path}[${ci}].syncing`] = on;
        update[`${path}[${ci}].syncPct`] = pct;
        update[`${path}[${ci}].syncText`] = text || '从商旅同步中';
      });
    };
    applyTo(this.data.list, 'list');
    this.data.win.forEach((w, wi) => applyTo(w.list, `win[${wi}].list`));
    Object.keys(this._dayLists || {}).forEach((k) => {
      (this._dayLists[k] || []).forEach((c) => {
        if (c.id === entryId) {
          c.syncing = on;
          c.syncPct = pct;
          c.syncText = text || '从商旅同步中';
        }
      });
    });
    if (Object.keys(update).length) this.setData(update);
  },

  // 「从商旅同步」：仅拉取本卡用车人的当日数据（后端异步执行，不一致以商旅为准覆盖），
  // 同步完成前本卡呈现进度条（进度轮询 /sgcc/sync/progress；旧服务端无 jobId 时回退 8 秒静默刷新）
  async onSyncPull(e) {
    const { date, id } = e.currentTarget.dataset;
    const entryId = Number(id) || 0;
    if (this._syncJobs[entryId]) return; // 本卡同步中，防连点
    try {
      const data = await request({ url: '/api/v1/sgcc/sync/pull', method: 'POST', data: this.teamBody({ date, entry_id: entryId || undefined }) });
      this.toast('已发起从商旅同步（仅本卡用车人）');
      if (data && data.jobId) {
        this.startCardSync(entryId, data.jobId, date);
        return;
      }
      if (this._pullTimer) clearTimeout(this._pullTimer);
      this._pullTimer = setTimeout(() => this.loadLogs(), 8000);
    } catch (err) {
      this.toast(err.message);
    }
  },

  // ---------- 照片单操作任务（上传/删除/改人名/resync）：后端任务化异步执行，退出页面不影响完成；
  // 卡片挂进度条（样式同从商旅同步卡片锁），发起者轮询 /op/status 收尾，旁观者经 /sync/active 的 ops 挂条 ----------

  // 发起者入口：登记任务 → 本卡挂进度条 → 1.5s 轮询直至终态
  startCardOp(entryId, opId, kind, extra) {
    this._photoOps[entryId] = { opId, kind, pct: 0, ...(extra || {}) };
    this.patchCardSync(entryId, 0, true, PHOTO_OP_TEXT[kind]);
    this.pollCardOp(entryId);
  },

  // 发起者轮询：/op/status 含进度/终态/错误/产物（photoUrl）；ok 按 kind toast（水印上传另存相册），fail 完整报错
  async pollCardOp(entryId) {
    const op = this._photoOps[entryId];
    if (!op || op.spectator) return;
    let fails = 0;
    while (!this._unloaded && this._photoOps[entryId] && !this._photoOps[entryId].spectator) {
      let st = null;
      try {
        st = await request({ url: `/api/v1/sgcc/op/status?op_id=${op.opId}${this.teamQuery('&')}` });
      } catch (err) { fails += 1; }
      if (this._unloaded) return; // 在途请求落地时页面已卸载：不再 setData
      if (st && st.status === 'running') {
        fails = 0;
        const pct = st.total ? Math.min(99, Math.round((st.done / st.total) * 100)) : 0;
        this._photoOps[entryId].pct = pct;
        this.patchCardSync(entryId, pct, true, PHOTO_OP_TEXT[op.kind]);
      } else if (st) {
        // 终态（任务不存在/过期按 ok 回，同走完成收尾）
        delete this._photoOps[entryId];
        this.patchCardSync(entryId, 0, false);
        if (st.status === 'fail') {
          wx.showModal({
            title: '商旅同步失败',
            content: `${st.failedName ? `成员「${st.failedName}」` : ''}${st.error || '商旅同步失败'}，请重试`,
            showCancel: false,
            confirmText: '知道了',
          });
        } else {
          let tip = PHOTO_OP_OK_TEXT[op.kind] || '已完成';
          if (op.kind === 'upload') {
            if (op.wm && st.photoUrl) tip = await this.saveWmPhotoToAlbum(st.photoUrl); // 失败不阻塞（上传已成功）
            else if (op.plain) tip = '已上传（非水印存档，不参与验证）';
          }
          this.toast(tip);
        }
        this.loadLogs();
        return;
      } else if (fails > 20) {
        // 连续网络失败兜底：摘条刷新（任务仍在后端执行，结果随刷新/核查呈现）
        delete this._photoOps[entryId];
        this.patchCardSync(entryId, 0, false);
        this.loadLogs();
        return;
      }
      await new Promise((r) => setTimeout(r, 1500));
    }
  },

  // 旁观者挂条：/sync/active 的 ops（本班组进行中的照片任务，他人/他端发起或本端重新进入）→ 对应记录卡挂进度条；
  // spectator 项在 ops 中消失（完成/失败）时摘条刷新。发起者记录（非 spectator）由 /op/status 轮询收口，不在此动
  syncSpectatorOps(ops) {
    const seen = {};
    (ops || []).forEach((o) => {
      seen[o.entryId] = o;
      const cur = this._photoOps[o.entryId];
      if (cur && !cur.spectator) return;
      const pct = o.total ? Math.min(99, Math.round((o.done / o.total) * 100)) : 0;
      this._photoOps[o.entryId] = { opId: o.opId, kind: o.kind, pct, spectator: true };
      this.patchCardSync(o.entryId, pct, true, PHOTO_OP_TEXT[o.kind]);
    });
    Object.keys(this._photoOps).forEach((id) => {
      const op = this._photoOps[id];
      if (!op.spectator || seen[id]) return;
      delete this._photoOps[id];
      this.patchCardSync(Number(id), 0, false);
      this.loadLogs();
    });
  },

  // 旁观者轮询：ops 非空期间 1.5s 轮询 /sync/active 驱动卡片进度，ops 空即停
  async pollSpectatorOps() {
    if (this._opPolling) return;
    this._opPolling = true;
    let fails = 0;
    for (;;) {
      if (this._unloaded) break; // 页面已卸载：退出轮询
      let p = null;
      try {
        p = await request({ url: `/api/v1/sgcc/sync/active${this.teamQuery('?')}` });
      } catch (err) { fails += 1; }
      if (this._unloaded) break; // 在途请求落地时页面已卸载：不再挂条/setData
      if (p) {
        fails = 0;
        this.syncSpectatorOps(p.ops || []);
        if (!(p.ops || []).length) break; // 全部完成
      } else if (fails > 20) {
        break; // 连续网络失败兜底
      }
      await new Promise((r) => setTimeout(r, 1500));
    }
    this._opPolling = false;
  },

  // ---------- 新建/改派车表单底部弹层 ----------

  // meta：车牌/目的地/成员（下拉与点亮数据源），passGate 后加载
  async loadMeta() {
    try {
      const data = await request({ url: `/api/v1/worklog/meta${this.teamQuery('?')}` });
      this._vehicles = (data && data.vehicles) || [];
      this._destinations = (data && data.destinations) || [];
      // 成员字典另存 _members：cross 模式表单会改用跨班成员覆盖 data.members，普通模式以此为源重建
      this._members = ((data && data.members) || []).map((m) => ({ ...m }));
      this.setData({ members: this._members.map((m) => ({ ...m, checked: false })) });
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

  // 跨班日志字典（仅超管；全部启用班组车牌/目的地/成员按名称去重，teams 为来源班组名数组）：
  // 首次进入 cross 模式拉取并缓存（跨班保存后失效重拉——车牌/目的地可能已在归属班字典自动补建）
  async ensureCrossMeta() {
    if (this._crossMeta) return this._crossMeta;
    const data = await request({ url: '/api/v1/worklog/logs/cross/meta' });
    this._crossMeta = {
      teams: (data && data.teams) || [],
      vehicles: (data && data.vehicles) || [],
      destinations: (data && data.destinations) || [],
      members: (data && data.members) || [],
    };
    return this._crossMeta;
  },

  // 「＋ 新建日志」：不再跳页，打开表单底部弹层（默认未出车）
  onCreate() {
    this.setData({ fabOpen: false, formCross: false });
    this.openForm(0);
  },

  // 「跨班日志」（仅超管）：打开 cross 模式表单（归属班组可选，车牌/目的地/人员按名称选择）
  onCreateCross() {
    this.setData({ fabOpen: false, formCross: true });
    this.openForm(0);
  },

  // ---------- 右下悬浮主钮（speed dial 展开/收起） ----------
  onFabToggle() {
    this.setData({ fabOpen: !this.data.fabOpen });
  },

  // 卡片车牌头部：打开改派车面板并回填该卡数据（派车情况/用车人；巡视内容不在此修改）
  // 跨班卡仅超管可改派车（非派车字段如备注/巡视内容不受限）；超管打开进入 cross 编辑模式
  onOpenForm(e) {
    const id = Number(e.currentTarget.dataset.id) || 0;
    const entry = this.data.list.find((x) => x.id === id);
    if (!entry) {
      this.toast('日志不存在或已被删除');
      return;
    }
    if (entry.crossTeam && !this.data.isAdmin) {
      this.toast('跨班日志仅超级管理员可修改派车');
      return;
    }
    this.setData({ formCross: !!entry.crossTeam });
    this.openForm(id);
  },

  async openForm(id) {
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
    // 同日期同人唯一（后端 40020 配套）：当日其他卡片已占用的人名置灰不可点亮（编辑时排除本卡）；
    // 普通模式按成员 id 比对，cross 模式无字典 id 改按人名集合比对
    const usedMids = new Set();
    const usedNames = new Set();
    this.data.list.forEach((x) => {
      if (id && x.id === id) return;
      (x.memberIds || []).forEach((mid) => usedMids.add(mid));
      (x.checks || []).forEach((c) => usedNames.add(c.name));
    });
    // cross 模式：数据源为全启用班组合并字典（按名称选择），先拉字典再开面板
    if (this.data.formCross) {
      let meta;
      try {
        meta = await this.ensureCrossMeta();
      } catch (err) {
        this.toast(err.message);
        return;
      }
      const entry = id ? this.data.list.find((x) => x.id === id) : null;
      if (id && !entry) {
        this.toast('日志不存在或已被删除');
        return;
      }
      // 归属班组：编辑回填卡片归属班；新建默认当前生效班组（_teamId 为 0 取第一个）
      const teamHit = meta.teams.find((t) => entry && t.id === entry.teamId)
        || meta.teams.find((t) => t.id === this._teamId)
        || meta.teams[0] || null;
      this.setData({
        ...base,
        patrol: entry ? entry.patrol : '', // 面板不展示；保存时原样带上（PUT 全量替换）
        crossTeamId: teamHit ? teamHit.id : 0,
        crossTeamText: teamHit ? teamHit.name : '',
        vehicleId: entry && entry.hasVehicle ? 1 : -1, // cross 车牌按名称选择，id 仅占位（0=未选 / -1=未出车 / 1=已选）
        vehicleText: entry && entry.hasVehicle ? entry.plateText : '未出车',
        isNoVehicle: !(entry && entry.hasVehicle),
        destId: entry && entry.hasVehicle && entry.destText ? 1 : 0, // cross 目的地同按名称
        destText: entry && entry.hasVehicle ? entry.destText : '',
        orderNo: entry ? entry.orderNo || '' : '',
        members: meta.members.map((m) => ({
          id: m.name, // cross 成员按名称点亮（t-check-tag wx:key 沿用 id 字段）
          name: m.name,
          sub: (m.teams || []).join('/'), // 来源班组小字（同名多班时可辨识）
          checked: entry ? entry.checks.some((c) => c.name === m.name) : false,
          disabled: usedNames.has(m.name),
        })),
        memberUsedTip: usedNames.size > 0,
      });
      return;
    }
    if (!id) {
      // 新建：默认未出车，人员全部未点亮（成员源取 _members 字典缓存，不受 cross 模式覆盖影响）
      this.setData({
        ...base,
        patrol: '',
        vehicleId: -1,
        vehicleText: '未出车',
        destId: 0,
        destText: '',
        orderNo: '',
        isNoVehicle: true,
        members: (this._members || []).map((m) => ({ ...m, checked: false, disabled: usedMids.has(m.id) })),
        memberUsedTip: usedMids.size > 0,
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
      orderNo: entry.orderNo || '',
      members: (this._members || []).map((m) => ({ ...m, checked: entry.memberIds.includes(m.id), disabled: usedMids.has(m.id) })),
      memberUsedTip: usedMids.size > 0,
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

  // 归属班组下拉（仅 cross 模式）
  onToggleTeamDrop() {
    this.toggleDrop('team');
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
    // cross 模式：车牌/目的地按名称选择（id 字段即名称），班组按 id；数据源为 _crossMeta
    if (this.data.formCross) {
      const meta = this._crossMeta || { teams: [], vehicles: [], destinations: [] };
      if (dropType === 'team') {
        list = meta.teams
          .filter((t) => !kw || t.name.includes(kw))
          .map((t) => ({ id: t.id, name: t.name, selected: t.id === this.data.crossTeamId }));
      } else if (dropType === 'vehicle') {
        list = meta.vehicles
          .filter((v) => !kw || v.name.includes(kw))
          .map((v) => ({ id: v.name, name: v.name, selected: !this.data.isNoVehicle && v.name === this.data.vehicleText }));
        if (!kw || '未出车'.includes(kw)) {
          list.push({ id: '', name: '未出车', none: true, selected: this.data.isNoVehicle });
        }
      } else {
        list = meta.destinations
          .filter((d) => !kw || d.name.includes(kw))
          .map((d) => ({ id: d.name, name: d.name, selected: d.name === this.data.destText }));
      }
      this.setData({ dropList: list });
      return;
    }
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
    // cross 模式：班组按 id，车牌/目的地按名称（dataset.id 即名称；「未出车」为空串）
    if (this.data.formCross) {
      const raw = e.currentTarget.dataset.id;
      if (this.data.dropType === 'team') {
        const tid = Number(raw);
        const hit = ((this._crossMeta && this._crossMeta.teams) || []).find((t) => t.id === tid);
        this.setData({ crossTeamId: tid, crossTeamText: hit ? hit.name : '', dropType: '' });
        return;
      }
      if (this.data.dropType === 'vehicle') {
        // 选中「未出车」：清空目的地与人员选择，人员段联动隐藏（与普通模式一致）
        const isNoVehicle = raw === '';
        const patch = {
          vehicleId: isNoVehicle ? -1 : 1, // id 仅占位（非 0 即已选），名称存 vehicleText
          vehicleText: isNoVehicle ? '未出车' : raw,
          isNoVehicle,
          dropType: '',
        };
        if (isNoVehicle) {
          patch.destId = 0;
          patch.destText = '';
          patch.members = this.data.members.map((m) => ({ ...m, checked: false }));
        }
        this.setData(patch);
        return;
      }
      this.setData({ destId: 1, destText: raw, dropType: '' }); // cross 目的地同按名称
      return;
    }
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
    if (this.data.members[index] && this.data.members[index].disabled) return; // 当日已在其他卡片，不可点亮
    this.setData({ [`members[${index}].checked`]: e.detail.checked });
  },

  // 派车单号输入（表单内单行文本，data-field 直写 data）
  onFormInput(e) {
    this.setData({ [e.currentTarget.dataset.field]: e.detail.value });
  },

  // ---------- 保存（无实时保存：仅「保 存」按钮提交，遮罩关闭 = 放弃修改） ----------

  buildPayload() {
    const { formDateStr, patrol, vehicleId, destId, orderNo, members } = this.data;
    return {
      log_date: formDateStr,
      patrol_content: patrol,
      vehicle_id: vehicleId > 0 ? vehicleId : null,
      destination_id: vehicleId > 0 && destId > 0 ? destId : null,
      dispatch_order_no: vehicleId > 0 ? (orderNo || '').trim() : '', // 派车单号（未出车传空，服务端强制置空）
      member_ids: vehicleId > 0 ? members.filter((m) => m.checked).map((m) => m.id) : [],
    };
  },

  // cross 模式提交体：车牌/目的地/成员均按名称（body 的 team_id 为归属班组；未出车不带目的地与人名）
  buildCrossPayload() {
    const { formDateStr, patrol, crossTeamId, vehicleText, destText, orderNo, members, isNoVehicle } = this.data;
    return {
      log_date: formDateStr,
      team_id: crossTeamId,
      plate_no: isNoVehicle ? '' : (vehicleText || ''),
      destination: isNoVehicle ? '' : (destText || ''),
      member_names: isNoVehicle ? [] : members.filter((m) => m.checked).map((m) => m.name),
      patrol_content: patrol,
      dispatch_order_no: isNoVehicle ? '' : (orderNo || '').trim(),
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
    // cross 模式：走 /logs/cross 接口（body 的 team_id 是归属班组，不可叠加 teamBody 的生效班组，直接发原始 data）
    if (this.data.formCross) {
      if (!this.data.crossTeamId) {
        this.toast('请选择归属班组');
        return;
      }
      this.setData({ formSaving: true });
      try {
        const body = this.buildCrossPayload();
        if (this.data.formId) {
          delete body.log_date; // PUT 不改日期
          await request({ url: `/api/v1/worklog/logs/cross/${this.data.formId}`, method: 'PUT', data: body });
        } else {
          await request({ url: '/api/v1/worklog/logs/cross', method: 'POST', data: body });
        }
        this._crossMeta = null; // 车牌/目的地可能已在归属班字典自动补建，下次进入重拉
        this.setData({ formVisible: false, formSaving: false, dropType: '', keyboardHeight: 0 });
        this.loadLogs();
      } catch (err) {
        this.setData({ formSaving: false });
        this.toast(err.message);
      }
      return;
    }
    this.setData({ formSaving: true });
    try {
      if (this.data.formId) {
        await request({ url: `/api/v1/worklog/logs/${this.data.formId}`, method: 'PUT', data: this.teamBody(this.buildPayload()) });
      } else {
        await request({ url: '/api/v1/worklog/logs', method: 'POST', data: this.teamBody(this.buildPayload()) });
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
    wx.hideKeyboard(); // 关层前收起 hold-keyboard 残留键盘
    this.setData({ patrolVisible: false, keyboardHeight: 0 });
  },

  onPatrolVisibleChange(e) {
    if (!e.detail.visible && this.data.patrolVisible) {
      wx.hideKeyboard(); // 遮罩关闭同步收起 hold-keyboard 残留键盘
      this.setData({ patrolVisible: false, keyboardHeight: 0 });
    }
  },

  // 保存巡视内容：PUT 全量字段（车牌/目的地/用车人取保存时卡片最新值，仅替换巡视内容），失败留弹层可重试
  async onPatrolSave() {
    if (this.data.patrolSaving) return;
    wx.hideKeyboard(); // 保存前收起 hold-keyboard 残留键盘
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
        data: this.teamBody({
          patrol_content: this.data.patrolDraft,
          vehicle_id: entry.vehicleId > 0 ? entry.vehicleId : null,
          destination_id: entry.vehicleId > 0 && entry.destId > 0 ? entry.destId : null,
          member_ids: entry.vehicleId > 0 ? entry.memberIds : [],
        }),
      });
      this.setData({ patrolVisible: false, patrolSaving: false, keyboardHeight: 0 });
      this.loadLogs();
    } catch (err) {
      this.setData({ patrolSaving: false });
      this.toast(err.message);
    }
  },

  // ---------- 备注编辑弹层（文字 + 附件；「保 存」时新附件先传 COS 再 PUT 全量字段） ----------

  // 打开：点卡片「备 注」按钮或备注块，回填当前备注与附件（已传附件带 cos_key，保存时原样回传）
  onOpenRemark(e) {
    const id = Number(e.currentTarget.dataset.id) || 0;
    const entry = this.data.list.find((x) => x.id === id);
    if (!entry) {
      this.toast('日志不存在或已被删除');
      return;
    }
    const files = (entry.remarkFiles || []).map((f) => ({
      ...f, key: f.cos_key, preview: f.url, isNew: false,
    }));
    this.setData({
      rmkVisible: true,
      rmkEntryId: id,
      rmkDraft: entry.remark || '',
      rmkFiles: files,
      rmkSaving: false,
      keyboardHeight: 0,
    });
    this.deriveRmkLists(files);
  },

  // 派生展示列表：图片/视频进缩略图区，doc 进文件条区
  deriveRmkLists(files) {
    this.setData({
      rmkMedia: files.filter((f) => f.type === 'image' || f.type === 'video'),
      rmkDocs: files.filter((f) => f.type === 'doc'),
    });
  },

  onRmkInput(e) {
    this.setData({ rmkDraft: e.detail.value });
  },

  onRmkCancel() {
    wx.hideKeyboard(); // 关层前收起 hold-keyboard 残留键盘
    this.setData({ rmkVisible: false, keyboardHeight: 0 });
  },

  onRmkVisibleChange(e) {
    if (!e.detail.visible && this.data.rmkVisible) {
      wx.hideKeyboard(); // 遮罩关闭同步收起 hold-keyboard 残留键盘
      this.setData({ rmkVisible: false, keyboardHeight: 0 });
    }
  },

  // 添加图片/视频附件（wx.chooseMedia，相册/拍摄均可；超过 50MB 直接剔除并提示）
  onRmkAddMedia() {
    const left = 9 - this.data.rmkFiles.length;
    if (left <= 0) {
      this.toast('附件最多 9 个');
      return;
    }
    wx.chooseMedia({
      count: left,
      mediaType: ['image', 'video'],
      sourceType: ['album', 'camera'],
      success: (res) => {
        const now = new Date();
        const stamp = `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}`
          + `-${pad(now.getHours())}${pad(now.getMinutes())}${pad(now.getSeconds())}`;
        const files = this.data.rmkFiles.slice();
        (res.tempFiles || []).forEach((t, i) => {
          if (t.size > 50 * 1024 * 1024) {
            this.toast('单个附件不超过 50MB');
            return;
          }
          const type = t.fileType === 'video' ? 'video' : 'image';
          const ext = (t.tempFilePath.split('.').pop() || (type === 'video' ? 'mp4' : 'jpg')).toLowerCase();
          files.push({
            type,
            name: `${type === 'video' ? '视频' : '图片'}-${stamp}${i ? `-${i}` : ''}.${ext}`,
            size: t.size,
            tempFilePath: t.tempFilePath,
            key: t.tempFilePath,
            preview: t.tempFilePath,
            isNew: true,
          });
        });
        this.setData({ rmkFiles: files.slice(0, 9) });
        this.deriveRmkLists(this.data.rmkFiles);
      },
    });
  },

  // 添加 Office 文档附件（wx.chooseMessageFile 从聊天文件选择；服务端按扩展名白名单复核）
  onRmkAddDoc() {
    const left = 9 - this.data.rmkFiles.length;
    if (left <= 0) {
      this.toast('附件最多 9 个');
      return;
    }
    const DOC_EXTS = ['doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'pdf'];
    wx.chooseMessageFile({
      count: left,
      type: 'file',
      extension: DOC_EXTS,
      success: (res) => {
        const files = this.data.rmkFiles.slice();
        (res.tempFiles || []).forEach((t) => {
          const ext = (t.name.split('.').pop() || '').toLowerCase();
          if (!DOC_EXTS.includes(ext)) {
            this.toast(`不支持的文件类型：${t.name}`);
            return;
          }
          if (t.size > 50 * 1024 * 1024) {
            this.toast('单个附件不超过 50MB');
            return;
          }
          files.push({ type: 'doc', name: t.name, size: t.size, tempFilePath: t.path, key: t.path, isNew: true });
        });
        this.setData({ rmkFiles: files.slice(0, 9) });
        this.deriveRmkLists(this.data.rmkFiles);
      },
    });
  },

  // 移除附件（仅改本地清单；已传 COS 的旧附件在保存时随全量清单差集由服务端删除）
  onRmkFileDel(e) {
    const { key } = e.currentTarget.dataset;
    const files = this.data.rmkFiles.filter((f) => f.key !== key);
    this.setData({ rmkFiles: files });
    this.deriveRmkLists(files);
  },

  // 保存：新附件逐个 wx.uploadFile 传 COS → PUT 全量字段（派车/目的地/用车人取卡片最新值）+ 备注
  async onRmkSave() {
    if (this.data.rmkSaving) return;
    wx.hideKeyboard(); // 保存前收起 hold-keyboard 残留键盘
    const entry = this.data.list.find((x) => x.id === this.data.rmkEntryId);
    if (!entry) {
      this.toast('日志不存在或已被删除');
      this.setData({ rmkVisible: false, keyboardHeight: 0 });
      return;
    }
    this.setData({ rmkSaving: true });
    try {
      const metas = [];
      for (const f of this.data.rmkFiles) {
        if (f.isNew) {
          // 逐个上传，失败整体中止留弹层可重试
          // eslint-disable-next-line no-await-in-loop
          metas.push(await this.uploadRemarkFile(entry.id, f));
        } else {
          metas.push({ name: f.name, url: f.url, cos_key: f.cos_key, type: f.type, size: f.size });
        }
      }
      await request({
        url: `/api/v1/worklog/logs/${entry.id}`,
        method: 'PUT',
        data: this.teamBody({
          patrol_content: entry.patrol,
          vehicle_id: entry.vehicleId > 0 ? entry.vehicleId : null,
          destination_id: entry.vehicleId > 0 && entry.destId > 0 ? entry.destId : null,
          member_ids: entry.vehicleId > 0 ? entry.memberIds : [],
          remark: this.data.rmkDraft.trim(),
          remark_files: metas,
        }),
      });
      this.setData({ rmkVisible: false, rmkSaving: false, keyboardHeight: 0 });
      this.toast('备注已保存');
      this.loadLogs();
    } catch (err) {
      this.setData({ rmkSaving: false });
      this.toast(err.message);
    }
  },

  // 上传单个备注附件（multipart 与网页端同接口；返回入库用文件元数据）
  uploadRemarkFile(entryId, f) {
    const token = wx.getStorageSync('token');
    return new Promise((resolve, reject) => {
      wx.uploadFile({
        url: `${BASE_URL}/api/v1/worklog/logs/${entryId}/remark-files`,
        filePath: f.tempFilePath,
        name: 'file',
        header: token ? { Authorization: `Bearer ${token}` } : {},
        formData: this._teamId ? { name: f.name, team_id: String(this._teamId) } : { name: f.name },
        success: (res) => {
          let body = {};
          try {
            body = JSON.parse(res.data || '{}');
          } catch (e) {
            // 非 JSON 响应按失败处理
          }
          if (res.statusCode >= 200 && res.statusCode < 300 && body.code === 0) {
            resolve(body.data);
            return;
          }
          reject(new Error(body.message || `附件上传失败（${res.statusCode}）`));
        },
        fail: () => reject(new Error('网络异常，请检查网络后重试')),
      });
    });
  },

  // 卡片备注附件：图片/视频 wx.previewMedia 全屏（视频直接播放）
  onRemarkMedia(e) {
    const { entryId, url } = e.currentTarget.dataset;
    const entry = this.data.list.find((x) => x.id === entryId);
    if (!entry) return;
    const sources = entry.remarkMedia.map((f) => ({ url: f.url, type: f.type }));
    const current = entry.remarkMedia.findIndex((f) => f.url === url);
    wx.previewMedia({ sources, current: Math.max(current, 0) });
  },

  // 卡片备注附件：Office 文档下载后 wx.openDocument 打开（showMenu 显示右上角菜单）
  onRemarkDoc(e) {
    const { url, name } = e.currentTarget.dataset;
    const ext = (String(name).split('.').pop() || '').toLowerCase();
    wx.showLoading({ title: '正在打开…', mask: true });
    // 注意：COS 域名需配置为小程序 downloadFile 合法域名（与水印照片下载同域，部署侧事项）
    wx.downloadFile({
      url,
      // 指定本地存储文件名，否则 openDocument 打开后显示的是随机临时文件名（乱码）
      filePath: `${wx.env.USER_DATA_PATH}/${name}`,
      success: (r) => {
        if (r.statusCode !== 200) {
          wx.hideLoading();
          this.toast('文件下载失败');
          return;
        }
        wx.openDocument({
          filePath: r.filePath,
          fileType: ext,
          showMenu: true,
          complete: () => wx.hideLoading(),
        });
      },
      fail: () => {
        wx.hideLoading();
        this.toast('文件下载失败');
      },
    });
  },

  // ---------- 水印照片（卡片直接改） ----------

  // 已被占用人名（每人限一张；excludePid 为当前正在修改的照片；非水印照片不占名额，跳过统计）
  usedPhotoNames(entry, excludePid) {
    const used = new Set();
    ((entry && entry.photos) || []).forEach((p) => {
      if (p.isPlain) return;
      if (excludePid && p.id === excludePid) return;
      (p.members || []).forEach((n) => used.add(n));
    });
    return used;
  },

  // 添加照片：先弹三选（①选择水印照片上传 ②拍摄/选择照片并添加水印 ③非水印照片直传），再进人名点亮层
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

  // 新增第三项「非水印照片」：相册选原图直传，仅存档并同步商旅费用照片（plain:true，不验证、不占每人限一张）
  onAddSheetPlain() {
    this.setData({ addSheetVisible: false });
    this.openMemberPicker(this._pendingPhotoEntryId, 'plain');
  },

  // 人名点亮层候选构建（三方式共用）：已上传置灰仅对水印方式（raw/wm，每人限一张）；非水印（plain）不限张数不受此限；
  // 商旅未绑定（标「未绑定」）/ 登录过期（标「登录过期」）一律置灰锁定——人名状态不可更改（无论增删），避免产生无法联动的商旅操作
  buildCandidates(entry, action, excludePid, checkedNames) {
    const used = this.usedPhotoNames(entry, excludePid);
    const checked = checkedNames || [];
    const offNames = [];
    const candidates = entry.checks.map((m) => {
      const off = !m.sgccBound ? '未绑定' : m.sgccTokenStatus === 0 ? '登录过期' : '';
      if (off) offNames.push(`${m.name}（${off}）`);
      const usedUp = action !== 'plain' && used.has(m.name);
      const disabled = usedUp || !!off;
      return { name: m.name, checked: checked.includes(m.name), disabled, note: usedUp ? '已上传' : off };
    });
    return {
      candidates,
      memberOffTip: offNames.length
        ? `${offNames.join('、')} 已置灰锁定，人名状态不可更改；登录过期者本人在「我的 → 绑定商旅」重新登录后恢复`
        : '',
    };
  },

  // 人名点亮层（候选 = 本卡用车人）；action: raw=水印直传 / wm=加水印上传 / plain=非水印直传
  openMemberPicker(entryId, action) {
    const entry = this.data.list.find((x) => x.id === entryId);
    if (!entry) return;
    const { candidates, memberOffTip } = this.buildCandidates(entry, action, 0, []);
    this.setData({
      memberVisible: true,
      memberMode: 'add',
      memberAction: action,
      memberPhotoId: 0,
      memberEntryId: entryId,
      candidates,
      memberOffTip,
      memberNote: action === 'plain'
        ? '点亮即本张照片所属人名（可多选，不限张数）；仅上传存档并同步所选人当日商旅费用照片，不参与卡片验证'
        : '点亮即本张水印照片中包含的人名（可多选）；照片同步进所选人当日商旅费用照片',
    });
  },

  // 修改已有照片人名：复用弹层（当前人名保持点亮；被其他照片占用者、未绑定/登录过期者均置灰锁定不可更改）
  onPhotoMembers(e) {
    const { entryId, pid, names } = e.currentTarget.dataset;
    const entry = this.data.list.find((x) => x.id === entryId);
    if (!entry) return;
    const { candidates, memberOffTip } = this.buildCandidates(entry, 'raw', pid, names || []);
    this.setData({
      memberVisible: true,
      memberMode: 'edit',
      memberPhotoId: pid,
      memberEntryId: entryId,
      candidates,
      memberOffTip,
      memberNote: '点亮即本张照片中包含的人名（可多选）；新增人名将上传其商旅费用照片，剔除人名将从其商旅费用照片中删除',
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

  // 弹层确认：edit=提交人名修改；add=关闭后进相册选片上传。
  // 取 checked 即可：置灰（已上传/未绑定/登录过期）标签不可点，checked 恒为初始态——锁定成员保持在名单内不被误剔
  async onMemberConfirm() {
    const names = this.data.candidates.filter((c) => c.checked).map((c) => c.name);
    if (!names.length) {
      this.toast('请选择照片所属人名');
      return;
    }
    if (this.data.memberMode === 'edit') {
      if (this.data.memberSaving) return; // 防连点
      this.setData({ memberSaving: true });
      // 任务化异步执行（剔除/补传在后台完成，服务端恒返回 opId）：本卡挂进度条，结果由 pollCardOp 收尾
      try {
        const data = await request({
          url: `/api/v1/worklog/photos/${this.data.memberPhotoId}/members`,
          method: 'PUT',
          data: this.teamBody({ members: names }),
          timeout: 120000,
        });
        this.setData({ memberVisible: false, memberSaving: false });
        this.toast('已发起，后台同步商旅中');
        this.startCardOp(this.data.memberEntryId, data.opId, 'members');
      } catch (err) {
        this.setData({ memberSaving: false });
        this.toast(err.message);
      }
      return;
    }
    this.setData({ memberVisible: false });
    if (this.data.memberAction === 'wm') {
      this.setData({ wmSourceType: 'album' });
      this.choosePhotoForWm(names);
    } else if (this.data.memberAction === 'plain') {
      this.chooseAndUpload(names, true); // 非水印照片：相册选图直传（plain:true，免验证、不占每人限一张）
    } else {
      this.chooseAndUpload(names);
    }
  },

  // 人名层「拍摄」：点亮人名后直接调相机（加水印流程；取 checked 口径同 onMemberConfirm）
  onMemberShoot() {
    const names = this.data.candidates.filter((c) => c.checked).map((c) => c.name);
    if (!names.length) {
      this.toast('请选择照片所属人名');
      return;
    }
    this.setData({ memberVisible: false, wmSourceType: 'camera' });
    this.choosePhotoForWm(names);
  },

  // ---------- 「选择照片并添加水印」：选片 →（按需 4:3 裁剪）→ 编辑字段 → 服务端加水印上传 ----------

  // 按 wmSourceType 来源取图（拍摄/相册，由人名层按钮决定）后走共享选片分流（sizeType 锁定原图，不允许压缩上传）；
  // 裁剪完成/免裁回调带人名进字段编辑弹层
  choosePhotoForWm(names) {
    this.chooseWmPhoto(this.data.wmSourceType, (p) => this.proceedWmForm(p, names));
  },

  // 裁剪完成/免裁：记录照片路径与人名、生成防伪码，进字段编辑弹层
  proceedWmForm(path, names) {
    this.setData({ wmPhotoPath: path, wmNames: names, wmCode: genAntiCode() });
    this.prefillWmForm();
  },

  // 历史带入的拍摄时间：保留日期（兼容 - / . 分隔、带秒或无时分等 OCR 回写格式差异），时分随机为 10:00-12:00 内且不与历史值相同；
  // 历史值缺失/无法识别时回退到记录日期（不能取当天——补录历史记录时当天 ≠ 记录日期）
  randomWmTime(shotTime) {
    const m = /^(\d{4})[./-](\d{1,2})[./-](\d{1,2})(?:\s+(\d{1,2}):(\d{2}))?/.exec(String(shotTime || '').trim());
    const datePart = m ? `${m[1]}.${pad(Number(m[2]))}.${pad(Number(m[3]))}` : this.data.dateStr.replace(/-/g, '.');
    const oldHm = m && m[4] ? `${m[4]}:${m[5]}` : '';
    let hm = oldHm;
    while (hm === oldHm) hm = randWmHm();
    return `${datePart} ${hm}`;
  },

  // 字段预填：有历史水印照片 → 带入其字段（经纬度随机偏移 ≤400m，拍摄时间随机化为 10:00-12:00，避免完全一致）；
  // 无历史 → 施工内容留空、经纬度/地点/天气按当前定位取值（高德地图）；
  //          拍摄时间当日取当前时间，非当日仅能确定日期 → 取记录日期 10:00-12:00 内随机时间
  // （两种预填场景均显示「选择杆塔坐标」入口，已选杆塔后定位回填不再覆盖）
  prefillWmForm() {
    this._wmGeoKey = null; // 「上次取值坐标」记录随表单一起重置（手动改经纬度防抖刷新去重用）
    this.resetTowerState();
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

  // ---------- 选择杆塔坐标（有/无历史照片预填场景均可进入；级联实现由 tower behavior 提供） ----------

  // 「选择杆塔坐标」按钮：级联确定回调分流标记置 wm（默认）后打开弹层（共享实现）
  onOpenTower() {
    this._towerFor = 'wm'; // 级联确定回调的分流标记：wm=水印表单（默认）/ ck=打卡确认弹层（onCkOpenTower 进入）
    this.openTowerCascade();
  },

  // 级联「确定」：_towerFor=ck（打卡确认弹层「选择杆塔带入坐标」）时杆塔原坐标直接带入打卡层，
  // 再调 /sgcc/geo 逆编码出地址串；默认（wm）走共享分支——坐标 ≤50m 随机波动后填入水印表单并按波动后坐标刷新地点、天气
  onTowerConfirm() {
    const t = this.data.towerTower;
    if (!t) return;
    wx.hideKeyboard(); // 关层前收起 hold-keyboard 残留键盘
    if (this._towerFor === 'ck') {
      this._towerFor = 'wm';
      const lng = t.lng.toFixed(6);
      const lat = t.lat.toFixed(6);
      this._ckLocTag = `杆塔带入 · ${this.data.towerLine} ${t.no}`;
      this._ckManualPos = false; // 选杆塔视同重新定位：geo 回填覆盖带入/手输地址
      this.setData({ towerVisible: false, ckLng: lng, ckLat: lat, ckLocating: false });
      this.ckGeo(lng, lat, this._ckLocTag);
      return;
    }
    this.applyTowerToWm(t);
  },

  // 确认：取 EXIF 方向 → 原图 base64 → 连同字段上传（服务端加水印）
  onWmConfirm() {
    if (this.data.wmUploading) return;
    wx.hideKeyboard(); // 确认前收起 hold-keyboard 残留键盘
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

  // 相册选片 → base64 → 上传（沿用 Call Me 聊天图片先例）；plain=true 为非水印直传
  // sizeType 锁定原图：水印/非水印照片均不允许压缩上传
  chooseAndUpload(names, plain) {
    wx.chooseMedia({
      count: 1,
      mediaType: ['image'],
      sizeType: ['original'],
      sourceType: ['album'],
      success: (res) => {
        const path = res.tempFiles[0].tempFilePath;
        wx.getFileSystemManager().readFile({
          filePath: path,
          encoding: 'base64',
          success: (r) => {
            const ext = (path.split('.').pop() || 'jpeg').toLowerCase();
            const mime = ext === 'png' ? 'png' : 'jpeg';
            this.uploadPhoto(`data:image/${mime};base64,${r.data}`, names, null, !!plain);
          },
          fail: () => this.toast('图片读取失败'),
        });
      },
    });
  },

  // 上传：wm 存在时走「加水印上传」（服务端渲染水印）；plain=true 为「非水印照片」原图直传（免验证、不占每人限一张）；
  // 否则为原「水印照片上传」。三类照片均由后端商旅远端先行：先同步进所属人名的当日商旅费用照片，成功才落本地（失败则整个上传报错）
  async uploadPhoto(image, members, wm, plain) {
    // 任务化异步执行：登记后立即返回 opId（服务端恒任务化；商旅远端先行 + COS 落库在后台串行队列完成），
    // 本卡挂进度条（退出页面不影响完成），结果由 pollCardOp 收尾（含水印照片保存相册）
    try {
      const data = await request({
        url: `/api/v1/worklog/logs/${this.data.memberEntryId}/photos`,
        method: 'POST',
        data: this.teamBody(wm ? { image, members, wm } : plain ? { image, members, plain: true } : { image, members }),
        timeout: 120000,
      });
      this.setData({ wmVisible: false, wmUploading: false });
      this.toast('已发起，后台同步商旅中');
      this.startCardOp(this.data.memberEntryId, data.opId, 'upload', { wm: !!wm, plain: !!plain });
    } catch (err) {
      this.setData({ wmUploading: false });
      wx.showModal({ title: '上传失败', content: err.message || '上传失败，请重试', showCancel: false, confirmText: '知道了' });
    }
  },

  // 加水印上传成功后自动保存到相册（复用批量下载的授权/下载/保存三件套）；
  // 返回完整提示语，授权被拒或下载保存失败均不抛出（上传已成功）
  async saveWmPhotoToAlbum(url) {
    try {
      const authed = await this.ensureAlbumAuth();
      if (!authed) return '已上传，验证中（保存相册需授权）';
      const tempFilePath = await this.dlFile(url);
      await this.saveToAlbum(tempFilePath);
      return '已上传，水印照片已存相册';
    } catch (err) {
      return '已上传，验证中（相册保存失败）';
    }
  },

  onDeletePhoto(e) {
    const { pid } = e.currentTarget.dataset;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '删除照片',
      content: '删除后不可恢复，确定删除该照片吗？（已同步商旅费用照片的会联动解除关联）',
      confirmBtn: '删除',
      cancelBtn: '取消',
    })
      .then(async () => {
        // 任务化异步执行（解除商旅关联在后台完成，服务端恒返回 opId）：本卡挂进度条，结果由 pollCardOp 收尾
        const entry = (this.data.list || []).find((x) => (x.photos || []).some((p) => String(p.id) === String(pid)));
        try {
          const data = await request({
            url: `/api/v1/worklog/photos/${pid}${this.teamQuery('?')}`,
            method: 'DELETE',
            timeout: 120000,
          });
          this.toast('已发起删除，后台解除商旅关联中');
          this.startCardOp(entry.id, data.opId, 'delete');
        } catch (err) {
          this.toast(err.message);
        }
      })
      .catch(() => {});
  },

  onDelete(e) {
    const { id } = e.currentTarget.dataset;
    // 跨班卡仅超管可删除（按钮已对非超管隐藏，此处为前置拦截兜底）
    const entry = this.data.list.find((x) => String(x.id) === String(id));
    if (entry && entry.crossTeam && !this.data.isAdmin) {
      this.toast('跨班日志仅超级管理员可删除');
      return;
    }
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
          await request({ url: `/api/v1/worklog/logs/${id}${this.teamQuery('?')}`, method: 'DELETE' });
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

  // 工作任务单（超管 / 班组管理员；与数据管理平级入口；sheet 页 ?type=task）
  onTaskSheet() {
    this.setData({ fabOpen: false });
    wx.navigateTo({ url: '/pkg-worklog/pages/sheet/sheet?type=task' });
  },

  // 费用汇总（超管 / 班组管理员；与工作任务单平级入口；sheet 页 ?type=fee）
  onFeeSheet() {
    this.setData({ fabOpen: false });
    wx.navigateTo({ url: '/pkg-worklog/pages/sheet/sheet?type=fee' });
  },

  // 派车汇总（超管 / 班组管理员；与费用汇总平级入口，生成派车单号清单 xlsx；sheet 页 ?type=dispatch）
  onDispatchSheet() {
    this.setData({ fabOpen: false });
    wx.navigateTo({ url: '/pkg-worklog/pages/sheet/sheet?type=dispatch' });
  },

  // 派车对齐（超管 / 班组管理员；与数据管理平级入口，派车单从聊天文件选取）
  onDispatch() {
    this.setData({ fabOpen: false });
    wx.navigateTo({ url: '/pkg-worklog/pages/dispatch/dispatch' });
  },

  // ---------- 批量下载水印照片 ----------

  // 首次打开默认范围：当天 1~10 日 → 上月整月；11 日及以后 → 本月 1 号到今天（批量下载面板用）
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
      const data = await request({ url: `/api/v1/worklog/photos?from=${dlFrom}&to=${dlTo}${this.teamQuery()}` });
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
    this.setData({
      dlVisible: false,
      dlCalValue: [parseDate(dlFrom).getTime(), parseDate(dlTo).getTime()],
      dlCalVisible: true,
    });
  },

  // range 日历确认：e.detail.value 为两个时间戳；回写下载面板范围并重开
  onDlCalConfirm(e) {
    const value = e.detail.value;
    if (!Array.isArray(value) || value.length < 2) {
      this.toast('请选择起止日期');
      return;
    }
    const from = fmtDate(new Date(value[0]));
    const to = fmtDate(new Date(value[1]));
    this.setData({ dlCalVisible: false, dlFrom: from, dlTo: to, dlVisible: true });
    this.loadDlPhotos();
  },

  // 未选直接关闭日历：重开下载面板（保留原范围）
  onDlCalClose() {
    if (!this.data.dlCalVisible) return;
    this.setData({ dlCalVisible: false, dlVisible: true });
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

  // 下载为 PDF：范围内水印照片组合 PDF（每卡两张一页、同页只放同一卡片，后端排版），下载后打开
  onDlPdf() {
    const { dlFrom, dlTo, pdfBusy } = this.data;
    if (pdfBusy) return;
    this.setData({ pdfBusy: true });
    wx.showLoading({ title: '正在生成 PDF…', mask: true }); // 照片多耗时长，全程 loading
    wx.downloadFile({
      url: `${BASE_URL}/api/v1/worklog/photos.pdf?from=${dlFrom}&to=${dlTo}${this.teamQuery()}`,
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      timeout: 120000,
      // 指定本地存储文件名，否则 openDocument 打开后显示的是随机临时文件名（乱码）
      filePath: `${wx.env.USER_DATA_PATH}/水印照片-${dlFrom}至${dlTo}.pdf`,
      success: (r) => {
        if (r.statusCode !== 200) {
          this.toast(r.statusCode === 404 ? '该范围内暂无水印照片' : `生成失败（${r.statusCode}）`);
          return;
        }
        wx.openDocument({
          filePath: r.filePath,
          fileType: 'pdf',
          showMenu: true, // 右上角菜单可另存/转发
          fail: () => this.toast('该类型暂不支持打开'),
        });
      },
      fail: () => this.toast('网络异常，请检查网络后重试'),
      complete: () => {
        wx.hideLoading();
        this.setData({ pdfBusy: false });
      },
    });
  },

  // PDF 存网盘：先弹网盘目录选择（nd-dirpicker，默认「出工日志」），confirm 后带 dir 生成并转存
  onDlPdfSave() {
    if (this.data.pdfBusy) return;
    this.setData({ ndSaveVisible: true });
  },

  onNdSaveDirClose() {
    this.setData({ ndSaveVisible: false });
  },

  onNdSaveDirConfirm(e) {
    const { dir = '', space = 'my' } = e.detail || {};
    this.setData({ ndSaveVisible: false });
    this.doDlPdfSave(dir, space);
  },

  // 同一范围在后端生成并转存网盘（dir/space 为自选的网盘目录与空间；failed 为合成失败的照片数）
  doDlPdfSave(dir, space) {
    const { dlFrom, dlTo, pdfBusy } = this.data;
    if (pdfBusy) return;
    this.setData({ pdfBusy: true });
    wx.showLoading({ title: '正在生成并存网盘…', mask: true });
    request({
      url: '/api/v1/worklog/photos-pdf/save-netdisk',
      method: 'POST',
      timeout: 120000,
      data: this.teamBody({ from: dlFrom, to: dlTo, dir, space }),
    }).then((data) => {
      const failed = data && data.failed ? `，${data.failed} 张照片合成失败` : '';
      this.toast(`已保存到网盘：${(data && data.path) || ''}${failed}`);
    }).catch((err) => this.toast(err.message))
      .finally(() => {
        wx.hideLoading();
        this.setData({ pdfBusy: false });
      });
  },

  // ---------- 汇总前核验（替代原「验证报告」；按月列未通过记录，默认当月可翻月，数据取 /worklog/report） ----------
  // 底部仅「确认」关面板；批量从商旅同步已迁移至悬浮钮独立面板（见下方「批量从商旅同步」分区）

  // 月份 → 面板字段（rpMonthText 展示文案 / rpMonthAtCur 控制「下一月」置灰，不看未来月）
  rpMonthData(month) {
    const [y, m] = month.split('-').map(Number);
    return { rpMonth: month, rpMonthText: `${y} 年 ${m} 月`, rpMonthAtCur: month >= fmtDate(new Date()).slice(0, 7) };
  },

  onOpenReport() {
    this.setData({ rpVisible: true, fabOpen: false, ...this.rpMonthData(this.data.rpMonth || fmtDate(new Date()).slice(0, 7)) });
    this.loadReport();
  },

  onCloseReport() {
    this.setData({ rpVisible: false });
  },

  onRpVisibleChange(e) {
    if (!e.detail.visible && this.data.rpVisible) this.setData({ rpVisible: false });
  },

  // 翻月（超过当月则忽略）
  onRpMonthShift(e) {
    const d = Number(e.currentTarget.dataset.d) || 0;
    const [y, m] = this.data.rpMonth.split('-').map(Number);
    const nd = new Date(y, m - 1 + d, 1);
    const nm = `${nd.getFullYear()}-${pad(nd.getMonth() + 1)}`;
    if (nm > fmtDate(new Date()).slice(0, 7)) return;
    this.setData(this.rpMonthData(nm));
    this.loadReport();
  },

  // 拉取当前月份核验结果：问题卡 = reasons 非空（接口范围 = 未通过 ∪ 有备注；未出车免验证不入列；
  // total=范围内全量条数（接口新口径），其余=total−未通过数）
  async loadReport() {
    const month = this.data.rpMonth;
    if (!month) return;
    const [y, m] = month.split('-').map(Number);
    const from = `${month}-01`;
    const to = fmtDate(new Date(y, m, 0)); // 当月最后一天
    this.setData({ rpLoading: true });
    try {
      const data = await request({ url: `/api/v1/worklog/report?from=${from}&to=${to}${this.scopeQuery()}${this.teamQuery()}` });
      const items = (data && data.list) || [];
      const issues = items
        .filter((x) => (x.reasons || []).length)
        .map((x) => ({
          id: x.id,
          logDate: x.log_date,
          dateText: `${Number(x.log_date.slice(5, 7))}月${Number(x.log_date.slice(8, 10))}日`,
          plateText: x.plate_no,
          membersText: (x.members || []).join('、'),
          reasons: x.reasons,
        }));
      const okCount = Number((data && data.total) || 0) - issues.length; // 其余 = total（范围内全量条数，接口新口径）− 未通过数
      this.setData({
        rpLoading: false,
        rpIssues: issues,
        rpOkText: issues.length ? `其余 ${okCount} 条记录全部通过` : '当月无未通过记录',
      });
    } catch (err) {
      this.toast(err.message);
      this.setData({ rpLoading: false });
    }
  },

  // 点问题卡：关面板，跳到该卡所在日期后滚动定位（短暂高亮；当前视图口径下无此卡时提示）
  async onRpIssueTap(e) {
    const { id, date } = e.currentTarget.dataset;
    if (!id) return;
    this.setData({ rpVisible: false });
    if (date && date !== this.data.dateStr) {
      this.setData({ win: [] }); // 跨日跳转：重置切日窗格（同日历选日口径）
      this.applyDate(date);
      await this.loadLogs();
    }
    this.scrollToCard(Number(id));
  },

  // ---------- 批量从商旅同步（悬浮钮独立面板，与数据管理/工作任务单/批量下载/汇总前核验同级） ----------
  // 区段拉取 POST /sgcc/sync/pull {from,to}；进行中出工日志子应用全局锁定（任何端任何人被进度遮罩覆盖，完成后解锁）；
  // 面板内同步日志：成功仅汇总「成功同步 N 条记录」，失败逐条明细；管理员可一键清除全部同步记录

  onOpenBatchSync() {
    const now = new Date();
    this.setData({
      bsVisible: true,
      fabOpen: false,
      bsFrom: this.data.bsFrom || `${now.getFullYear()}-${pad(now.getMonth() + 1)}-01`,
      bsTo: this.data.bsTo || fmtDate(now),
    });
    this.loadBsLogs();
  },

  onBsClose() {
    this.setData({ bsVisible: false });
  },

  onBsVisibleChange(e) {
    if (!e.detail.visible && this.data.bsVisible) this.setData({ bsVisible: false });
  },

  // 「改日期」：先关同步面板再开 range 日历（互斥，同下载面板改日期口径）
  onBsChangeDate() {
    this.setData({
      bsVisible: false,
      sgCalValue: [parseDate(this.data.bsFrom).getTime(), parseDate(this.data.bsTo).getTime()],
      sgCalVisible: true,
    });
  },

  // range 日历确认：回写同步面板区段并重开面板、重查日志（区段最多跨 62 天，服务端口径）
  onSgCalConfirm(e) {
    const value = e.detail.value;
    if (!Array.isArray(value) || value.length < 2) {
      this.toast('请选择起止日期');
      return;
    }
    const from = fmtDate(new Date(value[0]));
    const to = fmtDate(new Date(value[1]));
    const days = Math.round((parseDate(to).getTime() - parseDate(from).getTime()) / 86400000) + 1;
    if (days > 62) {
      this.toast('日期区段最多跨 62 天');
      return;
    }
    this.setData({ sgCalVisible: false, bsVisible: true, bsFrom: from, bsTo: to });
    this.loadBsLogs();
  },

  // 未选直接关闭日历：重开同步面板（保留原区段）
  onSgCalClose() {
    if (!this.data.sgCalVisible) return;
    this.setData({ sgCalVisible: false, bsVisible: true });
  },

  // 「发起同步」：POST /sgcc/sync/pull {from,to}；本机与他人同入全局锁遮罩（40909 = 已有进行中任务，并入锁态轮询）
  async onBsSync() {
    const { bsFrom, bsTo } = this.data;
    if (!bsFrom || !bsTo) return;
    if (this.data.sgSyncVisible) {
      this.toast('已有批量同步进行中，请等待完成');
      return;
    }
    try {
      await request({ url: '/api/v1/sgcc/sync/pull', method: 'POST', data: this.teamBody({ from: bsFrom, to: bsTo }) });
      this.toast('已发起批量从商旅同步');
    } catch (err) {
      this.toast(err.message);
      if (err.code !== 40909) return;
    }
    this.lockByActive(); // 无论本机发起还是他人进行中：进入全局锁轮询
  },

  // 同步日志：区段内成功仅计条数（ok/diff 均为成功同步），失败逐条明细
  async loadBsLogs() {
    const { bsFrom, bsTo } = this.data;
    if (!bsFrom || !bsTo) return;
    this.setData({ bsLoading: true });
    try {
      const data = await request({ url: `/api/v1/sgcc/sync/logs?from=${bsFrom}&to=${bsTo}${this.teamQuery()}` });
      const list = (data && data.list) || [];
      const TYPE = { auth: '登录态', clockin: '打卡', fee: '费用', photo: '费用照片' };
      const fails = list
        .filter((x) => x.result === 'fail')
        .map((x) => ({
          id: x.id,
          dateText: `${Number(x.sync_date.slice(5, 7))}月${Number(x.sync_date.slice(8, 10))}日`,
          memberName: x.member_name || '',
          typeText: TYPE[x.type] || x.type,
          detail: x.detail || '',
        }));
      this.setData({ bsLoading: false, bsOkCount: list.length - fails.length, bsFails: fails });
    } catch (err) {
      this.setData({ bsLoading: false });
      this.toast(err.message);
    }
  },

  // 「清除全部记录」（管理员）：二次确认后清空本班组同步日志
  onBsClearLogs() {
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '清除全部同步记录？',
      content: '本班组全部同步日志将被清空，该操作不可恢复。',
      confirmBtn: '清除',
      cancelBtn: '取消',
    })
      .then(async () => {
        try {
          await request({ url: `/api/v1/sgcc/sync/logs${this.teamQuery('?')}`, method: 'DELETE' });
          this.toast('已清除全部同步记录');
          this.loadBsLogs();
        } catch (err) {
          this.toast(err.message);
        }
      })
      .catch(() => {});
  },

  // ---------- 批量从商旅同步 · 子应用全局锁 ----------

  // 进入 / 刷新出工日志时探测：本班组有批量拉取进行中则出全页进度遮罩（任何人同锁；10s 节流避免随列表轮询过频）
  async checkSgccLock() {
    if (this.data.noTeam || this.data.sgSyncVisible) return; // 已锁定（含本机发起）时由锁内轮询收口
    const now = Date.now();
    if (this._lockCheckedAt && now - this._lockCheckedAt < 10000) return;
    this._lockCheckedAt = now;
    try {
      const p = await request({ url: `/api/v1/sgcc/sync/active${this.teamQuery('?')}` });
      if (p && p.running) this.lockByActive();
      // 每晚定时核查：进行中且当日在视图内 → 当日出车卡挂进度条并整卡锁定
      if (p && p.daily && p.daily.running && !this._dailySync) this.enterDailySync();
      // 照片单操作任务（他人/他端发起或本端重新进入）：旁观者挂条并起轮询
      if (p && (p.ops || []).length) {
        this.syncSpectatorOps(p.ops);
        this.pollSpectatorOps();
      }
    } catch (err) { /* 网络异常：不锁定（商旅恒挂载） */ }
  },

  // 全局锁遮罩：1.5s 轮询 /sync/active 直至完成；完成后解锁并刷新列表 / 核验面板 / 同步日志
  async lockByActive() {
    if (this.data.sgSyncVisible) return;
    this.setData({ sgSyncVisible: true, sgSyncPct: 0 });
    let fails = 0;
    const tick = async () => {
      let p = null;
      try {
        p = await request({ url: `/api/v1/sgcc/sync/active${this.teamQuery('?')}` });
      } catch (err) { /* 网络抖动：计数后续轮 */ }
      if (!p) {
        fails += 1;
        if (fails <= 20) {
          this._lockTimer = setTimeout(tick, 1500);
          return;
        }
      } else if (p.running) {
        fails = 0;
        this.setData({ sgSyncPct: p.total ? Math.min(99, Math.round((p.done / p.total) * 100)) : 0 });
        this._lockTimer = setTimeout(tick, 1500);
        return;
      }
      // 完成（或连续失败兜底）：解锁并刷新；batchDone = 近 2 分钟内完成的任务终态（区分 全部成功 / 部分失败 / 整体失败）
      this._lockTimer = null;
      this.setData({ sgSyncVisible: false, sgSyncPct: 100 });
      const bd = p && p.batchDone;
      const failed = bd && Number(bd.failed) > 0 ? Number(bd.failed) : 0;
      if (failed && failed >= Number(bd.total || 0)) this.toast(`从商旅同步失败：${bd.failMsg || '请稍后重试'}`);
      else if (failed) this.toast(`从商旅同步完成，${failed} 人失败（详见核查记录）`);
      else this.toast('从商旅同步完成');
      this.loadLogs();
      if (this.data.rpVisible) this.loadReport();
      if (this.data.bsVisible) this.loadBsLogs();
    };
    tick();
  },

  // ---------- 每日定时核查：当日出车卡片进度条 + 整卡不可操作（kind=daily，仅当日口径） ----------

  // 进入核查锁：重映射当日列表挂进度条，并起 1.5s 轮询直至完成
  enterDailySync() {
    if (this._dailySync) return;
    this._dailySync = { pct: 0 };
    this.loadLogs(); // mapLogList 据 _dailySync 给当日出车卡挂进度条
    this.pollDailySync();
  },

  async pollDailySync() {
    let fails = 0;
    while (this._dailySync) {
      let p = null;
      try {
        p = await request({ url: `/api/v1/sgcc/sync/active${this.teamQuery('?')}` });
      } catch (err) { /* 网络抖动：计数后续轮 */ }
      if (p && p.daily && p.daily.running) {
        fails = 0;
        this._dailySync.pct = p.daily.total ? Math.min(99, Math.round((p.daily.done / p.daily.total) * 100)) : 0;
        this.patchDailyCards();
      } else if (p || fails > 20) {
        break; // 完成（或连续失败兜底）
      } else {
        fails += 1;
      }
      await new Promise((r) => {
        this._dailyTimer = setTimeout(r, 1500);
      });
    }
    this._dailySync = null;
    this._dailyTimer = null;
    this.loadLogs(); // 解锁并落最新数据（卡片角标 / 打卡区随同步结果更新）
  },

  // 当日出车卡进度补丁（仅当前窗格；本卡手动同步中的卡由各自 jobId 轮询更新，不覆盖）
  patchDailyCards() {
    if (this.data.dateStr !== fmtDate(new Date())) return;
    const update = {};
    const applyTo = (list, path) => {
      (list || []).forEach((c, ci) => {
        if (c.hasVehicle && c.syncing && !(this._syncJobs || {})[c.id]) {
          update[`${path}[${ci}].syncPct`] = this._dailySync.pct;
        }
      });
    };
    applyTo(this.data.list, 'list');
    if (this.data.win.length === 3) applyTo(this.data.win[1].list, 'win[1].list');
    if (Object.keys(update).length) this.setData(update);
  },

  // 同步锁定中的卡片点击：仅提示（遮罩已阻断全部操作入口）
  onSyncLockTap() {
    this.toast('商旅同步中，暂不可操作');
  },

  // ---------- 水印信息手动修正（Dify 识别出错时用；照片验证状态点击进入，保存后重新核验） ----------

  onWmEdit(e) {
    const pid = Number(e.currentTarget.dataset.pid);
    let photo = null;
    (this.data.list || []).forEach((c) => (c.photos || []).forEach((p) => { if (p.id === pid) photo = p; }));
    if (!photo || !photo.editable) return;
    this.setData({
      wmEditVisible: true,
      wmEditPhotoId: pid,
      wmEditDraft: {
        content: photo.workContent || '',
        time: photo.shotTime || '',
        weather: photo.weather || '',
        location: photo.location || '',
        lng: photo.lng || '',
        lat: photo.lat || '',
      },
    });
  },

  onWmEditInput(e) {
    const k = e.currentTarget.dataset.k;
    this.setData({ [`wmEditDraft.${k}`]: e.detail.value });
  },

  onWmEditClose() {
    if (this.data.wmEditSaving) return;
    this.setData({ wmEditVisible: false });
  },

  onWmEditVisibleChange(e) {
    if (e.detail.visible) return;
    if (this.data.wmEditSaving) {
      this.setData({ wmEditVisible: true }); // 保存中不允许遮罩关闭
      return;
    }
    if (this.data.wmEditVisible) this.setData({ wmEditVisible: false });
  },

  // 保存：PUT /photos/:id/wm（服务端按记录日期与派车目的地重新核验），成功后刷新列表看角标
  async onWmEditSave() {
    if (this.data.wmEditSaving) return;
    const d = this.data.wmEditDraft;
    if (!d.content.trim()) {
      this.toast('施工内容不能为空');
      return;
    }
    if (!d.time.trim()) {
      this.toast('拍摄时间不能为空');
      return;
    }
    this.setData({ wmEditSaving: true });
    try {
      await request({
        url: `/api/v1/worklog/photos/${this.data.wmEditPhotoId}/wm`,
        method: 'PUT',
        data: this.teamBody({
          workContent: d.content.trim(),
          shotTime: d.time.trim(),
          weather: d.weather.trim(),
          location: d.location.trim(),
          lng: d.lng.trim(),
          lat: d.lat.trim(),
        }),
      });
      this.setData({ wmEditVisible: false, wmEditSaving: false });
      this.toast('已保存并重新判定验证');
      this.loadLogs();
    } catch (err) {
      this.setData({ wmEditSaving: false });
      this.toast(err.message);
    }
  },

  // 滚动到指定卡片并闪烁高亮（中间格滚区 scroll-into-view）；当前视图口径下无此卡（如「仅看我」未含该记录）时提示
  scrollToCard(id) {
    if (!this.data.list.some((x) => x.id === id)) {
      this.toast('当前视图下无该卡片，请切换到「全部」查看');
      return;
    }
    // 两次赋值保证 scroll-into-view 重复触发（先清空再 nextTick 写入锚点）
    this.setData({ flashId: id, intoView: '' });
    wx.nextTick(() => this.setData({ intoView: `logcard-${id}` }));
    if (this._flashTimer) clearTimeout(this._flashTimer);
    this._flashTimer = setTimeout(() => this.setData({ flashId: 0 }), 1600);
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'work-log', title: '出工日志' });
  },
});
