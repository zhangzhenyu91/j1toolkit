# AGENTS.md — Shade 壹匣

本文件是面向 AI 编码助手及新加入开发者的项目规则。开始任何改动前请先读完本文件。

## 一、项目概述

「Shade 壹匣」是班组数字化工具平台：微信小程序 + 网页端（Vue3 + Vite + TDesign，`web/`，构建产物 `web/dist` 与 API 同端口托管；原生 HTML/JS 回退实现 `server/public/` 未退役），本体负责统一登录（账号密码 + 微信登录）与应用权限控制，各业务应用以子页面/分包/网页形式持续接入。后端为 Go 服务（`go/`，Go 1.27+）；Node 实现（`server/`）为回退实现、未退役。crane-answer 为微信智能应答的迁移参考、未退役。「检修一班」为班组名，保留不变。

必读资料：

- `README.md` —— 项目概述、部署与环境变量清单
- `开发指南.md` —— 开发全参考（架构/数据库/接口/对接细节/踩坑约定），接入新应用前必读
- `design/` 现行基准稿 —— 布局以 `布局精修.html` 为准，交互以 `体验精修.html` / `体验精修-小程序.html` / `体验精修-Web.html` 为准，网页端宽屏以 `server/public/index.html` / `server/public/worklog.html` 实际页面为准（配套 `设计规范.md` 与 `index.html` 展厅总览，见第四节）；原 4 份首页/出工日志稿仅作历史结构参考

