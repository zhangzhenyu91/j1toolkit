// KVM 远程管理 · 设备状态列表（小程序仅展示状态：无终端/远程控制入口，操作请前往 PC 端壹匣）
// 数据实时代理自 GLKVM Cloud 平台（/api/v1/kvm/devices，需 kvm 应用权限）；下拉刷新 + 30s 静默轮询
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';

// 平台设备状态 → 展示（与网页端 kvm.html 同口径）
const STATUS_MAP = {
  online: { key: 'online', text: '在线' },
  disabled: { key: 'disabled', text: '禁用' },
  offline: { key: 'offline', text: '离线' },
};

const pad = (n) => (n < 10 ? `0${n}` : `${n}`);
// unix 秒 → 'YYYY-MM-DD HH:mm'（同网页端 Shade.fmtDate(d, true)）
const fmtLast = (sec) => {
  const d = new Date(sec * 1000);
  if (Number.isNaN(d.getTime())) return '—';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
};

Page({
  data: {
    gate: false, // 门控（参照首页 gate 模式）
    list: [],
    loading: true, // 首屏加载中
    error: '', // 首屏加载失败文案（已有内容时失败仅 toast）
  },

  onLoad() {
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
    this.setData({ gate: true });
    this.loadDevices('init');
    this.startTimer();
  },

  onShow() {
    if (!this.data.gate) return;
    // 首屏由 passGate 触发 init，此处仅后续进场（切后台回来等）静默刷新
    if (this._loaded) this.loadDevices('auto');
    this.startTimer();
  },

  onHide() {
    this.clearTimer();
  },

  onUnload() {
    this.clearTimer();
  },

  startTimer() {
    this.clearTimer();
    this._timer = setInterval(() => this.loadDevices('auto'), 30000);
  },

  clearTimer() {
    if (this._timer) {
      clearInterval(this._timer);
      this._timer = null;
    }
  },

  onPullDownRefresh() {
    this.loadDevices('manual').finally(() => wx.stopPullDownRefresh());
  },

  onRetry() {
    this.loadDevices('init');
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  // 加载模式：init 首屏/重试（显示加载中文案）；manual 下拉刷新；auto 定时/进场静默
  async loadDevices(mode) {
    if (mode === 'init') this.setData({ loading: true, error: '' });
    try {
      const data = await request({ url: '/api/v1/kvm/devices' });
      const list = (((data && data.items) || [])).map((d) => {
        const st = STATUS_MAP[d.status] || STATUS_MAP.offline;
        return {
          id: d.id,
          statusKey: st.key,
          statusText: st.text,
          group: d.deviceGroupName || '',
          name: d.description || d.ddns || `设备 #${d.id}`,
          ddns: d.ddns || '',
          ip: d.ip || '—',
          mac: d.mac || '—',
          last: d.status === 'online' ? '当前在线' : (d.connectedTime ? fmtLast(d.connectedTime) : '—'),
        };
      });
      this._loaded = true;
      this.setData({ list, loading: false, error: '' });
    } catch (err) {
      // 静默刷新失败不打断页面；已有内容时仅提示
      if (mode === 'auto' || this._loaded) {
        if (mode !== 'auto') this.toast(err.message);
      } else {
        this.setData({ loading: false, error: err.message || '设备列表加载失败' });
      }
    }
  },
});
