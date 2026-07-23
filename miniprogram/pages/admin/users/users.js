// 员工管理（仅管理员）：列表 / 新建 / 禁用·启用 / 重置密码
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';

Page({
  data: {
    users: [],
    loading: true,
    showAdd: false,
    saving: false,
    form: { username: '', nickname: '', team: '', password: '' },
  },

  onShow() {
    this.load();
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  async load() {
    this.setData({ loading: true });
    try {
      const data = await request({ url: '/api/v1/admin/users' });
      const users = (data.list || []).map((u) => ({
        ...u,
        char: (u.nickname || u.username || '?').slice(0, 1),
      }));
      this.setData({ users });
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ loading: false });
    }
  },

  // ---------- 新建员工 ----------
  openAdd() {
    this.setData({
      showAdd: true,
      form: { username: '', nickname: '', team: '检修一班', password: '' },
    });
  },

  closeAdd() {
    this.setData({ showAdd: false });
  },

  onField(e) {
    const key = e.currentTarget.dataset.k;
    this.setData({ [`form.${key}`]: e.detail.value });
  },

  async onAddConfirm() {
    if (this.data.saving) return;
    const { username, nickname, team, password } = this.data.form;
    if (!username.trim() || !password) {
      this.toast('请填写账号和初始密码');
      return;
    }
    this.setData({ saving: true });
    try {
      await request({
        url: '/api/v1/admin/users',
        method: 'POST',
        data: {
          username: username.trim(),
          nickname: nickname.trim(),
          team: team.trim(),
          password,
        },
      });
      this.toast('已创建');
      this.setData({ showAdd: false });
      this.load();
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ saving: false });
    }
  },

  // ---------- 员工操作 ----------
  onActions(e) {
    const user = this.data.users[e.currentTarget.dataset.index];
    const items = [user.status === 1 ? '禁用账号' : '启用账号', '重置密码'];
    wx.showActionSheet({
      itemList: items,
      success: ({ tapIndex }) => {
        if (tapIndex === 0) this.toggleStatus(user);
        if (tapIndex === 1) this.resetPassword(user);
      },
    });
  },

  // 禁用 / 启用
  toggleStatus(user) {
    const disabling = user.status === 1;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: disabling ? '禁用账号' : '启用账号',
      content: `确定${disabling ? '禁用' : '启用'}「${user.nickname || user.username}」吗？${
        disabling ? '禁用后该账号将无法登录。' : ''
      }`,
      confirmBtn: '确定',
      cancelBtn: '取消',
    }).then(async () => {
      try {
        await request({
          url: `/api/v1/admin/users/${user.id}`,
          method: 'PUT',
          data: { status: disabling ? 0 : 1 },
        });
        this.toast(disabling ? '已禁用' : '已启用');
        this.load();
      } catch (err) {
        this.toast(err.message);
      }
    }).catch(() => {});
  },

  // 重置密码
  resetPassword(user) {
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
});
