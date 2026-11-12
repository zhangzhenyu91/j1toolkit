# 离线验证码 OCR 模型

本目录用于缺陷识别自动登录的验证码识别，运行在 Electron 的 `onnxruntime-node`，
不访问互联网、不调用任何云端接口。

| 文件               | 来源                                                                                                                                | 用途                                          |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------- |
| `common.onnx`      | [ddddocr](https://github.com/sml2h3/ddddocr) 通用模型，经 [Mieru-OCR](https://github.com/MakotoArai-CN/Mieru-OCR) v1.2.4 发行包分发 | 静态字符验证码 OCR                            |
| `charsets.json`    | 同上                                                                                                                                | CTC 解码字符集                                |
| `captcha_ocr.cjs`  | 本项目                                                                                                                              | 推理入口（stdin/stdout JSON）                 |
| `runtime-deps.cjs` | 本项目                                                                                                                              | onnxruntime 原生依赖清单（构建期/运行期共用） |

校验值：

| 文件            | SHA256                                                             |
| --------------- | ------------------------------------------------------------------ |
| `common.onnx`   | `ceef522eb15a9353719f44a20b56e82e38961479da256f9a136183b9ece41a2d` |
| `charsets.json` | `b65239a57dff3e78f832d176c2389337bcea56e435cc6ba5b0a77b76845dc47c` |

- 许可证：MIT（沿用 ddddocr / Mieru-OCR）
- 推理：`captcha_ocr.cjs`，输入灰度像素，按 ddddocr common 模型预处理（高度 64、双线性缩放、/255）
- 该模型只覆盖通用静态字符验证码；滑块、点选、行为验证码不在支持范围。

## Windows：随包的 MSVC 运行库

`onnxruntime-node` 的 Windows 预编译库都动态链接 MSVC v14 运行库（PE 导入表实读）：

| 二进制                     | 依赖                                                                                       |
| -------------------------- | ------------------------------------------------------------------------------------------ |
| `onnxruntime_binding.node` | `MSVCP140.dll`、`VCRUNTIME140.dll`、`VCRUNTIME140_1.dll`                                   |
| `onnxruntime.dll`          | 上面三个 + `MSVCP140_1.dll`、`MSVCP140_ATOMIC_WAIT.dll`（另含 `api-ms-win-*` 系统 API 集） |

目标内网机若未安装 VC++ 运行库，`require` 原生模块即报
`The specified module could not be found`（v2.3.47 现场故障）。因此 Windows 打包时由
`desktop/build/prune-ort.cjs` → `desktop/build/vc-runtime.cjs` 把官方运行库原样复制到
`resources/app.asar.unpacked/node_modules/onnxruntime-node/bin/<napi>/win32/<arch>/`，
与 `onnxruntime_binding.node` 同目录（Windows 原生模块优先从模块所在目录解析依赖），
用户端无需安装器、不弹命令窗口。

- 来源：微软官方「最新受支持的 Visual C++ 可再发行组件」
  <https://learn.microsoft.com/cpp/windows/latest-supported-vc-redist>
  （`https://aka.ms/vs/17/release/vc_redist.x64.exe`，构建机静默安装后从 `System32` 复制）
- 许可：Microsoft Visual Studio 许可条款「可分发代码（Distributable Code）」，
  再分发说明见 <https://learn.microsoft.com/cpp/windows/redistributing-visual-cpp-files>；
  以「应用本地部署」方式随程序分发，不写系统目录、不改注册表
- 每个产物目录内附 `MSVC-RUNTIME-NOTICE.txt`，记录文件清单、来源与许可链接
- 构建辅助：`node desktop/build/vc-runtime.cjs print-redist` 输出地址与 SHA256（CI 单一来源），
  `verify --app-out-dir <解包目录>` 校验打包产物里运行库是否齐全且位数正确
- 冒烟：`node desktop/test/ocr-smoke.cjs --app-out-dir <解包目录>` 用打包 Electron
  （`ELECTRON_RUN_AS_NODE=1`）真跑 `captcha_ocr.cjs`，覆盖 `require` 原生模块、
  `InferenceSession.create`、`session.run`

## 失败诊断

加载失败时 `captcha_ocr.cjs` 输出 `{"error":"…"}` 并置退出码 1，内容包含：候选模块路径
（`UVMP_OCR_ORT_PATH` / `UVMP_NODE_MODULES` / 默认解析）、平台与架构、Electron/Node 版本、
`bin/` 实际平台布局、原生目录内逐个 DLL 的有无、错误码与底层错误文本。
诊断不含验证码像素与识别结果，可安全贴进工单与 CI 日志。
