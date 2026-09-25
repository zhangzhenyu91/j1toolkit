// 水印照片链路共享（选片分流 + 4:3 裁剪层 + 水印字段编辑公共件 + 相册保存；原在 pkg-wmadd/pages/index
// 与 pkg-worklog/pages/index 逐字重复，抽此复用）。createWmPhoto(options) → Behavior，页面 behaviors 引入：
//   geoBase  /geo 地点天气接口前缀（如 /api/v1/wmadd；高德地图逆编码由后端代理）
//   logTag   console.error 日志标签（如 水印添加）
// 页面侧职责：
//   data 自备 wmVisible/wmPhotoPath/wmForm/quickInputs/wmCode/wmUploading 与 keyboardHeight（含
//   onKeyboardHeight——出工日志多弹层共用，故不入本 Behavior）；提供 toast(message)；
//   proceedWmForm(path)（裁剪完成/免裁后的表单入口，两包实参口径不同，留在页面）；
//   选片入口调 this.chooseWmPhoto(src, onDone)，onDone 一般为 (p) => this.proceedWmForm(p)
// 杆塔三级级联见 utils/tower.js（其 applyTowerToWm 依赖本模块的 jitterCoord/refreshWmGeo，两 Behavior
// 需在页面同时引入）
const { request } = require('./request');
const { pad } = require('./util');

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

