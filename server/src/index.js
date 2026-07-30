// 检修一班工具箱后端入口
// 部署：上传至云服务器 Node.js Docker 环境，以 npm run start 启动（见 server/.env.example）
const express = require('express');
const config = require('./config');

config.validateConfig(); // 启动必需环境变量缺失即退出

const { ensureSchema } = require('./db');
const { ok, fail } = require('./utils/resp');

const app = express();
app.disable('x-powered-by');

// 反代前缀兼容：1Panel/Nginx 若保留 /j1toolkit 前缀转发（proxy_pass 无 URI 部分），
// 这里按 PROXY_PREFIX 剥离后再进入路由；前缀不存在时不影响任何请求
app.use((req, res, next) => {
  const prefix = config.proxyPrefix;
  if (prefix && req.url.startsWith(`${prefix}/`)) {
    req.url = req.url.slice(prefix.length);
  } else if (prefix && req.url === prefix) {
    req.url = '/';
  }
  next();
});

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

app.use('/api/v1/auth', require('./routes/auth'));
app.use('/api/v1/app', require('./routes/app'));
app.use('/api/v1/user', require('./routes/user'));
app.use('/api/v1/callme', require('./routes/callme'));
app.use('/api/v1/admin', require('./routes/admin'));
// 出工日志：env WORKLOG_ENABLED=true 时才挂载（建表/种子见 db.js）
if (config.worklog.enabled) {
  app.use('/api/v1/worklog', require('./worklog'));
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
      console.log(`[启动完成] 检修一班工具箱后端已监听 0.0.0.0:${config.port}`);
    });
  })
  .catch((err) => {
    console.error('[启动失败] 数据库初始化失败：', err.message);
    process.exit(1);
  });
