// 全局配置：所有环境相关值一律从环境变量读取（见 .env.example），不硬编码
require('dotenv').config();

function str(name, def = '') {
  const v = process.env[name];
  return v === undefined || v === '' ? def : v;
}
function num(name, def) {
  const v = parseInt(process.env[name] || '', 10);
  return Number.isNaN(v) ? def : v;
}

const config = {
  port: num('PORT', 3000),
  // 反向代理路径前缀（如 /j1toolkit）：反代保留前缀转发时，服务端先剥离再路由；留空则不处理
  proxyPrefix: str('PROXY_PREFIX').replace(/\/+$/, ''),
  jwt: {
    secret: str('JWT_SECRET'),
    expiresIn: str('JWT_EXPIRES', '7d'),
  },
  admin: {
    username: str('ADMIN_USERNAME', 'admin'),
    password: str('ADMIN_PASSWORD', 'Admin@123'),
    nickname: str('ADMIN_NICKNAME', '管理员'),
  },
  wx: {
    appid: str('WX_APPID'),
    secret: str('WX_SECRET'),
  },
  mysql: {
    host: str('MYSQL_HOST'),
    port: num('MYSQL_PORT', 3306),
    user: str('MYSQL_USER'),
    password: str('MYSQL_PASSWORD'),
    database: str('MYSQL_DATABASE'),
  },
  redis: {
    host: str('REDIS_HOST', '127.0.0.1'),
    port: num('REDIS_PORT', 6379),
    password: str('REDIS_PASSWORD'),
  },
  weknora: {
    apiUrl: str('WEKNORA_API_URL', 'https://know.j1net.com/api/v1'),
    apiKey: str('WEKNORA_API_KEY'),
    agentId: str('WEKNORA_AGENT_ID'),
  },
  cos: {
    secretId: str('COS_SECRET_ID'),
    secretKey: str('COS_SECRET_KEY'),
    bucket: str('COS_BUCKET'),
    region: str('COS_REGION'),
  },
  dify: {
    apiUrl: str('DIFY_API_URL'),
  },
  worklog: {
    enabled: str('WORKLOG_ENABLED') === 'true',
    cosPrefix: str('COS_WORKLOG_PREFIX', 'worklog/'),
    cosBaseUrl: str('COS_WORKLOG_BASE_URL'),
    difyKey: str('DIFY_WORKLOG_API_KEY'),
    // 腾讯位置服务（「选照片并添加水印」预填当前地点/天气用；未配置时对应字段留空手填）
    // 控制台 lbs.qq.com 创建应用时勾选 WebServiceAPI
    tencentMapKey: str('TENCENT_MAP_KEY'),
  },
};

// 启动必需项：缺失即拒绝启动，避免带病运行
const REQUIRED = [
  ['JWT_SECRET', config.jwt.secret],
  ['MYSQL_HOST', config.mysql.host],
  ['MYSQL_USER', config.mysql.user],
  ['MYSQL_DATABASE', config.mysql.database],
];

function validateConfig() {
  const missing = REQUIRED.filter(([, v]) => !v).map(([k]) => k);
  if (missing.length) {
    console.error(`[配置错误] 缺少环境变量：${missing.join('、')}，请参照 .env.example 配置`);
    process.exit(1);
  }
}

module.exports = { ...config, validateConfig };
