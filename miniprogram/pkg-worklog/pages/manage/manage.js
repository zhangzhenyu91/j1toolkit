// 出工日志 · 派车数据管理（超管 / 班组管理员）：车牌号 / 目的地 / 人员 三类字典同构维护（人员支持上移 / 下移排序）+ 杆塔坐标导入
// 班组口径：超管按主页切换器存下的 worklog_team_id 生效（全部请求带 team_id）；
// 班组管理员无需指定（后端强制本班，带上 storage 值也无妨，无则不传）
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { BASE_URL } from '../../../config';
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
    // 杆塔坐标板块（屏六底部）
    towerCount: null, // null=加载中
    // 导入弹层（屏七三步：下载模板 → 选 .xlsx → 确认导入）
    impOpen: false,
    impFile: null, // 待导入文件 {name, path}
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
    // 生效班组：取主页切换器存下的 team_id（仅超管真正生效；其余角色后端强制本班）
    this._teamId = Number(wx.getStorageSync('worklog_team_id')) || 0;
    this.setData({ isAdmin: true });
    this.loadList();
    this.loadTowerCount();
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

  // 当前分段列表
  async loadList() {
    this.setData({ loading: true });
    try {
      const data = await request({ url: `/api/v1/worklog/admin/${this.data.seg}${this.teamQuery('?')}` });
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
            url: `/api/v1/worklog/admin/${this.data.seg}/${id}`,
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

  // 停用 / 启用（删除不提供：被引用时后端拒绝，统一停用）
  async onToggle(e) {
    const { id, status } = e.currentTarget.dataset;
    try {
      await request({
        url: `/api/v1/worklog/admin/${this.data.seg}/${id}`,
        method: 'PUT',
        data: this.teamBody({ status: status === 1 ? 0 : 1 }),
      });
      this.toast(status === 1 ? '已停用' : '已启用');
      this.loadList();
    } catch (err) {
      this.toast(err.message);
    }
  },

  // 成员排序：上移 / 下移（点亮按钮顺序；工作任务单「工作负责人」取排序最前的用车人）
  async onMemberMove(e) {
    const { id, dir } = e.currentTarget.dataset;
    try {
      await request({
        url: `/api/v1/worklog/admin/members/${id}/move`,
        method: 'PUT',
        data: this.teamBody({ dir }),
      });
      this.loadList();
    } catch (err) {
      this.toast(err.message);
    }
  },

  /* ==================== 杆塔坐标板块（屏六/屏七：下载模板 + 导入 Excel 全量替换本班组坐标） ==================== */

  // 条数来自 GET /towers 返回数组长度（全量约 1400 行，只取 length 即丢弃，不进 setData）
  async loadTowerCount() {
    try {
      const data = await request({ url: `/api/v1/worklog/towers${this.teamQuery('?')}` });
      this.setData({ towerCount: data && Array.isArray(data.rows) ? data.rows.length : 0 });
    } catch (err) {
      this.setData({ towerCount: 0 });
      this.toast(err.message);
    }
  },

  // 下载模板：二进制 xlsx，wx.downloadFile 后 wx.openDocument 打开（同备注 Office 附件打开链路）
  onTplDownload() {
    wx.showLoading({ title: '正在下载…', mask: true });
    wx.downloadFile({
      url: `${BASE_URL}/api/v1/worklog/towers/template${this.teamQuery('?')}`,
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

  // ---------- 导入弹层（三步：下载模板 → 选 .xlsx → 确认导入） ----------

  onOpenImport() {
    this.setData({ impOpen: true, impFile: null, importing: false });
  },

  onImpClose() {
    if (this.data.importing) return; // 导入中不允许关
    this.setData({ impOpen: false });
  },

  onImpVisibleChange(e) {
    if (!e.detail.visible && !this.data.importing) this.setData({ impOpen: false });
  },

  // 选择填写好的 Excel（wx.chooseMessageFile 从聊天选取，扩展名白名单仅 .xlsx）
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
        this.setData({ impFile: { name: f.name, path: f.path } });
      },
    });
  },

  // 重选文件
  onImpRemove() {
    if (this.data.importing) return;
    this.setData({ impFile: null });
  },

  // 开始导入：wx.uploadFile POST /towers/import（文件字段名 file，formData 带 team_id）；
  // 成功 toast 后端返回的 message（含导入条数）并刷新板块条数
  onImpStart() {
    const f = this.data.impFile;
    if (!f || this.data.importing) return;
    this.setData({ importing: true });
    wx.uploadFile({
      url: `${BASE_URL}/api/v1/worklog/towers/import`,
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
          this.setData({ impOpen: false });
          this.toast(body.message || '导入完成');
          this.loadTowerCount();
          return;
        }
        this.toast(body.message || `导入失败（${res.statusCode}）`);
      },
      fail: () => this.toast('网络异常，请检查网络后重试'),
      complete: () => this.setData({ importing: false }),
    });
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'work-log', title: '出工日志管理' });
  },
});
