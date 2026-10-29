; Fyne 轨 NSIS 安装脚本（Unicode NSIS 3；UTF-8 带 BOM——ANSI 时代中文乱码/翻车教训，勿去 BOM）
; 由 packaging/fyne/assemble_nsis.ps1 调 makensis 传参装配：
;   makensis /DSTAGINGDIR=<装配布局> /DOUTFILE=<uvmp-toolkit-fyne-Setup-<ver>.exe>
;            /DVERSION=<ver> /DICONFILE=<icon.ico> [/DVI4=<a.b.c.d>] fyne-installer.nsi
;
; 约定：
;   - per-user 安装免管理员（RequestExecutionLevel user），默认 %LOCALAPPDATA%\uvmp-toolkit-fyne
;   - 支持静默：/S 静默安装；/D=<dir> 指定安装目录（必须是命令行最后一个参数）——安装冒烟用
;   - 快捷方式与自启动一律经 wscript 隐藏启动器 run-uvmp-gui.vbs：uvmp-toolkit.exe 是控制台
;     子系统二进制（build_fyne_core.ps1 未设 UVMP_FYNE_WINDOWED），直启会闪黑窗；启动器同时
;     把安装目录 native\ 注入 PATH（ONNX Runtime DLL），控制台启动器 uvmp-toolkit-fyne.cmd 保留排障用
;   - 所有外部命令（schtasks 检测/注册/终验、register-tasks.cmd、UAC 提权）经 nsExec 无窗调用，
;     安装/卸载全程不弹控制台窗口；提权只有 UAC 确认框可见（系统安全边界，不可绕过）
;   - 图标：ICONFILE 必填。assemble_nsis.ps1 由真正 Shade 图标（packaging/icon.png 重编码）生成
;     多尺寸 icon.ico，生成/校验失败即中止装配，绝不静默回退 NSIS 默认图标；安装目录落 icon.ico，
;     快捷方式与卸载项（DisplayIcon）都引用它
;   - OS 计划任务接管（功能 parity，与旧版同名升级替换而非双跑）：注册/卸载全部 5 个
;     UVMP-Toolkit-* 计划任务（Daily/TreeTick/TreeNightly/CrossingTick/CrossingNightly），
;     每分钟触发 run-uvmp.vbs 隐藏窗口调 uvmp-core.exe 同名参数；同名旧任务（Electron 轨/
;     源码模式/旧版本注册）检测后直接替换（后装者接管），卸载只删除、不恢复旧定义。
;     旧任务若由管理员权限创建（高完整性标签），中完整性替换/删除会被 Windows 拒绝——
;     失败即 UAC 提权重试一次（elevate-tasks.ps1，隐藏控制台），终验不过明确中止
Unicode true
SetCompressor /SOLID lzma
ManifestDPIAware true
; per-user：SetShellVarContext current 在下方 .onInit 中执行（安装器与卸载器共用）。

!ifndef STAGINGDIR
  !error "STAGINGDIR 未定义（由 assemble_nsis.ps1 传入）"
!endif
!ifndef OUTFILE
  !error "OUTFILE 未定义（由 assemble_nsis.ps1 传入）"
!endif
!ifndef ICONFILE
  !error "ICONFILE 未定义（由 assemble_nsis.ps1 传入真正 Shade 图标生成的多尺寸 icon.ico；不得回退 NSIS 默认图标）"
!endif
!ifndef VERSION
  !define VERSION "0.0.0-dev"
!endif

!define PRODUCTID "uvmp-toolkit-fyne"
!define UNINSTALLER "Uninstall-${PRODUCTID}.exe"
!define DISPLAYNAME "Shade 壹匣 - 内网（Fyne 灰度）"
!define PUBLISHER "检修一班"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\UvmpToolkitFyne"
!define RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
; 自启值名与 core-go/internal/platform/autostart.go runValueName 一致（应用侧自己写，卸载时清理）
!define RUN_VALUE "uvmp-toolkit"

Name "${DISPLAYNAME} ${VERSION}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\${PRODUCTID}"
InstallDirRegKey HKCU "Software\${PRODUCTID}" "InstallDir"
RequestExecutionLevel user
ShowInstDetails show
ShowUnInstDetails show

; 安装程序本体 + 卸载器图标（同一份多尺寸 ico；ICONFILE 缺失在上面已 !error）
Icon "${ICONFILE}"
UninstallIcon "${ICONFILE}"

