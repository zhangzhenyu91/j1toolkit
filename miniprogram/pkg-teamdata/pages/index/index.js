// 班组数据（超管 / 班组管理员）：车牌号 / 目的地 / 人员 三类字典同构维护（人员支持上移 / 下移排序）+ 运维线路信息（杆塔坐标模板下载 / Excel 导入全量替换）
// 后端 /api/v1/teamdata 与出工日志管理端点形态 1:1（交互照搬 pkg-worklog manage 页，仅换基路径）；
// 班组口径：仅超管按出工日志主页切换器存下的 worklog_team_id 生效（全部请求带 team_id）；班组管理员后端强制本班，不传
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { BASE_URL } from '../../../config';
import { shareAppMessage } from '../../../utils/share';

const API_BASE = '/api/v1/teamdata';

// 分段配置：接口路径段 / 名称 / 行图标 / 添加占位（lines 段为运维线路信息，无顶部添加栏）
const SEGMENTS = [
  { value: 'vehicles', label: '车牌号', icon: 'vehicle', placeholder: '输入新车牌号' },
  { value: 'destinations', label: '目的地', icon: 'location', placeholder: '输入新目的地名称' },
  { value: 'members', label: '人员', icon: 'user', placeholder: '输入新成员姓名' },
  { value: 'lines', label: '运维线路信息', icon: 'forest', placeholder: '' },
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
    // 运维线路信息段
    lines: [],
    linesLoading: false,
    importing: false,
  },

  async onLoad() {
    // 超管 / 班组管理员可访问（等启动自检完成再取角色）
    await getApp().globalData.ready;
    const user = getApp().globalData.userInfo || wx.getStorageSync('userInfo') || {};
    if (user.role !== 'admin' && user.role !== 'team_admin') {
      this.toast('仅管理员可访问');
      setTimeout(() => wx.navigateBack(), 1200);
      return;
    }
    // 生效班组：仅超管带切换器存下的 team_id（其余角色后端强制本班，带错值反而越不了权还可能误判，干脆不传）
    this._teamId = user.role === 'admin' ? Number(wx.getStorageSync('worklog_team_id')) || 0 : 0;
    this.setData({ isAdmin: true });
    this.loadList();
    this.loadLines();
  },

  // 生效班组 query 片段（lead 为前导连接符；无选中班组则不携带）
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

  segConf() {
    return SEGMENTS.find((s) => s.value === this.data.seg) || SEGMENTS[0];
  },

  // 当前分段列表（lines 段无字典接口，不查）
  async loadList() {
    if (this.data.seg === 'lines') return;
    this.setData({ loading: true });
    try {
      const data = await request({ url: `${API_BASE}/${this.data.seg}${this.teamQuery('?')}` });
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
    if (seg === 'lines') this.loadLines();
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
        url: `${API_BASE}/${this.data.seg}`,
        method: 'POST',
        data: this.teamBody({ name }),
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
            url: `${API_BASE}/${this.data.seg}/${id}`,
            method: 'PUT',
            data: this.teamBody({ name: newName }),
          });
          this.toast('已保存');
          this.loadList();
        } catch (err) {
          this.toast(err.message);
        }
      },
    });
  },

  // 停用 / 启用
  async onToggle(e) {
    const { id, status } = e.currentTarget.dataset;
    try {
      await request({
        url: `${API_BASE}/${this.data.seg}/${id}`,
        method: 'PUT',
        data: this.teamBody({ status: status === 1 ? 0 : 1 }),
      });
      this.toast(status === 1 ? '已停用' : '已启用');
      this.loadList();
    } catch (err) {
      this.toast(err.message);
    }
  },

  // 删除（仅无引用记录可删；被引用时后端拒绝并提示改停用，toast 原文透出）
  onDelete(e) {
    const { id, name } = e.currentTarget.dataset;
    wx.showModal({
      title: `删除${this.segConf().label}`,
      content: `确认删除「${name}」吗？已被引用的数据无法删除，可改为停用。`,
      confirmText: '删除',
      confirmColor: '#F53F3F',
      success: async (res) => {
        if (!res.confirm) return;
        try {
          await request({
            url: `${API_BASE}/${this.data.seg}/${id}${this.teamQuery('?')}`,
            method: 'DELETE',
            data: this.teamBody({}),
          });
          this.toast('已删除');
          this.loadList();
        } catch (err) {
          this.toast(err.message);
        }
      },
    });
  },

  // 成员排序：上移 / 下移（点亮按钮顺序；工作任务单「工作负责人」取排序最前的用车人）
  async onMemberMove(e) {
    const { id, dir } = e.currentTarget.dataset;
    try {
      await request({
        url: `${API_BASE}/members/${id}/move`,
        method: 'PUT',
        data: this.teamBody({ direction: dir }),
      });
      this.loadList();
    } catch (err) {
      this.toast(err.message);
    }
  },

  /* ==================== 运维线路信息（线路清单 + 杆塔坐标模板下载 / Excel 导入全量替换本班组） ==================== */

  async loadLines() {
    this.setData({ linesLoading: true });
    try {
      const data = await request({ url: `${API_BASE}/lines${this.teamQuery('?')}` });
      this.setData({ lines: Array.isArray(data) ? data : [] });
    } catch (err) {
      this.setData({ lines: [] });
      this.toast(err.message);
    } finally {
      this.setData({ linesLoading: false });
    }
  },

  // 下载导入模板：二进制 xlsx，wx.downloadFile 后 wx.openDocument 打开（同出工日志 manage 页链路）
  onTplDownload() {
    wx.showLoading({ title: '正在下载…', mask: true });
    wx.downloadFile({
      url: `${BASE_URL}${API_BASE}/towers/template${this.teamQuery('?')}`,
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      // 指定本地存储文件名，否则 openDocument 打开后显示的是随机临时文件名（乱码）
      filePath: `${wx.env.USER_DATA_PATH}/杆塔坐标导入模板.xlsx`,
      success: (r) => {
        if (r.statusCode !== 200) {
          this.toast('模板下载失败');
          return;
        }
        wx.openDocument({
          filePath: r.filePath,
          fileType: 'xlsx',
          showMenu: true, // 右上角菜单可另存/转发
          fail: () => this.toast('该类型暂不支持打开'),
        });
      },
      fail: () => this.toast('网络异常，请检查网络后重试'),
      complete: () => wx.hideLoading(),
    });
  },

  // 导入 Excel：wx.chooseMessageFile 选 .xlsx → 确认「全量替换本班组杆塔坐标数据」→ wx.uploadFile（带 Bearer 与 team_id 表单字段）
  onImpPick() {
    if (this.data.importing) return;
    wx.chooseMessageFile({
      count: 1,
      type: 'file',
      extension: ['xlsx'],
      success: (res) => {
        const f = (res.tempFiles || [])[0];
        if (!f) return;
        if (!/\.xlsx$/i.test(f.name || '')) {
          this.toast('仅支持 .xlsx 文件，请使用模板填写');
          return;
        }
        wx.showModal({
          title: '确认导入',
          content: `导入后全量替换本班组杆塔坐标数据，确认导入「${f.name}」吗？`,
          confirmText: '确认导入',
          success: (m) => {
            if (m.confirm) this.doImport(f);
          },
        });
      },
    });
  },

  // 执行导入：成功 toast 后端返回的 message（含导入条数）并刷新线路清单
  doImport(f) {
    if (this.data.importing) return;
    this.setData({ importing: true });
    wx.showLoading({ title: '正在导入…', mask: true });
    wx.uploadFile({
      url: `${BASE_URL}${API_BASE}/towers/import`,
      filePath: f.path,
      name: 'file',
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      formData: this._teamId ? { team_id: String(this._teamId) } : {},
      success: (res) => {
        let body = {};
        try {
          body = JSON.parse(res.data || '{}');
        } catch (e) {
          // 非 JSON 响应按失败处理
        }
        if (res.statusCode >= 200 && res.statusCode < 300 && body.code === 0) {
          this.toast(body.message || '导入完成');
          this.loadLines();
          return;
        }
        this.toast(body.message || `导入失败（${res.statusCode}）`);
      },
      fail: () => this.toast('网络异常，请检查网络后重试'),
      complete: () => {
        wx.hideLoading();
        this.setData({ importing: false });
      },
    });
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'team-data', title: '班组数据' });
  },
});
