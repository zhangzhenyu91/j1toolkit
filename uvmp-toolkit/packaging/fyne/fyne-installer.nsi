; Fyne 轨 NSIS 安装脚本（Unicode NSIS 3；UTF-8 带 BOM——ANSI 时代中文乱码/翻车教训，勿去 BOM）
; 由 packaging/fyne/assemble_nsis.ps1 调 makensis 传参装配：
;   makensis /DSTAGINGDIR=<装配布局> /DOUTFILE=<uvmp-toolkit-fyne-Setup-<ver>.exe> /DVERSION=<ver>
;            [/DICONFILE=<icon.ico>] fyne-installer.nsi
;
; 约定：
;   - per-user 安装免管理员（RequestExecutionLevel user），默认 %LOCALAPPDATA%\uvmp-toolkit-fyne
;   - 支持静默：/S 静默安装；/D=<dir> 指定安装目录（必须是命令行最后一个参数）——安装冒烟用
;   - 与 Electron 轨安装包并行不冲突：独立安装目录/卸载项/快捷方式名，产物名带 -fyne- 中缀
;   - OS 计划任务接管（功能 parity，与旧版同名升级替换而非双跑）：注册/卸载全部 5 个
;     UVMP-Toolkit-* 计划任务（Daily/TreeTick/TreeNightly/CrossingTick/CrossingNightly），
;     每分钟触发 run-uvmp.vbs 隐藏窗口调 uvmp-core.exe 同名参数；同名旧任务（Electron 轨/
;     源码模式/旧版本注册）检测后直接替换（后装者接管），卸载只删除、不恢复旧定义。
;     旧任务若由管理员权限创建（高完整性标签），中完整性替换/删除会被 Windows 拒绝——
;     失败即 UAC 提权重试一次，终验不过明确中止（注册/删除经 register-tasks.cmd，全 ASCII）
Unicode true
SetCompressor /SOLID lzma
ManifestDPIAware true

!ifndef STAGINGDIR
  !error "STAGINGDIR 未定义（由 assemble_nsis.ps1 传入）"
!endif
!ifndef OUTFILE
  !error "OUTFILE 未定义（由 assemble_nsis.ps1 传入）"
!endif
!ifndef VERSION
  !define VERSION "0.0.0-dev"
!endif

!define PRODUCTID "uvmp-toolkit-fyne"
!define DISPLAYNAME "Shade 壹匣 - 内网（Fyne 灰度）"
!define PUBLISHER "检修一班"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\UvmpToolkitFyne"

Name "${DISPLAYNAME} ${VERSION}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\${PRODUCTID}"
InstallDirRegKey HKCU "Software\${PRODUCTID}" "InstallDir"
RequestExecutionLevel user
ShowInstDetails show
ShowUnInstDetails show

!ifdef ICONFILE
  Icon "${ICONFILE}"
  UninstallIcon "${ICONFILE}"
!endif

Page directory
Page instfiles
UninstPage uninstConfirm
UninstPage instfiles

!include LogicLib.nsh

; ---- OS 计划任务（与旧版/源码模式/Electron 轨同名——升级替换语义：后装者接管，避免双跑）----
; 每分钟触发 run-uvmp.vbs 隐藏窗口调 uvmp-core.exe（due-check 门闸在核心，参数与旧版同名同义）。
; 注册/删除经 register-tasks.cmd（全 ASCII，防 cmd 多字节读块边界吞字节）；失败（多为旧任务
; 由管理员权限创建、高完整性拒绝中完整性写）→ UAC 提权重试一次，终验不过即中止/警告。
!macro DetectSchedTask TASKNAME
  ; 检测同名旧任务并明确提示（register-tasks.cmd 以 /f 原位替换定义，不产生第二条任务）
  ExecWait 'cmd /c schtasks /query /tn "${TASKNAME}" >nul 2>&1' $R0
  ${If} $R0 = 0
    DetailPrint "检测到旧计划任务 ${TASKNAME}（旧安装注册）→ 同名替换为本安装（升级接管，避免双跑）"
  ${Else}
    DetailPrint "注册计划任务 ${TASKNAME}（每分钟触发，隐藏窗口）"
  ${EndIf}
!macroend

!macro VerifySchedTask TASKNAME
  ; 终验：任务必须指向本安装 run-uvmp.vbs（缺失/未接管即中止，绝不放任双跑或漏任务）
  ExecWait 'cmd /c schtasks /query /tn "${TASKNAME}" /xml | find "run-uvmp.vbs" >nul' $R0
  ${If} $R0 <> 0
    Abort "计划任务 ${TASKNAME} 未注册到本安装（若旧任务由管理员权限创建，请以管理员身份运行安装程序）"
  ${EndIf}
!macroend