!ifdef VI4
  VIProductVersion "${VI4}"
  VIAddVersionKey /LANG=2052 "ProductName" "${DISPLAYNAME}"
  VIAddVersionKey /LANG=2052 "CompanyName" "${PUBLISHER}"
  VIAddVersionKey /LANG=2052 "FileDescription" "${DISPLAYNAME} 安装程序"
  VIAddVersionKey /LANG=2052 "FileVersion" "${VERSION}"
  VIAddVersionKey /LANG=2052 "ProductVersion" "${VERSION}"
  VIAddVersionKey /LANG=2052 "LegalCopyright" "${PUBLISHER}"
!endif

Page directory
Page instfiles
UninstPage uninstConfirm
UninstPage instfiles

!include LogicLib.nsh

Function .onInit
  ; NSIS 运行期命令只能位于 Function/Section，不能放在脚本全局。
  ; 安装器与卸载器复用同一脚本，均需保持当前用户的快捷方式上下文。
  SetShellVarContext current
FunctionEnd

; ---- 无窗外部调用约定 ----
; nsExec 不经控制台宿主，schtasks/cmd/powershell 都不弹黑窗；ExecToStack 先弹退出码再弹输出。
; 任务名/搜索串均为 ASCII 且无空格，故命令行不需要嵌引号（cmd 外层引号规则易翻车，这里刻意避开）。

; ---- OS 计划任务（与旧版/源码模式/Electron 轨同名——升级替换语义：后装者接管，避免双跑）----
; 每分钟触发 run-uvmp.vbs 隐藏窗口调 uvmp-core.exe（due-check 门闸在核心，参数与旧版同名同义）。
!macro DetectSchedTask TASKNAME
  ; 检测同名旧任务并明确提示（register-tasks.cmd 以 /f 原位替换定义，不产生第二条任务）
  nsExec::ExecToStack 'schtasks /query /tn ${TASKNAME}'
  Pop $R0
  Pop $R1
  ${If} $R0 = 0
    DetailPrint "检测到旧计划任务 ${TASKNAME}（旧安装注册）→ 同名替换为本安装（升级接管，避免双跑）"
  ${Else}
    DetailPrint "注册计划任务 ${TASKNAME}（每分钟触发，隐藏窗口）"
  ${EndIf}
!macroend

!macro VerifySchedTask TASKNAME
  ; 终验：任务必须指向本安装 run-uvmp.vbs（缺失/未接管即中止，绝不放任双跑或漏任务）。
  ; schtasks /xml 输出 UTF-16LE，独立 PowerShell 脚本读取并匹配，避免多层命令行转义。
  ; 注册后立即查询可能读到尚未提交的旧定义，故最多重试 30×500ms（15 秒）；重试耗尽仍失败才中止。
  StrCpy $R4 0
  ${Do}
    IntOp $R4 $R4 + 1
    nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$INSTDIR\verify-task.ps1" -Name ${TASKNAME} -Launcher "$INSTDIR\run-uvmp.vbs"'
    Pop $R0
    Pop $R1
    ${If} $R0 = 0
      ${ExitDo}
    ${EndIf}
    ${If} $R4 >= 30
      DetailPrint "计划任务 ${TASKNAME} 终验失败（退出码 $R0）：$R1"
      Abort "计划任务 ${TASKNAME} 未注册到本安装（校验退出码 $R0；若旧任务由管理员权限创建，请以管理员身份运行安装程序）"
    ${EndIf}
    Sleep 500
  ${Loop}
!macroend

!macro VerifySchedTaskGone TASKNAME
  ; 卸载终验：任务应已删除（不恢复旧任务定义）；删除被拒不阻断卸载，仅明确警告
  nsExec::ExecToStack 'schtasks /query /tn ${TASKNAME}'
  Pop $R0
  Pop $R1
  ${If} $R0 = 0
    DetailPrint "警告: 计划任务 ${TASKNAME} 未能删除（可手动 schtasks /delete /tn ${TASKNAME} /f）"
  ${Else}
    DetailPrint "已删除计划任务 ${TASKNAME}（不恢复旧任务定义）"
  ${EndIf}
!macroend

