# AGENTS.md — Shade 壹匣

本文件是面向 AI 编码助手及新加入开发者的项目规则。开始任何改动前请先读完本文件。

## 一、项目概述

「Shade 壹匣」是班组数字化工具平台：微信小程序 + 网页端（`server/public/`，与 API 同端口托管），本体负责统一登录（账号密码 + 微信登录）与应用权限控制，各业务应用以子页面/分包/网页形式持续接入。「检修一班」为班组名，保留不变。

必读资料：

- `README.md` —— 项目概述、部署与环境变量清单
- `开发指南.md` —— 开发全参考（架构/数据库/接口/对接细节/踩坑约定），接入新应用前必读
- `design/` 下 4 份基准稿 —— UI 定稿设计稿（「政企蓝白」：小程序首页 `小程序-首页.html` / 小程序出工日志 `小程序-出工日志.html` / Web 首页 `Web-首页.html` / Web 出工日志 `Web-出工日志.html`，配套 `设计规范.md` 与 `index.html` 展厅总览，见第四节）

**私有配套仓（重要）**：本仓为公开仓；不宜公开的内容在私有仓 [j1toolkit-private](https://github.com/zhangzhenyu91/j1toolkit-private)，以 git submodule 挂载于 `private/`（`git submodule update --init` 拉取；无权限时该目录为空、主仓功能不受影响）。内含：`private/esgcc/` 商旅打卡逆向分析仓与巡视台账素材、`private/WorkLogs/` 旧服务历史数据、`private/_archive/` 公开仓历史归档。（`uvmp-toolkit/` 内网客户端 2026-10 已迁回本仓根目录——安装包本就公开发布、PyInstaller/Electron 产物可解包，源码私密性有限；客户端改动前先读其《内网工具箱开发指南.md》）换机拉代码时若发现 `private/` 为空，先提醒用户拉私有仓。

## 二、协作与部署规则（最高优先级）

1. **只写文件，不做部署**：职责是在本地工作目录编写/修改文件。不执行部署、不上传服务器、不主动连接任何云端资源（MySQL / Redis / COS / WeKnora / Dify 只在代码中按 env 读取对接，不实际访问）。
2. **交付即可运行（Docker 镜像化部署）**：构建以 GitHub Actions 为主——仓根 `.github/workflows/release.yml` 于 `release-vX.Y.Z` / `v*` tag 触发，amd64（`ubuntu-latest`）+ arm64（`ubuntu-24.04-arm` 原生构建机）分架构原生构建仓根 `Dockerfile`（构建上下文为仓根，配 `.dockerignore`），合并多架构 manifest `:X.Y.Z` 推 **Docker Hub**（美国同岸快，带 registry 层缓存）；CNB（仓根 `.cnb.yml`，tag 由 Actions 回推触发）在国内重建同版多架构镜像推**腾讯云 TCR（国内正线）+ CNB 制品库**（GitHub 直推国内仓跨境慢/不稳——已踩坑；ACR 个人版镜像构建 2025-08 已停新增，退役）。Release 三侧同建：GitHub / Gitee 公开仓 / CNB（`.cnb.yml` git:release 接力）；main 推送由 `.github/workflows/mirror.yml` 主动同步到 Gitee / CNB（Gitee「仓库镜像」功能已弃用取消）。云服务器以容器运行（数据卷挂 `/app/server/data`，配置全走环境变量）。内网客户端（仓根 `uvmp-toolkit/`）与后端**同一条版本线**：同一 tag 由 `release.yml` 的 `client-*` job 出三平台安装包（Windows 版依赖 GitHub 的 Windows 构建机，Gitee Go / CNB 均无 Windows 节点，wine 跨构建已试验放弃），直传本仓 GitHub Release 后：`publish-images` job 推客户端产物镜像到 GHCR（`ghcr.io/zhangzhenyu91/j1toolkit`——GHCR 上服务端无镜像，同名无冲突；TCR/CNB 服务端仓库只放服务端镜像版本 tag，不混客户端产物）、`release-final` job 将 6 件安装包回传 Gitee 公开仓同名 Release（附件按 20MB 分卷+单卷重试保证跨境稳定、Gitee 单仓附件总量 1GB 仅留最新一份 Release）。因此必须保证：
   - `package.json` 包含 `"start"` 脚本（`node src/index.js`，与镜像 CMD 同口径）与 `"dev"` 脚本（`node --watch src/index.js`，本机开发热重启）；
   - 全部依赖写入 `package.json` 的 `dependencies` 并维护 `package-lock.json`（镜像内 `npm ci --omit=dev` 严格按 lockfile 安装），不得依赖全局安装或本地未声明的包；
   - 服务监听 `0.0.0.0`，端口从 env 读取（`PORT`，默认 `3000`）；单端口同时托管网页端（`server/public/`）与 `/api/v1`，用户自设反代 `https://toolkit.j1net.com → http://127.0.0.1:{PORT}`，无路径前缀配置（旧 `PROXY_PREFIX` 已废弃删除）；
   - 系统级依赖（如 LibreOffice，安全日非 PDF 附件转 PDF 用）必须装进仓根 `Dockerfile` 并在文档注明；可写数据只写 `server/data/`（容器内 `/app/server/data`，挂卷持久化），不入仓不进镜像；
   - 避免需要编译原生模块的依赖，确有必要时在文档中注明。
3. **配置与密钥**：所有环境相关配置一律从 env 读取，不硬编码、不入仓；只维护 `docker/.env.example`（全仓唯一一份，本机开发复制为 `server/.env`、服务器部署复制为 `docker/.env`），绝不创建真实 `.env` 或写入任何真实密钥。
4. **不擅自做 git 操作**：`git commit` / `push` / `reset` 等需用户明确指示。
5. **发版纪律**：日常开发只提交/推送代码，不打 tag、不发 Release、不触发发版构建；仅当用户明确说「发版/发布」时才执行打 tag + 构建 + Release 全链。**发版 tag 定版后不再删除/移动**——GitHub 删 tag 会带走其 Release 及全部附件（已踩坑丢过一轮客户端产物）；workflow 有修要验证时走 `release-publish.yml` 补跑或下一个 patch 版号。

## 三、技术栈（已定）

| 层 | 选型 |
|----|------|
| 前端 | 微信小程序原生 + tdesign-miniprogram 组件库；网页端原生 HTML/JS（`server/public/`，同一套 token） |
| 后端 | Node.js（云服务器 Docker 容器运行：GitHub Actions 按仓根 `Dockerfile` 构建多架构镜像，推腾讯云 TCR 并复制到 CNB 制品库；本机开发 `npm run dev`） |
| 存储 | MySQL（业务数据）/ Redis（会话、缓存）/ 腾讯云 COS（文件） |
| 鉴权 | JWT + Redis（详见 `开发指南.md` 第二章） |

## 四、UI 设计规范（已定稿：「政企蓝白」Enterprise Blue）

**实现方式**：组件使用 tdesign-miniprogram，在全局样式（`app.wxss`）覆盖 `--td-*` CSS 变量注入下列 token；状态标签、分段控件、月历色点等用 wxss 自绘。所有页面及后续应用子页面必须沿用同一套 token，与本体风格一致。定稿 token 以下表为准，定稿基准稿为 `design/` 下 4 份：`小程序-首页.html` / `小程序-出工日志.html` / `Web-首页.html` / `Web-出工日志.html`，组件级细则见 `design/设计规范.md`。网页端同用本套 token（`server/public/assets/theme.css` 注入）。旧定稿「包豪斯几何」「安全橙」已全部移除，不再作为实现依据。

| 用途 | 值 | 说明 |
|------|----|------|
| 品牌蓝 | `#0E3DA8` | 唯一主强调：主按钮、链接、激活态、图标、关键数据、FAB |
| 深蓝 | `#0A3592` | 仅小程序首页页首色带等页首区域，不作他用 |
| 浅蓝底 | `#E8F0FF` | 图标底、徽章底、信息条底、分段激活底 |
| 成功绿 | `#00B42A` | 验证通过 / 已完成（浅底 `#E8FFEA`、文字 `#0B8A37`） |
| 警告橙 | `#FF7D00` | 待核验 / 有备注 / 缺卡（浅底 `#FFF7E8`、文字 `#D25F00`） |
| 危险红 | `#F53F3F` | 未通过 / 删除 / 未读计数（浅底 `#FFECE8`） |
| 页面背景 | `#F5F7FA` | 双端统一 |
| 卡片 / 顶栏 | `#FFFFFF` | |
| 嵌套浅灰块 | `#F7F8FA` | 打卡块、月历摘要、搜索框底 |
| 正文 | `#1D2129` | |
| 次要文字 | `#4E5969` | |
| 辅助文字 | `#86909C` | |
| 边框 / 分隔线 | `#E5E6EB` | 发丝线 1px |
| 弱化占位 | `#C9CDD4` | 邻月日期、已读空心点、虚线占位 |

**形态语言**：白卡 12px 圆角 + 极轻阴影（`0 2px 8px rgba(0,0,0,.04)`，hover 加深至 `0 8px 20px rgba(0,0,0,.08)`），无描边无渐变无浮雕；按钮/输入框/分段控件 8px 圆角，标签 4px 小胶囊，FAB/头像圆形；**警示/提示信息块（未通过原因、备注、警示条等「浅底 + 左色条」一族）一律直角**；数字与时刻一律 tabular-nums；状态语义统一由「圆点 + 文字」小标签承担（通过绿 / 未通过红 / 待核验与备注橙 / 跨班与处理中浅蓝），月历图例固定绿=已验证、橙=有备注、红=待核实。

**特征元素**：小程序首页深蓝页首（状态栏白字）+ 数据概览卡负边距浮层（注意承载容器不可加 overflow:hidden，会裁掉浮层顶部）；宫格图标无底块、27px 深蓝线性图标直落白卡（Web 应用卡保留浅蓝底 44px 图标块）；每页主按钮至多一枚；板块标题 15–16px/600 不加几何记号。动效仅一次编排式上浮入场（`translateY(14px)` 淡出、时延链 ≤0.9s、落位静止），不做循环氛围动效，缓动 `cubic-bezier(.22,1,.36,1)`，全局 `prefers-reduced-motion: reduce` 关闭一切动画。字体：系统无衬线栈，标题 600/700 字重，三级文字颜色（`#1D2129` / `#4E5969` / `#86909C`）必须拉开层级。

> 禁止项：大面积渐变背景、复杂插画、厚重阴影、浮雕、多套圆角/间距/字体/按钮体系、促销感色条、娱乐化图标、仪表盘堆叠、每页多个主按钮、电商感与娱乐化装饰。

修改设计规范时，以本节为准并同步检查 `开发指南.md` 第九节的引用。

## 五、目录结构约定

- `miniprogram/` —— 微信小程序（原生 + tdesign-miniprogram）
- `uvmp-toolkit/` —— 内网工具箱客户端（Electron+Vue 壳 + Python 核心，装在内网被控机；UI 与本体同套「政企蓝白」token；2026-10 自私有仓迁回，改动前先读其《内网工具箱开发指南.md》；与后端同版本线发版，见 `release.yml`）
- `server/` —— 后端 Node.js 单端口整合服务（API + 托管网页端）
- `server/public/` —— 网页端（login.html / index.html 工作台 / callme.html / worklog.html / safeday.html / kvm.html / quiz.html / admin.html / client.html 内网客户端页（内部应用，仅 Web 端，直贴 CNB 制品库链接取安装包，见 `开发指南.md` 第十一节）/ sgcc-captcha.html 商旅顶象滑块接力页（小程序 web-view 承载，回传 captchaToken+constId 供绑定登录），公共资源 `assets/theme.css`、`assets/common.js`、`assets/icons.js`、`assets/md.js`（精简 Markdown 渲染器，通知中心与 Call Me 共用）；`kvm-device/` 为 KVM 设备一键接入安装包）
- `private/` —— 私有配套仓 [j1toolkit-private](https://github.com/zhangzhenyu91/j1toolkit-private) 的 submodule 挂载点（`git submodule update --init` 拉取）：`private/esgcc/` 待开发应用素材与商旅打卡逆向分析仓 `sgcc/`（交接文档/API 报告/抓包/tools；`base.apk` 107MB 不入仓本地留存）、`private/WorkLogs/` 旧独立服务历史数据归档（代码已整合进主服务，仅存数据）、`private/_archive/` 公开仓历史归档
- `design/` —— UI 设计稿：现行定稿「政企蓝白」（4 份基准稿 `小程序-首页.html` / `小程序-出工日志.html` / `Web-首页.html` / `Web-出工日志.html` + `设计规范.md` + `index.html` 展厅总览）；另存分享图生成工具链（`make_share_images.py` / `font/t.ttf` / `share-bg-preview.png`）；旧定稿已全部移除
- `manual/` —— 面向最终用户的使用指南（当前不在仓内；历史版截图含真实班组信息，已随公开化清理归档至私有仓 `_archive/manual/`——日后重建指南时截图须脱敏）
- 根目录 —— 文档与规则文件（AGENTS.md / README.md / 开发指南.md / LICENSE / .gitignore）+ 镜像构建文件（Dockerfile / .dockerignore，GitHub Actions 发版产线用，仅打包 `server/`）
- `docker/` —— docker compose 部署包（`docker-compose.yml` + `.env.example` 全仓唯一环境变量示例；自包含，拷到服务器 `cp .env.example .env` 填写后 `docker compose up -d`；本机开发复制为 `server/.env`）
- `.kimi-code/mcp.json` —— Kimi Code 项目级 MCP 配置（tdesign-mcp-server 组件知识库，随仓库分发，换机后启动会话自动生效；`.kimi-code/` 其余内容为会话数据，不入仓）

新应用接入 = 小程序分包页面 + 后端 `sys_app` 表配置（详见 `开发指南.md` 第五章；网页端页面接入见 `开发指南.md` 第十一节）。

## 六、编码规范

- 代码注释、提交信息、文档一律使用简体中文。
- 最小改动：只做任务需要的修改，不顺手重构、重命名或格式化无关代码。
- 新代码与周围既有代码的命名、注释密度、结构惯例保持一致。
- 交付完整代码，不留「略」「待补」占位。

## 七、验证规则

- 改动后必须做本地可行的自检：`node --check` 语法校验、JSON 合法性校验、（依赖已安装时）启动或测试检查。
- 本地无法模拟的环境（微信开发者工具真机预览、云端 Docker、数据库/Redis 实连）不得声称已验证，如实说明「待云端/真机验证」。
