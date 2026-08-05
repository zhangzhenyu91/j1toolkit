// Shade 壹匣后端入口（班组数字化工具平台：后端 API + 网页端同端口托管）
// 部署：上传至云服务器 Node.js Docker 环境，以 npm run start 启动（见 server/.env.example）
// 反代约定：toolkit.j1net.com → 127.0.0.1:PORT 单端口，网页与 API 同端口
const path = require('path');
const express = require('express');
const config = require('./config');

config.validateConfig(); // 启动必需环境变量缺失即退出

const { ensureSchema } = require('./db');
const { ok, fail } = require('./utils/resp');

const app = express();
app.disable('x-powered-by');

// 出工日志上传原图（加水印用）体积更大：worklog 路由单独放宽 JSON 上限到 20mb。
// 需挂在全局 12mb 解析器之前；body 已被解析过后续解析器会自动跳过
if (config.worklog.enabled) {
  app.use('/api/v1/worklog', express.json({ limit: '20mb' }));
}
app.use(express.json({ limit: '12mb' })); // 聊天图片以 base64 上送，放宽体积限制

// 简易访问日志
app.use((req, res, next) => {
  const start = Date.now();
  res.on('finish', () => {
    console.log(`${req.method} ${req.originalUrl} ${res.statusCode} ${Date.now() - start}ms`);
  });
  next();
});

// 健康检查（供 Docker/负载探活）
app.get('/healthz', (req, res) => ok(res, { status: 'up' }));

// 网页端静态资源（server/public）：/ 直接出 index.html，网页与 API 同端口
app.use(express.static(path.join(__dirname, '..', 'public')));

app.use('/api/v1/auth', require('./routes/auth'));
app.use('/api/v1/app', require('./routes/app'));
app.use('/api/v1/user', require('./routes/user'));
app.use('/api/v1/callme', require('./routes/callme'));
app.use('/api/v1/admin', require('./routes/admin'));
// 出工日志：env WORKLOG_ENABLED=true 时才挂载（建表/种子见 db.js）
if (config.worklog.enabled) {
  app.use('/api/v1/worklog', require('./worklog'));
}
// 安全日活动记录：env SAFEDAY_ENABLED=true 时才挂载（文件存储，上传走 multer 不经 JSON 解析器）
if (config.safeday.enabled) {
  app.use('/api/v1/safeday', require('./safeday'));
}

// 404 与统一错误处理
app.use((req, res) => fail(res, 404, 40404, '接口不存在'));
// eslint-disable-next-line no-unused-vars
app.use((err, req, res, next) => {
  console.error('[服务错误]', err);
  fail(res, 500, 50000, '服务器开小差了，请稍后再试');
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