!macro RunSchedTasks MODE
  ; MODE=install|remove：register-tasks.cmd 执行 5 任务注册/删除（nsExec 无窗）；
  ; 失败（多为旧任务由管理员权限创建）→ elevate-tasks.ps1 走 UAC 提权重试一次，隐藏控制台
  nsExec::ExecToStack 'cmd /c call "$INSTDIR\register-tasks.cmd" ${MODE}'
  Pop $R0
  Pop $R1
  ${If} $R0 <> 0
    DetailPrint "计划任务 ${MODE} 被拒（退出码 $R0；旧任务可能由管理员权限创建）→ UAC 提权重试一次"
    nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -File "$INSTDIR\elevate-tasks.ps1" -Mode ${MODE}'
    Pop $R2
    Pop $R3
    DetailPrint "提权重试结束（退出码 $R2）；结果以下方终验为准"
  ${EndIf}
!macroend

Section "安装" SEC_MAIN
  SetOutPath "$INSTDIR"
  SetOverwrite on
  ; 装配布局入包：uvmp-toolkit.exe / uvmp-core.exe / uvmp-export.exe / models / native /
  ; run-uvmp.vbs / register-tasks.cmd / run-uvmp-gui.vbs / elevate-tasks.ps1 / icon.ico
  ; （Windows 包不再随带 browser-runtime/Electron：装配脚本已剔除，CDP 走显式宿主）
  File /r "${STAGINGDIR}\*"

  ; 控制台启动器（排障与绿色包同款）：注入 native 后前台启动，会带控制台窗口，不作为默认入口
  FileOpen $0 "$INSTDIR\${PRODUCTID}.cmd" w
  FileWrite $0 '@echo off$\r$\n'
  FileWrite $0 'setlocal$\r$\n'
  FileWrite $0 'set "PATH=%~dp0native;%PATH%"$\r$\n'
  FileWrite $0 '"%~dp0uvmp-toolkit.exe" %*$\r$\n'
  FileClose $0

  ; 快捷方式（开始菜单 + 桌面，per-user）：目标 wscript.exe，参数为隐藏启动器脚本，
  ; 图标引安装目录 icon.ico（wscript 自身图标不是本产品品牌）
  CreateShortCut "$SMPROGRAMS\${DISPLAYNAME}.lnk" "$SYSDIR\wscript.exe" '"$INSTDIR\run-uvmp-gui.vbs"' "$INSTDIR\icon.ico" 0 SW_SHOWNORMAL
  CreateShortCut "$DESKTOP\${DISPLAYNAME}.lnk" "$SYSDIR\wscript.exe" '"$INSTDIR\run-uvmp-gui.vbs"' "$INSTDIR\icon.ico" 0 SW_SHOWNORMAL

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

  ; 自启目标修正：旧版本/手动设置可能把 HKCU Run 值指向 uvmp-toolkit.exe（控制台子系统，
  ; 登录时闪黑窗）。值存在即视为用户已启用自启——只把目标改指隐藏启动器，不改开关语义。
  ReadRegStr $0 HKCU "${RUN_KEY}" "${RUN_VALUE}"
  ${If} $0 != ""
    WriteRegStr HKCU "${RUN_KEY}" "${RUN_VALUE}" 'wscript.exe "$INSTDIR\run-uvmp-gui.vbs"'
    DetailPrint "已把开机自启目标改指隐藏启动器（原值: $0）"
  ${EndIf}

  ; 卸载项（快捷方式/卸载项图标一律安装目录 icon.ico，命令行带引号防中文路径截断）
  WriteUninstaller "$INSTDIR\${UNINSTALLER}"
  WriteRegStr HKCU "Software\${PRODUCTID}" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "${DISPLAYNAME}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "${PUBLISHER}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\icon.ico,0"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\${UNINSTALLER}"'
  WriteRegStr HKCU "${UNINSTALL_KEY}" "QuietUninstallString" '"$INSTDIR\${UNINSTALLER}" /S'
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
  ; 自启残留清理：应用启用过开机自启时会写 HKCU Run（值名与 autostart.go 一致），
  ; 卸载后若留着会指向已删除的启动器，登录时报错——这里一并删除
  DeleteRegValue HKCU "${RUN_KEY}" "${RUN_VALUE}"
  Delete "$SMPROGRAMS\${DISPLAYNAME}.lnk"
  Delete "$DESKTOP\${DISPLAYNAME}.lnk"
  ; 整目录删除（安装目录专用于本产品；/D= 自定义目录时只删本产品文件树）
  RMDir /r "$INSTDIR"
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
  DeleteRegKey HKCU "Software\${PRODUCTID}"
SectionEnd
