# WorkLogs 出工日志网页端后端

> **⚠️ 已废弃归档**：本服务已整合进主服务（`server/`）——网页端重构为 `server/public/worklog.html`（与 API 同源直连），ZIP 打包移植为 `POST /api/v1/worklog/zip`。本目录仅作历史归档，不再维护、不再部署。以下内容为历史文档。

「出工日志」PC 网页端的后端服务。本站不存任何业务数据：登录、权限与全部业务数据均来自主平台「检修一班工具箱」API，本服务只负责 **登录会话 + 业务 API 反向代理 + 照片 ZIP 打包下载**。

## 架构

```
浏览器 ── 会话 cookie（wl_session）──> WorkLogs（登录会话 + 反向代理 + ZIP 打包）
                                          │  服务端持有主平台 JWT，前端不可见
                                          └──── /api/v1/worklog/* ────> 主平台「检修一班工具箱」
```

- `POST /api/login`：转发主平台 `POST /api/v1/auth/login` 校验账号密码，再用拿到的 token 访问 `GET /api/v1/worklog/meta` 校验「出工日志」应用权限，两步通过才签发本地会话 cookie。
- `/api/wl/*`：透明代理到主平台 `/api/v1/worklog/*`，服务端自动附加 `Authorization: Bearer <主平台JWT>`，主平台状态码与信封 `{code,message,data}` 原样回传。
- `POST /api/zip`：按前端提交的照片 URL 列表逐张下载，流式打包为「水印照片.zip」（store 模式，图片不再二次压缩）；失败照片自动追加「下载失败清单.txt」。

## 功能清单

- 日志看板（按日期/月份查看出工卡片、用车人、照片）
- 水印照片（拍照上传，主平台服务端渲染时间/地点/人员水印）
- 批量下载 ZIP（照片打包下载，失败附清单）
- 验证报告（AI 验证结果查看与重试）
- 数据管理（车辆、目的地、人员等基础数据维护，管理员）

以上业务接口全部由主平台 `/api/v1/worklog` 提供，本服务透明转发，不在本地落库。

## 环境变量

| 变量 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- |
| `PORT` | 否 | `3000` | 服务监听端口（监听 `0.0.0.0`） |
| `J1TOOLKIT_API_URL` | 是 | 无 | 主平台 API 根地址，如 `https://j1net.com/j1toolkit`；缺失时登录与业务代理不可用 |
| `SESSION_SECRET` | 生产必填 | 随机兜底 | 会话 JWT 签名密钥；缺失时随机生成，重启后所有登录会话失效 |
| `SESSION_EXPIRES` | 否 | `7d` | 会话名义有效期；实际与主平台 JWT 同寿命（主平台默认 7d） |
| `COOKIE_SECURE` | 否 | `false` | 置 `true` 时 cookie 仅经 HTTPS 传输（HTTPS 部署时开启） |
| `PROXY_PREFIX` | 否 | 空 | 反代保留路径前缀转发时配置（如 `/wl`），与主平台同口径 |

## 部署

要求 Node.js >= 18（使用全局 `fetch` 与 `AbortSignal.timeout`）。

```bash
npm install
cp .env.example .env   # 按需修改配置
npm run start
```

### 1Panel 反代

与主平台同口径：若反代保留路径前缀转发（`proxy_pass` 无 URI 部分），在 `.env` 配置 `PROXY_PREFIX`（如 `/wl`），服务会自动剥离前缀再进入路由；未登录跳转登录页使用相对路径，天然兼容前缀部署。

## 目录结构

```
WorkLogs/
├── server.js        # 后端入口（登录会话 + 业务代理 + ZIP 打包）
├── public/          # 前端静态资源（index.html / login.html 等）
├── package.json
├── .env.example
└── .gitignore
```
