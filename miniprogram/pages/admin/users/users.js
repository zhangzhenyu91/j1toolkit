// 员工管理（仅超管）：班组筛选 tab + 分组列表 / 新建 / 编辑（班组下拉 + 角色三选）/ 启停用 / 重置密码
// 班组加载/筛选 tab/分组列表与 perms 页共用（pages/admin/common/user-groups Behavior）
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';
import userGroups from '../common/user-groups';

const ROLE_OPTIONS = [
  { value: 'admin', label: '超级管理员' },
  { value: 'team_admin', label: '班组管理员' },
  { value: 'user', label: '普通用户' },
];
const ROLE_TEXT = { admin: '超级管理员', team_admin: '班组管理员', user: '普通用户' };
const EMPTY_FORM = { id: null, username: '', nickname: '', password: '', team_id: null, role: 'user', status: 1 };

Page({
  behaviors: [userGroups],
  data: {
    keyword: '',
    loading: true,
    showEdit: false,
    saving: false,
    teamOpen: false, // 弹层内班组下拉是否展开
    teamName: '未分配', // 弹层内当前选中班组名
    isSelf: false, // 正在编辑本人（禁用角色降级与禁用）
    roleOptions: ROLE_OPTIONS,
    form: { ...EMPTY_FORM },
  },

  onShow() {
    this.setData({ selfId: (wx.getStorageSync('userInfo') || {}).id || null });
    this.loadTeams();
    this.loadUsers();
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  // 用户列表查询追加关键词（与班组筛选叠加）
  buildUsersQuery(query) {
    const kw = this.data.keyword.trim();
    if (kw) query.push(`keyword=${encodeURIComponent(kw)}`);
    return query;
  },

  // 用户项补充角色文案
  mapUserItem(u) {
    return { ...u, roleText: ROLE_TEXT[u.role] || '普通用户' };
  },

  // 关键词搜索（防抖 300ms，与班组筛选叠加）
  onKeyword(e) {
    this.setData({ keyword: e.detail.value });
    clearTimeout(this._kwTimer);
    this._kwTimer = setTimeout(() => this.loadUsers(), 300);
  },

  // ---------- 新建 / 编辑弹层 ----------
  openAdd() {
    this.setData({
      showEdit: true,
      teamOpen: false,
      isSelf: false,
      teamName: '未分配',
      form: { ...EMPTY_FORM },
    });
  },

  openEdit(e) {
    const user = this.data.users.find((u) => u.id === e.currentTarget.dataset.id);
    if (!user) return;
    this.setData({
      showEdit: true,
      teamOpen: false,
      isSelf: user.id === this.data.selfId,
      teamName: user.team || '未分配',
      form: {
        id: user.id,
        username: user.username,
        nickname: user.nickname || '',
        password: '',
        team_id: user.team_id || null,
        role: user.role || 'user',
        status: user.status,
      },
    });
  },

  closeEdit() {
    this.setData({ showEdit: false });
  },

  onVisibleChange(e) {
    // 遮罩点击关闭
    this.setData({ showEdit: e.detail.visible });
  },

  onField(e) {
    const key = e.currentTarget.dataset.k;
    this.setData({ [`form.${key}`]: e.detail.value });
  },

  // 班组下拉
  toggleTeam() {
    this.setData({ teamOpen: !this.data.teamOpen });
  },

  onTeamPick(e) {
    const raw = e.currentTarget.dataset.id;
    const team_id = raw === '' ? null : Number(raw);
    const teamName = team_id === null
      ? '未分配'
      : ((this.data.teams.find((t) => t.id === team_id) || {}).name || '未分配');
    this.setData({ 'form.team_id': team_id, teamName, teamOpen: false });
  },

  onRolePick(e) {
    if (this.data.isSelf) return;
    this.setData({ 'form.role': e.currentTarget.dataset.value });
  },

  onStatusChange(e) {
    this.setData({ 'form.status': e.detail.value ? 1 : 0 });
  },

  async onSave() {
    if (this.data.saving) return;
    const { id, username, nickname, password, team_id, role, status } = this.data.form;
    if (!id && (!username.trim() || !password)) {
      this.toast('请填写账号和初始密码');
      return;
    }
    if (role === 'team_admin' && !team_id) {
      this.toast('班组管理员必须选择所属班组');
      return;
    }
    this.setData({ saving: true });
    try {
      if (id) {
        const payload = { nickname: nickname.trim(), team_id };
        if (!this.data.isSelf) {
          payload.role = role;
          payload.status = status;
        }
        await request({ url: `/api/v1/admin/users/${id}`, method: 'PUT', data: payload });
      } else {
        await request({
          url: '/api/v1/admin/users',
          method: 'POST',
          data: { username: username.trim(), nickname: nickname.trim(), password, team_id, role },
        });
      }
      this.toast('已保存');
      this.setData({ showEdit: false });
      this.loadUsers();
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ saving: false });
    }
  },

  // ---------- 重置密码 ----------
  resetPassword() {
    const user = this.data.users.find((u) => u.id === this.data.form.id);
    if (!user) return;
    wx.showModal({
      title: `重置密码：${user.nickname || user.username}`,
      editable: true,
      placeholderText: '请输入新密码（至少 6 位）',
      confirmText: '重置',
      success: async (res) => {
        if (!res.confirm) return;
        const password = (res.content || '').trim();
        if (password.length < 6) {
          this.toast('密码至少 6 位');
          return;
        }
        try {
          await request({
            url: `/api/v1/admin/users/${user.id}`,
            method: 'PUT',
            data: { password },
          });
          this.toast('密码已重置');
        } catch (err) {
          this.toast(err.message);
        }
      },
    });
  },

  onShareAppMessage() {
    return shareAppMessage(this, { title: '用户管理' });
  },
});
