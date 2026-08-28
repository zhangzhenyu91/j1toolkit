// 商旅滑块验证 · web-view 承载页（出工日志扩展，2026-08-28）
// 顶象滑块只能在 H5 渲染：本页经 web-view 打开 server/public/sgcc-captcha.html（业务域名 toolkit.j1net.com），
// 用户拖完滑块后 H5 postMessage 回传 {captchaToken, constId}（navigateBack 时同步），转交上一页 sgccbind。
// 注意：业务域名需在微信公众平台配置（校验文件放 server/public/ 根目录）。
const config = require('../../../config.js');

Page({
  data: {
    url: '',
  },
  onLoad() {
    this.setData({ url: config.BASE_URL + '/sgcc-captcha.html?from=mp' });
  },
  // web-view postMessage 在页面回退时批量送达，取最后一条
  onMsg(e) {
    const list = (e.detail && e.detail.data) || [];
    const msg = list[list.length - 1];
    if (!msg || !msg.captchaToken) return;
    const pages = getCurrentPages();
    const prev = pages[pages.length - 2];
    if (prev && typeof prev.onCaptchaResult === 'function') {
      prev.onCaptchaResult({ captchaToken: msg.captchaToken, constId: msg.constId || '' });
    }
  },
});
