# Fyne 轨构建产线（灰度双轨，新增并行；Electron+Python 轨未退役、不动）

Go+Fyne 重构客户端的三平台构建/装配/门禁脚本集。**与既有 `packaging/build_*.sh|.ps1`
（Electron+Python 轨）并行共存**：本目录只做 Fyne 轨，旧链脚本与 `release.yml` 的
`client-*` job 不删不改（功能验收前保持双轨）。

## 一、公开契约（GUI 源码侧必须实现，冒烟门禁按此调用）

| 契约       | 约定                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| ---------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| GUI 入口   | Go 包 `UVMP_FYNE_PKG`（缺省 `./cmd/uvmp-toolkit`，即 `core-go/cmd/uvmp-toolkit`），所在模块 `UVMP_FYNE_MOD`（缺省 `core-go`）；产物二进制 `uvmp-toolkit`(.exe)（ASCII 命名，与 Electron 轨 executableName 同名）                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| GUI 冒烟   | `UVMP_FYNE_SMOKE=gui ./uvmp-toolkit`：完成首帧真实渲染后自行退出 0；失败非 0（GUI 源码侧按此契约实现）                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| OCR 冒烟   | `UVMP_FYNE_SMOKE=ocr ./uvmp-toolkit`：按安装相对布局加载 `models/{det,rec}.onnx` 与 `native/` 运行库，对内置空白图跑通推理后退出 0；失败非 0；**需 GUI 以 `-tags onnx` 构建**（`UVMP_FYNE_TAGS` Linux 缺省 onnx,x11（Windows 为 onnx），见下）                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| ONNX 标签  | ORT 实现 `internal/ocr/ort` 置于 `//go:build onnx` 之后（yalue/onnxruntime_go 为 CGO 包，启用步骤见 `core-go/internal/ocr/ort/doc.go`）；`build_fyne_core.*` Linux 缺省 `-tags onnx,x11`（Windows 为 `-tags onnx`） + CGO，`go vet/test` 仍默认标签                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| 运行时发现 | 浏览器运行时 `browser-runtime/`、模型 `models/`、ONNX 库 `native/` 均按可执行文件所在目录的**安装相对布局**发现（launcher 另注入 `LD_LIBRARY_PATH=native`）                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| CDP 浏览器 | 独立浏览器运行时 `browser-runtime/`（`app/{package.json,main.cjs}`，main.cjs 双模式）：导出同 `uvmp-export` 口径 `UVMP_BROWSER_BIN=browser-runtime/electron(.exe)` + `UVMP_BROWSER_APP=browser-runtime/app` + `UVMP_BROWSER_WORKER=--uvmp-browser-mode=export`；课程模式由 `internal/browser` 以 `--uvmp-browser-mode=course` 驱动同一 app；`uvmp-export selftest` 即 CDP 冒烟（data: 页零网络）                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| 导出进程   | `uvmp-export` 与旧链同产物同协议（复用 `packaging/build_go_core.sh \| .ps1`，CGO_ENABLED=0）                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| 核心 CLI   | `uvmp-core`（`core-go/cmd/uvmp-core`，CGO_ENABLED=0）：selfcheck / rpc / daily-export 等命令行与 RPC 能力，随包装入供命令行/排障（GUI 已由 `uvmp-toolkit` 承担）                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| OS 调度    | 安装包接管系统定时（功能 parity，非 GUI 自管）：Windows NSIS 注册/卸载 5 个计划任务 `UVMP-Toolkit-{DailyExport,TreeSyncTick,TreeSyncNightly,CrossingSyncTick,CrossingSyncNightly}`，`run-uvmp.vbs` 隐藏窗口调 `uvmp-core.exe` 同名参数（`daily-export --if-due` / `tree-sync --tick` / `tree-sync --nightly --if-due` / `crossing-sync --tick` / `crossing-sync --nightly --if-due`）；deb postinst 播种 `config.initial.json` 并装 `uvmp-toolkit.service/.timer`（`@RUN_CMD@`→`/opt/uvmp-toolkit-fyne/uvmp-core`，5 条 ExecStart 命令链同口径）。**与旧版（Electron 轨/源码模式）任务名/单元名一致 = 升级替换而非双跑**：安装时检测旧定义并原位替换（后装者接管，Windows 侧经 `register-tasks.cmd`，旧任务若由管理员权限创建则中完整性写被拒 → UAC 提权重试一次、终验不过明确中止），卸载只删除、不恢复旧定义；配置 `~/.config/uvmp-toolkit/config.json` 与状态镜像 `~/.local/share/uvmp-toolkit` 与旧版共用（回退旧版可用），postrm/卸载保留配置 |
| 版本来源   | 装配版本读 `uvmp-toolkit/version.json`（与 `desktop/package.json` 解耦）；`UVMP_FYNE_VERSION` 环境变量优先（CI tag 构建传 tag 版本）                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |

