# 检修一班工具箱

班组应用聚合平台微信小程序：本体提供统一登录（账号密码 + 微信）与应用权限控制，各应用以分包形式接入。已接入首个应用 **Call Me**（基于 WeKnora 的检修一班 AI 知识库问答）。

规则与约定见 `AGENTS.md`；需求与方案见 `项目文档.md`；UI 定稿为方案五 × TDesign 定制（token 见 `项目文档.md` 第八章）。

## 目录结构

```
miniprogram/   微信小程序（原生 + tdesign-miniprogram）
server/        后端 Node.js 服务（Express + MySQL + Redis）
```

## 后端部署（云服务器 Docker）

1. 上传 `server/` 目录至云服务器；
2. 配置环境变量：`cp .env.example .env`，按实际填写（必填：`JWT_SECRET`、`MYSQL_*`、`WEKNORA_API_KEY`、`WEKNORA_AGENT_ID`；微信登录需 `WX_APPID`/`WX_SECRET`）；
3. 安装依赖并启动：

```bash
npm install   # 或 npm ci（按 package-lock.json 精确安装）
npm run start # 监听 0.0.0.0:$PORT（默认 3000）
```

4. 验证：`curl http://127.0.0.1:3000/healthz` 返回 `{"code":0,...}` 即正常。

**反向代理（1Panel/Nginx）**：生产经 `https://j1net.com/j1toolkit/` 访问。服务端已兼容两种转发方式（剥/不剥前缀，`PROXY_PREFIX` 控制，默认 `/j1toolkit`），无需改动面板默认配置；但 **SSE 流式对话要求**在反代配置中补充：

```nginx
proxy_buffering off;      # 必需：否则流式响应被缓冲成整段返回
proxy_read_timeout 300s;  # 推荐：长生成不被掐断（服务端另有 15s 心跳兜底）
```

说明：

- 首次启动自动建表（`sys_user` / `sys_app` / `sys_user_app`），写入 Call Me 应用记录，并创建初始管理员（`ADMIN_USERNAME` / `ADMIN_PASSWORD`，默认 `admin` / `Admin@123`，**请尽快修改**）。
- 给其他用户开通 Call Me 权限（SQL）：

```sql
INSERT IGNORE INTO sys_user_app (user_id, app_id)
SELECT u.id, a.id FROM sys_user u JOIN sys_app a ON a.app_key = 'call-me'
WHERE u.username = '目标账号';
```

## 小程序开发（微信开发者工具）

1. 微信开发者工具导入 `miniprogram/` 目录；
2. 菜单「工具 → 构建 npm」一次即可（已配置 `packNpmManually`：主包 `tdesign-miniprogram` 构建到主包，`mp-html`/`markdown-it` 构建到 `pkg-callme` 分包，不占主包体积；日后给分包加依赖在 `miniprogram/pkg-callme/` 下 `npm install`，主包依赖在 `miniprogram/` 下安装，装完都需重新构建）；
3. 修改 `miniprogram/config.js` 的 `BASE_URL` 为后端实际地址；
4. 将 `project.config.json` 的 `appid` 替换为真实小程序 AppID；
5. 发布前在小程序后台「开发管理 → 服务器域名」配置 request 合法域名（须 HTTPS）。

## 验证状态

- 后端：依赖安装、`node --check` 语法、SSE 解析器（兼容 WeKnora `data:` 无空格格式）、配置校验均本地通过；**云端 MySQL/Redis/WeKnora 实连待部署后验证**。
- 小程序：全部 JS/JSON 静态校验通过；**微信开发者工具构建与真机效果待验证**。
