// 全局配置：所有环境相关值一律从环境变量读取（示例见 docker/.env.example），不硬编码
const path = require('path');
require('dotenv').config();

function str(name, def = '') {
  const v = process.env[name];
  return v === undefined || v === '' ? def : v;
}
function num(name, def) {
  const v = parseInt(process.env[name] || '', 10);
  return Number.isNaN(v) ? def : v;
}
// 路径/键前缀统一以 / 结尾（使用点不再行内拼接）；空串保持空串
function withTrailingSlash(v) {
  return v === '' || v.endsWith('/') ? v : `${v}/`;
}

const config = {
  port: num('PORT', 3000),
  jwt: {
    secret: str('JWT_SECRET'),
    expiresIn: str('JWT_EXPIRES', '7d'),
    // 网页端登录态有效期（单独缩短：网页端可远程控制公司电脑（KVM），需定期强制重新登录；
    // 仅 login.html 以 client:'web' 登录时生效，小程序走 wx-login 静默续期不受影响）
    webExpiresIn: str('JWT_WEB_EXPIRES', '12h'),
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
    // 微信消息推送工作流 key（通知微信外发；留空则微信推送整体停用，站内通知照常）
    wxpushKey: str('DIFY_WXPUSH_API_KEY'),
  },
  // 在线预览查看器地址（默认微软官方 Office 查看器；可换自部署 kkFileView 等，须支持 ?src=<回源URL> 口径）
  preview: {
    viewerUrl: str('PREVIEW_VIEWER_URL', 'https://view.officeapps.live.com/op/view.aspx'),
  },
  worklog: {
    cosPrefix: withTrailingSlash(str('COS_WORKLOG_PREFIX', 'worklog/')),
    cosBaseUrl: str('COS_WORKLOG_BASE_URL'),
    difyKey: str('DIFY_WORKLOG_API_KEY'),
    // 高德地图（「选照片并添加水印」预填当前地点/天气、商旅打卡定位解析用；未配置时对应字段留空手填）
    // 高德开放平台控制台 lbs.amap.com 创建应用，key 类型须为「Web 服务」
    amapMapKey: str('AMAP_MAP_KEY'),
    // 接口 base URL（默认官方地址；因费用问题走中转站时改为中转地址，接口路径 /v3/... 不变）
    amapBaseUrl: str('AMAP_BASE_URL', 'https://restapi.amap.com').replace(/\/+$/, ''),
    // 派车单每日自动同步（经 KVM 文件传输链路取回被控机导出件自动建卡）
    // 生效班组以 worklog_dispatch_sync_team 开关表为准（班组管理员/超管在「派车对齐」页按班组开关）；
    // team 仅为首次启动的种子开启班组（写入开关表后不再生效）；设备以 ddns 定位、MAC 校验防误操作他机
    dispatchSync: {
      team: str('WORKLOG_DISPATCH_SYNC_TEAM'),
      deviceDdns: str('WORKLOG_DISPATCH_DEVICE_DDNS'),
      deviceMac: str('WORKLOG_DISPATCH_DEVICE_MAC'),
      // 同步任务代登平台所用账号（须在 GLKVM 平台可见该设备组；留空回退 ADMIN_USERNAME）
      kvmUser: str('WORKLOG_DISPATCH_KVM_USER'),
      prepareTime: str('WORKLOG_DISPATCH_PREPARE_TIME', '09:10'), // 锁定设备并挂载 U 盘至被控机
      fetchTime: str('WORKLOG_DISPATCH_FETCH_TIME', '09:20'), // 取件建卡并解禁
    },
  },
  // 安全日活动记录（自 SafeDayLogs 独立服务合并的子模块，文件存储，不建库表）
  safeday: {
    // 记录 records.json 与生成产物（docs/）存放目录；已在 config 归一化为绝对路径（相对路径按服务启动目录解析）
    dataDir: path.resolve(str('SAFEDAY_DATA_DIR', './data/safeday')),
    // 安全日活动记录默认上级参加人员（留空则由用户手填）
    defaultSuperior: str('SAFEDAY_DEFAULT_SUPERIOR'),
    difyKey: str('DIFY_SAFEDAY_API_KEY'),
    // Dify 回调 token：留空则回调不做 token 校验（与原 CALLBACK_TOKEN 行为一致）
    callbackToken: str('SAFEDAY_CALLBACK_TOKEN'),
    // 多文件含非 PDF 时经 LibreOffice headless 转 PDF 再合并；默认取 PATH 中 soffice，可用 env 覆盖路径
    sofficePath: str('SAFEDAY_SOFFICE_PATH', 'soffice'),
  },
  // KVM 远程管理（GLKVM Cloud 平台对接：员工账号代登取设备列表 + 平台深链跳转，见 开发指南.md 第十二节）
  kvm: {
    url: str('GLKVM_URL').replace(/\/+$/, ''),
    password: str('GLKVM_PASSWORD'),
  },
  // 题库刷题（AI 解析走 Dify「题目解析」工作流；未配置 DIFY_QUIZ_API_KEY 时解析停用，刷题照常）
  quiz: {
    difyKey: str('DIFY_QUIZ_API_KEY'),
  },
  // 团队网盘（OpenList 中转：双端自定义前端经后端访问 OpenList；OpenList 仅监听回环/compose 内网，不对公网暴露，
  // 部署与对接细节见 开发指南.md 团队网盘章节）
  netdisk: {
    apiUrl: str('NETDISK_API_URL', 'http://127.0.0.1:5244').replace(/\/+$/, ''),
    // OpenList 服务账户（专用账户，base_path=/；后端自动登录并缓存令牌续期）
    username: str('NETDISK_USERNAME'),
    password: str('NETDISK_PASSWORD'),
    // 空间根目录（OpenList 内绝对路径）：公共区固定一个；个人空间 = personalRoot/<登录用户名>
    publicRoot: str('NETDISK_PUBLIC_ROOT', '/public'),
    personalRoot: str('NETDISK_PERSONAL_ROOT', '/personal'),
    // 单文件上传上限（MB）
    maxUploadMb: num('NETDISK_MAX_UPLOAD_MB', 1024),
  },
  // 商旅打卡（出工日志扩展）
  // 协议密钥取自商旅 App 逆向分析仓 private/esgcc/sgcc/tools/sgcc_client.js（私有仓），只走 env、不入仓
  sgcc: {
    jwtSecret: str('SGCC_JWT_SECRET'),       // H5/App 自签 JWT 密钥
    sm2ServerPub: str('SGCC_SM2_SERVER_PUB'), // jsonm 通道 SM2 服务器公钥
    sm2ClientPriv: str('SGCC_SM2_CLIENT_PRIV'), // jsonm 通道 SM2 客户端私钥
    rsaPub: str('SGCC_RSA_PUB'),             // jsonx default 通道请求加密公钥
    rsaPriv: str('SGCC_RSA_PRIV'),           // jsonx default 通道响应解密私钥
    dcuPub: str('SGCC_DCU_PUB'),             // jsonx slapp 通道请求加密公钥（res/dCu.pem 内容）
    wlaPriv: str('SGCC_WLA_PRIV'),           // jsonx slapp 通道响应解密私钥（res/wLA.pem 内容）
    adSecret: str('SGCC_AD_SECRET'),         // /api/ads 拉池密钥（App 内嵌客户端常量）
    dxAppId: str('SGCC_DX_APPID'),           // 顶象滑块 appId（绑定滑块页 sgcc-captcha.html 伺服时注入）
    // 商旅 App 版本号（请求头 version；App 升级后须同步更新此默认值与 protocol.js 兜底值、docker/.env.example，2026-10-09 起约定）
    version: str('SGCC_VERSION', '3.3.7'),
    // 商旅 App 构建号（请求头 version-code，3.3.7 实测值 202609232038；App 升级随 SGCC_VERSION 一并更新）
    versionCode: str('SGCC_VERSION_CODE', '202609232038'),
    // 风控 SDK 版本（请求头 grayversion，3.3.7 实测值 2.4.7.1）
    grayVersion: str('SGCC_GRAY_VERSION', '2.4.7.1'),
    // 每日自动核查时间（HH:mm，默认 23:00）
    syncTime: str('SGCC_SYNC_TIME', '23:00'),
    // 开始打卡未打午间提醒时间（HH:mm，默认 11:00；对当日卡上已绑定但未首次打卡的成员，仅发微信 本人+班组群，不写站内通知）
    startClockRemindTime: str('SGCC_STARTCLOCK_REMIND_TIME', '11:00'),
    // 结束打卡未打傍晚提醒时间（HH:mm，默认 18:00；仅发微信 本人+班组群，不写站内通知）
    endClockRemindTime: str('SGCC_ENDCLOCK_REMIND_TIME', '18:00'),
    // 批量拉取成员间隔（毫秒，防商旅侧风控；定时核查与手动 /sync/pull 共用）
    syncIntervalMs: parseInt(str('SGCC_SYNC_INTERVAL_MS', '1500'), 10) || 1500,
    // 商旅 API 出口 SOCKS5 代理（如 socks5h://user:pass@127.0.0.1:18070；空=直连）。
    // 用途：云服务器 IP 属地被商旅风控（图形码 99000 窗口），经家中宽带出口对齐打卡人实际属地；
    // 作用于全部商旅通信——protocol.js 的 API 调用与商旅费用照片下载（仅自家 COS 回源等非商旅流量不走代理）
    proxyUrl: str('SGCC_PROXY_URL'),
    // 出口代理健康探测间隔（毫秒，默认 300000=5 分钟；仅配置 SGCC_PROXY_URL 时启用）：
    // 定时对代理端口做 TCP 建连探测（仅探代理存活，不向商旅主机发请求），正常↔异常 跳变时通知超管（站内+微信）
    proxyProbeIntervalMs: parseInt(str('SGCC_PROXY_PROBE_INTERVAL_MS', '300000'), 10) || 300000,
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
    console.error(`[配置错误] 缺少环境变量：${missing.join('、')}，请参照 docker/.env.example 配置`);
    process.exit(1);
  }
  // 初始管理员密码沿用缺省值时醒目告警（仅提示，不改默认行为；生产环境务必在 .env 配置 ADMIN_PASSWORD）
  if (!process.env.ADMIN_PASSWORD) {
    console.warn('************************************************************');
    console.warn('[安全警告] 未配置 ADMIN_PASSWORD，初始管理员使用默认密码「Admin@123」，');
    console.warn('[安全警告] 存在被猜测/撞库风险，请尽快在 .env 中设置强密码！');
    console.warn('************************************************************');
  }
}

module.exports = { ...config, validateConfig };
