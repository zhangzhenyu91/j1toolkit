// 权限管理（仅管理员）：按员工设置应用授权；员工列表按班组分组 + 顶部班组筛选
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

Page({
  data: {
    teams: [], // 启用班组（筛选 tab 数据源）
    users: [],
    groups: [], // 分组展示结构 [{ name, users }]
    teamTab: '', // 当前筛选：''=全部 0=未分组 其余=班组 id
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

  async loadTeams() {
    try {
      const data = await request({ url: '/api/v1/admin/teams' });
      this.setData({ teams: (data.list || []).filter((t) => t.status === 1) });
      this.buildGroups(); // 分组依赖 teams+users 两个请求，后到的一方负责重建（此处 users 可能已就绪）
    } catch (err) {
      this.toast(err.message);
    }
  },

  async loadUsers() {
    this.setData({ loading: true });
    try {
      const { teamTab } = this.data;
      const url = `/api/v1/admin/users${teamTab !== '' ? `?team_id=${teamTab}` : ''}`;
      const data = await request({ url });
      const users = (data.list || []).map((u) => ({
        ...u,
        char: (u.nickname || u.username || '?').slice(0, 1),
      }));
      this.setData({ users });
      this.buildGroups();
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ loading: false });
    }
  },

  // 「全部」tab 按班组分组（未分组排最后）；指定 tab 仅一组
  buildGroups() {
    const { teamTab, teams, users } = this.data;
    let groups;
    if (teamTab === '') {
      groups = teams
        .map((t) => ({ name: t.name, users: users.filter((u) => u.team_id === t.id) }))
        .filter((g) => g.users.length > 0);
      const unassigned = users.filter((u) => !u.team_id);
      if (unassigned.length > 0) groups.push({ name: '未分组', users: unassigned });
    } else {
      const name = teamTab === 0 ? '未分组' : ((teams.find((t) => t.id === teamTab) || {}).name || '');
      groups = [{ name, users }];
    }
    this.setData({ groups });
  },

  onTeamTab(e) {
    const raw = e.currentTarget.dataset.id;
    const teamTab = raw === '' ? '' : Number(raw);
    if (teamTab === this.data.teamTab) return;
    this.setData({ teamTab });
    this.loadUsers();
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