function createWmPhoto({ geoBase, logTag }) {
  return Behavior({
    data: {
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

    methods: {
      // ---------- 选片 →（按需 4:3 裁剪）→ onDone 进表单 ----------

      // 按来源取图（camera=拍摄 / album=相册；sizeType 锁定原图取片，不允许压缩上传）后分流判定
      chooseWmPhoto(src, onDone) {
        wx.chooseMedia({
          count: 1,
          mediaType: ['image'],
          sizeType: ['original'],
          sourceType: [src === 'camera' ? 'camera' : 'album'],
          success: (res) => {
            const path = res.tempFiles[0].tempFilePath;
            wx.getImageInfo({
              src: path,
              success: (info) => this.afterPickWmPhoto(path, src, info || {}, onDone),
              fail: () => this.afterPickWmPhoto(path, src, {}, onDone),
            });
          },
        });
      },

      // 取图后分流：拍摄一律进裁剪；相册已为 4:3/3:4（±0.02 容差）则免裁直走 onDone。
      // EXIF 旋转 90/270° 时显示宽高互换；取信息失败按已是 4:3 处理（保持旧流程）
      afterPickWmPhoto(path, src, info, onDone) {
        const rotated = ['left', 'right', 'left-mirrored', 'right-mirrored'].indexOf(info.orientation) >= 0;
        const dispW = rotated ? info.height : info.width;
        const dispH = rotated ? info.width : info.height;
        const ratio = dispW && dispH ? dispW / dispH : 4 / 3;
        const is43 = Math.abs(ratio - 4 / 3) <= 0.02 || Math.abs(ratio - 3 / 4) <= 0.02;
        if (src === 'album' && is43) {
          onDone(path);
          return;
        }
        this.openWmCrop(path, dispW || 4, dispH || 3, info.orientation || '', onDone);
      },

      // ---------- 4:3 裁剪层 ----------
      // 交互：movable-view 拖拽 + 双指缩放（scale 原点为视图中心）；取景框锁定 4:3（横）/ 3:4（纵）
      // 导出：离屏 type=2d canvas 按可视区重绘裁出（createImage 解码应用 EXIF；个别机型未应用时手动旋转兜底）

      // 打开裁剪层：取景框横向顶满屏宽、纵向受高度限制；图片按 cover 适配并居中；
      // onDone 为导出完成回调（两包 proceedWmForm 实参口径不同，由页面选片入口传入）
      openWmCrop(path, dispW, dispH, orientation, onDone) {
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
        this._cropOnDone = onDone;
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

      // 确认裁剪：可视区换算到图片像素 → 离屏 canvas 重绘导出（长边压到 2560 内）→ onDone 进字段编辑弹层
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
                  if (this._cropOnDone) this._cropOnDone(r.tempFilePath);
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

      // ---------- 水印字段编辑公共件 ----------

      onWmInput(e) {
        const { field } = e.currentTarget.dataset;
        this.setData({ [`wmForm.${field}`]: e.detail.value });
        if (field === 'lng' || field === 'lat') this.scheduleWmGeoRefresh(); // 手动改经纬度同样刷新地点/天气
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
        wx.hideKeyboard(); // 关层前收起 hold-keyboard 残留键盘
        this.setData({ wmVisible: false });
      },

      onWmVisibleChange(e) {
        if (!e.detail.visible) {
          wx.hideKeyboard(); // 遮罩关闭同步收起 hold-keyboard 残留键盘
          this.setData({ wmVisible: false });
        }
      },

      // 经纬度补方向后缀：只填数字时自动补 °E/°N（已带符号则原样）
      withDegSuffix(v, suffix) {
        const s = String(v || '').trim();
        if (!s) return '';
        return /[°NSEWnsew]/.test(s) ? s : `${s}${suffix}`;
      },

      // 经纬度随机偏移：角度随机，半径 ≤maxMeters（出工日志历史带入取 400——对 500m 上限留余量；杆塔坐标带入取 50）
      jitterCoord(lng, lat, maxMeters = 400) {
        const r = Math.random() * maxMeters;
        const a = Math.random() * Math.PI * 2;
        const dLat = (r * Math.sin(a)) / 111320;
        const cosLat = Math.cos((lat * Math.PI) / 180);
        const dLng = (r * Math.cos(a)) / (111320 * (Math.abs(cosLat) > 1e-6 ? cosLat : 1e-6));
        return { lng: (lng + dLng).toFixed(6), lat: (lat + dLat).toFixed(6) };
      },

      // 当前定位取值：经纬度直接填；地点/天气调后端 /geo（高德地图）。授权被拒或失败均留空手填（失败原因打控制台）
      fillWmByLocation() {
        wx.getLocation({
          type: 'gcj02',
          success: (loc) => {
            if (!this.data.wmVisible) return; // 弹层已关则不再回填
            if (this.data.wmTowerPicked) return; // 已选杆塔坐标，定位结果不再覆盖
            const lng = loc.longitude.toFixed(6);
            const lat = loc.latitude.toFixed(6);
            this.setData({
              'wmForm.lng': lng,
              'wmForm.lat': lat,
            });
            this._wmGeoKey = `${parseFloat(lng)},${parseFloat(lat)}`; // 与手动改经纬度的去重口径一致
            request({ url: `${geoBase}/geo?lng=${lng}&lat=${lat}`, timeout: 10000 })
              .then((r) => {
                if (!this.data.wmVisible || this.data.wmTowerPicked) return;
                this.setData({
                  'wmForm.weather': this.data.wmForm.weather || (r && r.weather) || '',
                  'wmForm.location': this.data.wmForm.location || (r && r.location) || '',
                });
              })
              .catch((err) => console.error(`[${logTag}] /geo 地点天气获取失败（留空手填）：`, err));
          },
          fail: (err) => console.error(`[${logTag}] wx.getLocation 定位失败（留空手填）：`, err),
        });
      },

      // 经纬度变化（杆塔选定 / 手动修改）统一调后端 /geo（高德地图）覆盖刷新地点/天气；失败清空留空手填（与定位失败口径一致）。
      // 响应仅在弹层仍打开且表单经纬度未被再次改动时应用，避免旧响应覆盖新输入
      refreshWmGeo(lng, lat, tip) {
        lng = parseFloat(lng);
        lat = parseFloat(lat);
        this._wmGeoKey = `${lng},${lat}`; // 记录本次取值坐标，供手动输入防抖去重
        request({ url: `${geoBase}/geo?lng=${lng}&lat=${lat}`, timeout: 10000 })
          .then((r) => {
            if (!this.data.wmVisible) return;
            if (parseFloat(this.data.wmForm.lng) !== lng || parseFloat(this.data.wmForm.lat) !== lat) return;
            this.setData({
              'wmForm.weather': (r && r.weather) || '',
              'wmForm.location': (r && r.location) || '',
            });
            if (tip) this.toast(tip);
          })
          .catch((err) => {
            console.error(`[${logTag}] /geo 地点天气刷新失败（留空手填）：`, err);
            if (!this.data.wmVisible) return;
            if (parseFloat(this.data.wmForm.lng) !== lng || parseFloat(this.data.wmForm.lat) !== lat) return;
            this.setData({ 'wmForm.weather': '', 'wmForm.location': '' });
          });
      },

      // 手动改经纬度：停顿 800ms 防抖后按新坐标刷新地点/天气；
      // 经纬度未填完整或超出合法范围不请求，与上次取值坐标相同则跳过
      scheduleWmGeoRefresh() {
        clearTimeout(this._wmGeoTimer);
        this._wmGeoTimer = setTimeout(() => {
          const lng = parseFloat(this.data.wmForm.lng);
          const lat = parseFloat(this.data.wmForm.lat);
          if (!Number.isFinite(lng) || !Number.isFinite(lat) || Math.abs(lng) > 180 || Math.abs(lat) > 90) return;
          if (`${lng},${lat}` === this._wmGeoKey) return;
          this.refreshWmGeo(lng, lat, '已按经纬度更新地点、天气');
        }, 800);
      },

      // ---------- 相册保存 ----------

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
    },
  });
}

module.exports = { createWmPhoto, genAntiCode, fmtWmTime };
