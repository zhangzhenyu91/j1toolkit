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
  // basemetas 文件预览服务（安全日记录在线预览用；留空=未启用，网页端不显示预览）
  basemetas: {
    url: str('BASEMETAS_URL'),
  },
  worklog: {
    enabled: str('WORKLOG_ENABLED') === 'true',
    cosPrefix: str('COS_WORKLOG_PREFIX', 'worklog/'),
    cosBaseUrl: str('COS_WORKLOG_BASE_URL'),
    difyKey: str('DIFY_WORKLOG_API_KEY'),
    // 高德地图（「选照片并添加水印」预填当前地点/天气、商旅打卡定位解析用；未配置时对应字段留空手填）
    // 高德开放平台控制台 lbs.amap.com 创建应用，key 类型须为「Web 服务」
    amapMapKey: str('AMAP_MAP_KEY'),
  },
  // 安全日活动记录（自 SafeDayLogs 独立服务合并的子模块，文件存储，不建库表）
  safeday: {
    enabled: str('SAFEDAY_ENABLED') === 'true',
    // 记录 records.json 与生成产物（docs/）存放目录，相对路径按服务启动目录（server/）解析
    dataDir: str('SAFEDAY_DATA_DIR', './data/safeday'),
    difyKey: str('DIFY_SAFEDAY_API_KEY'),
    // Dify 回调 token：留空则回调不做 token 校验（与原 CALLBACK_TOKEN 行为一致）
    callbackToken: str('SAFEDAY_CALLBACK_TOKEN'),
  },
  // KVM 远程管理（GLKVM Cloud 平台对接：员工账号代登取设备列表 + 平台深链跳转，见 开发指南.md 第十二节）
  kvm: {
    enabled: str('KVM_ENABLED') === 'true',
    url: str('GLKVM_URL').replace(/\/+$/, ''),
    password: str('GLKVM_PASSWORD'),
  },
  // 题库刷题（AI 解析走 Dify「题目解析」工作流；未配置 DIFY_QUIZ_API_KEY 时解析停用，刷题照常）
  quiz: {
    enabled: str('QUIZ_ENABLED') === 'true',
    difyKey: str('DIFY_QUIZ_API_KEY'),
  },
  // 商旅打卡（出工日志扩展）：env SGCC_CLOCKIN_ENABLED=true 时才挂载
  // 协议密钥取自商旅 App 逆向分析仓 esgcc/sgcc/tools/sgcc_client.js，只走 env、不入仓
  sgcc: {
    enabled: str('SGCC_CLOCKIN_ENABLED') === 'true',
    jwtSecret: str('SGCC_JWT_SECRET'),       // H5/App 自签 JWT 密钥
    sm2ServerPub: str('SGCC_SM2_SERVER_PUB'), // jsonm 通道 SM2 服务器公钥
    sm2ClientPriv: str('SGCC_SM2_CLIENT_PRIV'), // jsonm 通道 SM2 客户端私钥
    rsaPub: str('SGCC_RSA_PUB'),             // jsonx default 通道请求加密公钥
    rsaPriv: str('SGCC_RSA_PRIV'),           // jsonx default 通道响应解密私钥
    dcuPub: str('SGCC_DCU_PUB'),             // jsonx slapp 通道请求加密公钥（res/dCu.pem 内容）
    wlaPriv: str('SGCC_WLA_PRIV'),           // jsonx slapp 通道响应解密私钥（res/wLA.pem 内容）
    // 商旅 App 版本号（请求头 version；App 升级后优先只改此项验证是否仍通）
    version: str('SGCC_VERSION', '3.3.5'),
    // 每日自动核查时间（HH:mm，默认 23:00）
    syncTime: str('SGCC_SYNC_TIME', '23:00'),
    // 批量拉取成员间隔（毫秒，防商旅侧风控；定时核查与手动 /sync/pull 共用）
    syncIntervalMs: parseInt(str('SGCC_SYNC_INTERVAL_MS', '1500'), 10) || 1500,
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
