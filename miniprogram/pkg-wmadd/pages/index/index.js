// 水印添加 · 移动端独立子应用（app_key wm-add，功能口径见《开发指南》第十三节）
// 流程：拍摄/相册选片 →（按需 4:3/3:4 裁剪）→ 编辑水印信息（无历史口径：定位带出 + 选择杆塔坐标）
//       → POST /api/v1/wmadd/render 服务端渲染仅回图（不传 COS、不入库、不验证）
//       → 自动存相册（wx.saveImageToPhotosAlbum）→ 微信全屏展示（wx.previewImage）
// 选片/裁剪/编辑/杆塔选择与出工日志共用实现：utils/wmphoto.js（选片分流+裁剪层+字段公共件+相册保存）、
// utils/tower.js（杆塔三级级联）；本页仅保留预填口径（无历史）与「渲染回图 → 存相册 → 全屏展示」链路
import Toast from 'tdesign-miniprogram/toast/index';
import { uploadFile } from '../../../utils/request';
import { BASE_URL } from '../../../config';
import { shareAppMessage } from '../../../utils/share';
import { createWmPhoto, genAntiCode, fmtWmTime } from '../../../utils/wmphoto';
import { createTowerCascade } from '../../../utils/tower';

// 水印照片链路共享（/api/v1/wmadd/geo 地点天气）
const wmPhoto = createWmPhoto({ geoBase: '/api/v1/wmadd', logTag: '水印添加' });
// 杆塔三级级联共享（缓存按本人班组 id 隔离，避免串班组的旧缓存；接口不带班组参数，后端按登录人班组取数）
const towerCascade = createTowerCascade({
  towersUrl: () => '/api/v1/wmadd/towers',
  cacheKey: () => {
    const user = getApp().globalData.userInfo || wx.getStorageSync('userInfo') || {};
    return `worklog_towers_${user.team_id || 0}`;
  },
  logTag: '水印添加',
});

Page({
  behaviors: [wmPhoto, towerCascade],
  data: {
    // ---------- 水印字段编辑弹层 ----------
    wmVisible: false,
    wmPhotoPath: '', // 用户所选/裁剪后原图临时路径
    wmForm: { content: '', time: '', weather: '', location: '', lng: '', lat: '' },
    quickInputs: ['110kV', '220kV', 'Ⅰ', 'Ⅱ', '线巡视'], // 快捷输入，点击追加到内容末尾（与出工日志水印施工内容共用）
    wmCode: '', // 防伪码（自动生成，用户不可编辑）
    wmUploading: false,
    // 杆塔级联与 4:3 裁剪层数据（wmTowerPicked/tower*/crop*）由 tower / wmphoto behavior 提供
    // 弹层输入框统一由系统原生避让键盘，不再按键盘高度重排整个弹层。
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  onShareAppMessage() {
    return shareAppMessage(this, { app: 'wm-add', title: '水印添加' });
  },

  // ---------- 选片 →（按需 4:3 裁剪）→ 编辑字段 ----------

  // 起始页两个大选项：拍摄 / 从相册选择（取图与裁剪分流由 wmphoto behavior 的 chooseWmPhoto 完成）
  onPickPhoto(e) {
    const src = e.currentTarget.dataset.src === 'camera' ? 'camera' : 'album';
    this.chooseWmPhoto(src, (p) => this.proceedWmForm(p));
  },

  // 裁剪完成/免裁：记录照片路径、生成防伪码，进字段编辑弹层。
  // 防重复执行：表单已开或上传中时忽略迟到/重复回调，避免重置重绘用户正在编辑的表单
  proceedWmForm(path) {
    if (this.data.wmVisible || this.data.wmUploading) return;
    this.resetTowerState();
    this.setData({ wmPhotoPath: path, wmCode: genAntiCode() });
    this.prefillWmForm();
  },

  // 字段预填（无历史口径）：施工内容留空、拍摄时间取当前、经纬度/地点/天气按当前定位取值（高德地图）
  prefillWmForm() {
    this._wmGeoKey = null; // 「上次取值坐标」记录随表单一起重置（手动改经纬度防抖刷新去重用）
    this.setData({
      wmVisible: true,
      wmForm: {
        content: '',
        time: fmtWmTime(new Date()),
        weather: '',
        location: '',
        lng: '',
        lat: '',
      },
    });
    this.fillWmByLocation();
  },

  // 「选择杆塔坐标」按钮：打开级联弹层（共享实现；首次打开先加载数据，失败关层提示，已选状态保留供重选带回）
  onOpenTower() {
    this.openTowerCascade();
  },

  // 级联「确定」：共享默认分支——所选杆塔坐标按 ≤50m 随机波动后填入水印表单，并按波动后坐标刷新地点、天气
  onTowerConfirm() {
    const t = this.data.towerTower;
    if (!t) return;
    wx.hideKeyboard(); // 关层前收起 hold-keyboard 残留键盘
    this.applyTowerToWm(t);
  },

  // ---------- 生成水印 → 存相册 → 全屏展示 ----------

  // 确认：取 EXIF 方向 → multipart 原图与字段上送渲染 → 下载短时结果（不入库）
  onWmConfirm() {
    if (this.data.wmUploading) return;
    wx.hideKeyboard(); // 确认前收起 hold-keyboard 残留键盘
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
    this.renderAndSave(this.data.wmPhotoPath, wm);
  },

  // 原图二进制渲染 → 下载短时结果 → 自动存相册 → 全屏展示。
  async renderAndSave(filePath, wm) {
    wx.showLoading({ title: '正在生成水印…', mask: true });
    let resultPath = '';
    try {
      const data = await uploadFile({
        url: '/api/v1/wmadd/render',
        filePath,
        name: 'image',
        data: { wm: JSON.stringify(wm) },
        timeout: 120000,
      });
      if (!data || !data.url) throw new Error('水印生成失败');
      resultPath = await new Promise((resolve, reject) => {
        wx.downloadFile({
          url: BASE_URL + data.url,
          header: { Authorization: `Bearer ${wx.getStorageSync('token') || ''}` },
          timeout: 120000,
          success: (res) => {
            if (res.statusCode >= 200 && res.statusCode < 300) resolve(res.tempFilePath);
            else reject(new Error('水印结果下载失败'));
          },
          fail: () => reject(new Error('水印结果下载失败')),
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
        await this.saveToAlbum(resultPath);
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
    setTimeout(() => wx.previewImage({ urls: [resultPath] }), 1500);
  },
});
