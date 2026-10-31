# AGENTS.md — Shade 壹匣

本文件是面向 AI 编码助手及新加入开发者的项目规则。开始任何改动前请先读完本文件。

## 一、项目概述

「Shade 壹匣」是班组数字化工具平台：微信小程序 + 网页端 + 内网桌面客户端。本体负责统一登录（账号密码 + 微信登录）、班组隔离与应用级权限控制，各业务应用以小程序分包、网页页面或客户端插件形式持续接入。

- 后端：Go 服务（`go/`，Go 1.27+，module `shade`），单端口同时托管 `/api/v1` 与网页端构建产物。
- 网页端：Vue 3 + Vite + TDesign Vue Next（`web/`，构建产物 `web/dist`）；`server/public/` 为 Go 托管的原生 HTML/JS 回退页面。
- 小程序：原生 + tdesign-miniprogram（`miniprogram/`）。
- 内网客户端：Electron 33 + Vue 3 壳 + Python sidecar（`uvmp-toolkit/`）。
- 旧 Node 后端与历史客户端构建轨均已删除；文档与示例统一使用脱敏后的名称、域名与数据。

必读资料：

| 文档                                 | 用途                                                                             |
| ------------------------------------ | -------------------------------------------------------------------------------- |
| `README.md`                          | 项目简介、功能、快速开始、部署与发版                                             |
| `开发指南.md`                        | 开发全参考：架构、数据库、接口、应用对接、接入手册、部署运维、踩坑与环境变量附录 |
| `design/设计规范.md`                 | 「政企蓝白」组件级细则                                                           |
| `uvmp-toolkit/内网工具箱开发指南.md` | 客户端架构、插件规范、RPC、构建与验证                                            |

**私有配套仓（重要）**：本仓为公开仓；不宜公开的内容在私有仓 `j1toolkit-private`，以 git submodule 挂载于 `private/`（`git submodule update --init` 拉取；无权限时该目录为空，主仓功能不受影响）。内含商旅打卡逆向分析仓与巡视素材、旧服务历史数据、公开仓历史归档。换机拉代码时若发现 `private/` 为空，先提醒用户拉私有仓。

## 二、协作与部署规则（最高优先级）

1. **只写文件，不做部署**：职责是在本地工作目录编写/修改文件。不执行部署、不上传服务器、不主动连接任何云端资源（MySQL / Redis / COS / WeKnora / Dify 等只在代码中按 env 读取对接，不实际访问）。
2. **交付即可运行（Docker 镜像化部署）**：构建以 GitHub Actions 为主——仓根 `.github/workflows/release.yml` 于 `release-vX.Y.Z` / `v*` tag 触发，amd64 + arm64 分架构原生构建仓根 `Dockerfile`（上下文为仓根，配 `.dockerignore`），合并多架构 manifest 推 GHCR + Docker Hub；CNB（仓根 `.cnb.yml`，tag 由 Actions 回推触发）在国内重建同版镜像推腾讯云 TCR + CNB 制品库，并接力上传客户端 Release 附件。
   - 后端 Go 依赖写入 `go/go.mod` 并提交 `go.sum`；网页端依赖写入 `web/package.json` 并维护 `web/package-lock.json`；客户端 Python 依赖写入构建脚本并锁定版本。
   - 服务监听 `0.0.0.0`，端口读 `PORT`（默认 3000）；单端口同时托管网页端（`SHADE_WEB_DIR`）与 `/api/v1`，反代只做域名 → `127.0.0.1:PORT`，无路径前缀。
   - 系统级依赖（如 LibreOffice）必须装进仓根 `Dockerfile` 并在文档注明；可写数据只写数据目录（容器内 `/app/server/data`，挂卷持久化），不入仓不进镜像。
   - 测试一律 fake/httptest，不连真实 MySQL/Redis/云端服务；本地无库验证用 `SHADE_LOCAL_FIXTURE=1`。
3. **配置与密钥**：所有环境相关配置从 env 读取，不硬编码、不入仓；只维护 `docker/.env.example`（全仓唯一一份）。业务配置首次缺值导入运行配置中心（`sys_runtime_config`，敏感项加密），此后以管理端「应用配置」为准。绝不创建真实 `.env` 或写入真实密钥。
4. **不擅自做 git 操作**：`git commit` / `push` / `reset` 等需用户明确指示。
5. **发版纪律**：日常开发只提交/推送代码，不打 tag、不发 Release、不触发发版构建；仅当用户明确说「发版/发布」时才执行打 tag + 构建 + Release 全链。**发版 tag 定版后不再删除/移动**——删 tag 会带走 Release 及附件；workflow 修复验证走 `release-publish.yml` 或下一个 patch 版本。
6. **文档同步**：改配置、路由、数据目录、部署方式或业务口径时，同步更新 `README.md`、`开发指南.md`、`docker/.env.example` 与必要的行内注释；纯格式化单独提交，不混入业务改动。

## 三、技术栈（已定）

