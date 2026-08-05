# Shade 壹匣

班组数字化工具平台：微信小程序 + 网页端，本体提供统一登录（账号密码 + 微信）与应用权限控制，各应用以分包/网页形式持续接入。

已接入应用：

- **Call Me**（`pkg-callme` + 网页端 `callme.html`）：基于 WeKnora 的 AI 知识库问答（SSE 流式对话）
- **出工日志**（`pkg-worklog` + 网页端 `worklog.html`）：派车/巡视/打卡记录，水印照片经 Dify 工作流验证（`WORKLOG_ENABLED` 开关）；网页端重构自旧 `WorkLogs/` 独立服务，同源直连 `/api/v1/worklog`
- **安全日活动记录**（`pkg-safeday` 引导页 + 网页端 `safeday.html`）：上传活动文档经 Dify 工作流生成记录文件；后端已合并进主服务（`server/src/safeday/`，`SAFEDAY_ENABLED` 开关），网页端登录要求 `safe-day` 应用权限

**文档导航**：协作规则与 UI 定稿 token 见 `AGENTS.md`；开发全参考（架构/数据库/接口/对接细节/踩坑）见 `开发指南.md`；小程序 UI 定稿见 `design/style-5.html`，网页端 UI 定稿见 `design/web/`（方案A「安全橙传承」）。

## 技术栈

| 层 | 选型 |
|----|------|
| 前端 | 微信小程序原生 + tdesign-miniprogram（方案五「安全橙」定制主题）；网页端原生 HTML/JS（同源 token，方案A「安全橙传承」） |
| 后端 | Node.js + Express（云服务器 Docker，`npm run start` 启动；单端口同时托管网页端与 `/api/v1`） |
| 存储 | MySQL（业务数据）/ Redis（JWT 黑名单、会话）/ 腾讯云 COS（文件/照片） |
| 鉴权 | JWT + Redis，客户端 `Authorization: Bearer <token>` 携带（网页端 token 存 localStorage） |
| 外部服务 | WeKnora 知识库（Call Me）、Dify 工作流（出工日志照片验证、安全日活动记录生成） |

## 目录结构

```
miniprogram/   微信小程序（主包：登录/首页/我的/管理页）
  pkg-callme/    Call Me 分包
  pkg-worklog/   出工日志分包（主页 + 常用数据管理页）
  pkg-safeday/   安全日活动记录分包（网页端应用引导页）
server/        后端 Node.js 单端口整合服务（API + 托管网页端）
  public/        网页端（login.html / index.html 工作台 / callme / worklog / safeday + assets 公共资源）
  src/routes/    本体路由（auth/user/app/admin/callme）
  src/worklog/   出工日志后端子模块（schema/cos/dify/verify/路由）
  src/safeday/   安全日活动记录后端子模块（dify/merge/store/路由）
design/        UI 设计稿（小程序定稿 style-5.html；design/web/ 网页端六方案，定稿方案A）
SafeDayLogs/   安全日活动记录旧独立服务（已整合进 server/，仅作历史归档，不再维护部署）
WorkLogs/      出工日志旧 PC 网页端独立服务（已整合进 server/，仅作历史归档，不再维护部署）
```

## 后端部署（云服务器 Docker）

1. 上传 `server/` 目录至云服务器；
2. 配置环境变量：`cp .env.example .env`，按实际填写（必填：`JWT_SECRET`、`MYSQL_*`；微信登录需 `WX_APPID`/`WX_SECRET`；Call Me 需 `WEKNORA_API_KEY`/`WEKNORA_AGENT_ID`；出工日志需 `WORKLOG_ENABLED=true` + COS + Dify 配置；安全日活动记录需 `SAFEDAY_ENABLED=true` + `DIFY_SAFEDAY_API_KEY`）；
3. 安装依赖并启动：

```bash
npm install   # 或 npm ci（按 package-lock.json 精确安装）
npm run start # 监听 0.0.0.0:$PORT（默认 3000）
```

4. 验证：`curl http://127.0.0.1:3000/healthz` 返回 `{"code":0,...}` 即正常。

**网页端入口**：与 API 同端口同源——`https://toolkit.j1net.com/login.html` 登录页（账号密码登录，JWT 存 localStorage），`https://toolkit.j1net.com/` 即门户工作台/应用中心（index.html），各应用页 `callme.html` / `worklog.html` / `safeday.html`，管理员另有 `admin.html`（员工与权限管理）。

**反向代理（1Panel/Nginx）**：用户自设反代 `https://toolkit.j1net.com → http://127.0.0.1:{PORT}`，**一个端口同时服务网页与 API，无任何路径前缀配置**（旧 `PROXY_PREFIX` 机制已删除）；**SSE 流式对话必须**在反代配置补充：

```nginx
proxy_buffering off;      # 必需：否则流式响应被缓冲成整段返回
proxy_read_timeout 300s;  # 推荐：长生成不被掐断（服务端另有 15s 心跳兜底）
client_max_body_size 20m; # 图片上传（base64）需要
```

