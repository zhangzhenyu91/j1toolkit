# 内网工具箱（uvmp-toolkit）

> 本客户端现居公开仓 [j1toolkit](https://github.com/zhangzhenyu91/j1toolkit) 根目录 `uvmp-toolkit/`（2026-10 自私有仓迁回：安装包本就公开发布、产物可解包，源码私密性有限）。

内网被控机（银河麒麟 V10 SP1、统信 UOS 20 优先，兼顾 Windows 10）上的桌面客户端与自动化工具集，面向派车系统（UVMP）的导出类业务。壳为 **Electron 45.3.0**，业务核心跑在随包内置的 **CPython 3.15.2** 独立运行时上——目标机无需安装 Python。仅面向上述三个平台的近期稳定版本；更老的发行版回退源码 CLI 模式。

## 功能

| 应用     | 说明                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| -------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 题库搜题 | 屏幕双扫描框实时 OCR 搜题：Excel 题库导入本机落盘（多题库启停管理，`.xlsx` 走 read-excel-file、老 `.xls` 走内置读取器，不再依赖已停维护的 SheetJS），框选区域对准题目即出答案悬浮窗；全离线本地推理，不经网络；页面按 `engineReady/engineError` 显示 OCR 就绪状态与失败原因                                                                                                                                                                                                                                                                                                                                                             |
| 学堂刷课 | 人工登录受控浏览器后自动学习学堂平台课程：多标签识别、全分页扫描、视频真实 ended/上报确认、非视频加载，单课 3 次未过跳过继续、末页后从头复核；支持仅扫描、暂停恢复与按培训班断点续刷                                                                                                                                                                                                                                                                                                      |
| 机巡审核 | 受控浏览器批量审核机巡缺陷识别任务：按实例账号自动登录（唯一登录方式；启动网址可只填根地址，由系统自动跳 `/login`；离线验证码 OCR，失败无限重试），点击开始即自动打开浏览器、登录并进入任务页；支持一键全部启动、浏览器最大化，全屏实时大屏按实例展示简洁日志/进度，点击实例切换真实浏览器并显示完整日志浮窗，支持一键停止全部；慢图健康检测、任务边界/超未审核阈值立即重启、崩溃/OOM/卡死看门狗；任务名称跳过表、失败/按钮未就绪/系统缺陷汇总、无视缺陷框；缺陷照片原图按「线路名称/杆塔号」归档（缩略图不当原图）并另存带缺陷框的标注图，标注返回 `drawn`/`skipped` 计入汇总；重跑按输出目录的持久化归属索引（路径→fileGuid+大小+sha256）只补缺的原图或标注、不重复下载，同名不同 fileGuid 另存 (2)，照片下载/标注失败的任务本轮不进跳过表以便下轮补齐（同一任务跨重试累计），下载拒绝 206 部分内容与超 80MB 响应，系统缺陷框明细读不到按异常重试而不用空列表冒充无框；跳过表 xlsx 只补 A 列不改整簿、读失败不重写（未填跳过表时默认用输出目录 `跳过任务.txt`，同目录多次启停继续追加）；同一输出目录启动时自动恢复 `审核汇总.jsonl`/`.xlsx` 历史，重跑不丢旧行也不重复登记；每实例独立浏览器 profile（升级到本版后首次需重新登录一次）；全部超时按实例配置 |
| 轨迹导出 | 按派车单号清单（xlsx 文件或粘贴文本）按序导出派车单 PDF、行驶轨迹 PDF 与轨迹点 CSV，支持断点续传                                                                                                                                                                                                                                                                                                                                                                                          |
| 树障同步 | 按壹匣服务端经 U 盘下发的指令同步树障统计表 xlsx：snapshot 把内网表拷贝上盘，replace 用 U 盘新表覆盖内网表（原文件自动备份 .bak）；`usb-agent` 常驻消费指令并回写结果                                                                                                                                                                                                                                                                                                                     |
| 交跨同步 | 按壹匣服务端经 U 盘下发的指令同步交跨统计表 xlsx：snapshot 把内网表拷贝上盘，replace 用 U 盘新表覆盖内网表（原文件自动备份 .bak）；契约同树障                                                                                                                                                                                                                                                                                                                                             |
| 派车同步 | 按壹匣服务端经 U 盘下发的指令导出当日派车单（全部状态）为 `yyyy-mm-dd-派车单.xlsx`，写入 KVM 虚拟 U 盘（卷标 GLKVM），供服务端取走                                                                                                                                                                                                                                                                                                                                                        |
| 节点集群 | Hub/Worker 入网审批、心跳、节点表、Hub 指令转发（支持跨班组 Hub 中转）、多节点导出分片、按平台/架构手动确认更新分发；支持服务端按班组绑定同步节点。管理接口只接受本机来源（Host 白名单 + Origin 同源 + 0600 本机随机令牌），分片结果带归属校验，升级前的无归属旧缓存自动重算                                                                                                                                                                                                                                                                                                                                         |

技术要点：Electron 45.3.0 + Vue 3 + TDesign 桌面客户端（「政企蓝白」主题，壹匣同族），12 页界面（工作台、执行历史、设置 + 8 个业务/工具页 + 集群）；业务核心为内置 CPython 3.15.2 独立运行时上的 Python sidecar（SSO 国密登录 + 请求签名逆向，见《接口分析.md》），壳与核心经 stdio JSON-RPC 通信；PDF 包版打印优先用自带 Electron（`--print-to-pdf`），官方轨迹直出（CDP）才需要目标机宿主 Chrome 或公司指定浏览器；U 盘同步由服务端下达指令，客户端 `usb-agent` 常驻消费（udev + 兜底轮询，单实例）；集群服务在同一常驻进程内按 config 启停，节点间 HMAC + Hub 人工审批入网；题库搜题的截屏/OCR/悬浮窗全部在壳层（onnxruntime-node + PP-OCRv4 mobile 模型随包，CPU 推理无厂商绑定，win-x64/麒麟 amd64/arm64 同构；机巡自动登录复用同一运行时，随包 ddddocr common 离线验证码模型，见 `desktop/electron/ocr/`）；课程自动学习复用 `course_autoplay_trial.py` 引擎（Playwright/greenlet 与分平台 Node driver 随包收编，安装包离线安装后无需 pip / `playwright install`）。

## 下载与安装（内网机）

> 平台边界：只面向 Windows 10、银河麒麟 V10 SP1、统信 UOS 20 的近期稳定版本（amd64/arm64）。Electron 45 对 glibc 与桌面会话要求较高，更老的发行版请用源码 CLI 模式（见下）。

从公开仓 [j1toolkit](https://github.com/zhangzhenyu91/j1toolkit) 的 Release 下载对应架构安装包（免登录；Release 只发布 3 件安装包，不含绿色包）：

| 平台               | 安装包（推荐）                                                                   |
| ------------------ | -------------------------------------------------------------------------------- |
| Windows 10+ amd64  | `uvmp-toolkit-Setup-x.y.z.exe`（免管理员；自动建快捷方式与计划任务，带卸载程序） |
| 麒麟 V10 SP1 amd64 | `uvmp-toolkit_x.y.z_amd64.deb`                                                   |
| 麒麟 V10 SP1 arm64 | `uvmp-toolkit_x.y.z_arm64.deb`                                                   |

```bash
# 麒麟（deb：装到 /opt/uvmp-toolkit，自动配好 usb-agent 服务与桌面菜单项「Shade 壹匣 - 内网」）
sudo apt install ./uvmp-toolkit_*_arm64.deb      # 或 amd64
# Windows：双击 Setup.exe
```

安装后打开「内网工具箱」→ 设置页填 SSO 凭据。同步任务由服务端经 U 盘下发指令，客户端 `usb-agent` 常驻消费；无需在客户端配置定时。
树障/交跨/派车页顶部的「后台监听」行会显示 agent 是否在跑、最后检查时间与 U 盘挂载/失败原因（如 polkit 未授权、只读、残留挂载），排查“指令没执行”先看它。

> 绿色 `uvmp-toolkit-*-win-portable.zip` / `*.tar.gz` 仅由本机构建通道（`packaging/build_local.ps1`）产出，不上传 Release，也不进入 CNB 取件；CI 正式发版只上传上述 3 件安装包。
> 官方轨迹直出需要目标机 Chrome 或公司指定浏览器；PDF 包版打印优先使用自带 Electron，目标机无需额外浏览器。若 Electron 在老旧麒麟上起不来：回退源码 CLI 模式——克隆仓库后 `sudo bash install/linux/install.sh`（安装 U 盘单监听 agent，无 GUI）。

## 源码运行（开发）

```bash
# 桌面端（需 node 24+；本机 Python 不在 PATH 时用 UVMP_PYTHON 指定，建议 3.15.2）
cd desktop && npm install && UVMP_PYTHON=python3.15 npm run dev

# 核心 CLI / 无头模式（源码侧要求 Python 3.10+，开发与 CI 用 3.15.2）
python3.15 toolkit/app.py usb-agent                            # U 盘单监听（常驻；--once 手工检查）
python3.15 toolkit/app.py usb-agent --once                     # 手工检查/执行一轮
python3.15 toolkit/app.py daily-export                         # 本地手动导出（调试；正式由服务端指令触发）
python3.15 toolkit/app.py order-export --xlsx 清单.xlsx --outdir ./output
python3.15 toolkit/app.py run-script 脚本.py -- 参数...         # 脚本测试页同款运行入口
python3.15 toolkit/app.py selfcheck                            # 自检
python3.15 toolkit/app.py rpc                                  # stdio JSON-RPC（Electron 壳调用）

# 测试（零真实网络；离线机器先装 packaging/requirements-core.txt 或 offline_packages/）
python3.15 -m unittest discover -s tests -v
```

> 不再承诺 Python 3.7 源码兜底：测试与核心都按 3.10+ 语法编写；`build_on_device.sh` 在低于 3.10 的解释器上只产出旧版兼容核心且课程自动学习不可用。

## 文档

- 《内网工具箱开发指南.md》——架构、插件规范（新应用接入）、RPC、配置密钥、调度、平台兼容、CI 构建、验证规则
- 《部署说明.md》——麒麟/统信部署、日常使用、排障
- 《接口分析.md》——派车系统接口逆向分析
- 《内网执行手册.md》——内网机上的分步执行手册（给现场 AI 助手）

## 构建（GitHub Actions）

公开仓根 `.github/workflows/release.yml` 与后端同一条版本线：release-init（仅 tag 先建空 Release）→ `client-windows` / `client-linux-amd64` / `client-linux-arm64` 三 job 各自一 job 到底（各 job 以 `defaults.run.working-directory: uvmp-toolkit` 进入本目录）→ `release-final` 补全 GitHub Release 说明并回推 tag 到 CNB。

三平台统一用 `packaging/python-runtime.env` 定义的 CPython 3.15.2 资产（下载后校验 SHA256，失败即中止、不回退 latest），依赖全钉在 `packaging/requirements-core.txt`。三平台门禁分布：Windows 跑 Python unittest/selfcheck、PyInstaller、CDP 冒烟（缺宿主浏览器时明确报告跳过）、desktop node 测试、NSIS 打包与 GUI 冒烟；Linux amd64 在 buster 容器构建核心（含 selfcheck/RPC ping）、CDP 冒烟（缺宿主浏览器时明确报告跳过）、desktop node 测试、deb/tar.gz 打包、OCR 运行时 glibc 门禁与 xvfb GUI 冒烟；Linux arm64 为 QEMU 纯打包（核心 selfcheck/RPC ping、desktop node 测试、deb/tar.gz、OCR glibc 文件门禁），明确不执行 GUI/CDP，不冒充。

只有 tag ref 才建 Release 并直传 3 件安装包（Windows Setup exe + 麒麟 amd64/arm64 deb）；zip/tar.gz 只在本地产出，不上传 Release、不进入 CNB 取件。非 tag 的 `workflow_dispatch` 只构建/门禁，不发布；tag 上的手动 dispatch 属补跑，仍只传 3 件。tag 构建的版本号注入 `desktop/package.json`（唯一版本源）；CNB 侧「上传 Release 附件」流水线接力传 CNB Release；无 docker 产物镜像；历史 Go 轨已删除，不再构建。

## 仓库注意

**本客户端含内网系统对接细节，密钥一律不入仓：SSO 凭据只存在于各机器的 `toolkit/config.json`（600 权限）或 `~/.config/uvmp-toolkit/config.json`；仓库内 `config.ini` 不含真实密码。** 配置写回是原子操作（唯一临时名 + fsync + 原子替换），旧文件损坏先备份、读不出来则拒绝覆盖；`getConfig` 只回 `has_password`/`secret_set`，机巡实例密码同样脱敏，留空或 `******` 表示不修改。
