// 团队网盘 · 分享管理（app_key netdisk 二级页；设计稿 design/小程序-团队网盘.html 屏④）
// 分段「我创建的 / 全部（班管）」：全部仅班管（team_admin）/超管（admin）可见，
// 无权限隐藏分段；仍请求到 40304 时兜底回退「我创建的」并隐藏分段
// 操作按 state 显隐：active=复制链接/停用/删除；disabled=复制链接/重新启用/删除；expired=删除记录
// （expired 不可重新启用，后端 40030；重新启用 / 停用 / 删除由后端校验本人或班管/超管）
// 分享链接口径：{BASE_URL}/share.html#/s/{share_id}（小程序内无法直接打开网页，仅复制）
import Toast from 'tdesign-miniprogram/toast/index';
import Dialog from 'tdesign-miniprogram/dialog/index';
import { request } from '../../../utils/request';
import { pad, extOf, parseDate } from '../../../utils/util';
import config from '../../../config';

const API = '/api/v1/netdisk';

// 状态胶囊（全端色族：生效中绿 / 已过期橙 / 已停用红）
const STATE_MAP = {
  active: { cls: 'st-ok', text: '生效中' },
  expired: { cls: 'st-warn', text: '已过期' },
  disabled: { cls: 'st-fail', text: '已停用' },
};

// 时间 → 'MM.DD'（解析失败给占位）
const fmtDay = (input) => {
  const d = parseDate(input);
  return d ? `${pad(d.getMonth() + 1)}.${pad(d.getDate())}` : '—';
};

// 状态 → 底部操作行（复制链接蓝 / 停用 / 重新启用蓝 / 删除红）
function opsOf(state) {
  const copy = { k: 'copy', t: '复制链接', icon: 'link', color: '#0E3DA8', cls: 'blue' };
  const disable = { k: 'disable', t: '停用', icon: 'pause-circle', color: '#4E5969', cls: '' };
  const enable = { k: 'enable', t: '重新启用', icon: 'check', color: '#0E3DA8', cls: 'blue' };
  const remove = { k: 'remove', t: state === 'expired' ? '删除记录' : '删除', icon: 'delete', color: '#F53F3F', cls: 'danger' };
  if (state === 'active') return [copy, disable, remove];
  if (state === 'disabled') return [copy, enable, remove];
  return [remove]; // expired：链接已失效，仅可删除记录
}

Page({
  data: {
    isManager: false, // 班管/超管：显示「全部（班管）」分段与提示条
    scope: 'mine', // mine / all
    list: [],
    loading: true,
  },

  async onLoad() {
    // 等启动自检完成再取角色（同 quiz manage 口径）
    await getApp().globalData.ready;
    const user = getApp().globalData.userInfo || wx.getStorageSync('userInfo') || {};
    this.setData({ isManager: user.role === 'admin' || user.role === 'team_admin' });
    this.loadShares(true);
  },

  onShow() {
    // 切回页面（如从主页新建分享后返回）静默刷新一次
    if (this._loaded) this.loadShares(false);
  },

  onPullDownRefresh() {
    this.loadShares(false).finally(() => wx.stopPullDownRefresh());
  },

  toast(message) {
    Toast({ context: this, selector: '#t-toast', message });
  },

  async loadShares(isInitial) {
    if (isInitial) this.setData({ loading: true });
    try {
      const rows = await request({ url: `${API}/shares?scope=${this.data.scope}` });
      this._loaded = true;
      this.setData({ list: (rows || []).map((r) => this.mapShare(r)), loading: false });
    } catch (err) {
      this.setData({ loading: false });
      // 「全部」无权限兜底：回退「我创建的」并隐藏分段
      if (err && err.code === 40304 && this.data.scope === 'all') {
        this.setData({ scope: 'mine', isManager: false });
        this.toast(err.message);
        this.loadShares(true);
        return;
      }
      this.toast(err.message);
    }
  },

  // 分享记录 → 卡片结构（多项取首个 + N；无扩展名按文件夹图标）
  mapShare(row) {
    const names = row.items || [];
    const first = names[0] || '（内容已失效）';
    const ext = extOf(first);
    const st = STATE_MAP[row.state] || STATE_MAP.active;
    let expireText;
    if (!row.expire_at) expireText = '永久有效';
    else if (row.state === 'expired') expireText = `已于 ${fmtDay(row.expire_at)} 过期`;
    else expireText = `有效期至 ${fmtDay(row.expire_at)}`;
    return {
      id: row.share_id,
      title: names.length > 1 ? `${first} 等 ${names.length} 项` : first,
      isDir: !ext,
      letter: ext ? ext.slice(0, 4).toUpperCase() : 'FILE',
      stateCls: st.cls,
      stateText: st.text,
      sub: `${row.password ? `提取码 ${row.password} · ` : ''}${expireText} · ${row.creator || '—'} 创建于 ${fmtDay(row.created_at)}`,
      ops: opsOf(row.state),
    };
  },

  onScopeTap(e) {
    const scope = e.currentTarget.dataset.scope;
    if (!scope || scope === this.data.scope) return;
    this.setData({ scope, list: [] });
    this.loadShares(true);
  },

  onOp(e) {
    const { op, id } = e.currentTarget.dataset;
    const item = this.data.list.find((r) => r.id === id);
    if (!item) return;
    if (op === 'copy') {
      wx.setClipboardData({ data: `${config.BASE_URL}/share.html#/s/${item.id}` });
      return;
    }
    if (op === 'disable') {
      Dialog.confirm({
        context: this,
        selector: '#t-dialog',
        title: '停用分享',
        content: `停用后「${item.title}」的外链访问立即中断，可随时重新启用。`,
        confirmBtn: '停用',
        cancelBtn: '取消',
      }).then(() => this.shareAct(item, 'disable', '已停用')).catch(() => {});
      return;
    }
    if (op === 'remove') {
      Dialog.confirm({
        context: this,
        selector: '#t-dialog',
        title: '删除分享',
        content: `删除后「${item.title}」的外链永久失效，不可恢复。`,
        confirmBtn: '确认删除',
        cancelBtn: '取消',
      }).then(() => this.shareAct(item, 'remove', '已删除')).catch(() => {});
      return;
    }
    if (op === 'enable') this.shareAct(item, 'enable', '已重新启用');
  },

  // 停用 / 重新启用 / 删除（失败文案按后端返回展示，如 40030 分享已过期）
  shareAct(item, act, okText) {
    return request({
      url: act === 'remove' ? `${API}/shares/${item.id}` : `${API}/shares/${item.id}/${act}`,
      method: act === 'remove' ? 'DELETE' : 'POST',
    })
      .then(() => {
        this.toast(okText);
        this.loadShares(false);
      })
      .catch((err) => this.toast(err.message));
  },
});
