// 树障隐患记录 · 详情页：index 页点卡 wx.navigateTo 进入，记录对象经 eventChannel（'record'/'ctx' 两个事件）带入；
// 全字段网格 + 已处理三列 + 照片三区（辅证只读；处理前/后多选上传、点图预览、长按删除——仅上传者本人或班管/超管）；
// 底部固定条：处理弹层（形态同 index 页批量处理）+ 删除记录（班管/超管，自 index 长按迁入）。
// 本页不回传数据：index 页 onShow 按 _fromDetail 标记重拉列表与看板
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { pad } from '../../../utils/util';

const API_BASE = '/api/v1/treehazard';

// 'YYYY-MM-DD' → 'YYYY.MM.DD'（展示口径）
function fmtDot(s) {
  return s ? String(s).slice(0, 10).replace(/-/g, '.') : '';
}

// 今日 'YYYY-MM-DD'
function todayStr() {
  const d = new Date();
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

// 电压等级幂等显示：纯数字（如 110）→ 拼 kV；已含 kV 后缀（110kV）→ 原样；空 → '—'
function fmtKv(v) {
  const s = String(v === null || v === undefined ? '' : v).trim();
  if (!s) return '—';
  return /^\d+(\.\d+)?$/.test(s) ? `${s}kV` : s;
}

// 空值展示占位
function dash(v) {
  const s = String(v === null || v === undefined ? '' : v).trim();
  return s || '—';
}

// 记录来源文案（treehazard_record.source：crane 直推 / staging 暂存区导入 / intranet 内网回写）
const SOURCE_TEXT = { crane: '排查直推', staging: '暂存导入', intranet: '内网回写' };

Page({
  data: {
    record: null, // index 页 mapRecord 产物（含 pz 照片分区 / overThreshold / 点分日期文本）
    noRecord: false, // 空态兜底：eventChannel 送达有毫秒级时延，超时仍未收到记录才显「不存在」
    fields: [], // 全字段网格 [{label, value, bad}]（bad = 垂直距离 ≤ 阈值红显）
    canManage: false, // 班管/超管（删除记录入口）
    // 处理弹层（形态同 index 页）
    handleVisible: false,
    handleName: '',
    handleNote: '',
    handleDate: '', // YYYY-MM-DD（提交口径）
    handleDateText: '', // yyyy.MM.dd（显示口径）
    handling: false,
    // 处理时间日历（单选，点日期即选中并自动关闭）
    calVisible: false,
    calValue: null,
    minDate: 0,
    maxDate: 0,
  },

  onLoad() {
    const user = getApp().globalData.userInfo || wx.getStorageSync('userInfo') || {};
    this._userId = Number(user.id) || 0; // 当前用户 id（照片「仅上传者本人可删」判定用）
    this._nickname = user.nickname || '';
    this._teamId = 0; // 生效班组（仅超管 >0，由 index 页 ctx 事件带入）
    const now = new Date();
    this.setData({
      canManage: user.role === 'admin' || user.role === 'team_admin',
      // 处理时间可选区间：去年今日 ~ 三个月后（同 index 页口径）
      minDate: new Date(now.getFullYear() - 1, now.getMonth(), now.getDate()).getTime(),
      maxDate: new Date(now.getFullYear(), now.getMonth() + 3, now.getDate()).getTime(),
    });
    const ec = this.getOpenerEventChannel && this.getOpenerEventChannel();
    if (ec) {
      ec.on('record', (record) => {
        if (record && record.id) this.setRecord(record);
      });
      ec.on('ctx', (ctx) => {
        this._teamId = Number(ctx && ctx.teamId) || 0;
      });
    }
    // 超时未收到记录（分享卡片等直接进入场景）→ 显空态
    this._noRecTimer = setTimeout(() => {
      if (!this.data.record) this.setData({ noRecord: true });
    }, 1500);
  },

  onUnload() {
    clearTimeout(this._noRecTimer);
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  // 生效班组 query 片段（仅超管 _teamId>0 时携带，其余角色后端强制本班无需传）
  teamQuery(lead) {
    return this._teamId ? `${lead || '&'}team_id=${this._teamId}` : '';
  },

  // 生效班组 body 注入（POST/PUT/DELETE JSON 用，口径同 teamQuery）
  teamBody(data) {
    return this._teamId ? Object.assign({}, data, { team_id: this._teamId }) : data;
  },

  /* ==================== 记录装配 ==================== */

  // 装入记录并派生全字段网格（垂直距离 ≤ 阈值的格子红显）
  setRecord(record) {
    this.setData({ record, fields: this.buildFields(record) });
  },

  buildFields(r) {
    return [
      { label: '电压等级', value: fmtKv(r.voltage_level) },
      { label: '线路名称', value: dash(r.line_name) },
      { label: '区段', value: dash(r.section) },
      { label: '位置说明', value: dash(r.position_note) },
      { label: '树种×数量', value: r.tree_species ? `${r.tree_species}${r.tree_count ? ` ×${r.tree_count}` : ''}` : '—' },
      { label: '直径', value: dash(r.diameter) },
      { label: '距小号', value: dash(r.dist_small) },
      { label: '距大号', value: dash(r.dist_big) },
      { label: '水平距离', value: dash(r.horizontal) },
      { label: '垂直距离', value: dash(r.vertical), bad: !!r.overThreshold },
      { label: '发现人', value: dash(r.finder_name) },
      { label: '发现时间', value: r.foundAtText || '—' },
      { label: '来源', value: SOURCE_TEXT[r.source] || dash(r.source) },
    ];
  },

  /* ==================== 照片（辅证只读；处理前/后可上传） ==================== */

  // 点击缩略图预览（urls = 该区全部照片地址）
  onPreview(e) {
    const { kind, current } = e.currentTarget.dataset;
    const pz = (this.data.record && this.data.record.pz) || {};
    const urls = (pz[kind] || []).map((p) => p.url);
    if (!urls.length) return;
    wx.previewImage({ current, urls });
  },

  // 「+」格：wx.chooseMedia 多选原图（处理前/后照片大多不止一张）→ 逐张 base64 上传
  onAddPhoto(e) {
    const { kind } = e.currentTarget.dataset;
    if (this._uploading || !this.data.record) return;
    wx.chooseMedia({
      count: 9,
      mediaType: ['image'],
      sizeType: ['original'],
      sourceType: ['album', 'camera'],
      success: (res) => {
        const files = res.tempFiles || [];
        if (files.length) this.uploadPhotos(kind, files);
      },
    });
  },

  // 多选逐张上传（进度 n/m），成功后局部刷新该记录的照片区
  async uploadPhotos(kind, files) {
    this._uploading = true;
    const rec = this.data.record;
    const added = [];
    let failed = 0;
    wx.showLoading({ title: `上传中 0/${files.length}`, mask: true });
    for (const f of files) {
      try {
        const r = await new Promise((resolve, reject) => {
          wx.getFileSystemManager().readFile({
            filePath: f.tempFilePath,
            encoding: 'base64',
            success: resolve,
            fail: () => reject(new Error('图片读取失败')),
          });
        });
        const ext = (String(f.tempFilePath).split('.').pop() || 'jpeg').toLowerCase();
        const mime = ext === 'png' ? 'png' : 'jpeg';
        const photo = await request({
          url: `${API_BASE}/records/${rec.id}/photos?kind=${kind}${this._teamId ? `&team_id=${this._teamId}` : ''}`,
          method: 'POST',
          data: this.teamBody({ image: `data:image/${mime};base64,${r.data}` }),
          timeout: 120000,
        });
        if (photo && photo.url) added.push(photo);
      } catch (err) {
        failed += 1;
      }
      wx.showLoading({ title: `上传中 ${added.length + failed}/${files.length}`, mask: true });
    }
    wx.hideLoading();
    this._uploading = false;
    if (added.length && this.data.record && this.data.record.id === rec.id) {
      const pz = Object.assign({}, rec.pz);
      pz[kind] = pz[kind].concat(added);
      this.setRecord(Object.assign({}, rec, { photos: rec.photos.concat(added), pz }));
    }
    if (!added.length && failed) this.toast('上传失败，请重试');
    else if (failed) this.toast(`上传完成：成功 ${added.length} 张，失败 ${failed} 张`);
    else this.toast(`已上传 ${added.length} 张`);
  },

  // 长按缩略图删除照片（仅处理前/后区绑定；辅证区只读，与网页端对齐）：
  // 仅上传者本人（photo.uploaded_by === 当前用户 id）或班管/超管可删，不满足时长按静默无反应
  onPhotoLongPress(e) {
    const { pid, uploadedBy, kind } = e.currentTarget.dataset;
    const mine = Number(uploadedBy) > 0 && Number(uploadedBy) === this._userId;
    if (!mine && !this.data.canManage) return; // 无权限：保持安静
    const rec = this.data.record;
    if (!rec) return;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '删除照片',
      content: '删除这张照片？',
      confirmBtn: '删除',
      cancelBtn: '取消',
    })
      .then(async () => {
        try {
          await request({
            url: `${API_BASE}/records/${rec.id}/photos/${pid}${this.teamQuery('?')}`,
            method: 'DELETE',
            data: this.teamBody({}),
          });
          if (this.data.record && this.data.record.id === rec.id) {
            const pz = Object.assign({}, rec.pz);
            pz[kind] = pz[kind].filter((p) => Number(p.id) !== Number(pid));
            this.setRecord(Object.assign({}, rec, {
              photos: rec.photos.filter((p) => Number(p.id) !== Number(pid)),
              pz,
            }));
          }
          this.toast('已删除');
        } catch (err) {
          this.toast(err.message);
        }
      })
      .catch(() => {});
  },

  /* ==================== 处理（单条） ==================== */

  // 打开处理弹层：处理人预填当前用户昵称，处理时间默认今日
  onOpenHandle() {
    if (!this.data.record || this.data.record.status === 'handled') return;
    const today = todayStr();
    this.setData({
      handleVisible: true,
      handleName: this._nickname,
      handleNote: '',
      handleDate: today,
      handleDateText: fmtDot(today),
    });
  },

  onCloseHandle() {
    if (this.data.handling) return;
    this.setData({ handleVisible: false });
  },

  onHandleVisibleChange(e) {
    if (!e.detail.visible && !this.data.handling) this.setData({ handleVisible: false });
  },

  onHandleName(e) {
    this.setData({ handleName: e.detail.value });
  },

  onHandleNote(e) {
    this.setData({ handleNote: e.detail.value });
  },

  // 快捷标签（削枝 / 砍伐）：点亮填入处理情况，再点取消；手工输入后标签自动失去选中
  onQuickTag(e) {
    const word = e.currentTarget.dataset.word;
    const checked = e.detail.checked;
    this.setData({ handleNote: checked ? word : '' });
  },

  // 处理时间：展开日历（单选）
  onOpenCal() {
    this.setData({
      calVisible: true,
      calValue: new Date(`${this.data.handleDate}T00:00:00`).getTime(),
    });
  },

  // 单选日历：点日期即选中并自动关闭（confirm-btn 已置空）
  onCalChange(e) {
    const value = e.detail.value;
    if (!value) return;
    const d = new Date(value);
    const date = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
    this.setData({ calVisible: false, handleDate: date, handleDateText: fmtDot(date) });
  },

  onCalClose() {
    this.setData({ calVisible: false });
  },

  // 确认处理：POST /records/:id/handle；成功后本地落处理信息（处理人留空时后端取当前用户昵称，前端同口径回填）
  async onConfirmHandle() {
    if (this.data.handling) return;
    const rec = this.data.record;
    if (!rec) return;
    const note = (this.data.handleNote || '').trim();
    if (!note) {
      this.toast('请填写处理情况');
      return;
    }
    this.setData({ handling: true });
    try {
      const handlerName = (this.data.handleName || '').trim() || this._nickname;
      await request({
        url: `${API_BASE}/records/${rec.id}/handle${this.teamQuery('?')}`,
        method: 'POST',
        data: this.teamBody({
          handler_name: handlerName,
          handle_note: note,
          handled_at: this.data.handleDate,
        }),
      });
      this.toast('已处理');
      this.setRecord(Object.assign({}, rec, {
        status: 'handled',
        handler_name: handlerName,
        handle_note: note,
        handled_at: this.data.handleDate,
        handledAtText: fmtDot(this.data.handleDate),
      }));
      this.setData({ handleVisible: false });
    } catch (err) {
      this.toast(err.message);
    } finally {
      this.setData({ handling: false });
    }
  },

  /* ==================== 删除记录（班管/超管，自 index 长按迁入） ==================== */

  onDeleteRecord() {
    if (!this.data.canManage || !this.data.record) return;
    const rec = this.data.record;
    Dialog.confirm({
      context: this,
      selector: '#t-dialog',
      title: '删除记录',
      content: '删除后不可恢复，记录内照片一并删除，确定删除吗？',
      confirmBtn: '删除',
      cancelBtn: '取消',
    })
      .then(async () => {
        try {
          await request({
            url: `${API_BASE}/records/${rec.id}${this.teamQuery('?')}`,
            method: 'DELETE',
            data: this.teamBody({}),
          });
          this.toast('已删除');
          setTimeout(() => wx.navigateBack({ delta: 1 }), 600); // 留 toast 露出时间；返回后 index onShow 重拉
        } catch (err) {
          this.toast(err.message);
        }
      })
      .catch(() => {});
  },
});
