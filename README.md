# Shade 壹匣

[![GitHub stars](https://img.shields.io/github/stars/zhangzhenyu91/j1toolkit)](https://github.com/zhangzhenyu91/j1toolkit/stargazers)
[![GitHub license](https://img.shields.io/github/license/zhangzhenyu91/j1toolkit)](https://github.com/zhangzhenyu91/j1toolkit/blob/main/LICENSE)
[![GitHub last commit](https://img.shields.io/github/last-commit/zhangzhenyu91/j1toolkit)](https://github.com/zhangzhenyu91/j1toolkit/commits/main)
![微信小程序](https://img.shields.io/badge/微信小程序-原生-07C160?logo=wechat&logoColor=white)
![TDesign](https://img.shields.io/badge/TDesign-Miniprogram-0052D9)
![Node.js](https://img.shields.io/badge/Node.js-%E2%89%A518-339933?logo=nodedotjs&logoColor=white)
![Express](https://img.shields.io/badge/Express-4-000000?logo=express&logoColor=white)
![MySQL](https://img.shields.io/badge/MySQL-业务存储-4479A1?logo=mysql&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-会话缓存-DC382D?logo=redis&logoColor=white)
![腾讯云COS](https://img.shields.io/badge/腾讯云_COS-文件存储-006DFF)
![JWT](https://img.shields.io/badge/鉴权-JWT-000000?logo=jsonwebtokens&logoColor=white)

> 仓库托管：主仓 [CNB](https://cnb.cool/j1net/j1toolkit/j1toolkit)，[GitHub](https://github.com/zhangzhenyu91/j1toolkit) 为备份镜像（推送时双仓自动同步）。

班组数字化工具平台：微信小程序 + 网页端，本体提供统一登录（账号密码 + 微信）与应用权限控制，各应用以分包/网页形式持续接入。

已接入应用：

- **Call Me**（`pkg-callme` + 网页端 `callme.html`）：基于 WeKnora 的 AI 知识库问答（SSE 流式对话）
- **出工日志**（`pkg-worklog` + 网页端 `worklog.html`）：派车/巡视/打卡记录，水印照片经 Dify 工作流验证（`WORKLOG_ENABLED` 开关）；网页端重构自旧 `WorkLogs/` 独立服务，同源直连 `/api/v1/worklog`
- **安全日活动记录**（分包 `pkg-safeday` + 网页端 `safeday.html`）：上传活动文档经 Dify 工作流生成记录文件；后端已合并进主服务（`server/src/safeday/`，`SAFEDAY_ENABLED` 开关），两端均要求 `safe-day` 应用权限；小程序端上传从聊天选取文件，记录经 `wx.openDocument` 打开
- **远程连接计算机**（PC 端，网页端 `kvm.html`）：对接 GLKVM Cloud 平台（员工同名账号代登取设备列表，卡片展示实时状态），终端/远程控制经带态跳转进平台页（仅能通过壹匣登录平台）；后端子模块 `server/src/kvm/`（`KVM_ENABLED` 开关），要求 `kvm` 应用权限
- **文件传输**（移动端，小程序分包 `pkg-filetransfer`）：向 KVM 设备虚拟 U 盘推送/取回文件（设备列表同上；上传弹层逐文件推送后统一挂载，下载点按即存相册或打开）；经壹匣转发点 `/api/v1/kvm/devices/{id}/push|mount|files|download`，要求 `file-transfer` 应用权限
- **水印添加**（移动端，小程序分包 `pkg-wmadd`）：选片/拍摄 → 4:3 裁剪 → 编辑水印（含防伪码、杆塔坐标选择）→ 服务端渲染水印仅回图（不传 COS、不入库、不触发 Dify 验证），自动存相册并全屏展示；后端 `server/src/wmadd/`（无 env 开关、无业务表），要求 `wm-add` 应用权限
- **题库刷题**（分包 `pkg-quiz` + 网页端 `quiz.html`）：Excel 导入题库（单选/多选/判断，仅网页端上传），顺序/随机/错题三模式刷题 + 背题模式（答案解析常显）+ 答题卡跳题与清空做题记录（保留错题本），错题本按题库分组专项练习（连对 3 次自动移出），题库分班组池/全部池、用户自行添加进个人题库，Dify 工作流 AI 逐题生成解析（导入后异步，全局并发 3）；网页端 quiz.html 为刷题练习 + 题库管理双页签（管理页签仅 admin/本班 team_admin）；后端 `server/src/quiz/`（`QUIZ_ENABLED` 开关），要求 `quiz` 应用权限
- **商旅打卡**（出工日志扩展）：商旅账号短信绑定 / 两次打卡 / 费用 / 照片双端同步 / 每日定时核查，权限并入 `work-log`（不单设应用）；后端 `server/src/sgccclockin/`（`SGCC_CLOCKIN_ENABLED` 开关，需先开启出工日志）
- **派车单每日自动同步**（出工日志扩展，仅检修一班）：每日 09:10 锁定目标 KVM 设备并把 U 盘挂载至被控机（被控机 09:15 起由桌面客户端「内网工具箱」导出，独立项目不在本仓），09:20 经文件传输链路取回当日派车单 xlsx 自动建出车卡片（派车单号随卡写入 `dispatch_order_no`，供「派车汇总」导出）、删 U 盘文件并解禁，结果通知超管与班组管理员；后端 `server/src/worklog/dispatch-sync.js`（`WORKLOG_DISPATCH_SYNC_ENABLED` 开关，需先开启出工日志与 KVM，详见 开发指南.md 7.7）

应用均带「适配终端」参数（`sys_app.terminal`：`both` 双端 / `mobile` 仅小程序 / `pc` 仅网页端），小程序与网页端宫格按端过滤展示。

**文档导航**：协作规则与 UI 定稿 token 见 `AGENTS.md`；开发全参考（架构/数据库/接口/对接细节/踩坑）见 `开发指南.md`；UI 定稿为「政企蓝白 Enterprise Blue」，基准稿见 `design/` 下 4 份（小程序/Web 首页与出工日志）+ `设计规范.md`。不宜公开的内容（内网客户端、逆向分析仓、历史数据）在私有仓 [j1toolkit-private](https://cnb.cool/j1net/j1toolkit/j1toolkit-private)，经 submodule 挂载于 `private/`。

## 技术栈

| 层 | 选型 |
|----|------|
| 前端 | 微信小程序原生 + tdesign-miniprogram（「政企蓝白」定制主题）；网页端原生 HTML/JS（同源 token） |
| 后端 | Node.js + Express（云服务器以 Docker 容器运行：阿里云 ACR 按仓根 `Dockerfile` 自动构建镜像，另有 CNB 云原生构建双轨见仓根 `.cnb.yml`；单端口同时托管网页端与 `/api/v1`） |
| 存储 | MySQL（业务数据）/ Redis（JWT 黑名单、会话）/ 腾讯云 COS（文件/照片） |
| 鉴权 | JWT + Redis，客户端 `Authorization: Bearer <token>` 携带（网页端 token 存 localStorage） |
| 外部服务 | WeKnora 知识库（Call Me）、Dify 工作流（出工日志照片验证、安全日活动记录生成、题库 AI 解析）、GLKVM Cloud（远程连接计算机/文件传输）、高德地图 Web 服务（出工日志/水印添加/商旅打卡地点天气）、商旅平台中继（商旅打卡） |

## 目录结构

```
miniprogram/   微信小程序（主包：登录/首页/我的/管理页）
  pkg-callme/    Call Me 分包
  pkg-worklog/   出工日志分包（主页 + 常用数据管理页）
  pkg-safeday/   安全日活动记录分包
  pkg-filetransfer/  文件传输分包（KVM 设备虚拟 U 盘上传/下载）
  pkg-wmadd/     水印添加分包（选片/裁剪/编辑水印/杆塔选择单页）
  pkg-quiz/      题库刷题分包（index/bank/practice/wrong/manage/pool 六页）
server/        后端 Node.js 单端口整合服务（API + 托管网页端）
  public/        网页端（login.html / index.html 工作台 / callme / worklog / safeday / kvm / quiz / admin + assets 公共资源 + kvm-device 设备安装包）
  src/routes/    本体路由（auth/user/app/admin/callme）
  src/worklog/   出工日志后端子模块（schema/cos/dify/verify 验证规则/photoverify 验证流水线/dispatch-sync 派车单同步/路由）
  src/safeday/   安全日活动记录后端子模块（dify/merge/store/路由）
  src/kvm/       远程连接计算机/文件传输 后端子模块（GLKVM 平台 API 客户端/路由/文件转发点）
  src/quiz/      题库刷题后端子模块（schema 建表种子/dify 解析/analyzer 队列/路由）
  src/wmadd/     水印添加后端子模块（渲染回图/geo/杆塔路由）
  src/sgccclockin/ 商旅打卡后端子模块（出工日志扩展：protocol 协议层/schema 建表种子/路由）
design/        UI 设计稿（定稿「政企蓝白 Enterprise Blue」：小程序/Web 首页与出工日志 4 份基准稿 + 设计规范.md + index.html 展厅 + 分享图工具链；旧定稿已全部移除）
private/       私有配套仓 j1toolkit-private（git submodule，需有权限账号 `git submodule update --init`）：uvmp-toolkit 内网工具箱客户端 / esgcc 商旅逆向分析仓与巡视素材 / WorkLogs 旧服务历史数据
```

## 后端部署（云服务器 Docker 容器）

镜像由阿里云容器镜像服务（ACR）「代码变更自动构建」产出：仓库已绑定 GitHub，构建规则 `tags: release-v$version`（上下文 `/`、仓根 `Dockerfile`）。发版即打 tag：

```bash
git tag release-v1.0.0 && git push origin release-v1.0.0   # ACR 自动构建镜像 :1.0.0
```

服务器侧运行两种方式任选：

**方式一（推荐）：docker compose**——仓库 `docker/` 目录为自包含部署包（`docker-compose.yml` + `.env.example`，**全仓唯一一份环境变量示例**，开发/部署共用），拷到服务器后：

```bash
cd docker
cp .env.example .env   # 按实际填写：IMAGE（镜像完整地址含版本标签）、JWT_SECRET、MYSQL_* 等
docker compose up -d   # 端口映射/数据卷（./data → /app/server/data）/重启策略均已配置
```

升级：改 `.env` 的 `IMAGE` 版本号 → `docker compose pull && docker compose up -d`。MySQL/Redis 在容器外时 `.env` 中 `MYSQL_HOST`/`REDIS_HOST` 不能填 `127.0.0.1`（容器内回环是容器自己），填宿主内网 IP 或 docker 网桥网关。

**方式二：1Panel 图形化建容器**——镜像 `registry.cn-beijing.aliyuncs.com/<命名空间>/j1toolkit:<版本>`；端口映射宿主 `3000` → 容器 `3000`（反代 `toolkit.j1net.com → 127.0.0.1:3000` 不变）；环境变量按 `docker/.env.example` 逐条配置（必填：`JWT_SECRET`、`MYSQL_*`；微信登录需 `WX_APPID`/`WX_SECRET`；Call Me 需 `WEKNORA_API_KEY`/`WEKNORA_AGENT_ID`；出工日志需 `WORKLOG_ENABLED=true` + COS + Dify 配置；安全日活动记录需 `SAFEDAY_ENABLED=true` + `DIFY_SAFEDAY_API_KEY`；题库刷题需 `QUIZ_ENABLED=true` + `DIFY_QUIZ_API_KEY`）；数据卷宿主目录 → `/app/server/data`；重启策略 always。

镜像已内置 LibreOffice（安全日非 PDF 附件转 PDF 合并用）与中文字体；验证：`curl http://127.0.0.1:3000/healthz` 返回 `{"code":0,...}` 即正常（Dockerfile 已配 HEALTHCHECK，容器列表可直看健康状态）。**首次迁移需把旧部署 `server/data/`（安全日记录、client-releases 安装包）拷入数据卷目录。**

**本机开发（不用镜像）**：`cd server` → 首次 `npm install` 并 `cp ../docker/.env.example .env` 按实际填写 → `npm run dev`（`node --watch` 改代码自动重启），浏览器访问 `http://127.0.0.1:3000/login.html` 测页面；前提为本机可连 `.env` 所指的 MySQL/Redis。

**网页端入口**：与 API 同端口同源——`https://toolkit.j1net.com/login.html` 登录页（账号密码登录，JWT 存 localStorage），`https://toolkit.j1net.com/` 即门户工作台/应用中心（index.html），各应用页 `callme.html` / `worklog.html` / `safeday.html` / `kvm.html` / `quiz.html`，管理员另有 `admin.html`（员工与权限管理）。

**反向代理（1Panel/Nginx）**：用户自设反代 `https://toolkit.j1net.com → http://127.0.0.1:{PORT}`，**一个端口同时服务网页与 API，无任何路径前缀配置**（旧 `PROXY_PREFIX` 机制已删除）；**SSE 流式对话必须**在反代配置补充：

```nginx
proxy_buffering off;      # 必需：否则流式响应被缓冲成整段返回
proxy_read_timeout 300s;  # 推荐：长生成不被掐断（服务端另有 15s 心跳兜底）
client_max_body_size 20m; # 图片上传（base64）需要
```

**初始化**：首次启动自动建 `sys_user` / `sys_team` / `sys_app` / `sys_user_app` / `sys_notice` / `sys_notice_read` / `sys_notice_del` 七张表，写入 Call Me、安全日活动记录、远程连接计算机、文件传输、水印添加应用记录（含适配终端 terminal），创建初始管理员（`ADMIN_USERNAME` / `ADMIN_PASSWORD`，默认 `admin` / `Admin@123`，**请尽快修改**）；`WORKLOG_ENABLED=true` 时再建出工日志 7 张业务表并写入应用与 7 名成员种子；`QUIZ_ENABLED=true` 时再建题库刷题 quiz_* 表并写入应用种子。给用户开权限：管理员在小程序「我的 → 权限管理」勾选即可。

## 环境变量清单

所有配置统一从 env 读取，敏感信息不入仓；`docker/.env.example`（全仓唯一一份）随功能同步维护。

| 变量名 | 说明 |
|--------|------|
| `PORT` | 服务端口（默认 3000；网页端与 API 同端口，反代 `toolkit.j1net.com → 127.0.0.1:PORT`） |
| `JWT_SECRET` | JWT 签名密钥 |
| `JWT_EXPIRES` | JWT 有效期（如 `7d`） |
| `JWT_WEB_EXPIRES` | 网页端登录态有效期（如 `12h`，默认 12 小时；网页端可远程控制公司电脑（KVM），到期强制重新登录，仅网页端登录生效，小程序不受影响） |
| `ADMIN_USERNAME` / `ADMIN_PASSWORD` / `ADMIN_NICKNAME` | 初始管理员（仅首次启动、账号不存在时创建；指定账号始终为 admin） |
| `WX_APPID` / `WX_SECRET` | 微信小程序 AppID / AppSecret（code 换 openid 用） |
| `MYSQL_HOST` / `MYSQL_PORT` / `MYSQL_USER` / `MYSQL_PASSWORD` / `MYSQL_DATABASE` | MySQL 连接 |
| `REDIS_HOST` / `REDIS_PORT` / `REDIS_PASSWORD` | Redis 连接（JWT 黑名单/会话） |
| `WEKNORA_API_URL` / `WEKNORA_API_KEY` / `WEKNORA_AGENT_ID` | WeKnora 知识库（Call Me；详见开发指南 Call Me 一节） |
| `COS_SECRET_ID` / `COS_SECRET_KEY` / `COS_BUCKET` / `COS_REGION` | 腾讯云 COS（出工日志照片） |
| `DIFY_API_URL` | Dify 地址（只填域名如 `http://10.2.24.13:8082`，`/v1` 由代码拼接；**所有 Dify 工作流共用此地址**，各工作流独立 API_KEY） |
| `DIFY_WXPUSH_API_KEY` | 通知微信外发工作流的 Dify API Key（仅系统自动触发的通知使用，手动推送不发微信：把通知文本发到个人/班组群 wxid，群消息支持@成员；wxid 由超管在网页端管理页维护，未配置则微信推送停用、站内通知照常，见开发指南第十六节） |
| `DIFY_WORKLOG_API_KEY` | 出工日志照片验证工作流的 Dify API Key |
| `WORKLOG_ENABLED` | 出工日志后端开关：`true` 开启（建表/种子/挂载路由），`false` 关闭 |
| `COS_WORKLOG_PREFIX` | 出工日志照片在 COS 的独立文件夹前缀（如 `worklog/`） |
| `COS_WORKLOG_BASE_URL` | 照片访问域名（可选；留空按 `https://{bucket}.cos.{region}.myqcloud.com` 拼接） |
| `AMAP_MAP_KEY` | 高德地图 Web 服务（出工日志「选择照片并添加水印」预填当前地点/天气、商旅打卡定位解析；key 类型须为「Web 服务」，未配置则对应字段手填，见开发指南 7.3/15.5） |
| `AMAP_BASE_URL` | 高德接口 base URL（可选，默认 `https://restapi.amap.com`；因费用问题走中转站时改为中转地址，接口路径 `/v3/...` 不变） |
| `SAFEDAY_ENABLED` | 安全日活动记录后端开关：`true` 开启（初始化数据目录并挂载 `/api/v1/safeday`），`false` 关闭 |
| `SAFEDAY_DATA_DIR` | 安全日记录 records.json 与生成产物（docs/）存放目录（默认 `./data/safeday`，相对路径按 server/ 解析） |
| `SAFEDAY_DEFAULT_SUPERIOR` | 安全日活动记录默认上级参加人员（可选；留空则由用户手填） |
| `DIFY_SAFEDAY_API_KEY` | 安全日记录生成工作流的 Dify API Key（与出工日志工作流共用 `DIFY_API_URL`） |
| `SAFEDAY_CALLBACK_TOKEN` | Dify 回调 token（可选；配置后回调接口须带 `?token=` 校验，留空则不校验） |
| `SAFEDAY_SOFFICE_PATH` | LibreOffice soffice 路径（可选，默认 `soffice`；安全日多文件含非 PDF 时后端转 PDF 合并依赖它，仓根 Dockerfile 构建的镜像已内置 LibreOffice） |
| `BASEMETAS_URL` | basemetas 文件预览服务地址（可选，如 `https://cloud.j1net.com/view`；配置后安全日记录可点击预览） |
| `KVM_ENABLED` | 远程连接计算机后端开关：`true` 开启（挂载 `/api/v1/kvm`），`false` 关闭 |
| `GLKVM_URL` / `GLKVM_PASSWORD` | GLKVM Cloud 平台地址与员工平台账号统一密码（以员工同名账号代登平台取设备列表；详见 开发指南.md 第十二节） |
| `QUIZ_ENABLED` | 题库刷题后端开关：`true` 开启（建 quiz_* 表并挂载 `/api/v1/quiz`），`false` 关闭 |
| `DIFY_QUIZ_API_KEY` | 题库「题目解析」工作流的 Dify API Key（与出工日志/安全日工作流共用 `DIFY_API_URL`；inputs 固定 type/stem/options/answer 四变量，输出 analysis；未配置则解析留空，其余功能不受影响，见开发指南第十四节） |
| `SGCC_CLOCKIN_ENABLED` | 商旅打卡（出工日志扩展）后端开关：`true` 开启（建表/种子并挂载 `/api/v1/sgcc`），`false` 关闭；需先开启出工日志 |
| `SGCC_JWT_SECRET` / `SGCC_SM2_SERVER_PUB` / `SGCC_SM2_CLIENT_PRIV` | 商旅平台协议密钥（取自商旅 App 逆向分析，联系维护者获取；密钥即 App 内固定值，各环境通用） |
| `SGCC_RSA_PUB` / `SGCC_RSA_PRIV` | 商旅 jsonx default 通道（打卡/详情/模板）：请求加密公钥 / 响应解密私钥 |
| `SGCC_DCU_PUB` / `SGCC_WLA_PRIV` | 商旅 jsonx slapp 通道（费用保存必走）：请求加密公钥 / 响应解密私钥 |
| `SGCC_VERSION` | 商旅 App 版本号（请求头 version，默认 `3.3.6`；App 升级后优先只改此项验证是否仍通） |
| `SGCC_VERSION_CODE` | 商旅 App 构建号（请求头 version-code，默认 `202609101630`；随 SGCC_VERSION 一并更新） |
| `SGCC_GRAY_VERSION` | 风控 SDK 版本（请求头 grayversion，默认 `2.4.7.1`） |
| `SGCC_SYNC_TIME` | 商旅打卡每日自动核查时间（HH:mm，默认 `23:00`） |
| `SGCC_STARTCLOCK_REMIND_TIME` | 开始打卡未打午间提醒时间（HH:mm，默认 `11:00`；提醒前先对当日卡上成员做打卡同步再判定，同步失败转人工核查通知；仅发微信提醒：本人 + 班组群，不写站内通知） |
| `SGCC_ENDCLOCK_REMIND_TIME` | 结束打卡未打傍晚提醒时间（HH:mm，默认 `18:00`；提醒前先对当日卡上成员做打卡同步再判定，同步失败转人工核查通知；仅发微信提醒：本人 + 班组群，不写站内通知） |
| `SGCC_SYNC_INTERVAL_MS` | 商旅打卡批量拉取成员间隔（毫秒，防风控，默认 `1500`） |
| `SGCC_PROXY_URL` | 商旅 API 出口 SOCKS5 代理（留空=直连）：`socks5h://用户名:密码@主机:端口`（socks5h=远端 DNS，推荐）。机房 IP 易触发风控 99000 窗口时，经目标属地宽带出口对齐打卡人位置；仅作用于商旅 API 小 JSON 调用，照片下载不走代理；代理不可用即报错，不静默降级直连（见 开发指南.md 15.1） |
| `SGCC_PROXY_PROBE_INTERVAL_MS` | 出口代理健康探测间隔（毫秒，默认 `300000`=5 分钟；仅配置 `SGCC_PROXY_URL` 时启用）：定时对代理端口做 TCP 建连探测（不向商旅主机发请求），正常↔异常 跳变时通知超管（站内+微信） |
| `WORKLOG_DISPATCH_SYNC_ENABLED` | 派车单每日自动同步开关：`true` 开启（每日定时取回被控机导出派车单自动建卡），`false` 关闭；需先开启出工日志与 KVM |
| `WORKLOG_DISPATCH_SYNC_TEAM` | 同步生效班组名（仅该班组启用，目前仅检修一班） |
| `WORKLOG_DISPATCH_DEVICE_DDNS` / `WORKLOG_DISPATCH_DEVICE_MAC` | 目标 KVM 设备定位（ddns）与 MAC 校验（防误操作他机） |
| `WORKLOG_DISPATCH_KVM_USER` | 同步任务代登平台账号（须平台可见该设备组；留空回退 `ADMIN_USERNAME`） |
| `WORKLOG_DISPATCH_PREPARE_TIME` / `WORKLOG_DISPATCH_FETCH_TIME` | 每日准备时间（锁定设备并挂载 U 盘至被控机，默认 `09:10`）与取件时间（下载建卡解禁，默认 `09:20`），北京时间 |

## 小程序开发（微信开发者工具）

1. 微信开发者工具导入 `miniprogram/` 目录；
2. 菜单「工具 → 构建 npm」（已配置 `packNpmManually`：主包 `tdesign-miniprogram` 构建到主包，`mp-html`/`markdown-it` 构建到 `pkg-callme` 分包；分包专用依赖在分包目录安装，装完都需重新构建）；
3. 修改 `miniprogram/config.js` 的 `BASE_URL` 为后端实际地址；
4. 将 `project.config.json` 的 `appid` 替换为真实小程序 AppID；
5. 小程序后台「开发管理 → 服务器域名」：request 合法域名配置后端域名（须 HTTPS）；**出工日志照片所在的 COS 域名需配置 downloadFile 合法域名**（批量下载功能依赖）。

## 验证状态

- 后端：依赖安装、`node --check` 语法、配置校验均本地通过；云端 MySQL/Redis/COS/WeKnora/Dify 实连以部署后日志为准。
- 小程序：全部 JS/JSON 静态校验通过；构建 npm 与真机交互（登录、键盘、相册、日历着色、批量下载）以真机验证为准。
