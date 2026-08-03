// 出工日志 · 派车数据管理（仅 admin）：车牌号 / 目的地 / 人员 三类字典同构维护
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

// 分段配置：接口路径段 / 名称 / 行图标 / 添加占位
const SEGMENTS = [
  { value: 'vehicles', label: '车牌号', icon: 'vehicle', placeholder: '输入新车牌号' },
  { value: 'destinations', label: '目的地', icon: 'location', placeholder: '输入新目的地名称' },
  { value: 'members', label: '人员', icon: 'user', placeholder: '输入新成员姓名' },
];

Page({
  data: {
    isAdmin: false,
    seg: 'vehicles',
    segOptions: SEGMENTS.map((s) => ({ label: s.label, value: s.value })),
    placeholder: SEGMENTS[0].placeholder,
    icon: SEGMENTS[0].icon,
    keyword: '',
    list: [],
    loading: true,
    adding: false,
  },

  async onLoad() {
    // 仅管理员可访问（等启动自检完成再取角色）
    await getApp().globalData.ready;
    const user = getApp().globalData.userInfo || wx.getStorageSync('userInfo') || {};
    if (user.role !== 'admin') {
      this.toast('仅管理员可访问');
      setTimeout(() => wx.navigateBack(), 1200);
      return;
    }
    this.setData({ isAdmin: true });
    this.loadList();
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  segConf() {
    return SEGMENTS.find((s) => s.value === this.data.seg) || SEGMENTS[0];
  },

  // 当前分段列表
  async loadList() {
    this.setData({ loading: true });
    try {
      const data = await request({ url: `/api/v1/worklog/admin/${this.data.seg}` });
      this.setData({ list: (data && data.list) || [] });
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ loading: false });
    }
  },

  // 切换分段
  onSegChange(e) {
    const seg = e.detail.value;
    if (seg === this.data.seg) return;
    const conf = SEGMENTS.find((s) => s.value === seg);
    this.setData({ seg, keyword: '', placeholder: conf.placeholder, icon: conf.icon, list: [] });
    this.loadList();
  },

  onKeywordInput(e) {
    this.setData({ keyword: e.detail.value });
  },

  // 顶部输入即添加
  async onAdd() {
    if (this.data.adding) return;
    const name = (this.data.keyword || '').trim();
    if (!name) {
      this.toast(`请${this.data.placeholder}`);
      return;
    }
    this.setData({ adding: true });
    try {
      await request({
        url: `/api/v1/worklog/admin/${this.data.seg}`,
        method: 'POST',
        data: { name },
      });
      this.setData({ keyword: '' });
      this.toast('已添加');
      this.loadList();
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ adding: false });
    }
  },

  // 编辑名称（系统可输入弹窗）
  onEdit(e) {
    const { id, name } = e.currentTarget.dataset;
    wx.showModal({
      title: `编辑${this.segConf().label}`,
      editable: true,
      content: name,
      placeholderText: '请输入新名称',
      confirmText: '保存',
      success: async (res) => {
        if (!res.confirm) return;
        const newName = (res.content || '').trim();
        if (!newName) {
          this.toast('名称不能为空');
          return;
        }
        if (newName === name) return;
        try {
          await request({
            url: `/api/v1/worklog/admin/${this.data.seg}/${id}`,
            method: 'PUT',
            data: { name: newName },
          });
          this.toast('已保存');
          this.loadList();
        } catch (err) {
          this.toast(err.message);
        }
      },
    });
  },

  // 停用 / 启用（删除不提供：被引用时后端拒绝，统一停用）
  async onToggle(e) {
    const { id, status } = e.currentTarget.dataset;
    try {
      await request({
        url: `/api/v1/worklog/admin/${this.data.seg}/${id}`,
        method: 'PUT',
        data: { status: status === 1 ? 0 : 1 },
      });
      this.toast(status === 1 ? '已停用' : '已启用');
      this.loadList();
    } catch (err) {
      this.toast(err.message);
    }
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'work-log', title: '出工日志管理' });
  },
});
