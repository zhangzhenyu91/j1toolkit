// 团队网盘 · 压缩包预览 / 包内浏览（app_key netdisk 二级页；后端 archive/* 中转 OpenList）
// 进入参数：space + path（压缩包相对空间根的完整路径）+ name
// 流程：先取 meta（不带密码）→ encrypted=true 弹密码输入层（tdesign Dialog 无输入能力，用底部弹层实现），
//   密码经 meta 校验通过后 archive_pass 随 list/download 透传；未加密直接列根目录
// 包内浏览：文件夹行进入（archive/list inner_path，rel 为包内路径）；文件行点按 → 图片/视频 previewMedia
//   （直链；openDocument 不支持媒体），其余类型 archive/download 下载后一律 wx.openDocument 打开（2026-10-09 起）
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../../utils/request';
import { pad, fmtSize, extOf, parseDate } from '../../../utils/util';
import config from '../../../config';
import { fileIconUrl } from '../../fileicon';

const API = '/api/v1/netdisk';

const IMG_EXTS = ['jpg', 'jpeg', 'png', 'gif', 'bmp', 'webp'];
const VIDEO_EXTS = ['mp4', 'mov', 'm4v'];

// 时间 → 'MM.DD'（解析失败给占位）
const fmtDay = (input) => {
  const d = parseDate(input);
  return d ? `${pad(d.getMonth() + 1)}.${pad(d.getDate())}` : '—';
};

