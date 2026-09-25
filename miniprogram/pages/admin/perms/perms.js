// 权限管理（仅管理员）：按员工设置应用授权；员工列表按班组分组 + 顶部班组筛选
// 班组加载/筛选 tab/分组列表与 users 页共用（pages/admin/common/user-groups Behavior）
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';
import userGroups from '../common/user-groups';

Page({
  behaviors: [userGroups],
  data: {
    apps: [],
    selectedUserId: 0,
    checkedMap: {}, // { [app_id]: true/false }
    loading: true,
    saving: false,
  },

  onLoad() {
    this.init();
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

  // 选择员工后加载其已授权应用
  async onSelectUser(e) {
    const id = e.currentTarget.dataset.id;
    if (id === this.data.selectedUserId) return;
    this.setData({ selectedUserId: id, checkedMap: {} });
    try {
      const data = await request({ url: `/api/v1/admin/users/${id}/apps` });
      const checkedMap = {};
      (data.app_ids || []).forEach((appId) => {
        checkedMap[appId] = true;
      });
      this.setData({ checkedMap });
    } catch (err) {
      this.toast(err.message);
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
      this.toast('授权已保存');
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