| 层     | 选型                                                                         |
| ------ | ---------------------------------------------------------------------------- |
| 前端   | 微信小程序原生 + tdesign-miniprogram；网页端 Vue 3 + Vite + TDesign Vue Next |
| 后端   | Go 1.27+（chi 路由；本机 `go run ./cmd/shade`，自检 `go test ./...`）        |
| 存储   | MySQL（业务数据）/ Redis（会话、黑名单、锁）/ 腾讯云 COS（文件）             |
| 鉴权   | JWT + Redis 黑名单；网页端短时效 token，小程序微信静默续期                   |
| 客户端 | Electron 33 + Vue 3 + Python sidecar（PyInstaller）                          |
| 部署   | Docker 多架构镜像、docker compose、Nginx 反代                                |

## 四、UI 设计规范（已定稿：「政企蓝白」Enterprise Blue）

**实现方式**：组件使用 tdesign-miniprogram，在全局样式（`app.wxss`）覆盖 `--td-*` CSS 变量注入下列 token；状态标签、分段控件、月历色点等用 wxss 自绘。所有页面及后续应用子页面必须沿用同一套 token。布局以 `design/布局精修.html` 为准，交互以 `design/体验精修.html` / `体验精修-小程序.html` / `体验精修-Web.html` 为准，网页端宽屏以 `server/public/index.html` / `server/public/worklog.html` 实际页面为准，组件级细则见 `design/设计规范.md`。

| 用途          | 值        | 说明                                                     |
| ------------- | --------- | -------------------------------------------------------- |
| 品牌蓝        | `#0E3DA8` | 唯一主强调：主按钮、链接、激活态、图标、关键数据、FAB    |
| 深蓝          | `#0A3592` | 仅小程序首页页首色带等页首区域                           |
| 浅蓝底        | `#E8F0FF` | 图标底、徽章底、信息条底、分段激活底                     |
| 成功绿        | `#00B42A` | 验证通过 / 已完成（浅底 `#E8FFEA`、文字 `#0B8A37`）      |
| 警告橙        | `#FF7D00` | 待核验 / 有备注 / 缺卡（浅底 `#FFF7E8`、文字 `#D25F00`） |
| 危险红        | `#F53F3F` | 未通过 / 删除 / 未读计数（浅底 `#FFECE8`）               |
| 页面背景      | `#F5F7FA` | 双端统一                                                 |
| 卡片 / 顶栏   | `#FFFFFF` |                                                          |
| 嵌套浅灰块    | `#F7F8FA` | 打卡块、月历摘要、搜索框底                               |
| 正文          | `#1D2129` |                                                          |
| 次要文字      | `#4E5969` |                                                          |
| 辅助文字      | `#86909C` |                                                          |
| 边框 / 分隔线 | `#E5E6EB` | 发丝线 1px                                               |
| 弱化占位      | `#C9CDD4` | 邻月日期、已读空心点、虚线占位                           |

**形态语言**：白卡 12px 圆角 + 极轻阴影（`0 2px 8px rgba(0,0,0,.04)`，hover 加深至 `0 8px 20px rgba(0,0,0,.08)`）；数据条、工具卡与高密度信息卡可用 1px 发丝线；禁止大面积渐变和浮雕；按钮/输入框/分段控件 8px 圆角，标签 4px 小胶囊，FAB/头像圆形；**警示/提示信息块（未通过原因、备注、警示条等「浅底 + 左色条」一族）一律直角**；数字与时刻一律 tabular-nums；状态语义统一由「圆点 + 文字」小标签承担；月历图例固定绿=已验证、橙=有备注、红=待核实。

**特征元素**：小程序首页深蓝页首 + 轻量数据条，应用入口优先、通知默认最新 1 条可展开；宫格图标无底块、27px 深蓝线性图标直落白卡（Web 应用卡保留浅蓝底 44px 图标块，五件套同构：图标块 + 名称 + 一句话描述 + 标签胶囊 +「进入应用」）；Web 工作台为应用主任务区 + 通知辅助栏；Web 出工日志常规为左筛选月历 + 右内容主列（≥1600px 保留右侧月历与核验组合，窄屏显示摘要可展开）；小程序出工日志 FAB 使用「记录与核验 / 管理与汇总」分组底部面板且新建日志固定底部。每页主按钮至多一枚；板块标题 15–16px/600 不加几何记号。动效以一次编排式上浮入场与克制的状态过渡（120–240ms）为主，缓动 `cubic-bezier(.22,1,.36,1)`；`prefers-reduced-motion: reduce` 关闭动画与延迟，自建组件样式隔离时须在组件内同步降级。字体：系统无衬线栈，标题 600/700 字重，三级文字颜色必须拉开层级。

> 禁止项：大面积渐变背景、复杂插画、厚重阴影、浮雕、多套圆角/间距/字体/按钮体系、促销感色条、娱乐化图标、仪表盘堆叠、每页多个主按钮、电商感与娱乐化装饰。

