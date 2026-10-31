// 权限管理（仅管理员）：按员工设置应用授权；员工列表按班组分组 + 顶部班组筛选
// 班组加载/筛选 tab/分组列表与 users 页共用（pages/admin/common/user-groups Behavior）
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { createRequestGuard } from '../../../utils/generation';
import { shareAppMessage } from '../../../utils/share';
import userGroups from '../common/user-groups';
import { iconColors } from '../../common/icon-theme';

Page({
  behaviors: [userGroups],
  data: {
    ico: iconColors('light'), // t-icon 图标色计算值（随系统主题刷新）
    apps: [],
    selectedUserId: 0,
    checkedMap: {}, // { [app_id]: true/false }
    loading: true,
    saving: false,
  },

  onLoad() {
    this._userGuard = createRequestGuard(); // 选中员工请求代际：旧响应不得覆盖新选中项
    // t-icon 图标色按系统主题计算，主题变化时刷新
    this._onThemeChange = (res) => this.setData({ ico: iconColors(res && res.theme) });
    this.setData({ ico: iconColors() });
    wx.onThemeChange(this._onThemeChange);
    this.init();
  },

  onShow() {
    this.setData({ ico: iconColors() });
  },

  // 页面卸载解除主题监听
  onUnload() {
    if (this._onThemeChange) {
      wx.offThemeChange(this._onThemeChange);
      this._onThemeChange = null;
    }
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  async init() {
    this.loadTeams();
    this.loadUsers();
    try {
      const appsData = await request({ url: '/api/v1/admin/apps' });
      this.setData({ apps: appsData.list || [] });
    } catch (err) {
      this.toast(err.message);
    }
  },

  // 选择员工后加载其已授权应用（只接受当前选中员工的响应，快速切换时旧响应丢弃）
  async onSelectUser(e) {
    const id = e.currentTarget.dataset.id;
    if (id === this.data.selectedUserId) return;
    const ticket = this._userGuard.begin(id);
    this.setData({ selectedUserId: id, checkedMap: {} });
    try {
      const data = await request({ url: `/api/v1/admin/users/${id}/apps` });
      if (!this._userGuard.isCurrent(ticket)) return;
      const checkedMap = {};
      (data.app_ids || []).forEach((appId) => {
        checkedMap[appId] = true;
      });
      this.setData({ checkedMap });
    } catch (err) {
      if (this._userGuard.isCurrent(ticket)) this.toast(err.message);
    }
  },

  onToggle(e) {
    const id = e.currentTarget.dataset.id;
    const key = `checkedMap.${id}`;
    this.setData({ [key]: !this.data.checkedMap[id] });
  },

  // 保存：全量替换该员工的授权
  async onSave() {
    const { selectedUserId, checkedMap, saving } = this.data;
    if (saving) return;
    if (!selectedUserId) {
      this.toast('请先选择员工');
      return;
    }
    const appIds = Object.keys(checkedMap)
      .filter((k) => checkedMap[k])
      .map(Number);
    this.setData({ saving: true });
    try {
      await request({
        url: `/api/v1/admin/users/${selectedUserId}/apps`,
        method: 'PUT',
        data: { app_ids: appIds },
      });
      if (this.data.selectedUserId === selectedUserId) this.toast('授权已保存');
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ saving: false });
    }
  },

  onShareAppMessage() {
    return shareAppMessage(this, { title: '权限管理' });
  },
});
