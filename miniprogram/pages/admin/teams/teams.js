// 班组管理（仅超管）：列表 / 新增 / 编辑（改名级联安全日目录）/ 启停用 / 删除
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

const EMPTY_FORM = { id: null, name: '', status: 1 };

Page({
  data: {
    teams: [],
    loading: true,
    showEdit: false,
    saving: false,
    form: { ...EMPTY_FORM },
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
      const data = await request({ url: '/api/v1/admin/teams' });
      const teams = (data.list || []).map((t) => ({
        ...t,
        // 首个启用班组即「默认班组」（既有数据迁移归属 + KVM 设备回退来源）
        isDefault: false,
      }));
      const firstEnabled = teams.find((t) => t.status === 1);
      if (firstEnabled) firstEnabled.isDefault = true;
      this.setData({ teams });
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ loading: false });
    }
  },

  // ---------- 新增 / 编辑弹层 ----------
  openAdd() {
    this.setData({ showEdit: true, form: { ...EMPTY_FORM } });
  },

  openEdit(e) {
    const team = this.data.teams[e.currentTarget.dataset.index];
    this.setData({
      showEdit: true,
      form: {
        id: team.id,
        name: team.name,
        status: team.status,
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

  onStatusChange(e) {
    this.setData({ 'form.status': e.detail.value ? 1 : 0 });
  },

  async onSave() {
    if (this.data.saving) return;
    const { id, name, status } = this.data.form;
    if (!name.trim()) {
      this.toast('请填写班组名称');
      return;
    }
    this.setData({ saving: true });
    try {
      if (id) {
        await request({
          url: `/api/v1/admin/teams/${id}`,
          method: 'PUT',
          data: { name: name.trim(), status },
        });
      } else {
        await request({
          url: '/api/v1/admin/teams',
          method: 'POST',
          data: { name: name.trim() },
        });
      }
      this.toast('已保存');
      this.setData({ showEdit: false });
      this.load();
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ saving: false });
    }
  },

  // ---------- 删除（仅无任何数据的班组可删，409 时展示后端提示） ----------
  onDelete() {
    const { id, name } = this.data.form;
    if (!id) return;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '删除班组',
      content: `确定删除「${name}」吗？删除仅限无任何数据的班组，有数据的班组请改为停用。`,
      confirmBtn: '删除',
      cancelBtn: '取消',
    }).then(async () => {
      try {
        await request({ url: `/api/v1/admin/teams/${id}`, method: 'DELETE' });
        this.toast('已删除');
        this.setData({ showEdit: false });
        this.load();
      } catch (err) {
        this.toast(err.message);
      }
    }).catch(() => {});
  },

  onShareAppMessage() {
    return shareAppMessage(this, { title: '班组管理' });
  },
});
