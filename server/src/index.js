// Shade 壹匣后端入口（班组数字化工具平台：后端 API + 网页端同端口托管）
// 部署：阿里云 ACR 按仓根 Dockerfile 自动构建镜像，云服务器以容器运行（数据卷挂 /app/server/data，配置全走环境变量，见 README「后端部署」）
// 反代约定：toolkit.j1net.com → 127.0.0.1:PORT 单端口，网页与 API 同端口
const path = require('path');
const fs = require('fs');
const express = require('express');
const config = require('./config');

config.validateConfig(); // 启动必需环境变量缺失即退出

const { ensureSchema } = require('./db');
const { ok, fail } = require('./utils/resp');

const app = express();
app.disable('x-powered-by');
// 同机反代回环（nginx → 127.0.0.1:PORT）可信：信任回环来源的 X-Forwarded-For 取真实客户端 IP，
// 更外层伪造的 XFF 不生效；req.ip 语义随之落到真实来源（登录限流按 IP 统计依赖此口径），直连部署不受影响
app.set('trust proxy', 'loopback');

// 出工日志上传原图（加水印用）体积更大：worklog 路由单独放宽 JSON 上限到 20mb。
// 需挂在全局 12mb 解析器之前；body 已被解析过后续解析器会自动跳过
app.use('/api/v1/worklog', express.json({ limit: '20mb' }));
// 水印添加同口径：原图 base64 上送渲染，单独放宽 JSON 上限
app.use('/api/v1/wmadd', express.json({ limit: '20mb' }));
app.use(express.json({ limit: '12mb' })); // 聊天图片以 base64 上送，放宽体积限制

// 简易访问日志
app.use((req, res, next) => {
  const start = Date.now();
  res.on('finish', () => {
    // originalUrl 可能携带 ?token= 敏感参数（如 Dify 回调），访问日志中脱敏
    console.log(`${req.method} ${req.originalUrl.replace(/([?&]token=)[^&]+/, '$1***')} ${res.statusCode} ${Date.now() - start}ms`);
  });
  next();
});

// 健康检查（供 Docker/负载探活）
app.get('/healthz', (req, res) => ok(res, { status: 'up' }));

// sgcc-captcha.html：顶象 appId 走 env（SGCC_DX_APPID）伺服时注入，密钥不落公开仓（页内为占位符）
let sgccCaptchaHtml;
app.get('/sgcc-captcha.html', (req, res) => {
  if (!sgccCaptchaHtml) {
    sgccCaptchaHtml = fs.readFileSync(path.join(__dirname, '..', 'public', 'sgcc-captcha.html'), 'utf8');
  }
  res.type('html').send(sgccCaptchaHtml.replaceAll('__SGCC_DX_APPID__', config.sgcc.dxAppId || ''));
});

// 网页端静态资源（server/public）：/ 直接出 index.html，网页与 API 同端口
app.use(express.static(path.join(__dirname, '..', 'public')));

app.use('/api/v1/auth', require('./routes/auth'));
app.use('/api/v1/app', require('./routes/app'));
app.use('/api/v1/user', require('./routes/user'));
app.use('/api/v1/callme', require('./routes/callme'));
app.use('/api/v1/admin', require('./routes/admin'));
// 通知推送：本体基础能力，无条件挂载（建表见 db.js）
app.use('/api/v1/notice', require('./notice'));
// 出工日志：派车/巡视/打卡记录（建表/种子见 db.js）
app.use('/api/v1/worklog', require('./worklog'));
// 水印添加：移动端独立子应用（仅渲染回图，无业务表，依赖出工日志渲染/地理模块）
app.use('/api/v1/wmadd', require('./wmadd'));
// 安全日活动记录：文件存储，上传走 multer 不经 JSON 解析器
app.use('/api/v1/safeday', require('./safeday'));
// KVM 远程管理：设备列表代理自 GLKVM 平台
app.use('/api/v1/kvm', require('./kvm'));
// 题库刷题（建表/种子见 db.js）
app.use('/api/v1/quiz', require('./quiz'));
// 商旅打卡（出工日志扩展，建表见 db.js）
app.use('/api/v1/sgcc', require('./sgccclockin'));

// 404 与统一错误处理
app.use((req, res) => fail(res, 404, 40404, '接口不存在'));
// Express 统一错误处理器：靠 4 参签名识别（项目无 eslint，无需禁告注释）
app.use((err, req, res, next) => {
  // body-parser 的解析错误带有 err.type，映射为准确的状态码（默认会吞成 500）
  if (err.type === 'entity.too.large') {
    return fail(res, 413, 41301, '上传内容超出大小限制，请压缩或分批后重试');
  }
  if (err.type === 'entity.parse.failed') {
    return fail(res, 400, 40001, '请求体不是合法 JSON');
  }
  console.error('[服务错误]', err);
  return fail(res, 500, 50000, '服务器开小差了，请稍后再试');
});

ensureSchema()
  .then(() => {
    app.listen(config.port, '0.0.0.0', () => {
      console.log(`[启动完成] Shade 壹匣后端已监听 0.0.0.0:${config.port}`);
    });
  })
  .catch((err) => {
    console.error('[启动失败] 数据库初始化失败：', err.message);
    process.exit(1);
  });
