// 班组数据（超管 / 班组管理员）：车牌号 / 目的地（含市外/驻地互斥标记）/ 人员 三类字典同构维护（人员支持上移 / 下移排序）
// + 杆塔信息（线路清单 + 杆塔坐标模板下载 / Excel 导入全量替换）+ 运维线路（双输入添加 / 排序 / 模板下载 / Excel 导入全量替换）
// 后端 /api/v1/teamdata 与出工日志管理端点形态 1:1（交互照搬 pkg-worklog manage 页，仅换基路径）；
// 班组口径：仅超管按出工日志主页切换器存下的 worklog_team_id 生效（全部请求带 team_id）；班组管理员后端强制本班，不传
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { BASE_URL } from '../../../config';
import { shareAppMessage } from '../../../utils/share';

const API_BASE = '/api/v1/teamdata';

// 分段配置：接口路径段 / 名称 / 行图标 / 添加占位（lines 杆塔信息段与 oplines 运维线路段为独立块，非字典同构）
const SEGMENTS = [
  { value: 'vehicles', label: '车牌号', icon: 'vehicle', placeholder: '输入新车牌号' },
  { value: 'destinations', label: '目的地', icon: 'location', placeholder: '输入新目的地名称' },
  { value: 'members', label: '人员', icon: 'user', placeholder: '输入新成员姓名' },
  { value: 'lines', label: '杆塔信息', icon: 'forest', placeholder: '' },
  { value: 'oplines', label: '运维线路', icon: 'thunder', placeholder: '' },
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
    // 杆塔信息段
    lines: [],
    linesLoading: false,
    importing: false,
    // 运维线路段
    oplines: [],
    opLoading: false,
    opVolt: '',
    opName: '',
    opAdding: false,
    // 目的地编辑弹层（名称 + 市外/驻地互斥标记）
    destEditVisible: false,
    destEditId: 0,
    destEditName: '',
    destOutOfCity: 0,
    destStationed: 0,
    destSaving: false,
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
    this.loadOplines();
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

  // 当前分段列表（lines / oplines 段为独立块，不走字典接口）
  async loadList() {
    if (this.data.seg === 'lines' || this.data.seg === 'oplines') return;
    this.setData({ loading: true });
    try {
      const data = await request({ url: `${API_BASE}/${this.data.seg}${this.teamQuery('?')}` });
      const rows = (data && data.list) || [];
      // 目的地行补市外/驻地标记数值化（兜底后端直出为非数字）
      const list = this.data.seg === 'destinations'
        ? rows.map((it) => Object.assign({}, it, {
          out_of_city: Number(it.out_of_city) || 0,
          stationed: Number(it.stationed) || 0,
        }))
        : rows;
      this.setData({ list });
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
    if (seg === 'oplines') this.loadOplines();
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

  // 编辑名称（目的地段开自绘弹层带市外/驻地互斥标记；其余段用系统可输入弹窗）
  onEdit(e) {
    const { id, name } = e.currentTarget.dataset;
    if (this.data.seg === 'destinations') {
      const item = (this.data.list || []).find((it) => it.id === id) || {};
      this.setData({
        destEditVisible: true,
        destEditId: id,
        destEditName: name,
        destOutOfCity: Number(item.out_of_city) || 0,
        destStationed: Number(item.stationed) || 0,
      });
      return;
    }
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

  /* ==================== 目的地编辑弹层（名称 + 市外/驻地互斥标记，费用口径同出工日志） ==================== */

  onDestEditVisibleChange(e) {
    if (!e.detail.visible && !this.data.destSaving) this.setData({ destEditVisible: false });
  },

  onCloseDestEdit() {
    if (this.data.destSaving) return;
    this.setData({ destEditVisible: false });
  },

  onDestNameInput(e) {
    this.setData({ destEditName: e.detail.value });
  },

  // 市外 / 驻地互斥：点亮一个即清另一个，再点取消
  onDestTag(e) {
    const k = e.currentTarget.dataset.k;
    const checked = e.detail.checked;
    if (k === 'ooc') {
      this.setData({ destOutOfCity: checked ? 1 : 0, destStationed: checked ? 0 : this.data.destStationed });
    } else {
      this.setData({ destStationed: checked ? 1 : 0, destOutOfCity: checked ? 0 : this.data.destOutOfCity });
    }
  },

  // 保存：名称 + 两标记一并 PUT（后端同置 1 会拒绝，前端已互斥不会触发）
  async onDestEditSave() {
    if (this.data.destSaving) return;
    const name = (this.data.destEditName || '').trim();
    if (!name) {
      this.toast('目的地名称不能为空');
      return;
    }
    this.setData({ destSaving: true });
    try {
      await request({
        url: `${API_BASE}/destinations/${this.data.destEditId}`,
        method: 'PUT',
        data: this.teamBody({
          name,
          out_of_city: this.data.destOutOfCity,
          stationed: this.data.destStationed,
        }),
      });
      this.toast('已保存');
      this.setData({ destEditVisible: false });
      this.loadList();
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ destSaving: false });
    }
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

  /* ==================== 杆塔信息（线路清单 + 杆塔坐标模板下载 / Excel 导入全量替换本班组） ==================== */

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

  /* ==================== 运维线路（team_line：双输入添加 / 编辑 / 删除 / 上移下移 + 模板下载 / Excel 导入全量替换本班组） ==================== */

  // 电压等级展示幂等格式化：纯数字补 kV 后缀，已含 kV 或其他文本原样
  fmtVoltage(v) {
    const s = String(v || '').trim();
    return /^\d+$/.test(s) ? `${s}kV` : s;
  },

  async loadOplines() {
    this.setData({ opLoading: true });
    try {
      const data = await request({ url: `${API_BASE}/oplines${this.teamQuery('?')}` });
      const rows = Array.isArray(data) ? data : ((data && data.list) || []);
      this.setData({
        oplines: rows.map((it) => Object.assign({}, it, { voltageText: this.fmtVoltage(it.voltage_level) })),
      });
    } catch (err) {
      this.setData({ oplines: [] });
      this.toast(err.message);
    } finally {
      this.setData({ opLoading: false });
    }
  },

  onOpVoltInput(e) {
    this.setData({ opVolt: e.detail.value });
  },

  onOpNameInput(e) {
    this.setData({ opName: e.detail.value });
  },

  // 顶部双输入添加（电压等级 + 线路名称，缺一不可）
  async onOpAdd() {
    if (this.data.opAdding) return;
    const voltage = (this.data.opVolt || '').trim();
    const lineName = (this.data.opName || '').trim();
    if (!voltage) {
      this.toast('请输入电压等级');
      return;
    }
    if (!lineName) {
      this.toast('请输入线路名称');
      return;
    }
    this.setData({ opAdding: true });
    try {
      await request({
        url: `${API_BASE}/oplines`,
        method: 'POST',
        data: this.teamBody({ voltage_level: voltage, line_name: lineName }),
      });
      this.setData({ opVolt: '', opName: '' });
      this.toast('已添加');
      this.loadOplines();
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ opAdding: false });
    }
  },

  // 编辑（系统可输入弹窗只有一个输入框：电压等级与线路名同条编辑、空格分隔，按首个空白切分）
  onOpEdit(e) {
    const { id, volt, name } = e.currentTarget.dataset;
    wx.showModal({
      title: '编辑运维线路',
      editable: true,
      content: `${volt} ${name}`,
      placeholderText: '电压等级 线路名称（空格分隔）',
      confirmText: '保存',
      success: async (res) => {
        if (!res.confirm) return;
        const text = (res.content || '').trim();
        const sep = text.search(/\s/);
        const voltage = sep < 0 ? text : text.slice(0, sep);
        const lineName = sep < 0 ? '' : text.slice(sep).trim();
        if (!voltage || !lineName) {
          this.toast('请输入「电压等级 线路名称」，空格分隔');
          return;
        }
        if (voltage === String(volt) && lineName === String(name)) return;
        try {
          await request({
            url: `${API_BASE}/oplines/${id}`,
            method: 'PUT',
            data: this.teamBody({ voltage_level: voltage, line_name: lineName }),
          });
          this.toast('已保存');
          this.loadOplines();
        } catch (err) {
          this.toast(err.message);
        }
      },
    });
  },

  // 删除（无外键引用拦截；树障记录按 line_name 文本关联，后端不拦）
  onOpDelete(e) {
    const { id, name } = e.currentTarget.dataset;
    wx.showModal({
      title: '删除运维线路',
      content: `确认删除「${name}」吗？`,
      confirmText: '删除',
      confirmColor: '#F53F3F',
      success: async (res) => {
        if (!res.confirm) return;
        try {
          await request({
            url: `${API_BASE}/oplines/${id}${this.teamQuery('?')}`,
            method: 'DELETE',
            data: this.teamBody({}),
          });
          this.toast('已删除');
          this.loadOplines();
        } catch (err) {
          this.toast(err.message);
        }
      },
    });
  },

  // 排序：上移 / 下移（与班内相邻线路换位，direction 口径同成员排序）
  async onOpMove(e) {
    const { id, dir } = e.currentTarget.dataset;
    try {
      await request({
        url: `${API_BASE}/oplines/${id}/move`,
        method: 'PUT',
        data: this.teamBody({ direction: dir }),
      });
      this.loadOplines();
    } catch (err) {
      this.toast(err.message);
    }
  },

  // 下载导入模板：二进制 xlsx，wx.downloadFile 后 wx.openDocument 打开（同杆塔坐标模板链路）
  onOpTplDownload() {
    wx.showLoading({ title: '正在下载…', mask: true });
    wx.downloadFile({
      url: `${BASE_URL}${API_BASE}/oplines/template${this.teamQuery('?')}`,
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      // 指定本地存储文件名，否则 openDocument 打开后显示的是随机临时文件名（乱码）
      filePath: `${wx.env.USER_DATA_PATH}/运维线路导入模板.xlsx`,
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

  // 导入 Excel：wx.chooseMessageFile 选 .xlsx → 确认「全量替换本班组运维线路数据」→ wx.uploadFile（带 Bearer 与 team_id 表单字段）
  onOpImpPick() {
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
          content: `导入后全量替换本班组运维线路数据，确认导入「${f.name}」吗？`,
          confirmText: '确认导入',
          success: (m) => {
            if (m.confirm) this.doOpImport(f);
          },
        });
      },
    });
  },

  // 执行导入：成功 toast 后端返回的 message（含导入条数）并刷新运维线路列表
  doOpImport(f) {
    if (this.data.importing) return;
    this.setData({ importing: true });
    wx.showLoading({ title: '正在导入…', mask: true });
    wx.uploadFile({
      url: `${BASE_URL}${API_BASE}/oplines/import`,
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
          this.loadOplines();
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
