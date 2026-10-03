// 绑定商旅 · 账号绑定页（出工日志扩展）：短信/密码双方式登录绑定（均为双通道：图形码优先、失败降级顶象滑块）
// 两个状态：未绑定（登录表单 + 打卡设备信息）/ 已绑定（账号状态 + 解除绑定/重新登录）
// 滑块在 sgccweb 页（web-view 打开 server/public/sgcc-captcha.html）完成，回传 captchaToken + constId；
// 顶象 token 有效期短：发短信/绑定失败即清空，下次操作前重新拖滑块
// 班组口径同 pkg-worklog 其他页面：超管按主页切换器存下的 worklog_team_id 生效（请求带 team_id）
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

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
    // 打卡设备信息（未绑定态随 bind 一并提交；已绑定态为后端值，可修改）
    deviceType: '',
    systemVersion: '',
    // 登录表单（短信/密码双方式；短信为双通道：图形码优先、失败降级滑块）
    loginType: 'sms', // sms=短信登录 / pwd=密码登录
    formMobile: '',
    password: '', // 密码输入（pwd 方式）
    imgCode: '', // 图形验证码输入（sms 图形通道）
    captchaImg: '', // 图形验证码图（base64 dataURL；非空即显示图形码 field）
    smsCode: '', // 短信验证码输入（sms 方式）
    captchaToken: '', // 顶象滑块凭据（sgccweb 回传，滑块通道）
    constId: '', // 顶象设备指纹（sgccweb 回传，滑块通道）
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

  // 拉取绑定状态与账号信息；商旅恒挂载，无出工日志权限 / 未分配班组（403）时静默降级为未绑定态
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

  // 已绑定态数据装配（账号状态行：商旅账号 / 登录状态 / 打卡设备信息）
  applyAccount(data) {
    this.setData({
      bound: true,
      mobile: data.mobile || '',
      tokenStatus: Number(data.tokenStatus) === 1 ? 1 : 0,
      deviceType: data.deviceType || '',
      systemVersion: data.systemVersion || '',
    });
  },

  /* ---------- 登录表单（未绑定态：短信/密码双方式；均为图形码优先、失败降级滑块双通道） ---------- */

  onMobileInput(e) {
    this.setData({ formMobile: e.detail.value });
  },

  onPasswordInput(e) {
    this.setData({ password: e.detail.value });
  },

  onSmsCodeInput(e) {
    this.setData({ smsCode: e.detail.value });
  },

  onImgCodeInput(e) {
    this.setData({ imgCode: e.detail.value });
  },

  // 切换登录方式：清空已采集的滑块凭据（顶象 token 与操作绑定，换方式重新拖）
  onSwitchType(e) {
    const loginType = e.currentTarget.dataset.type;
    if (loginType === this.data.loginType) return;
    this.setData({ loginType, captchaToken: '', constId: '' });
  },

  checkMobile() {
    const mobile = (this.data.formMobile || '').trim();
    if (!/^1\d{10}$/.test(mobile)) {
      this.toast('手机号格式不正确');
      return '';
    }
    return mobile;
  },

  // 去拖滑块（sgccweb 页）；action 记录滑块回来后的后续动作（'sms' 发短信 / 'bind' 绑定）
  goCaptcha(action) {
    const mobile = this.checkMobile();
    if (!mobile) return;
    this._pendingAction = action;
    wx.navigateTo({ url: '/pkg-worklog/pages/sgccweb/sgccweb' });
  },

  // sgccweb 回传滑块结果（web-view bindmessage → 上一页回调）；进入滑块通道，弃用图形码
  onCaptchaResult({ captchaToken, constId }) {
    this.setData({ captchaToken: captchaToken || '', constId: constId || '', captchaImg: '', imgCode: '' });
    this.toast('安全验证已通过');
    const action = this._pendingAction;
    this._pendingAction = null;
    if (action === 'sms') this.sendSms();
    else if (action === 'bind') this.doBind();
  },

  // 取图形验证码：成功返回 true；40035（风控窗口 99000）返回 false，调用方降级滑块
  async fetchCaptcha() {
    const mobile = this.checkMobile();
    if (!mobile) return false;
    wx.showLoading({ title: '正在获取验证码…', mask: true });
    try {
      const data = await request({
        url: '/api/v1/sgcc/login/captcha',
        method: 'POST',
        data: this.teamBody({ mobile }),
      });
      wx.hideLoading();
      this.setData({ captchaImg: (data && data.image) || '', imgCode: '' });
      return true;
    } catch (err) {
      wx.hideLoading();
      return false;
    }
  },

  // 点图形码图刷新一张；取不到（风控窗口）则降级滑块
  async onRefreshCaptcha() {
    if (!(await this.fetchCaptcha())) {
      this.setData({ captchaImg: '', imgCode: '' });
      this.toast('图形验证码暂不可用，请完成滑块验证');
      this.goCaptcha('sms');
    }
  },

  // 「改用滑块验证」：弃图形码，直接去拖滑块（短信模式回来续发短信，密码模式回来续绑定）
  onUseSlider() {
    if (!this.checkMobile()) return;
    this.setData({ captchaImg: '', imgCode: '' });
    this.goCaptcha(this.data.loginType === 'pwd' ? 'bind' : 'sms');
  },

  // 发送短信验证码：滑块凭据 → v3 直发；有图形码 → v2 发送；都没有 → 先取图形码，取不到降级滑块
  async onSendSms() {
    if (this.data.smsCountdown > 0) return;
    if (!this.checkMobile()) return;
    if (this.data.captchaToken) {
      this.sendSms();
      return;
    }
    if (this.data.captchaImg) {
      const checkImgCode = (this.data.imgCode || '').trim();
      if (!checkImgCode) {
        this.toast('请填写图形验证码');
        return;
      }
      this.sendSmsV2(checkImgCode);
      return;
    }
    if (!(await this.fetchCaptcha())) {
      this.toast('图形验证码暂不可用，请完成滑块验证');
      this.goCaptcha('sms');
    }
  },

  // 图形码通道发送（v2）；失败自动刷新图形码，刷新也失败则降级滑块
  async sendSmsV2(checkImgCode) {
    const mobile = (this.data.formMobile || '').trim();
    wx.showLoading({ title: '正在发送短信…', mask: true });
    try {
      await request({
        url: '/api/v1/sgcc/login/sms',
        method: 'POST',
        data: this.teamBody({ mobile, checkImgCode }),
      });
      wx.hideLoading();
      this.toast('验证码已发送');
      // 图形码一次性：发送成功即作废，重发时重新取图
      this.setData({ captchaImg: '', imgCode: '' });
      this.startSmsCountdown();
    } catch (err) {
      wx.hideLoading();
      this.toast(err.message);
      if (!(await this.fetchCaptcha())) {
        this.setData({ captchaImg: '', imgCode: '' });
        this.toast('图形验证码暂不可用，请完成滑块验证');
        this.goCaptcha('sms');
      }
    }
  },

  // 滑块通道发送（v3）；失败清空凭据，下次重发前重新拖
  async sendSms() {
    const mobile = (this.data.formMobile || '').trim();
    wx.showLoading({ title: '正在发送短信…', mask: true });
    try {
      await request({
        url: '/api/v1/sgcc/login/sms',
        method: 'POST',
        data: this.teamBody({ mobile, captchaToken: this.data.captchaToken, constId: this.data.constId }),
      });
      wx.hideLoading();
      this.toast('验证码已发送');
      this.startSmsCountdown();
    } catch (err) {
      wx.hideLoading();
      this.toast(err.message);
      // 滑块凭据失效：清空，下次重发前重新拖
      this.setData({ captchaToken: '', constId: '' });
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

  // 登录并绑定：校验表单 → 密码方式无凭据先取图形码（取不到降级滑块）→ 短信码/密码换 token
  async onBind() {
    if (this.data.binding) return;
    if (!this.checkMobile()) return;
    if (this.data.loginType === 'sms' && !(this.data.smsCode || '').trim()) {
      this.toast('请填写短信验证码');
      return;
    }
    if (this.data.loginType === 'pwd') {
      if (!(this.data.password || '').trim()) {
        this.toast('请填写登录密码');
        return;
      }
      // 密码双通道：有滑块凭据走 v4；有图形码走 v3；都没有先取图形码（取不到降级滑块）
      if (!this.data.captchaToken && !this.data.captchaImg) {
        if (!(await this.fetchCaptcha())) {
          this.toast('图形验证码暂不可用，请完成滑块验证');
          this.goCaptcha('bind');
        }
        return;
      }
      if (!this.data.captchaToken && !(this.data.imgCode || '').trim()) {
        this.toast('请填写图形验证码');
        return;
      }
    }
    this.doBind();
  },

  async doBind() {
    if (this.data.binding) return;
    const mobile = (this.data.formMobile || '').trim();
    const body = {
      mobile,
      captchaToken: this.data.captchaToken,
      constId: this.data.constId,
      deviceType: this.data.deviceType,
      systemVersion: this.data.systemVersion,
    };
    if (this.data.loginType === 'pwd') {
      body.password = (this.data.password || '').trim();
      // 无滑块凭据时走图形码通道（token/v3）
      if (!this.data.captchaToken) body.checkImgCode = (this.data.imgCode || '').trim();
    } else {
      body.checkCode = (this.data.smsCode || '').trim();
    }
    this.setData({ binding: true });
    wx.showLoading({ title: '正在绑定…', mask: true });
    try {
      await request({
        url: '/api/v1/sgcc/login/bind',
        method: 'POST',
        data: this.teamBody(body),
      });
      wx.hideLoading();
      this.toast('绑定成功');
      this.setData({ formMobile: '', password: '', smsCode: '', imgCode: '', captchaImg: '', captchaToken: '', constId: '' });
      this.loadAccount();
    } catch (err) {
      wx.hideLoading();
      this.toast(err.message);
      // 滑块凭据失效/登录失败：清空，重试前重新拖
      this.setData({ captchaToken: '', constId: '' });
      // 密码图形码通道失败：自动刷新图形码（取不到自动降级滑块）
      if (this.data.loginType === 'pwd' && this.data.captchaImg) this.onRefreshCaptcha();
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
        this.setData({ bound: false, formMobile: '', password: '', smsCode: '', imgCode: '', captchaImg: '', captchaToken: '', constId: '' });
        this.detectDevice();
      } catch (err) {
        this.toast(err.message);
      }
    }).catch(() => {});
  },

  // 重新登录：回未绑定态表单重新走登录（本地绑定保留，bind 成功后覆盖 token）
  onRelogin() {
    this.setData({ bound: false, formMobile: '', password: '', smsCode: '', imgCode: '', captchaImg: '', captchaToken: '', constId: '' });
  },

  onShareAppMessage() {
    return shareAppMessage(this);
  },
});
