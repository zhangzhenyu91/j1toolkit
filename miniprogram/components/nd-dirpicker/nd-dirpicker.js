// 共享组件 · 网盘目录选择器（各业务「保存到网盘」前在「我的空间」内自选目录；t-popup 底部弹层）
// 用法：页面 json usingComponents 加 "nd-dirpicker": "/components/nd-dirpicker/nd-dirpicker"，
//   wxml：<nd-dirpicker visible="{{x}}" default-path="出工日志" bind:confirm="onDirConfirm" bind:close="onDirClose" />
// properties：
//   visible: Boolean —— 弹层显隐（父页面控制；confirm/close 事件后父页面置 false）
//   defaultPath: String —— 打开时定位目录（如 出工日志，多级用 / 分隔；目录不存在时静默回退根目录）
// events：
//   bind:confirm —— detail = { dir }（我的空间内相对路径，不带首尾斜杠，根目录为 ''）
//   bind:close —— 弹层关闭（遮罩 / 取消 / 关闭按钮；父页面据此将 visible 置 false）
// 接口：POST /api/v1/netdisk/list {space:'my', path}（只列文件夹）；POST /api/v1/netdisk/mkdir {space:'my', path, name}
import Toast from 'tdesign-miniprogram/toast/index';
import { request } from '../../utils/request';

const joinPath = (base, name) => (base === '/' ? `/${name}` : `${base}/${name}`);
// defaultPath（如 出工日志/2026-10）→ 组件内部口径（/出工日志/2026-10；根目录 /）
const toInternal = (p) => {
  const segs = String(p || '').split('/').filter(Boolean);
  return segs.length ? `/${segs.join('/')}` : '/';
};

Component({
  properties: {
    visible: { type: Boolean, value: false },
    defaultPath: { type: String, value: '' },
  },

  data: {
    innerVisible: false, // 弹层显隐内部镜像（保存中拦截遮罩关闭时可强制回开，参照网盘主页弹层口径）
    dir: '/', // 当前目录（/ 为根）
    crumbs: [], // 面包屑 [{name, path, last}]
    dirs: [], // 当前目录下子文件夹 [{name}]
    loading: false,
    error: '',
    // 新建文件夹行内输入（同网盘主页名称校验口径）
    mkOpen: false,
    mkName: '',
    mkSaving: false,
    keyboardHeight: 0,
  },

  observers: {
    visible(v) {
      this.setData({ innerVisible: v });
      if (v) this.openPicker();
    },
  },

  methods: {
    toast(message) {
      Toast({ context: this, selector: '#t-toast', message });
    },

    // 打开：定位 defaultPath（先探测，不存在等异常静默回退根目录）
    async openPicker() {
      const target = toInternal(this.data.defaultPath);
      this.setData({
        dir: target, dirs: [], loading: true, error: '',
        mkOpen: false, mkName: '', mkSaving: false, keyboardHeight: 0,
      });
      this.buildCrumbs();
      try {
        const dirs = await this.fetchDirs(target);
        this.setData({ dirs, loading: false });
      } catch (err) {
        if (target !== '/') {
          this.setData({ dir: '/', dirs: [], loading: true, error: '' });
          this.buildCrumbs();
          this.loadDirs();
          return;
        }
        this.setData({ loading: false, error: err.message || '目录加载失败' });
      }
    },

    // 拉目录（只保留文件夹）
    async fetchDirs(path) {
      const data = await request({ url: '/api/v1/netdisk/list', method: 'POST', data: { space: 'my', path } });
      return ((data && data.items) || []).filter((o) => o.is_dir).map((o) => ({ name: o.name }));
    },

    async loadDirs() {
      this.setData({ loading: true, error: '' });
      this.buildCrumbs();
      try {
        const dirs = await this.fetchDirs(this.data.dir);
        this.setData({ dirs, loading: false });
      } catch (err) {
        this.setData({ loading: false, error: err.message || '目录加载失败' });
      }
    },

    // 面包屑：我的空间 / 各级目录（父级可点回跳）
    buildCrumbs() {
      const segs = this.data.dir.split('/').filter(Boolean);
      const crumbs = [{ name: '我的空间', path: '/', last: segs.length === 0 }];
      segs.forEach((s, i) => {
        crumbs.push({ name: s, path: `/${segs.slice(0, i + 1).join('/')}`, last: i === segs.length - 1 });
      });
      this.setData({ crumbs });
    },

    onCrumbTap(e) {
      const path = e.currentTarget.dataset.path;
      if (path === this.data.dir) return;
      this.setData({ dir: path, dirs: [] });
      this.loadDirs();
    },

    onDirTap(e) {
      this.setData({ dir: joinPath(this.data.dir, e.currentTarget.dataset.name), dirs: [] });
      this.loadDirs();
    },

    /* ---------- 新建文件夹（行内输入） ---------- */

    onMkOpen() {
      this.setData({ mkOpen: true, mkName: '', keyboardHeight: 0 });
    },

    onMkInput(e) {
      this.setData({ mkName: e.detail.value });
    },

    onKeyboardHeight(e) {
      const h = e.detail.height || 0;
      this.setData({ keyboardHeight: h > 0 ? h : 0 });
    },

    onMkCancel() {
      if (this.data.mkSaving) return;
      wx.hideKeyboard(); // 关输入行前收起 hold-keyboard 残留键盘
      this.setData({ mkOpen: false, mkName: '', keyboardHeight: 0 });
    },

    async onMkSave() {
      if (this.data.mkSaving) return;
      const name = (this.data.mkName || '').trim();
      if (!name) {
        this.toast('请输入文件夹名称');
        return;
      }
      if (/[/\\\r\n]/.test(name) || name === '.' || name === '..') {
        this.toast('名称不能包含斜杠或纯点');
        return;
      }
      wx.hideKeyboard();
      this.setData({ mkSaving: true });
      try {
        await request({
          url: '/api/v1/netdisk/mkdir',
          method: 'POST',
          data: { space: 'my', path: this.data.dir, name },
        });
        this.setData({ mkOpen: false, mkName: '', keyboardHeight: 0 });
        this.toast('已创建');
        this.loadDirs();
      } catch (err) {
        this.toast(err.message);
      } finally {
        this.setData({ mkSaving: false });
      }
    },

    /* ---------- 关闭 / 确认 ---------- */

    onCloseTap() {
      if (this.data.mkSaving) return;
      wx.hideKeyboard();
      this.triggerEvent('close');
    },

    onVisibleChange(e) {
      if (e.detail.visible) return;
      // 新建文件夹保存中不允许遮罩关闭（强制回开，同网盘主页弹层口径）
      if (this.data.mkSaving) {
        this.setData({ innerVisible: true });
        return;
      }
      wx.hideKeyboard();
      this.triggerEvent('close');
    },

    onConfirm() {
      if (this.data.loading || this.data.error || this.data.mkSaving) return;
      wx.hideKeyboard();
      // 对外口径：相对路径，不带首尾斜杠，根目录 ''
      this.triggerEvent('confirm', { dir: this.data.dir.replace(/^\/+|\/+$/g, '') });
    },
  },
});