## 二、资产清单（固定版本 + SHA256，均实测记录、禁止编造）

- `assets/electron33-runtime.json`：Electron **33.4.11**（Chromium 130，与 Electron 轨
  `desktop/package-lock.json` 钉版一致）三平台官方发行 zip 的 URL + SHA256
  （取自官方 `SHASUMS256.txt`，2026-10-28 实取）+ 装配布局。
- `assets/onnx-assets.json`：onnxruntime-node@1.20.1 预编译 CPU EP 库（win32-x64 /
  linux-x64 / linux-arm64）与 PP-OCRv4 模型（det/rec/cls + ppocr_keys.txt）的 SHA256
  （2026-10-28 对本地真实文件实测）+ 装配布局。

装配脚本对每个文件**逐个校验 SHA256**，不匹配即失败。升级资产 = 改清单版本与 sha256 +
同步官方校验来源记录。

## 三、脚本一览（工作目录一律 uvmp-toolkit/）

| 脚本                                       | 平台                                 | 职责                                                                                                                                                                                        |
| ------------------------------------------ | ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `build_fyne_core.sh`                       | linux-amd64/arm64（buster 容器原生） | Fyne CGO 构建 uvmp-toolkit + uvmp-core + uvmp-export（`-tags onnx` 缺省；CGO 交叉禁用，arm64 走原生构建机）                                                                                 |
| `build_fyne_core.ps1`                      | windows-amd64（MSYS2 mingw-w64）     | 同上（`CC=gcc`；CI 用 `msys2/setup-msys2` MINGW64 + path-type inherit）                                                                                                                     |
| `fetch_browser_runtime.sh                  | .ps1`                                | 三平台                                                                                                                                                                                      | 按清单下载/校验/装配 Electron 33 运行时 + 独立浏览器运行时应用（`app/`，拷 `browser-runtime/{package.json,main.cjs}`，main=main.cjs 双模式） |
| `assemble_onnx.sh                          | .ps1`                                | 三平台                                                                                                                                                                                      | 按清单校验/装配 ONNX 库（`native/`）与模型（`models/`）                                                                                      |
| `assemble_deb.sh`                          | linux-amd64/arm64                    | dpkg-deb 出 deb + tar.gz 绿色包（`/opt/uvmp-toolkit-fyne` + launcher + 桌面项 + `config.initial.json` + systemd 模板；postinst 播种配置/装 timer、postrm 保留配置，出包后校验包内脚本齐全） |
| `assemble_nsis.ps1` + `fyne-installer.nsi` | windows-amd64                        | NSIS 出 Setup exe（per-user、`/S`、`/D=<dir>`）+ win-portable zip；注册/卸载 5 个计划任务（`run-uvmp.vbs` 隐藏窗口）；nsi 为 UTF-8 BOM Unicode                                              |
| `run-uvmp.vbs`                             | windows-amd64                        | 计划任务隐藏窗口调用器：参数即 uvmp-core 子命令/开关，日志按子命令分文件（随 NSIS payload 入包）                                                                                            |
| `register-tasks.cmd`                       | windows-amd64                        | 5 任务注册/清理（install/remove 模式，同名 /f 升级替换；全 ASCII 防 cmd 多字节读块边界吞字节；被拒时安装器 UAC 提权重试，见 fyne-installer.nsi）                                            |
| `gate_smoke.sh                             | .ps1`                                | 三平台                                                                                                                                                                                      | glibc/ldd（linux）/ DLL 依赖（windows）+ GUI/CDP/OCR 冒烟门禁                                                                                |
| `install_smoke.sh                          | .ps1`                                | 三平台                                                                                                                                                                                      | 安装冒烟：实装 → 布局/权限 → OS 调度接管（任务/单元同名替换）→ launcher 实跑 → 卸载清理（配置保留）                                          |

本地通道（离线/内网）：`UVMP_BROWSER_ARCHIVE`（本地 Electron zip）、`UVMP_BROWSER_DIR`
（已解包目录）、`UVMP_ORT_DIR` / `UVMP_ORT_ARCHIVE`（onnxruntime 库来源）。

## 四、产物命名（`-fyne-` 中缀避免覆盖旧链产物；保留 `.exe`/`.deb` CNB 取件后缀）

