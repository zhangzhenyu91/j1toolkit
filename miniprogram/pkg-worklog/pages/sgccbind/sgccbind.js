// 商旅打卡 · 账号绑定页（出工日志扩展）：短信登录三步（图形验证码 → 发短信 → 短信换 token 绑定）
// 两个状态：未绑定（短信登录表单 + 打卡设备信息）/ 已绑定（账号状态 + 今日打卡/费用 + 解除绑定/重新登录）
// 班组口径同 pkg-worklog 其他页面：超管按主页切换器存下的 worklog_team_id 生效（请求带 team_id）
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

const pad = (n) => (n < 10 ? `0${n}` : `${n}`);
// 打卡时刻 → HH:MM（兼容 MySQL DATETIME 的 ISO 串与 'YYYY-MM-DD HH:mm:ss' 两种口径）
const fmtHm = (s) => {
  if (!s) return '';
  const raw = String(s);
  const d = new Date(raw.indexOf('T') >= 0 ? raw : raw.replace(/-/g, '/'));
  if (Number.isNaN(d.getTime())) return '';
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
};
// 费用金额展示（decimal 串去尾零：'60.00' → '60'）
const fmtFee = (v) => {
  const n = Number(v);
  return Number.isFinite(n) ? String(n) : String(v || 0);
};

