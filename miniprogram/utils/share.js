// 页面分享统一配置：应用页面用应用图标分享图，其他页面用工具箱分享图
// 分享图由 design/make_share_images.py 生成，存放在 images/share/
const SHARE_IMAGES = {
  toolbox: '/images/share/share-toolbox.jpg',
  'call-me': '/images/share/share-call-me.jpg',
  'work-log': '/images/share/share-work-log.jpg',
  'safe-day': '/images/share/share-safe-day.jpg',
  'file-transfer': '/images/share/share-file-transfer.jpg',
  'wm-add': '/images/share/share-wm-add.jpg',
};

// 在页面 onShareAppMessage 中调用：
//   onShareAppMessage() { return shareAppMessage(this, { app: 'work-log', title: '出工日志' }); }
// page 传页面实例（取当前页路径作为分享路径）；app 传 sys_app 的 app_key，缺省用工具箱分享图
function shareAppMessage(page, { title, app, path } = {}) {
  return {
    title: title ? `${title} · Shade 壹匣` : 'Shade 壹匣',
    path: path || `/${page.route}`,
    imageUrl: SHARE_IMAGES[app] || SHARE_IMAGES.toolbox,
  };
}

module.exports = { shareAppMessage };