**私有配套仓（重要）**：本仓为公开仓；不宜公开的内容在私有仓 [j1toolkit-private](https://github.com/zhangzhenyu91/j1toolkit-private)，以 git submodule 挂载于 `private/`（`git submodule update --init` 拉取；无权限时该目录为空、主仓功能不受影响）。内含：`private/esgcc/` 商旅打卡逆向分析仓与巡视台账素材、`private/WorkLogs/` 旧服务历史数据、`private/_archive/` 公开仓历史归档。（`uvmp-toolkit/` 内网客户端 2026-10 已迁回本仓根目录——安装包本就公开发布、PyInstaller/Electron 产物可解包，源码私密性有限；客户端改动前先读其《内网工具箱开发指南.md》）换机拉代码时若发现 `private/` 为空，先提醒用户拉私有仓。

## 二、协作与部署规则（最高优先级）

1. **只写文件，不做部署**：职责是在本地工作目录编写/修改文件。不执行部署、不上传服务器、不主动连接任何云端资源（MySQL / Redis / COS / WeKnora / Dify 只在代码中按 env 读取对接，不实际访问）。
2. **交付即可运行（Docker 镜像化部署）**：构建以 GitHub Actions 为主——仓根 `.github/workflows/release.yml` 于 `release-vX.Y.Z` / `v*` tag 触发，amd64（`ubuntu-latest`）+ arm64（`ubuntu-24.04-arm` 原生构建机）分架构原生构建仓根 `Dockerfile`（构建上下文为仓根，配 `.dockerignore`），合并多架构 manifest `:X.Y.Z` 推 **GHCR + Docker Hub**（美国同岸快，带 registry 层缓存）；CNB（仓根 `.cnb.yml`，tag 由 Actions 回推触发）在国内重建同版多架构镜像推**腾讯云 TCR（国内正线）+ CNB 制品库**，并接力把客户端安装包传进 CNB Release 附件（GitHub 直推国内仓/平台跨境慢且不稳——已踩坑；ACR 个人版镜像构建 2025-08 已停新增，退役；Gitee 因单附件 100MB 限制与跨境上传不稳已弃用）。Release 双建：GitHub / CNB（`.cnb.yml` git:release 接力）；main 推送由 `.github/workflows/mirror.yml` 主动同步到 CNB（**只同步 main，不带 tag**——release tag 一律由 `release.yml` 全部构建完成后经 release-final 回推，防止 CNB 提前接力下载未就绪的客户端附件判失败，2026-10-09 起）。云服务器以容器运行（数据卷挂 `/app/server/data`，配置分层：启动必需项走环境变量 + 业务配置首次缺值导入运行配置中心，见 `开发指南.md` 第二十二节）。内网客户端（仓根 `uvmp-toolkit/`）与后端**同一条版本线**：同一 tag 由 `release.yml` 的 `client-*` job 出三平台安装包（Windows 版依赖 GitHub 的 Windows 构建机，Gitee Go / CNB 均无 Windows 节点，wine 跨构建已试验放弃），直传本仓 GitHub Release，CNB 侧「上传 Release 附件」流水线接力传 CNB Release（客户端只发 Release 附件 3 件：Windows exe + 麒麟 amd64/arm64 deb，无 docker 产物镜像）。因此必须保证：
   - 后端 Go 依赖写入 `go/go.mod` 并提交 `go.sum`（镜像内按模块缓存严格构建 `go build ./cmd/shade`），不得依赖全局工具或本地未声明的包；网页端依赖写入 `web/package.json` 并维护 `web/package-lock.json`（`npm ci` + `npm run build` 出 `web/dist` 随镜像托管）；
   - 服务监听 `0.0.0.0`，端口从 env 读取（`PORT`，默认 `3000`）；单端口同时托管网页端（`web/dist`，`SHADE_WEB_DIR` 可改）与 `/api/v1`，用户自设反代 `https://toolkit.j1net.com → http://127.0.0.1:{PORT}`，无路径前缀配置（旧 `PROXY_PREFIX` 已废弃删除）；
   - 系统级依赖（如 LibreOffice，安全日非 PDF 附件转 PDF 用）必须装进仓根 `Dockerfile` 并在文档注明；可写数据只写数据目录（容器内 `/app/server/data`，挂卷持久化），不入仓不进镜像；
   - 测试一律 fake/httptest，不连真实 MySQL/Redis/云端服务；配置分层（启动必需项 + 首次缺值导入进运行配置中心）见 `开发指南.md` 第二十二节；
3. **配置与密钥**：所有环境相关配置一律从 env 读取，不硬编码、不入仓；只维护 `docker/.env.example`（全仓唯一一份，本机开发复制为 `server/.env`、服务器部署复制为 `docker/.env`），绝不创建真实 `.env` 或写入任何真实密钥。
4. **不擅自做 git 操作**：`git commit` / `push` / `reset` 等需用户明确指示。
5. **发版纪律**：日常开发只提交/推送代码，不打 tag、不发 Release、不触发发版构建；仅当用户明确说「发版/发布」时才执行打 tag + 构建 + Release 全链。**发版 tag 定版后不再删除/移动**——GitHub 删 tag 会带走其 Release 及全部附件（已踩坑丢过一轮客户端产物）；workflow 有修要验证时走 `release-publish.yml` 补跑或下一个 patch 版号。

## 三、技术栈（已定）

| 层   | 选型                                                                                                                                                                              |
| ---- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 前端 | 微信小程序原生 + tdesign-miniprogram 组件库；网页端 Vue3 + Vite + TDesign Vue Next（`web/`，构建产物 `web/dist`）                                                                     |
| 后端 | Go 1.27+（`go/`，chi 路由，云服务器 Docker 容器运行：GitHub Actions 按仓根 `Dockerfile` 构建多架构镜像推 GHCR + Docker Hub，CNB 国内重建同版镜像推腾讯云 TCR + CNB 制品库；本机开发 `go run ./cmd/shade`，自检 `go test ./...`（fake/httptest））；Node.js ≥22 + Express（`server/`）为回退实现、未退役 |
| 存储 | MySQL（业务数据）/ Redis（会话、缓存）/ 腾讯云 COS（文件）                                                                                                                        |
| 鉴权 | JWT + Redis（详见 `开发指南.md` 第二章）                                                                                                                                          |

## 四、UI 设计规范（已定稿：「政企蓝白」Enterprise Blue）

**实现方式**：组件使用 tdesign-miniprogram，在全局样式（`app.wxss`）覆盖 `--td-*` CSS 变量注入下列 token；状态标签、分段控件、月历色点等用 wxss 自绘。所有页面及后续应用子页面必须沿用同一套 token，与本体风格一致。定稿 token 以下表为准；布局以 `design/布局精修.html` 为准，交互以 `design/体验精修.html` / `体验精修-小程序.html` / `体验精修-Web.html` 为准，网页端宽屏以 `server/public/index.html` / `server/public/worklog.html` 实际页面为准，组件级细则见 `design/设计规范.md`，原 4 份首页/出工日志稿仅作历史结构参考。仅颜色与圆角锁定，布局、字阶、间距、阴影、图标、导航和动效可按任务效率与可访问性优化。网页端同用本套 token（Vue 版 `web/src/styles.css` 注入，回退实现 `server/public/assets/theme.css` 注入）。旧定稿「包豪斯几何」「安全橙」已全部移除，不再作为实现依据。

| 用途          | 值        | 说明                                                     |
| ------------- | --------- | -------------------------------------------------------- |
| 品牌蓝        | `#0E3DA8` | 唯一主强调：主按钮、链接、激活态、图标、关键数据、FAB    |
| 深蓝          | `#0A3592` | 仅小程序首页页首色带等页首区域，不作他用                 |
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

**形态语言**：白卡 12px 圆角 + 极轻阴影（`0 2px 8px rgba(0,0,0,.04)`，hover 加深至 `0 8px 20px rgba(0,0,0,.08)`）；数据条、工具卡与高密度信息卡可用 1px 发丝线，禁止大面积渐变和浮雕；按钮/输入框/分段控件 8px 圆角，标签 4px 小胶囊，FAB/头像圆形；**警示/提示信息块（未通过原因、备注、警示条等「浅底 + 左色条」一族）一律直角**；数字与时刻一律 tabular-nums；状态语义统一由「圆点 + 文字」小标签承担（通过绿 / 未通过红 / 待核验与备注橙 / 跨班与处理中浅蓝），月历图例固定绿=已验证、橙=有备注、红=待核实。

**特征元素**：小程序首页深蓝页首（状态栏白字）+ 轻量数据条，应用入口优先、通知默认最新 1 条可展开；宫格图标无底块、27px 深蓝线性图标直落白卡（Web 应用卡保留浅蓝底 44px 图标块，五件套同构：图标块 + 名称 + 一句话描述 + 标签胶囊 +「进入应用」，可采用横向行卡——描述/标签在 `index.html` `APP_META` 登记，新应用必须同步登记，细则见 `开发指南.md` 第十一节「工作台应用卡展示规范」）；Web 工作台为应用主任务区 + 通知辅助栏，Web 出工日志常规为左筛选月历 + 右内容主列（≥1600px 保留右侧月历与核验组合，窄屏显示摘要可展开）；小程序出工日志 FAB 使用「记录与核验 / 管理与汇总」分组底部面板且新建日志固定底部，批量下载与汇总前核验均在手机底部面板展示；树障卡片折叠首屏为区段/垂距首行 + 树种×数量/水平距离/发现时间次行，发现时间与其他次行字段同级；与 Web 一致仅取 `found_at`，空值省略该字段，不使用 `created_at` 或 `handled_at` 兜底。每页主按钮至多一枚；板块标题 15–16px/600 不加几何记号。动效以一次编排式上浮入场（`translateY(14px)` 淡出、时延链 ≤0.9s、落位静止）与克制的状态过渡（展开、选中、焦点等 120–240ms）为主，不做旋转回弹或循环氛围动效，缓动 `cubic-bezier(.22,1,.36,1)`；`prefers-reduced-motion: reduce` 关闭动画与延迟，自建组件样式隔离时须在组件内同步降级，TDesign 内部及原生滚动行为另行核验。字体：系统无衬线栈，标题 600/700 字重，三级文字颜色（`#1D2129` / `#4E5969` / `#86909C`）必须拉开层级。

> 禁止项：大面积渐变背景、复杂插画、厚重阴影、浮雕、多套圆角/间距/字体/按钮体系、促销感色条、娱乐化图标、仪表盘堆叠、每页多个主按钮、电商感与娱乐化装饰。

**系统深色与小程序运行边界**：上表为浅色定稿，浅色色值与全端圆角不变；深色统一覆盖 `server/public/assets/theme.css` / `app.wxss` 的语义变量，小程序另启用 `app.json darkmode` + `theme.json`，不得页面各自造色板或简单反色。内联 `t-icon color` 用主题计算色并监听/解监听；主题读取优先 `wx.getAppBaseInfo()`，旧基础库回退 `wx.getSystemInfoSync()`；`wx.getWindowInfo()` 不提供 `theme`，只用于窗口尺寸与状态栏。小程序仅支持手机移动端，不提供 PC、iPad 等宽屏或横竖屏适配，不启用 `resizable`，不得新增 600/900/1200px 等宽屏断点、侧栏或双栏布局；页面、导航与弹层均按手机端实现，固定头/独立滚动体/固定操作脚、底部安全区与键盘避让必须同时成立，下拉展开后不能压住操作按钮。网页端宽屏仍以 `server/public/index.html` / `server/public/worklog.html` 实际页面为准。具体深色色阶与手机弹层骨架见 `design/设计规范.md`。

修改设计规范时，以本节为准并同步检查 `开发指南.md` 第九节的引用。

## 五、目录结构约定

- `miniprogram/` —— 微信小程序（原生 + tdesign-miniprogram）
- `uvmp-toolkit/` —— 内网工具箱客户端（Electron+Vue 壳 + Python 核心，装在内网被控机；UI 与本体同套「政企蓝白」token；2026-10 自私有仓迁回，改动前先读其《内网工具箱开发指南.md》；与后端同版本线发版，见 `release.yml`）
- `go/` —— 后端 Go 服务（Go 1.27+，module `shade`：`cmd/shade` 装配入口 + `internal/apps` 业务模块（含 authwx 微信登录、adminapi 管理、wechatanswer 微信智能应答、bridge 跨模块桥）+ `internal/config` 运行配置中心 + `internal/httpapi` 路由/鉴权/SPA 托管 + `internal/integrations` 外部服务对接）
- `web/` —— 网页端（Vue3 + Vite + TDesign Vue Next；`npm run build` 出 `web/dist` 由 Go 服务 SPA 托管，与 `/api/v1` 同端口）
- `server/` —— 后端 Node.js 回退实现（**未退役**，接口契约与 Go 实现一致：API + 托管网页端，细节见 `开发指南.md`）
- `server/public/` —— 网页端原生 HTML/JS 回退实现（login.html / index.html 工作台 / callme.html / worklog.html / wmadd.html 水印添加 / safeday.html / kvm.html / quiz.html / netdisk.html 团队网盘 / treehazard.html 树障隐患记录 / crossing.html 交跨记录 / teamdata.html 班组数据（worklog 原「数据管理」页签迁移于此） / office.html ONLYOFFICE 承载页（网盘在线编辑 + 出工日志表格/备注附件/安全日记录的 OO 只读预览共用壳） / share.html 网盘公开分享页（免登录，`#/s/<share_id>`）/ admin.html / sgcc-captcha.html 商旅顶象滑块接力页（小程序 web-view 承载，回传 captchaToken+constId 供绑定登录），公共资源 `assets/theme.css`、`assets/common.js`、`assets/icons.js`、`assets/md.js`（精简 Markdown 渲染器，通知中心与 Call Me 共用）；`kvm-device/` 为 KVM 设备一键接入安装包；内网客户端下载入口在登录页推广区（CNB 公开仓 Releases 最新 tag 实时拼接，见 `开发指南.md` 第十一节））
- `private/` —— 私有配套仓 [j1toolkit-private](https://github.com/zhangzhenyu91/j1toolkit-private) 的 submodule 挂载点（`git submodule update --init` 拉取）：`private/esgcc/` 待开发应用素材与商旅打卡逆向分析仓 `sgcc/`（交接文档/API 报告/抓包/tools；`base.apk` 107MB 不入仓本地留存）、`private/WorkLogs/` 旧独立服务历史数据归档（代码已整合进主服务，仅存数据）、`private/_archive/` 公开仓历史归档
- `crane-answer/` —— 微信群智能应答平台 [crane-answer](https://github.com/zhangzhenyu91/crane-answer) 的 submodule 挂载点（`git submodule update --init` 拉取）：群消息监听按规则调 Dify Workflow 的后端 + Web 管理端，「树木排查（会话捕获）」产出的结构化记录经 `POST /api/v1/treehazard/ingest` 直推入本仓树障台账（对接契约见 `开发指南.md` 19.3）；本仓微信智能应答能力迁移自该平台，**迁移参考、未退役**（直推端点继续兼容）；在该目录内改动前先读其自带 `AGENTS.md`
- `design/` —— UI 设计稿：现行定稿「政企蓝白」（布局基准 `布局精修.html`，交互基准 `体验精修.html` / `体验精修-小程序.html` / `体验精修-Web.html`，网页端宽屏基准 `web/` 实际页面（回退实现 `server/public/index.html` / `server/public/worklog.html`） + `设计规范.md` + `index.html` 展厅总览；原 4 份首页/出工日志稿仅作历史结构参考，应用设计稿 `小程序-团队网盘.html` / `Web-团队网盘.html` / `Web-微信智能应答.html` / `Web-应用日志.html` / `Web-应用配置.html`）；另存分享图生成工具链（`make_share_images.py` / `font/t.ttf` / `share-bg-preview.png`）与 `dify-json-extract.py`（Dify 自定义代码节点脚本，现行为本仓维护版本，支持字母杆号；`crane-answer/design/` 同名文件为迁移参考、未同步）；旧定稿已全部移除
- `manual/` —— 面向最终用户的使用指南（当前不在仓内；历史版截图含真实班组信息，已随公开化清理归档至私有仓 `_archive/manual/`——日后重建指南时截图须脱敏）
- 根目录 —— 文档与规则文件（AGENTS.md / README.md / 开发指南.md / LICENSE / .gitignore）+ 镜像构建文件（Dockerfile / .dockerignore，GitHub Actions 发版产线用，打包 `go/` 服务、`web/` 构建产物与 `server/assets/` 素材；`server/public/sgcc-captcha.html` 用于商旅滑块接力）
- `docker/` —— docker compose 部署包（`docker-compose.yml` + `.env.example` 全仓唯一环境变量示例；compose 仅含 Shade 服务端本身，统一入 `j1toolkit` 外部网络——OpenList/ONLYOFFICE 等配套容器由部署者自装并加入同名网络按容器名互通；自包含，拷到服务器 `cp .env.example .env` 填写后 `docker compose up -d`；本机开发复制为 `server/.env`；`onlyoffice/` 子目录为 ONLYOFFICE 自装对接说明与「Shade 壹匣」品牌替换素材，见 `docker/onlyoffice/README.md`）
- `.kimi-code/mcp.json` —— Kimi Code 项目级 MCP 配置（tdesign-mcp-server 组件知识库，随仓库分发，换机后启动会话自动生效；`.kimi-code/` 其余内容为会话数据，不入仓）

新应用接入 = 小程序分包页面 + 后端 `sys_app` 表配置（详见 `开发指南.md` 第五章；网页端页面接入见 `开发指南.md` 第十一节）。

## 六、编码规范

- 代码注释、提交信息、文档一律使用简体中文。
- 最小改动：只做任务需要的修改，不顺手重构、重命名或格式化无关代码。
- 新代码与周围既有代码的命名、注释密度、结构惯例保持一致。
- 交付完整代码，不留「略」「待补」占位。
- Go 代码（`go/`）用 `gofmt` / `go fmt` 格式化；Vue/JS/JSON/HTML/CSS/Markdown/YAML 走下述 Prettier 单一来源，Python（uvmp-toolkit）用 `ruff format`，各管各的语言范围。
- **全仓格式化单一来源**：仓根 `.prettierrc.json` / `.prettierignore`（Prettier 3）。JS/JSON/HTML/CSS/Vue/Markdown/YAML 统一 `npx --yes prettier@3 --write .`；Python（uvmp-toolkit）用 `ruff format`；`.wxml` 无 Prettier 推断 parser，需临时改后缀 `.html` 并以 `--parser html` 处理。排除范围（依赖、构建产物、两个 submodule、运行数据、二进制/Office 文档等）以 `.prettierignore` 为准；**格式化单独成提交，不得混入业务改动**。

## 七、验证规则

- 改动后必须做本地可行的自检：Go 改动过 `/d/Program Files/Go/bin/go.exe test ./...`（Windows；macOS/Linux 即 `go test ./...`）+ `go vet ./...` + `go build ./...`；**Go 测试一律 fake/httptest，不连真实 MySQL/Redis/云端服务**（本地无库验证可用 `SHADE_LOCAL_FIXTURE=1`）；网页端 `web/` 过 `npm run build`；小程序与 Node 回退实现过 `node --check` 语法校验、JSON 合法性校验、（依赖已安装时）启动或测试检查。
- 本地无法模拟的环境（微信开发者工具真机预览、云端 Docker、数据库/Redis/COS/WeKnora/Dify/商旅/WeChatPadPro 实连）不得声称已验证，如实说明「待云端/真机验证」。
