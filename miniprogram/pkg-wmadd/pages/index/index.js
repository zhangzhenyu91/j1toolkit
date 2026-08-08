// 水印添加 · 移动端独立子应用（app_key wm-add，设计稿 design/wm-add.html）
// 流程：拍摄/相册选片 →（按需 4:3/3:4 裁剪）→ 编辑水印信息（无历史口径：定位带出 + 选择杆塔坐标）
//       → POST /api/v1/wmadd/render 服务端渲染仅回图（不传 COS、不入库、不验证）
//       → 自动存相册（wx.saveImageToPhotosAlbum）→ 微信全屏展示（wx.previewImage）
// 选片/裁剪/编辑/杆塔选择实现均复制自出工日志 pkg-worklog/pages/index，去掉派车/人名/上传验证链路
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { shareAppMessage } from '../../../utils/share';

const pad = (n) => (n < 10 ? `0${n}` : `${n}`);
// 水印拍摄时间格式：2026.07.30 11:02（与今日水印相机样式一致）
const fmtWmTime = (d) =>
  `${d.getFullYear()}.${pad(d.getMonth() + 1)}.${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;

// 防伪码字符集：14 位大写字母+数字，去 0/O、1/I 等易混淆字符（与服务端校验规则一致）
const CODE_CHARS = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789';
const genAntiCode = () => {
  let s = '';
  for (let i = 0; i < 14; i += 1) s += CODE_CHARS[Math.floor(Math.random() * CODE_CHARS.length)];
  return s;
};

Page({
  data: {
    // ---------- 水印字段编辑弹层 ----------
    wmVisible: false,
    wmPhotoPath: '', // 用户所选/裁剪后原图临时路径
    wmForm: { content: '', time: '', weather: '', location: '', lng: '', lat: '' },
    quickInputs: ['110kV', '220kV', 'Ⅰ', 'Ⅱ', '线巡视'], // 快捷输入，点击追加到内容末尾（与出工日志水印施工内容共用）
    wmCode: '', // 防伪码（自动生成，用户不可编辑）
    wmUploading: false,
    // ---------- 选择杆塔坐标弹层 ----------
    wmTowerPicked: null, // 已选杆塔 { level, line, no, lng, lat }；未选为 null（已选后天气/地点行显「已更新」标）
    towerVisible: false,
    towerLoading: false,
    towerRows: null, // 全量行 [电压等级, 线路名称, 杆塔号, 经度, 纬度]（storage 缓存优先，后台静默刷新）
    towerOpen: '', // 当前展开选项列表的级：level / line / tower（''=全收起）
    towerLevels: [],
    towerLevel: '',
    towerLines: [], // 当前展示的线路选项（= 本电压等级全部线路按 towerLineKw 关键字过滤）
    towerLineKw: '', // 线路名称输入框内容（搜索关键字 / 已选线路名）
    towerLine: '',
    towerTowers: [], // [{ no, lng, lat, lngText, latText }]
    towerTower: null,
    // ---------- 4:3 裁剪层（拍摄必裁；相册非 4:3 才裁，横拍锁 4:3 / 纵拍锁 3:4） ----------
    cropVisible: false,
    cropSrc: '', // 待裁原图临时路径
    cropLandscape: true, // true=横向 4:3 / false=纵向 3:4
    cropFrameW: 0, // 取景框尺寸（px）
    cropFrameH: 0,
    cropViewW: 0, // 图片 cover 适配后的显示尺寸（px，未缩放）
    cropViewH: 0,
    cropX: 0, // movable-view 位置（px；缩放原点为视图中心，事件值存于 _cropX/_cropY/_cropScale）
    cropY: 0,
    cropScale: 1,
    cropExporting: false,
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'wm-add', title: '水印添加' });
  },

  // ---------- 选片 →（按需 4:3 裁剪）→ 编辑字段 ----------

  // 起始页两个大选项：拍摄 / 从相册选择
  onPickPhoto(e) {
    const src = e.currentTarget.dataset.src === 'camera' ? 'camera' : 'album';
    wx.chooseMedia({
      count: 1,
      mediaType: ['image'],
      sourceType: [src],
      sizeType: ['original'], // 原图取片（与出工日志加水印同策略）
      success: (res) => {
        const path = res.tempFiles[0].tempFilePath;
        wx.getImageInfo({
          src: path,
          success: (info) => this.afterPickPhoto(path, src, info || {}),
          fail: () => this.afterPickPhoto(path, src, {}),
        });
      },
    });
  },

  // 取图后分流：拍摄一律进裁剪；相册已为 4:3/3:4（±0.02 容差）则免裁直进表单。
  // EXIF 旋转 90/270° 时显示宽高互换；取信息失败按已是 4:3 处理（保持旧流程）
  afterPickPhoto(path, src, info) {
    const rotated = ['left', 'right', 'left-mirrored', 'right-mirrored'].indexOf(info.orientation) >= 0;
    const dispW = rotated ? info.height : info.width;
    const dispH = rotated ? info.width : info.height;
    const ratio = dispW && dispH ? dispW / dispH : 4 / 3;
    const is43 = Math.abs(ratio - 4 / 3) <= 0.02 || Math.abs(ratio - 3 / 4) <= 0.02;
    if (src === 'album' && is43) {
      this.proceedWmForm(path);
      return;
    }
    this.openCrop(path, dispW || 4, dispH || 3, info.orientation || '');
  },

  // 裁剪完成/免裁：记录照片路径、生成防伪码，进字段编辑弹层
  proceedWmForm(path) {
    this.resetTowerState();
    this.setData({ wmPhotoPath: path, wmCode: genAntiCode() });
    this.prefillWmForm();
  },

  // 字段预填（无历史口径）：施工内容留空、拍摄时间取当前、经纬度/地点/天气按当前定位取值（腾讯地图）
  prefillWmForm() {
    this.setData({
      wmVisible: true,
      wmForm: { content: '', time: fmtWmTime(new Date()), weather: '', location: '', lng: '', lat: '' },
    });
    this.fillWmByLocation();
  },

  // 每次进入水印编辑前重置杆塔选择态（towerRows 坐标数据缓存保留，供下次直接打开）
  resetTowerState() {
    this.setData({
      wmTowerPicked: null,
      towerVisible: false,
      towerOpen: '',
      towerLevel: '',
      towerLine: '',
      towerLineKw: '',
      towerLines: [],
      towerTowers: [],
      towerTower: null,
    });
  },

  // 当前定位取值：经纬度直接填；地点/天气调后端 /geo（腾讯地图）。授权被拒或失败均留空手填（失败原因打控制台）
  fillWmByLocation() {
    wx.getLocation({
      type: 'gcj02',
      success: (loc) => {
        if (!this.data.wmVisible) return; // 弹层已关则不再回填
        if (this.data.wmTowerPicked) return; // 已选杆塔坐标，定位结果不再覆盖
        this.setData({
          'wmForm.lng': loc.longitude.toFixed(6),
          'wmForm.lat': loc.latitude.toFixed(6),
        });
        request({ url: `/api/v1/wmadd/geo?lng=${loc.longitude}&lat=${loc.latitude}`, timeout: 10000 })
          .then((r) => {
            if (!this.data.wmVisible || this.data.wmTowerPicked) return;
            this.setData({
              'wmForm.weather': this.data.wmForm.weather || (r && r.weather) || '',
              'wmForm.location': this.data.wmForm.location || (r && r.location) || '',
            });
          })
          .catch((err) => console.error('[水印添加] /geo 地点天气获取失败（留空手填）：', err));
      },
      fail: (err) => console.error('[水印添加] wx.getLocation 定位失败（留空手填）：', err),
    });
  },

  // ---------- 4:3 裁剪层 ----------
  // 交互：movable-view 拖拽 + 双指缩放（scale 原点为视图中心）；取景框锁定 4:3（横）/ 3:4（纵）
  // 导出：离屏 type=2d canvas 按可视区重绘裁出（createImage 解码应用 EXIF；个别机型未应用时手动旋转兜底）

  // 打开裁剪层：取景框横向顶满屏宽、纵向受高度限制；图片按 cover 适配并居中
  openCrop(path, dispW, dispH, orientation) {
    const win = wx.getWindowInfo ? wx.getWindowInfo() : wx.getSystemInfoSync();
    const maxW = win.windowWidth - 48;
    const maxH = Math.max(win.windowHeight - 260, 200); // 标题/提示/按钮预留
    let fw;
    let fh;
    if (dispW >= dispH) {
      fw = maxW;
      fh = (fw * 3) / 4;
    } else {
      fh = Math.min((maxW * 4) / 3, maxH);
      fw = (fh * 3) / 4;
    }
    const k = Math.max(fw / dispW, fh / dispH); // cover 适配
    const vw = dispW * k;
    const vh = dispH * k;
    const x = (fw - vw) / 2;
    const y = (fh - vh) / 2;
    this._cropOrientation = orientation;
    this._cropDispW = dispW;
    this._cropDispH = dispH;
    this._cropX = x;
    this._cropY = y;
    this._cropScale = 1;
    this.setData({
      cropVisible: true,
      cropSrc: path,
      cropLandscape: dispW >= dispH,
      cropFrameW: fw,
      cropFrameH: fh,
      cropViewW: vw,
      cropViewH: vh,
      cropX: x,
      cropY: y,
      cropScale: 1,
      cropExporting: false,
    });
  },

  onCropMove(e) {
    this._cropX = e.detail.x;
    this._cropY = e.detail.y;
  },

  onCropScale(e) {
    this._cropX = e.detail.x;
    this._cropY = e.detail.y;
    this._cropScale = e.detail.scale;
  },

  onCropCancel() {
    this.setData({ cropVisible: false, cropExporting: false });
  },

  onCropVisibleChange(e) {
    if (!e.detail.visible) this.setData({ cropVisible: false });
  },

  // 确认裁剪：可视区换算到图片像素 → 离屏 canvas 重绘导出（长边压到 2560 内）→ 进字段编辑弹层
  onCropConfirm() {
    if (this.data.cropExporting) return;
    this.setData({ cropExporting: true });
    const { cropFrameW: fw, cropFrameH: fh, cropViewW: vw, cropViewH: vh } = this.data;
    const s = this._cropScale || 1;
    const dispW = this._cropDispW || vw;
    const dispH = this._cropDispH || vh;
    // movable-view 缩放原点为中心：取景框左/上缘在图片显示坐标中的位置
    const left = (vw * s) / 2 - (this._cropX + vw / 2);
    const top = (vh * s) / 2 - (this._cropY + vh / 2);
    const sx = Math.max(0, (left * dispW) / (vw * s));
    const sy = Math.max(0, (top * dispH) / (vh * s));
    const sw = Math.min(dispW - sx, (fw * dispW) / (vw * s));
    const sh = Math.min(dispH - sy, (fh * dispH) / (vh * s));
    const outK = Math.min(1, 2560 / Math.max(sw, sh));
    const ow = Math.round(sw * outK);
    const oh = Math.round(sh * outK);
    wx.createSelectorQuery()
      .select('#wmCropCanvas')
      .fields({ node: true })
      .exec((res) => {
        const canvas = res && res[0] && res[0].node;
        if (!canvas) {
          this.setData({ cropExporting: false });
          this.toast('裁剪失败，请重试');
          return;
        }
        canvas.width = ow;
        canvas.height = oh;
        const ctx = canvas.getContext('2d');
        const img = canvas.createImage();
        img.onload = () => {
          ctx.fillStyle = '#000000';
          ctx.fillRect(0, 0, ow, oh); // jpg 无透明通道，兜黑底
          // EXIF 兜底：解码后宽高未按 EXIF 互换（个别机型）时按 orientation 手动旋转
          const swapped = ['left', 'right', 'left-mirrored', 'right-mirrored'].indexOf(this._cropOrientation) >= 0;
          const dimsMatch = Math.abs(img.width - dispW) <= 2 && Math.abs(img.height - dispH) <= 2;
          if (swapped && !dimsMatch) {
            this.drawCropRotated(ctx, img, sx, sy, sw, sh, ow, oh);
          } else {
            ctx.drawImage(img, sx, sy, sw, sh, 0, 0, ow, oh);
          }
          wx.canvasToTempFilePath({
            canvas,
            fileType: 'jpg',
            quality: 0.92,
            success: (r) => {
              // 导出期间用户已取消：丢弃结果，不再进字段编辑弹层
              if (!this.data.cropVisible) return;
              this.setData({ cropVisible: false, cropExporting: false });
              this.proceedWmForm(r.tempFilePath);
            },
            fail: () => {
              this.setData({ cropExporting: false });
              this.toast('裁剪失败，请重试');
            },
          });
        };
        img.onerror = () => {
          this.setData({ cropExporting: false });
          this.toast('图片读取失败');
        };
        img.src = this.data.cropSrc;
      });
  },

  // EXIF 90/270° 手动旋转兜底：sx/sy/sw/sh 为显示坐标系裁剪框，换算到底图原始坐标后旋转绘制
  drawCropRotated(ctx, img, sx, sy, sw, sh, ow, oh) {
    if (this._cropOrientation === 'left' || this._cropOrientation === 'left-mirrored') {
      ctx.translate(0, oh);
      ctx.rotate(-Math.PI / 2);
      ctx.drawImage(img, img.width - sy - sh, sx, sh, sw, 0, 0, oh, ow);
    } else {
      // right / right-mirrored
      ctx.translate(ow, 0);
      ctx.rotate(Math.PI / 2);
      ctx.drawImage(img, sy, img.height - sx - sw, sh, sw, 0, 0, oh, ow);
    }
  },

  // ---------- 水印字段编辑 ----------

  onWmInput(e) {
    const { field } = e.currentTarget.dataset;
    this.setData({ [`wmForm.${field}`]: e.detail.value });
  },

  // 施工内容快捷输入：点击将字符追加到当前输入内容末尾（不超出 textarea 的 maxlength 500）
  onWmQuickInput(e) {
    const { text } = e.currentTarget.dataset;
    if (!text) return;
    const content = (this.data.wmForm.content + text).slice(0, 500);
    this.setData({ 'wmForm.content': content });
  },

  onWmCodeRefresh() {
    this.setData({ wmCode: genAntiCode() });
  },

  onWmCancel() {
    this.setData({ wmVisible: false });
  },

  onWmVisibleChange(e) {
    if (!e.detail.visible) this.setData({ wmVisible: false });
  },

  // 经纬度补方向后缀：只填数字时自动补 °E/°N（已带符号则原样）
  withDegSuffix(v, suffix) {
    const s = String(v || '').trim();
    if (!s) return '';
    return /[°NSEWnsew]/.test(s) ? s : `${s}${suffix}`;
  },

  // 经纬度随机偏移：角度随机，半径 ≤maxMeters（杆塔坐标带入取 50）
  jitterCoord(lng, lat, maxMeters = 400) {
    const r = Math.random() * maxMeters;
    const a = Math.random() * Math.PI * 2;
    const dLat = (r * Math.sin(a)) / 111320;
    const cosLat = Math.cos((lat * Math.PI) / 180);
    const dLng = (r * Math.cos(a)) / (111320 * (Math.abs(cosLat) > 1e-6 ? cosLat : 1e-6));
    return { lng: (lng + dLng).toFixed(6), lat: (lat + dLat).toFixed(6) };
  },

  // ---------- 选择杆塔坐标 ----------

  // 杆塔坐标数据：storage 缓存优先并后台静默刷新；无缓存则请求服务端（与出工日志共用同一 storage 键）
  loadTowerRows() {
    const KEY = 'worklog_towers';
    const cached = wx.getStorageSync(KEY);
    if (cached && Array.isArray(cached.rows) && cached.rows.length) {
      request({ url: '/api/v1/wmadd/towers', timeout: 10000 })
        .then((r) => { if (r && Array.isArray(r.rows) && r.rows.length) wx.setStorageSync(KEY, r); })
        .catch(() => {});
      return Promise.resolve(cached.rows);
    }
    return request({ url: '/api/v1/wmadd/towers', timeout: 10000 }).then((r) => {
      if (!r || !Array.isArray(r.rows) || !r.rows.length) throw new Error('杆塔坐标数据为空');
      wx.setStorageSync(KEY, r);
      return r.rows;
    });
  },

  towerLinesOf(level) {
    const rows = this.data.towerRows || [];
    return [...new Set(rows.filter((r) => r[0] === level).map((r) => r[1]))];
  },

  towerTowersOf(level, line) {
    const rows = this.data.towerRows || [];
    return rows
      .filter((r) => r[0] === level && r[1] === line)
      .map((r) => ({ no: r[2], lng: r[3], lat: r[4], lngText: r[3].toFixed(6), latText: r[4].toFixed(6) }));
  },

  // 「选择杆塔坐标」按钮：打开级联弹层；首次打开需先加载数据（失败关层提示，已选状态保留供重选带回）
  onOpenTower() {
    if (this.data.towerRows) {
      this.setData({ towerVisible: true, towerOpen: '' });
      return;
    }
    this.setData({ towerVisible: true, towerLoading: true });
    this.loadTowerRows()
      .then((rows) => {
        if (!this.data.towerVisible) return;
        this.setData({
          towerRows: rows,
          towerLoading: false,
          towerLevels: [...new Set(rows.map((r) => r[0]))],
        });
      })
      .catch((err) => {
        console.error('[水印添加] 杆塔坐标加载失败：', err);
        this.setData({ towerLoading: false, towerVisible: false });
        this.toast('杆塔坐标加载失败，请稍后重试');
      });
  },

  // 展开/收起某级选项列表（禁用级不响应：选线路需先选电压等级，选杆塔需先选线路名称）；
  // 线路行经箭头展开时恢复完整列表（清空输入过滤，便于改选）
  onTowerToggle(e) {
    const { key } = e.currentTarget.dataset;
    if (key === 'line' && !this.data.towerLevel) return;
    if (key === 'tower' && !this.data.towerLine) return;
    const open = this.data.towerOpen === key ? '' : key;
    const patch = { towerOpen: open };
    if (key === 'line' && open === 'line') patch.towerLines = this.towerLinesOf(this.data.towerLevel);
    this.setData(patch);
  },

  // 选中上级后清空下级并自动展开下一级选项
  onPickLevel(e) {
    const v = e.currentTarget.dataset.v;
    if (v === this.data.towerLevel) {
      this.setData({ towerOpen: '' });
      return;
    }
    this.setData({
      towerLevel: v,
      towerLines: this.towerLinesOf(v),
      towerLine: '',
      towerLineKw: '',
      towerTowers: [],
      towerTower: null,
      towerOpen: 'line',
    });
  },

  // 线路名称输入：按关键字过滤下拉选项并展开；输入与已选值不一致时清空已选及下级
  onTowerLineInput(e) {
    const v = e.detail.value;
    const kw = v.trim();
    const all = this.towerLinesOf(this.data.towerLevel);
    const patch = {
      towerLineKw: v,
      towerLines: kw ? all.filter((n) => n.indexOf(kw) !== -1) : all,
      towerOpen: 'line',
    };
    if (this.data.towerLine && v !== this.data.towerLine) {
      patch.towerLine = '';
      patch.towerTower = null;
      patch.towerTowers = [];
    }
    this.setData(patch);
  },

  onTowerLineFocus() {
    if (this.data.towerLevel) this.setData({ towerOpen: 'line' });
  },

  onPickLine(e) {
    const v = e.currentTarget.dataset.v;
    if (v === this.data.towerLine) {
      this.setData({ towerOpen: '' });
      return;
    }
    this.setData({
      towerLine: v,
      towerLineKw: v,
      towerTowers: this.towerTowersOf(this.data.towerLevel, v),
      towerTower: null,
      towerOpen: 'tower',
    });
  },

  onPickTower(e) {
    const t = this.data.towerTowers[e.currentTarget.dataset.i];
    if (!t) return;
    this.setData({ towerTower: t, towerOpen: '' });
  },

  // 确定：所选杆塔坐标按 ≤50m 随机波动后填入水印表单（不直接带入原值，仍可手改），
  // 并再次调腾讯地图接口按波动后坐标覆盖刷新地点、天气
  onTowerConfirm() {
    const t = this.data.towerTower;
    if (!t) return;
    const jittered = this.jitterCoord(t.lng, t.lat, 50);
    this.setData({
      towerVisible: false,
      wmTowerPicked: { level: this.data.towerLevel, line: this.data.towerLine, no: t.no, lng: t.lng, lat: t.lat },
      'wmForm.lng': jittered.lng,
      'wmForm.lat': jittered.lat,
    });
    this.refreshWmGeoByTower(jittered.lng, jittered.lat);
  },

  // 杆塔选定后调后端 /geo（腾讯地图）覆盖刷新地点/天气；失败清空留空手填（与定位失败口径一致）
  refreshWmGeoByTower(lng, lat) {
    request({ url: `/api/v1/wmadd/geo?lng=${lng}&lat=${lat}`, timeout: 10000 })
      .then((r) => {
        if (!this.data.wmVisible || !this.data.wmTowerPicked) return;
        this.setData({
          'wmForm.weather': (r && r.weather) || '',
          'wmForm.location': (r && r.location) || '',
        });
        this.toast('已按杆塔坐标更新地点、天气');
      })
      .catch((err) => {
        console.error('[水印添加] 杆塔坐标 /geo 刷新失败（地点天气留空手填）：', err);
        if (!this.data.wmVisible) return;
        this.setData({ 'wmForm.weather': '', 'wmForm.location': '' });
      });
  },

  onTowerCancel() {
    this.setData({ towerVisible: false });
  },

  onTowerVisibleChange(e) {
    if (!e.detail.visible) this.setData({ towerVisible: false });
  },

  // ---------- 生成水印 → 存相册 → 全屏展示 ----------

  // 确认：取 EXIF 方向 → 原图 base64 → 连同字段上送服务端渲染（仅回图，不上传存档）
  onWmConfirm() {
    if (this.data.wmUploading) return;
    this.setData({ wmUploading: true });
    wx.getImageInfo({
      src: this.data.wmPhotoPath,
      success: (info) => this.readAndRender((info && info.orientation) || ''),
      fail: () => this.readAndRender(''),
    });
  },

  readAndRender(orientation) {
    const f = this.data.wmForm;
    const wm = {
      content: f.content,
      time: f.time,
      weather: f.weather,
      location: f.location,
      longitude: this.withDegSuffix(f.lng, '°E'),
      latitude: this.withDegSuffix(f.lat, '°N'),
      antiCode: this.data.wmCode,
      orientation,
    };
    wx.getFileSystemManager().readFile({
      filePath: this.data.wmPhotoPath,
      encoding: 'base64',
      success: (r) => {
        const ext = (this.data.wmPhotoPath.split('.').pop() || 'jpeg').toLowerCase();
        const mime = ext === 'png' ? 'png' : 'jpeg';
        this.renderAndSave(`data:image/${mime};base64,${r.data}`, wm);
      },
      fail: () => {
        this.setData({ wmUploading: false });
        this.toast('图片读取失败');
      },
    });
  },

  // 渲染 → 写临时文件 → 自动存相册（授权被拒/保存失败不阻塞展示）→ 原生 toast → wx.previewImage 全屏展示
  async renderAndSave(image, wm) {
    wx.showLoading({ title: '正在生成水印…', mask: true });
    let filePath = '';
    try {
      const data = await request({ url: '/api/v1/wmadd/render', method: 'POST', data: { image, wm }, timeout: 120000 });
      const base64 = data && data.image ? String(data.image).replace(/^data:image\/\w+;base64,/, '') : '';
      if (!base64) throw new Error('水印生成失败');
      filePath = `${wx.env.USER_DATA_PATH}/wmadd_${Date.now()}.jpg`;
      await new Promise((resolve, reject) => {
        wx.getFileSystemManager().writeFile({
          filePath,
          data: base64,
          encoding: 'base64',
          success: resolve,
          fail: () => reject(new Error('图片写入失败')),
        });
      });
    } catch (err) {
      wx.hideLoading();
      this.setData({ wmUploading: false });
      this.toast(err.message || '水印生成失败');
      return;
    }
    wx.showLoading({ title: '正在保存到相册…', mask: true });
    const authed = await this.ensureAlbumAuth();
    let saved = false;
    if (authed) {
      try {
        await this.saveToAlbum(filePath);
        saved = true;
      } catch (e) {
        console.error('[水印添加] 相册保存失败（仍可全屏查看，长按手动保存）：', e);
      }
    }
    wx.hideLoading();
    this.setData({ wmVisible: false, wmUploading: false });
    wx.showToast({
      title: saved ? '已保存到相册' : '已生成，保存相册需授权',
      icon: saved ? 'success' : 'none',
      duration: 1500,
    });
    setTimeout(() => wx.previewImage({ urls: [filePath] }), 1500);
  },

  // 相册授权：先 getSetting，未授权走 authorize，被拒绝返回 false（由调用方按口径提示）
  ensureAlbumAuth() {
    return new Promise((resolve) => {
      wx.getSetting({
        success: (res) => {
          if (res.authSetting['scope.writePhotosAlbum']) {
            resolve(true);
            return;
          }
          wx.authorize({
            scope: 'scope.writePhotosAlbum',
            success: () => resolve(true),
            fail: () => resolve(false),
          });
        },
        fail: () => resolve(false),
      });
    });
  },

  saveToAlbum(filePath) {
    return new Promise((resolve, reject) => {
      wx.saveImageToPhotosAlbum({ filePath, success: resolve, fail: reject });
    });
  },
});