```
uvmp-toolkit-fyne-Setup-<version>.exe        # Windows NSIS 安装程序
uvmp-toolkit-fyne-<version>-win-portable.zip # Windows 绿色包
uvmp-toolkit-fyne-<version>_<arch>.deb       # 麒麟 deb（amd64/arm64）
uvmp-toolkit-fyne-<version>-<arch>.tar.gz    # 麒麟绿色包
```

装配布局（`dist-fyne/<platform>/`，发版产物由此打出）：

```
uvmp-toolkit(.exe) / uvmp-core(.exe) / uvmp-export(.exe)
browser-runtime/   # Electron 33 运行时 + app/{package.json,main.cjs}（export/course 双模式）
models/            # det.onnx / rec.onnx / cls.onnx / ppocr_keys.txt
native/            # onnxruntime.dll 或 libonnxruntime.so.1(.20.1)
```

安装包附加脚本（装配时并入，install_smoke 校验齐全）：

```
NSIS payload:  run-uvmp.vbs                      # 5 个计划任务的隐藏窗口调用器
deb (/opt/uvmp-toolkit-fyne/):  config.initial.json
                               install/linux/uvmp-toolkit.{service,timer}   # systemd 模板（postinst 填占位）
```

## 五、CI（仓根 `.github/workflows/release.yml` 灰度通道）

- job：`fyne-windows` / `fyne-linux-amd64` / `fyne-linux-arm64`（linux 走 debian:buster
  **原生**容器，arm64 用 ubuntu-24.04-arm 原生构建机，不冒充交叉测试）。
- 版本：tag 构建传 `UVMP_FYNE_VERSION`（tag 版本）；`workflow_dispatch` 等非 tag 构建不传，
  装配脚本回退 `uvmp-toolkit/version.json`（与 `desktop/package.json` 解耦）。
- 触发：`workflow_dispatch` 填 `fyne_track=build`（只构建+门禁）或 `publish`（另传
  GitHub Release 附件）。**tag 常规发版不跑 fyne job、Release 仍是正式 3 件套**
  （CNB 接力取件按 exe/deb 过滤；`.cnb.yml` 断言已双轨兼容：正式件恒 3 件，
  `-fyne-` 件 0 或 3 件，off 默认仍 3 件）。
- 灰度转正联动：`publish` 的 `-fyne-` 附件会进 exe/deb 取件口径并上传 CNB Release
  （win-portable zip / tar.gz 不带 exe/deb 后缀，CNB 取件不过滤——已知差异）；
  正式轨附件名不含 `-fyne-`，两轨数量分别断言、互不挤占。
- CNB 附件补传接力（`fyne-cnb-relay`，2026-10-29）：`publish` 且 ref 为 tag 时，三平台附件
  传 GitHub Release 后由该 job 经 CNB CLI 触发 `.cnb.yml` 的 `api_trigger` 流水线
  （`npx --yes @cnbcool/cnb-cli build start-build --repo j1net/j1toolkit/j1toolkit --tag <tag>
--event api_trigger --env RELAY_TAG=<tag> --sync true`），CNB 侧共用脚本
  `docker/cnb-relay-release-assets.sh` 从同一 GitHub Release 取件、断言**正式 3 件 + Fyne 3 件**
  后补传 CNB Release（overwrite 幂等；不重建镜像、不动 tag）。正式 tag 接力（tag_push 通道，
  断言为正式 3 件 + Fyne 0 或 3 件）随 `release-final` 推 tag 只跑一次，publish 补传不会重走
  （`release-final` 见 tag 已存在即跳过——已踩坑，故需此 job 补触发）。GitHub 侧 `CNB_TOKEN`
  需含流水线触发权限（`repo-cnb-trigger:rw`）与仓库 git 写；CNB 侧取件/上传用流水线内置
  `CNB_TOKEN`。接力 tag 须含新 `.cnb.yml` 与共用脚本（1.24.0 起）。
- 门禁顺序：构建 → 运行时/ONNX 装配（SHA256）→ deb/NSIS 装配（含包内脚本齐全性断言）→
  gate_smoke（glibc/ldd + GUI/CDP/OCR）→ install_smoke（安装冒烟，含 OS 调度接管/同名替换/
  卸载清理/配置保留校验）。任一失败即 job 失败。

## 六、纪律

- 简体中文注释；交付完整代码无占位；清单 SHA256 与 URL 只写实测值，禁止编造。
- 二进制不入仓：`packaging/.fyne-cache/`（下载缓存）、`dist-fyne/`（装配产物）均已忽略。
- 本机无 gcc 时只做脚本/YAML 语法检查，不触发 workflow；CI 构建/冒烟待云端验证。
