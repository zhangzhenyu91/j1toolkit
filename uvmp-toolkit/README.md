# 内网工具箱（uvmp-toolkit）

> 本客户端现居公开仓 [j1toolkit](https://github.com/zhangzhenyu91/j1toolkit) 根目录 `uvmp-toolkit/`（2026-10 自私有仓迁回：安装包本就公开发布、产物可解包，源码私密性有限）。

内网被控机（银河麒麟 V10 SP1 amd64/arm64 优先，兼容 Windows 10+）上的桌面客户端与自动化工具集，面向派车系统（UVMP）的导出类业务。

## 功能

| 应用 | 说明 |
|---|---|
| 每日派车单同步 | 每日定时导出当日派车单（全部状态）为 `yyyy-mm-dd-派车单.xlsx`，写入 KVM 虚拟 U 盘（卷标 GLKVM），供壹匣服务端取走同步 |
| 派车单·轨迹导出 | 按派车单号清单（xlsx 文件或粘贴文本）按序导出派车单 PDF、行驶轨迹 PDF 与轨迹点 CSV，支持断点续传 |
| 题库搜题 | 屏幕双扫描框实时 OCR 搜题：Excel 题库导入本机落盘（多题库启停管理），框选区域对准题目即出答案悬浮窗；全离线本地推理，不经网络 |

技术要点：Electron + Vue 3 + TDesign 桌面客户端（「政企蓝白」主题，壹匣同族）；业务核心为 Python sidecar（SSO 国密登录 + 请求签名逆向，见《接口分析.md》），壳与核心经 stdio JSON-RPC 通信；PDF 由目标机既有 Chrome 无头渲染；定时采用 due-check 模式（系统定时器高频唤醒 + 配置时刻判定，客户端改时间零权限）；题库搜题的截屏/OCR/悬浮窗全部在壳层（onnxruntime-node + PP-OCRv4 mobile 模型随包，CPU 推理无厂商绑定，win-x64/麒麟 amd64/arm64 同构）。

## 下载与安装（内网机）

从公开仓 [j1toolkit](https://github.com/zhangzhenyu91/j1toolkit) 的 Release 下载对应架构（免登录）：

| 平台 | 安装包（推荐） | 绿色包 |
|---|---|---|
| Windows 10+ amd64 | `uvmp-toolkit-Setup-x.y.z.exe`（免管理员；自动建快捷方式与计划任务，带卸载程序） | `uvmp-toolkit-x.y.z-win-portable.zip` |
| 麒麟 V10 SP1 amd64 | `uvmp-toolkit_x.y.z_amd64.deb` | `uvmp-toolkit-x.y.z-amd64.tar.gz` |
| 麒麟 V10 SP1 arm64 | `uvmp-toolkit_x.y.z_arm64.deb` | `uvmp-toolkit-x.y.z-arm64.tar.gz` |

```bash
# 麒麟（deb：装到 /opt/uvmp-toolkit，自动配好定时器与桌面菜单项「Shade 壹匣 - 内网」）
sudo apt install ./uvmp-toolkit_*_arm64.deb      # 或 amd64
# Windows：双击 Setup.exe
```

安装后打开「内网工具箱」→ 设置页填 SSO 凭据 → 「每日派车单同步」页启用定时（装好后定时默认停用，等凭据就绪再启用，避免空跑报错）。

> 客户端仍依赖目标机自带的 Chrome/Edge（PDF 渲染用，麒麟机抓包用的 Chrome 即可）。
> 若 Electron 在老旧麒麟上起不来：回退源码 CLI 模式——克隆仓库后 `sudo bash install/linux/install.sh`（仅每日定时导出，无 GUI）。

## 源码运行（开发）

```bash
# 桌面端（需 node 24+；本机 Python 不在 PATH 时用 UVMP_PYTHON 指定）
cd desktop && npm install && UVMP_PYTHON=python3 npm run dev

# 核心 CLI / 无头模式
python3 toolkit/app.py daily-export --if-due                # 定时导出（供系统定时器）
python3 toolkit/app.py order-export --xlsx 清单.xlsx --outdir ./output
python3 toolkit/app.py selfcheck                            # 自检
python3 toolkit/app.py rpc                                  # stdio JSON-RPC（Electron 壳调用）
```

## 文档

- 《内网工具箱开发指南.md》——架构、插件规范（新应用接入）、RPC、配置密钥、调度、平台兼容、CI 构建、验证规则
- 《部署说明.md》——麒麟部署、日常使用、排障
- 《接口分析.md》——派车系统接口逆向分析
- 《内网执行手册.md》——内网机上的分步执行手册（给现场 AI 助手）

## 构建（GitHub Actions）

私有仓根 `.github/workflows/build.yml`：release-init（仅 tag 先建空 Release）→ windows / linux-amd64 / linux-arm64 三 job 各自一 job 到底（PyInstaller 核心 + electron-builder 打包 + 冒烟；各 job 以 `defaults.run.working-directory: uvmp-toolkit` 进入本目录）。push tag `v*` 或手动触发；tag 触发时自动建 Release 挂载产物，并由 publish-cnb job 回传产物镜像至 CNB 制品库。

## 仓库注意

**本客户端含内网系统对接细节，密钥一律不入仓：SSO 凭据只存在于各机器的 `toolkit/config.json`（600 权限）或 `~/.config/uvmp-toolkit/config.json`；仓库内 `config.ini` 不含真实密码。**