Page({
  data: {
    gate: false, // 门控（参照 worklog 主页 gate 模式）
    loading: true,
    bound: false, // 是否已绑定商旅账号
    // 用户信息（已绑定态头部：昵称首字头像 + 班组名）
    userInfo: {},
    avatarChar: '检',
    // 已绑定态账号状态（GET /api/v1/sgcc/account）
    mobile: '', // 脱敏手机号（后端已脱敏）
    tokenStatus: 0, // 1=登录有效 / 0=已过期
    clockCount: 0, // 今日打卡次数
    clockLine: '', // 今日打卡明细行（开始 HH:MM · 结束 HH:MM）
    feeText: '', // 今日费用主行（伙食 ¥x · 交通 ¥y）
    feeSub: '', // 今日费用副行（成本中心）
    // 打卡设备信息（未绑定态随 bind 一并提交；已绑定态为后端值，可修改）
    deviceType: '',
    systemVersion: '',
    // 短信登录表单
    formMobile: '',
    imgCode: '', // 图形验证码输入
    captchaImg: '', // 图形验证码图（base64 dataURL）
    smsCode: '', // 短信验证码输入
    smsCountdown: 0, // 短信重发倒计时（秒）
    binding: false, // 「登录并绑定」提交中
  },

  onLoad() {
    // gate 兜底：保证登录态就绪后再加载（口径同 worklog 主页）
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
    this._teamId = Number(wx.getStorageSync('worklog_team_id')) || 0; // 生效班组 id
    this.setData({
      gate: true,
      userInfo: user,
      avatarChar: (user.nickname || user.username || '检').slice(0, 1),
    });
    this.detectDevice();
    this._loaded = true; // 首屏加载标记（onShow 据此跳过，避免与本次重复请求）
    this.loadAccount();
  },

  onShow() {
    // 返回本页时刷新绑定状态；首次显示时由 passGate 触发加载，跳过
    if (!this.data.gate) return;
    if (this._loaded) {
      this._loaded = false;
      return;
    }
    this.loadAccount();
  },

  onUnload() {
    this.clearSmsTimer();
  },

  // 生效班组 query 片段（lead 为前导连接符；仅超管 _teamId>0 时携带，其余角色后端强制本班无需传）
  teamQuery(lead) {
    return this._teamId ? `${lead || '&'}team_id=${this._teamId}` : '';
  },

  // 生效班组 body 注入（POST/PUT JSON 用，口径同 teamQuery）
  teamBody(data) {
    return this._teamId ? Object.assign({}, data, { team_id: this._teamId }) : data;
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  // 打卡设备默认值：「品牌 型号」拼接（如 Xiaomi 2509FPN0BC）与系统版本，取不到用默认
  detectDevice() {
    let deviceType = 'Xiaomi 2509FPN0BC';
    let systemVersion = 'Android 16';
    try {
      const info = wx.getDeviceInfo ? wx.getDeviceInfo() : {};
      const brand = String(info.brand || '').trim();
      const model = String(info.model || '').trim();
      if (brand || model) deviceType = `${brand} ${model}`.trim();
      if (info.system) systemVersion = String(info.system).trim();
    } catch (e) {
      // 获取失败时使用默认值
    }
    this.setData({ deviceType, systemVersion });
  },

  // 拉取绑定状态与今日摘要；无 sgcc-clockin 权限 / 后端未开启（403/404）时静默降级为未绑定态
  async loadAccount() {
    this.setData({ loading: true });
    try {
      const data = await request({ url: `/api/v1/sgcc/account${this.teamQuery('?')}` });
      if (!data || !data.bound) {
        this.setData({ bound: false });
        return;
      }
      this.applyAccount(data);
    } catch (err) {
      if (err.statusCode === 403 || err.statusCode === 404 || err.code === 40020) {
        this.setData({ bound: false });
      } else {
        this.toast(err.message);
      }
    } finally {
      this.setData({ loading: false });
    }
  },

  // 已绑定态数据装配（今日打卡行 / 今日费用行在此拼装展示文案）
  applyAccount(data) {
    const clockins = Array.isArray(data.clockins) ? data.clockins : [];
    const begin = clockins.find((c) => Number(c.seq) === 1) || clockins[0];
    const end = clockins.find((c) => Number(c.seq) === 2);
    let clockLine = '';
    if (begin) clockLine = `开始 ${fmtHm(begin.clock_time)}`;
    if (end) clockLine += ` · 结束 ${fmtHm(end.clock_time)}`;
    if (clockLine) clockLine += '（已同步商旅平台）';
    const fee = data.fee || null;
    const costCenter = fee ? `${fee.cost_center_code || ''} ${fee.cost_center_name || ''}`.trim() : '';
    this.setData({
      bound: true,
      mobile: data.mobile || '',
      tokenStatus: Number(data.tokenStatus) === 1 ? 1 : 0,
      deviceType: data.deviceType || '',
      systemVersion: data.systemVersion || '',
      clockCount: clockins.length,
      clockLine,
      feeText: fee ? `伙食 ¥${fmtFee(fee.food_fee)} · 交通 ¥${fmtFee(fee.transit_fee)}` : '未同步',
      feeSub: costCenter ? `成本中心 ${costCenter}` : '',
    });
  },

  /* ---------- 短信登录表单（未绑定态） ---------- */

  onMobileInput(e) {
    this.setData({ formMobile: e.detail.value });
  },

  onImgCodeInput(e) {
    this.setData({ imgCode: e.detail.value });
  },

  onSmsCodeInput(e) {
    this.setData({ smsCode: e.detail.value });
  },

  checkMobile() {
    const mobile = (this.data.formMobile || '').trim();
    if (!/^1\d{10}$/.test(mobile)) {
      this.toast('手机号格式不正确');
      return '';
    }
    return mobile;
  },

  // 获取 / 点击刷新图形验证码（返回 base64 dataURL 直接贴图）
  async onCaptcha() {
    const mobile = this.checkMobile();
    if (!mobile) return;
    try {
      const data = await request({
        url: '/api/v1/sgcc/login/captcha',
        method: 'POST',
        data: this.teamBody({ mobile }),
      });
      this.setData({ captchaImg: (data && data.image) || '' });
    } catch (err) {
      this.toast(err.message);
    }
  },

  // 发送短信验证码（需先过图形验证码），成功起 60s 倒计时
  async onSendSms() {
    if (this.data.smsCountdown > 0) return;
    const mobile = this.checkMobile();
    if (!mobile) return;
    const checkImgCode = (this.data.imgCode || '').trim();
    if (!checkImgCode) {
      this.toast('请填写图形验证码');
      return;
    }
    try {
      await request({
        url: '/api/v1/sgcc/login/sms',
        method: 'POST',
        data: this.teamBody({ mobile, checkImgCode }),
      });
      this.toast('验证码已发送');
      this.startSmsCountdown();
    } catch (err) {
      this.toast(err.message);
      // 图形验证码错误/失效时自动刷新一张，便于直接重试
      this.onCaptcha();
    }
  },

  startSmsCountdown() {
    this.clearSmsTimer();
    this.setData({ smsCountdown: 60 });
    this._smsTimer = setInterval(() => {
      const left = this.data.smsCountdown - 1;
      if (left <= 0) {
        this.clearSmsTimer();
        this.setData({ smsCountdown: 0 });
        return;
      }
      this.setData({ smsCountdown: left });
    }, 1000);
  },

  clearSmsTimer() {
    if (this._smsTimer) {
      clearInterval(this._smsTimer);
      this._smsTimer = null;
    }
  },

  // 登录并绑定：短信换 token，设备型号/系统版本一并提交
  async onBind() {
    if (this.data.binding) return;
    const mobile = this.checkMobile();
    if (!mobile) return;
    const checkCode = (this.data.smsCode || '').trim();
    if (!checkCode) {
      this.toast('请填写短信验证码');
      return;
    }
    this.setData({ binding: true });
    try {
      await request({
        url: '/api/v1/sgcc/login/bind',
        method: 'POST',
        data: this.teamBody({
          mobile,
          checkCode,
          deviceType: this.data.deviceType,
          systemVersion: this.data.systemVersion,
        }),
      });
      this.toast('绑定成功');
      this.setData({ formMobile: '', imgCode: '', captchaImg: '', smsCode: '' });
      this.loadAccount();
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ binding: false });
    }
  },

  /* ---------- 已绑定态 ---------- */

  // 「可修改」编辑打卡设备信息（两态共用）：未绑定态仅改本地随 bind 提交；已绑定态保存调 PUT /account/device
  onEditDevice(e) {
    const field = e.currentTarget.dataset.field; // deviceType / systemVersion
    const label = field === 'deviceType' ? '设备型号' : '系统版本';
    wx.showModal({
      title: `修改${label}`,
      editable: true,
      content: this.data[field],
      placeholderText: field === 'deviceType' ? '「厂商 型号」格式，如 Xiaomi 2509FPN0BC' : '如 Android 16',
      confirmText: '保存',
      success: async (res) => {
        if (!res.confirm) return;
        const value = (res.content || '').trim();
        if (!value) {
          this.toast(`${label}不能为空`);
          return;
        }
        if (!this.data.bound) {
          this.setData({ [field]: value });
          return;
        }
        try {
          await request({
            url: '/api/v1/sgcc/account/device',
            method: 'PUT',
            data: this.teamBody({
              deviceType: field === 'deviceType' ? value : this.data.deviceType,
              systemVersion: field === 'systemVersion' ? value : this.data.systemVersion,
            }),
          });
          this.setData({ [field]: value });
          this.toast('已保存');
        } catch (err) {
          this.toast(err.message);
        }
      },
    });
  },

  // 解除绑定：二次确认后删除本地绑定（商旅 App 侧不受影响），回未绑定态
  onUnbind() {
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '解除绑定',
      content: '解除后将删除本地绑定与已同步的打卡/费用数据，商旅 App 侧不受影响。确定解除吗？',
      confirmBtn: '解除绑定',
      cancelBtn: '取消',
    }).then(async () => {
      try {
        await request({ url: `/api/v1/sgcc/account${this.teamQuery('?')}`, method: 'DELETE' });
        this.toast('已解除绑定');
        this.setData({ bound: false, formMobile: '', imgCode: '', captchaImg: '', smsCode: '' });
        this.detectDevice();
      } catch (err) {
        this.toast(err.message);
      }
    }).catch(() => {});
  },

  // 重新登录：回未绑定态表单重新走短信登录（本地绑定保留，bind 成功后覆盖 token）
  onRelogin() {
    this.setData({ bound: false, formMobile: '', imgCode: '', captchaImg: '', smsCode: '' });
  },

  onShareAppMessage() {
    return shareAppMessage(this);
  },
});