Page({
  data: {
    name: '', // 压缩包名（navbar 标题与面包屑根）
    innerPath: '/', // 当前包内目录
    crumbs: [], // 包内面包屑 [{name, path, last}]
    items: [], // 包内行（文件夹在前）
    loading: true,
    error: '', // 读取失败文案（含「已加密待输入密码」态）
    encrypted: false,
    // 密码输入弹层（加密压缩包）
    pwSheet: { open: false, value: '', saving: false },
    keyboardHeight: 0,
  },

  onLoad(options) {
    this._space = decodeURIComponent((options && options.space) || '');
    this._path = decodeURIComponent((options && options.path) || '');
    this._pass = ''; // 压缩包密码（encrypted 时经密码层校验后缓存）
    this.setData({ name: decodeURIComponent((options && options.name) || '') });
    this.boot();
  },

  onPullDownRefresh() {
    if (this.data.pwSheet.open || !this._loaded) {
      wx.stopPullDownRefresh();
      return;
    }
    this.loadInner(this.data.innerPath, false).finally(() => wx.stopPullDownRefresh());
  },

  onRetry() {
    // 错误重试：加密态重新弹密码层，其余重新走 boot
    if (this.data.encrypted && !this._pass) {
      this.setData({ error: '', pwSheet: { open: true, value: '', saving: false } });
      return;
    }
    this.boot();
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  onExpired() {
    wx.removeStorageSync('token');
    wx.removeStorageSync('userInfo');
    wx.reLaunch({ url: '/pages/login/login' });
  },

  /* ==================== 元信息与密码层 ==================== */

  // 先取 meta：encrypted=true 弹密码层；未加密直接列根目录
  async boot() {
    this.setData({ loading: true, error: '' });
    try {
      const meta = await request({
        url: `${API}/archive/meta`,
        method: 'POST',
        data: { space: this._space, path: this._path, ...(this._pass ? { archive_pass: this._pass } : {}) },
      });
      if (meta && meta.encrypted && !this._pass) {
        this.setData({ encrypted: true, loading: false, pwSheet: { open: true, value: '', saving: false } });
        return;
      }
      this.loadInner('/', true);
    } catch (err) {
      this.setData({ loading: false, error: err.message || '压缩包读取失败' });
    }
  },

  onPwInput(e) {
    this.setData({ 'pwSheet.value': e.detail.value });
  },

  onKeyboardHeight(e) {
    const h = e.detail.height || 0;
    this.setData({ keyboardHeight: h > 0 ? h : 0 });
  },

  // 取消输入：回到「已加密」错误态（可点重试再次弹出）
  onPwCancel() {
    if (this.data.pwSheet.saving) return;
    wx.hideKeyboard();
    this.setData({
      keyboardHeight: 0,
      pwSheet: { open: false, value: '', saving: false },
      error: '该压缩包已加密，需输入密码后查看',
    });
  },

  onPwVisibleChange(e) {
    if (e.detail.visible) return;
    if (this.data.pwSheet.saving) {
      this.setData({ 'pwSheet.open': true });
      return;
    }
    if (this.data.pwSheet.open) this.onPwCancel();
  },

  // 提交密码：先经 meta 校验（错误密码会以上游错误返回），通过后缓存并列根目录
  onPwSubmit() {
    const pw = (this.data.pwSheet.value || '').trim();
    if (!pw || this.data.pwSheet.saving) {
      if (!pw) this.toast('请输入压缩包密码');
      return;
    }
    wx.hideKeyboard();
    this.setData({ 'pwSheet.saving': true });
    request({
      url: `${API}/archive/meta`,
      method: 'POST',
      data: { space: this._space, path: this._path, archive_pass: pw },
    }).then(() => {
      this._pass = pw;
      this.setData({ keyboardHeight: 0, pwSheet: { open: false, value: '', saving: false } });
      this.loadInner('/', true);
    }).catch((err) => this.toast(err.message))
      .finally(() => this.setData({ 'pwSheet.saving': false }));
  },

  /* ==================== 包内目录浏览 ==================== */

  // 包内面包屑：压缩包名 / 各级内层目录
  buildCrumbs() {
    const segs = this.data.innerPath.split('/').filter(Boolean);
    const crumbs = [{ name: this.data.name, path: '/', last: segs.length === 0 }];
    segs.forEach((s, i) => {
      crumbs.push({ name: s, path: `/${segs.slice(0, i + 1).join('/')}`, last: i === segs.length - 1 });
    });
    this.setData({ crumbs });
  },

  onCrumbTap(e) {
    const path = e.currentTarget.dataset.path;
    if (path === this.data.innerPath) return;
    this.loadInner(path, true);
  },

  // 加载包内某层目录（isInitial 显示骨架屏；quiet 保留下拉刷新前的旧列表）
  async loadInner(inner, isInitial) {
    if (isInitial) this.setData({ loading: true, error: '', innerPath: inner });
    try {
      const data = await request({
        url: `${API}/archive/list`,
        method: 'POST',
        data: {
          space: this._space,
          path: this._path,
          inner_path: inner,
          ...(this._pass ? { archive_pass: this._pass } : {}),
        },
      });
      const rows = ((data && data.items) || []).map((o) => this.mapRow(o));
      this._loaded = true;
      this.setData({
        innerPath: (data && data.inner_path) || inner,
        items: rows.filter((r) => r.isDir).concat(rows.filter((r) => !r.isDir)),
        loading: false,
        error: '',
      });
      this.buildCrumbs();
    } catch (err) {
      if (isInitial && !this._loaded) this.setData({ loading: false, error: err.message || '压缩包读取失败' });
      else this.toast(err.message);
    }
  },

  mapRow(o) {
    const ext = extOf(o.name);
    const isDir = !!o.is_dir;
    const media = !isDir && (IMG_EXTS.includes(ext) || VIDEO_EXTS.includes(ext));
    return {
      name: o.name,
      isDir,
      ext,
      media,
      letter: isDir ? '' : (ext ? ext.slice(0, 4).toUpperCase() : 'FILE'),
      // 真实文件类型图标（/assets/filetypes/，与 Web 同源）；iconFail 时回退字母块/线性文件夹
      icon: fileIconUrl(o.name, isDir),
      iconFail: false,
      sub: isDir ? `${fmtDay(o.modified)}` : `${fmtSize(o.size)} · ${fmtDay(o.modified)}`,
      rel: o.rel || `/${o.name}`, // 包内路径（下载与下钻用）
    };
  },

  onRowTap(e) {
    const item = e.currentTarget.dataset.item;
    if (item.isDir) {
      this.loadInner(item.rel, true);
      return;
    }
    this.previewFile(item);
  },

  // 类型图标加载失败回退字母块/线性文件夹
  onIconErr(e) {
    this.setData({ [`items[${e.currentTarget.dataset.index}].iconFail`]: true });
  },

  /* ==================== 包内文件预览 / 下载 ==================== */

  // 包内文件下载地址（GET 流；query 带 archive_pass，previewMedia 场景另带 token 鉴权）
  downloadUrl(rel, withToken) {
    let url = `${config.BASE_URL}${API}/archive/download?space=${encodeURIComponent(this._space)}` +
      `&path=${encodeURIComponent(this._path)}&inner=${encodeURIComponent(rel)}`;
    if (this._pass) url += `&archive_pass=${encodeURIComponent(this._pass)}`;
    if (withToken) url += `&token=${encodeURIComponent(wx.getStorageSync('token') || '')}`;
    return url;
  },

  // 文件预览：图片/视频 previewMedia（直链；openDocument 不支持媒体）；其余类型一律下载后 wx.openDocument 打开
  previewFile(item) {
    if (item.media) {
      wx.previewMedia({
        sources: [{ url: this.downloadUrl(item.rel, true), type: IMG_EXTS.includes(item.ext) ? 'image' : 'video' }],
        current: 0,
      });
      return;
    }
    this.downloadFile(item);
  },

  downloadFile(item) {
    wx.showLoading({ title: '下载中…', mask: true });
    wx.downloadFile({
      url: this.downloadUrl(item.rel),
      header: { Authorization: `Bearer ${wx.getStorageSync('token')}` },
      timeout: 120000,
      // 指定本地存储文件名，否则 openDocument 打开后显示的是随机临时文件名（乱码）
      filePath: `${wx.env.USER_DATA_PATH}/${item.name}`,
      success: (res) => {
        if (res.statusCode === 401) {
          this.onExpired();
          return;
        }
        if (res.statusCode !== 200) {
          this.toast(`下载失败（${res.statusCode}）`);
          return;
        }
        wx.openDocument({
          filePath: res.filePath,
          fileType: item.ext,
          showMenu: true, // 右上角菜单可另存/转发
          fail: () => this.toast('该类型暂不支持打开'),
        });
      },
      fail: () => this.toast('网络异常，请检查网络后重试'),
      complete: () => wx.hideLoading(),
    });
  },
});