**初始化**：首次启动自动建 `sys_user` / `sys_app` / `sys_user_app` 三张表，写入 Call Me、安全日活动记录应用记录，创建初始管理员（`ADMIN_USERNAME` / `ADMIN_PASSWORD`，默认 `admin` / `Admin@123`，**请尽快修改**）；`WORKLOG_ENABLED=true` 时再建出工日志 6 张业务表并写入应用与 7 名成员种子。给用户开权限：管理员在小程序「我的 → 权限管理」勾选即可。

## 环境变量清单

所有配置统一从 env 读取，敏感信息不入仓；`.env.example` 随功能同步维护。

| 变量名 | 说明 |
|--------|------|
| `PORT` | 服务端口（默认 3000；网页端与 API 同端口，反代 `toolkit.j1net.com → 127.0.0.1:PORT`） |
| `JWT_SECRET` | JWT 签名密钥 |
| `JWT_EXPIRES` | JWT 有效期（如 `7d`） |
| `ADMIN_USERNAME` / `ADMIN_PASSWORD` / `ADMIN_NICKNAME` | 初始管理员（仅首次启动、账号不存在时创建；指定账号始终为 admin） |
| `WX_APPID` / `WX_SECRET` | 微信小程序 AppID / AppSecret（code 换 openid 用） |
| `MYSQL_HOST` / `MYSQL_PORT` / `MYSQL_USER` / `MYSQL_PASSWORD` / `MYSQL_DATABASE` | MySQL 连接 |
| `REDIS_HOST` / `REDIS_PORT` / `REDIS_PASSWORD` | Redis 连接（JWT 黑名单/会话） |
| `WEKNORA_API_URL` / `WEKNORA_API_KEY` / `WEKNORA_AGENT_ID` | WeKnora 知识库（Call Me；详见开发指南 Call Me 一节） |
| `COS_SECRET_ID` / `COS_SECRET_KEY` / `COS_BUCKET` / `COS_REGION` | 腾讯云 COS（出工日志照片） |
| `DIFY_API_URL` | Dify 地址（只填域名如 `http://10.2.24.13:8082`，`/v1` 由代码拼接；**所有 Dify 工作流共用此地址**，各工作流独立 API_KEY） |
| `DIFY_WORKLOG_API_KEY` | 出工日志照片验证工作流的 Dify API Key |
| `WORKLOG_ENABLED` | 出工日志后端开关：`true` 开启（建表/种子/挂载路由），`false` 关闭 |
| `COS_WORKLOG_PREFIX` | 出工日志照片在 COS 的独立文件夹前缀（如 `worklog/`） |
| `COS_WORKLOG_BASE_URL` | 照片访问域名（可选；留空按 `https://{bucket}.cos.{region}.myqcloud.com` 拼接） |
| `TENCENT_MAP_KEY` | 腾讯位置服务（出工日志「选择照片并添加水印」预填当前地点/天气；key 类型须勾选 WebServiceAPI，未配置则对应字段手填，见开发指南 7.3） |
| `SAFEDAY_ENABLED` | 安全日活动记录后端开关：`true` 开启（初始化数据目录并挂载 `/api/v1/safeday`），`false` 关闭 |
| `SAFEDAY_DATA_DIR` | 安全日记录 records.json 与生成产物（docs/）存放目录（默认 `./data/safeday`，相对路径按 server/ 解析） |
| `DIFY_SAFEDAY_API_KEY` | 安全日记录生成工作流的 Dify API Key（与出工日志工作流共用 `DIFY_API_URL`） |
| `SAFEDAY_CALLBACK_TOKEN` | Dify 回调 token（可选；配置后回调接口须带 `?token=` 校验，留空则不校验） |
| `BASEMETAS_URL` | basemetas 文件预览服务地址（可选，如 `https://cloud.j1net.com/view`；配置后安全日记录可点击预览） |

## 小程序开发（微信开发者工具）

1. 微信开发者工具导入 `miniprogram/` 目录；
2. 菜单「工具 → 构建 npm」（已配置 `packNpmManually`：主包 `tdesign-miniprogram` 构建到主包，`mp-html`/`markdown-it` 构建到 `pkg-callme` 分包；分包专用依赖在分包目录安装，装完都需重新构建）；
3. 修改 `miniprogram/config.js` 的 `BASE_URL` 为后端实际地址；
4. 将 `project.config.json` 的 `appid` 替换为真实小程序 AppID；
5. 小程序后台「开发管理 → 服务器域名」：request 合法域名配置后端域名（须 HTTPS）；**出工日志照片所在的 COS 域名需配置 downloadFile 合法域名**（批量下载功能依赖）。

## 验证状态

- 后端：依赖安装、`node --check` 语法、配置校验均本地通过；云端 MySQL/Redis/COS/WeKnora/Dify 实连以部署后日志为准。
- 小程序：全部 JS/JSON 静态校验通过；构建 npm 与真机交互（登录、键盘、相册、日历着色、批量下载）以真机验证为准。
