// 小程序端配置
// BASE_URL：后端服务地址（生产环境经 Nginx 反向代理：https://toolkit.j1net.com → http://127.0.0.1:PORT，无路径前缀）。
// 微信强制要求 HTTPS 域名，并需在小程序后台「开发管理-服务器域名」中将 https://toolkit.j1net.com
// 配置为 request 合法域名（按域名放行）。
module.exports = {
  BASE_URL: 'https://toolkit.j1net.com',
};