!macro VerifySchedTaskGone TASKNAME
  ; 卸载终验：任务应已删除（不恢复旧任务定义）；删除被拒不阻断卸载，仅明确警告
  ExecWait 'cmd /c schtasks /query /tn "${TASKNAME}" >nul 2>&1' $R0
  ${If} $R0 = 0
    DetailPrint "警告: 计划任务 ${TASKNAME} 未能删除（可手动 schtasks /delete /tn $\"${TASKNAME}$\" /f）"
  ${Else}
    DetailPrint "已删除计划任务 ${TASKNAME}（不恢复旧任务定义）"
  ${EndIf}
!macroend

!macro RunSchedTasks MODE
  ; MODE=install|remove：register-tasks.cmd 执行 5 任务注册/删除；失败 → UAC 提权重试一次
  ExecWait 'cmd /c "$INSTDIR\register-tasks.cmd" ${MODE}' $R0
  ${If} $R0 <> 0
    DetailPrint "计划任务 ${MODE} 被拒（旧任务可能由管理员权限创建）→ UAC 提权重试一次"
    ExecWait 'powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath \"$INSTDIR\register-tasks.cmd\" -ArgumentList ${MODE} -Verb RunAs -Wait"' $R0
    DetailPrint "提权重试结束（结果以下方终验为准）"
  ${EndIf}
!macroend

Section "安装" SEC_MAIN
  SetOutPath "$INSTDIR"
  SetOverwrite on
  ; 装配布局全量入包：uvmp-toolkit.exe / uvmp-core.exe / uvmp-export.exe / browser-runtime\ / models\ / native\
  File /r "${STAGINGDIR}\*"

  ; 启动器（注入 ONNX 运行时库搜索路径后启动 GUI；与 deb 的 launcher 同口径）
  FileOpen $0 "$INSTDIR\${PRODUCTID}.cmd" w
  FileWrite $0 "@echo off$\r$\n"
  FileWrite $0 "setlocal$\r$\n"
  FileWrite $0 "set \"PATH=%~dp0native;%PATH%\"$\r$\n"
  FileWrite $0 "\"%~dp0uvmp-toolkit.exe\" %*$\r$\n"
  FileClose $0

  ; 快捷方式（开始菜单 + 桌面，per-user）
  CreateShortCut "$SMPROGRAMS\${DISPLAYNAME}.lnk" "$INSTDIR\${PRODUCTID}.cmd"
  CreateShortCut "$DESKTOP\${DISPLAYNAME}.lnk" "$INSTDIR\${PRODUCTID}.cmd"

  ; OS 计划任务接管（5 个，与旧版同名升级替换；run-uvmp.vbs 隐藏窗口调 uvmp-core.exe 同名参数）
  !insertmacro DetectSchedTask "UVMP-Toolkit-DailyExport"
  !insertmacro DetectSchedTask "UVMP-Toolkit-TreeSyncTick"
  !insertmacro DetectSchedTask "UVMP-Toolkit-TreeSyncNightly"
  !insertmacro DetectSchedTask "UVMP-Toolkit-CrossingSyncTick"
  !insertmacro DetectSchedTask "UVMP-Toolkit-CrossingSyncNightly"
  !insertmacro RunSchedTasks install
  ; 终验：5 任务全部指向本安装（缺一即中止，绝不放任双跑/漏任务）
  !insertmacro VerifySchedTask "UVMP-Toolkit-DailyExport"
  !insertmacro VerifySchedTask "UVMP-Toolkit-TreeSyncTick"
  !insertmacro VerifySchedTask "UVMP-Toolkit-TreeSyncNightly"
  !insertmacro VerifySchedTask "UVMP-Toolkit-CrossingSyncTick"
  !insertmacro VerifySchedTask "UVMP-Toolkit-CrossingSyncNightly"

  ; 卸载项
  WriteUninstaller "$INSTDIR\Uninstall-${PRODUCTID}.exe"
  WriteRegStr HKCU "Software\${PRODUCTID}" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "${DISPLAYNAME}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "${PUBLISHER}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\uvmp-toolkit.exe"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" "$INSTDIR\Uninstall-${PRODUCTID}.exe"
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  ; OS 计划任务清理（5 个全删；不恢复旧安装的任务定义——如需 Electron 轨调度请重装该轨）
  !insertmacro RunSchedTasks remove
  !insertmacro VerifySchedTaskGone "UVMP-Toolkit-DailyExport"
  !insertmacro VerifySchedTaskGone "UVMP-Toolkit-TreeSyncTick"
  !insertmacro VerifySchedTaskGone "UVMP-Toolkit-TreeSyncNightly"
  !insertmacro VerifySchedTaskGone "UVMP-Toolkit-CrossingSyncTick"
  !insertmacro VerifySchedTaskGone "UVMP-Toolkit-CrossingSyncNightly"
  Delete "$SMPROGRAMS\${DISPLAYNAME}.lnk"
  Delete "$DESKTOP\${DISPLAYNAME}.lnk"
  ; 整目录删除（安装目录专用于本产品；/D= 自定义目录时只删本产品文件树）
  RMDir /r "$INSTDIR"
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
  DeleteRegKey HKCU "Software\${PRODUCTID}"
SectionEnd