**系统深色与小程序运行边界**：浅色 token 与全端圆角不变；深色统一覆盖 `server/public/assets/theme.css` / `app.wxss` 的语义变量，小程序另启用 `app.json darkmode` + `theme.json`，不得页面各自造色板或简单反色。内联 `t-icon color` 用主题计算色并监听/解监听；主题读取优先 `wx.getAppBaseInfo()`，旧基础库回退 `wx.getSystemInfoSync()`；`wx.getWindowInfo()` 不提供 `theme`，只用于窗口尺寸与状态栏。小程序仅支持手机移动端，不提供 PC、iPad 等宽屏或横竖屏适配，不启用 `resizable`，不得新增 600/900/1200px 等宽屏断点、侧栏或双栏布局；固定头/独立滚动体/固定操作脚、底部安全区与键盘避让必须同时成立。网页端宽屏仍以 `server/public/index.html` / `server/public/worklog.html` 实际页面为准。

## 五、目录结构约定

- `miniprogram/` —— 微信小程序（主包 + `pkg-*` 各应用分包）。
- `go/` —— 后端 Go 服务（`cmd/shade` 装配入口；`internal/apps` 业务模块；`internal/config` 运行配置中心；`internal/httpapi` 路由/鉴权/SPA；`internal/integrations` 外部服务对接；`internal/compat` 兼容层）。
- `web/` —— 网页端 Vue 3（`npm run build` 出 `web/dist`，由 Go 服务 SPA 托管）。
- `server/` —— Go 运行素材与静态回退页（`public/` 原生 HTML/JS，`assets/` 字体/模板/素材）；**不含 Node 服务端代码、`package.json` 或 `node_modules`**。
- `uvmp-toolkit/` —— 内网工具箱客户端（Electron 壳 + Python 核心 + 构建/测试/安装脚本；现行唯一正式轨，历史构建轨已删除）。
- `design/` —— UI 设计稿与设计规范。
- `docker/` —— 部署包（compose + `.env.example` + ONLYOFFICE 对接说明）。
- `.github/workflows/` —— 发版、补跑、main 同步流水线。
- `private/` —— 私有配套仓 submodule，无权限时为空。

新应用接入 = 小程序分包/网页页面 + `sys_app` 配置 + 后端模块（详见 `开发指南.md` 第六节）。

## 六、编码规范

- 代码注释、提交信息、文档一律使用简体中文。
- 最小改动：只做任务需要的修改，不顺手重构、重命名或格式化无关代码。
- 新代码与周围既有代码的命名、注释密度、结构惯例保持一致。
- 交付完整代码，不留「略」「待补」占位；删除被新实现取代的死代码、别名与兼容壳。
- Go 代码（`go/`）用 `gofmt` / `go fmt`；Vue/JS/JSON/HTML/CSS/Markdown/YAML 用仓根 `.prettierrc.json` / `.prettierignore`（Prettier 3）；Python（`uvmp-toolkit/`）用 `ruff format`。
- **全仓格式化单一来源**：JS/JSON/HTML/CSS/Vue/Markdown/YAML 统一 `npx --yes prettier@3 --write .`；`.wxml` 无 Prettier 推断 parser，需临时改后缀 `.html` 并以 `--parser html` 处理。排除范围以 `.prettierignore` 为准；**格式化单独成提交，不得混入业务改动**。
- 配置键、路由、数据表或目录结构变化时，同步更新 `开发指南.md` 与 `docker/.env.example`；README 只保留面向使用者的必要信息。

## 七、验证规则

- **Go**：`cd go && go test ./... && go vet ./... && go build ./...`。测试一律 fake/httptest，不连真实 MySQL/Redis/云端；本地无库可用 `SHADE_LOCAL_FIXTURE=1`。
- **网页端**：`cd web && npm run build`。
- **小程序 / 静态回退页**：`node --check` 语法校验、JSON 合法性校验；涉及交互的改动需在微信开发者工具或真机确认。
- **内网客户端**：`cd uvmp-toolkit && python -m unittest discover -s tests -v`、`python toolkit/app.py selfcheck`、RPC ping、`cd desktop && node --test test/*.test.mjs test/*.test.cjs && npm run build`；分层验证（CDP 自检/Core 冒烟/Linux 实机）见其开发指南第十二节。
- 本地无法模拟的环境（微信真机、云端 Docker、数据库/Redis/COS/WeKnora/Dify/商旅/WeChatPadPro 实连）不得声称已验证，如实说明「待云端/真机验证」。
- 行为类改动必须先复现、后验证；测试通过不等于端到端可用，UI 与关键链路需实际运行观察。

## 八、提交与文档

- 提交信息使用中文，按「类型：摘要」组织（如 `修复：…`、`构建：…`、`文档：…`）。
- 一次提交只做一个主题；格式化、依赖升级、业务改动分提交。
- 文档改动与代码改动同批提交；涉及公开仓库的内容必须再次检查脱敏（单位、线路、人员、内部 IP、真实域名、密钥）。
- 不确定是否应提交的本地临时文件（调试日志、截图、构建产物）一律不入仓，并补进 `.gitignore`。
