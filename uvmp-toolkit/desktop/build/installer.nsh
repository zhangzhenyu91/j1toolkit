; electron-builder NSIS hooks（makensis 由 electron-builder 以 -INPUTCHARSET UTF8 读取本文件）。
;
; USB agent: one logon task runs the hidden agent; the agent consumes server commands.
; Remove legacy per-minute tasks on upgrade to avoid double runs.
; Do not write user config into $INSTDIR: upgrades/uninstall wipe it; config lives in the user profile.
;
; 进程回收的挂点（勿随意挪动）：electron-builder 26 的 installSection.nsh 顺序是
;   CHECK_APP_RUNNING → uninstallOldVersion → installApplicationFiles → customInstall，
; 且 CHECK_APP_RUNNING 优先展开 customCheckAppRunning。只有这个位置在「关旧版 + 覆盖文件」之前；
; customInstall 时文件已经写完，再回收已来不及（旧 uvmp-core.exe 仍占用 resources\core 下的文件，
; 用户只能去任务管理器手杀）。卸载时 un.onInit（静默）与卸载安装段也走同一个 CHECK_APP_RUNNING，
; 所以安装与卸载共用下面这份回收逻辑。
;
; 只回收本客户端独有的两个映像名：客户端本体（uvmp-toolkit.exe，由 electron-builder 的
; APP_EXECUTABLE_FILENAME 给出）与 uvmp-core.exe（Python 核心，也是登录计划任务拉起的后台
; usb-agent）。命令一律按完整映像名精确匹配，不用通配符，不动任务计划、不删用户数据目录。
;
; 宏参数限制：NSIS 的 !insertmacro 参数以空格切分，带空格的文案/命令行不能当参数传，
; 因此这里的宏要么不带参数、要么只接一个变量名（$R0 之类）。

!include LogicLib.nsh

!define UVMP_CORE_IMAGE uvmp-core.exe

; ---------------------------------------------------------------------------
; 进程探测与结束（命令全部经 nsExec 隐藏执行，不会闪出控制台窗口）
; ---------------------------------------------------------------------------

; 同名进程是否存在：tasklist 命中且 findstr 命中返回 0，否则返回 1。
; per-user 安装只处理当前登录用户，不能结束其他会话里的同名进程。
; tasklist 在没有匹配时仍返回 0 并打印「没有运行的任务匹配指定标准」，
; 所以必须由 findstr 定论；/B 锚定行首、/C:"\"名字\"" 锚定完整 CSV 首字段，
; uvmp-core-helper.exe、my-uvmp-core.exe 之类都不会被算进来。
; 命令形态与 electron-builder 自带 FIND_PROCESS 一致：整条命令放在反引号里，内层双引号原样交给 cmd。
!macro UVMP_CLIENT_COUNT _OUT
  nsExec::Exec `"$SYSDIR\cmd.exe" /C tasklist /FI "USERNAME eq %USERNAME%" /FI "IMAGENAME eq ${APP_EXECUTABLE_FILENAME}" /FO CSV /NH | "$SYSDIR\findstr.exe" /B /I /C:"\"${APP_EXECUTABLE_FILENAME}\""`
  Pop ${_OUT}
!macroend

!macro UVMP_CORE_COUNT _OUT
  nsExec::Exec `"$SYSDIR\cmd.exe" /C tasklist /FI "USERNAME eq %USERNAME%" /FI "IMAGENAME eq ${UVMP_CORE_IMAGE}" /FO CSV /NH | "$SYSDIR\findstr.exe" /B /I /C:"\"${UVMP_CORE_IMAGE}\""`
  Pop ${_OUT}
!macroend

; 温和结束（等同任务管理器「结束任务」，发 WM_CLOSE）；只对 GUI 客户端有意义。
!macro UVMP_CLIENT_CLOSE
  DetailPrint "请求关闭客户端：${APP_EXECUTABLE_FILENAME}"
  nsExec::ExecToLog `"$SYSDIR\taskkill.exe" /IM "${APP_EXECUTABLE_FILENAME}" /FI "USERNAME eq %USERNAME%"`
  Pop $R3
!macroend

!macro UVMP_CLIENT_KILL
  DetailPrint "强制结束客户端：${APP_EXECUTABLE_FILENAME}"
  nsExec::ExecToLog `"$SYSDIR\taskkill.exe" /F /IM "${APP_EXECUTABLE_FILENAME}" /FI "USERNAME eq %USERNAME%"`
  Pop $R3
!macroend

; 核心与后台 usb-agent 是无窗口控制台进程，没有消息循环，只能强制结束。
!macro UVMP_CORE_KILL
  DetailPrint "强制结束后台核心：${UVMP_CORE_IMAGE}"
  nsExec::ExecToLog `"$SYSDIR\taskkill.exe" /F /IM "${UVMP_CORE_IMAGE}" /FI "USERNAME eq %USERNAME%"`
  Pop $R3
!macroend

; ---------------------------------------------------------------------------
; 回收本客户端进程
; ---------------------------------------------------------------------------
; 顺序：先关 GUI 客户端（让托盘/窗口收尾），再结束核心与后台 usb-agent；
; 进程退出后系统释放 resources\core 下的文件锁，旧文件才能被覆盖。
; 进程还在就不许继续：继续只会撞文件占用，所以给出可操作的提示并中止，绝不静默往下走。
;
; 本宏在 customCheckAppRunning 中只展开一次，标签使用固定后缀，
; 不使用可能被 NSIS 依赖覆盖的行号符号。
!macro UVMP_RELEASE_PROCESSES
  !ifdef UVMP_RELEASE_ID
    !undef UVMP_RELEASE_ID
  !endif
  !define UVMP_RELEASE_ID core

  Push $R0
  Push $R1
  Push $R2
  Push $R3
  Push $R4

  SetDetailsPrint both
  SetDetailsView show

  StrCpy $R2 0

  uvmp_release_retry_${UVMP_RELEASE_ID}:
    IntOp $R2 $R2 + 1
    DetailPrint "第 $R2 次回收本客户端进程（${APP_EXECUTABLE_FILENAME} / ${UVMP_CORE_IMAGE}）"

    ; 客户端：先温和关闭，仍活着再强制结束（「关闭到托盘」时温和关闭无效）。
    !insertmacro UVMP_CLIENT_COUNT $R0
    ${If} $R0 == 0
      !insertmacro UVMP_CLIENT_CLOSE
      Sleep 700
      !insertmacro UVMP_CLIENT_COUNT $R0
      ${If} $R0 == 0
        !insertmacro UVMP_CLIENT_KILL
      ${EndIf}
    ${EndIf}

    ; 核心 / 后台 usb-agent：直接强制结束，随后等它释放文件句柄。
    !insertmacro UVMP_CORE_COUNT $R1
    ${If} $R1 == 0
      !insertmacro UVMP_CORE_KILL
      Sleep 800
    ${EndIf}

    !insertmacro UVMP_CLIENT_COUNT $R0
    !insertmacro UVMP_CORE_COUNT $R1
    StrCpy $R4 ""
    ${If} $R0 == 0
      StrCpy $R4 "客户端仍在运行"
    ${EndIf}
    ${If} $R1 == 0
      ${If} $R4 == ""
        StrCpy $R4 "后台核心仍在运行"
      ${Else}
        StrCpy $R4 "$R4、后台核心仍在运行"
      ${EndIf}
    ${EndIf}
    ${If} $R4 != ""
      DetailPrint "回收未完成：$R4"
      Goto uvmp_release_blocked_${UVMP_RELEASE_ID}
    ${EndIf}

    DetailPrint "客户端与后台核心均已退出，继续。"
    Goto uvmp_release_done_${UVMP_RELEASE_ID}

  uvmp_release_blocked_${UVMP_RELEASE_ID}:
    ${If} $R2 < 2
      Sleep 1200
      Goto uvmp_release_retry_${UVMP_RELEASE_ID}
    ${EndIf}

    ; 进程还活着（常见：客户端以管理员身份运行，本安装器无权结束）必须停下来说明怎么办。
    DetailPrint "无法结束 ${APP_EXECUTABLE_FILENAME} / ${UVMP_CORE_IMAGE}，已中止本次操作，未改动任何文件。"
    ${If} ${Silent}
      SetErrorLevel 2
      Quit
    ${EndIf}
    MessageBox MB_RETRYCANCEL|MB_ICONSTOP "无法关闭正在运行的客户端或后台核心（${APP_EXECUTABLE_FILENAME} / ${UVMP_CORE_IMAGE}）。$\r$\n本次操作已暂停，尚未改动任何文件。$\r$\n$\r$\n请打开任务管理器，在「详细信息」里结束这两个进程后点「重试」；点「取消」退出。" IDRETRY uvmp_release_retry_${UVMP_RELEASE_ID}
    DetailPrint "已取消：请先在任务管理器结束 ${APP_EXECUTABLE_FILENAME} 与 ${UVMP_CORE_IMAGE}，再重新运行。"
    SetErrorLevel 2
    Quit

  uvmp_release_done_${UVMP_RELEASE_ID}:
  Pop $R4
  Pop $R3
  Pop $R2
  Pop $R1
  Pop $R0
!macroend

; ---------------------------------------------------------------------------
; 安装界面：展开安装明细
; ---------------------------------------------------------------------------
; common.nsh 里写死 ShowInstDetails nevershow，customHeader 在它之后插入，可覆盖默认值。
; installSection.nsh 在非静默时还会 SetDetailsPrint none 压掉 electron-builder 自己的输出，
; 所以回收步骤里再 SetDetailsPrint both + SetDetailsView show（都只在安装段内执行；
; 不放到 .onInit —— 那时安装文件页尚未创建，SetDetailsView 没有文档保证可用）。
; 只覆盖明细显示，不改 oneClick：辅助安装器（oneClick=false）与可改安装目录都保持不变。
!macro customHeader
  ShowInstDetails show
  !ifdef BUILD_UNINSTALLER
    ShowUninstDetails show
  !endif
!macroend

; ---------------------------------------------------------------------------
; 安装/卸载的进程检查入口（替代 electron-builder 只查客户端 exe 的默认实现）
; ---------------------------------------------------------------------------
!macro customCheckAppRunning
  !ifdef UVMP_CONFIRM_ID
    !undef UVMP_CONFIRM_ID
  !endif
  !define UVMP_CONFIRM_ID install

  !insertmacro UVMP_CLIENT_COUNT $R0
  !ifndef BUILD_UNINSTALLER
    ; 客户端开着时先征得同意；静默安装与自动更新（/updated）不打扰，直接回收。
    ${If} $R0 == 0
    ${AndIfNot} ${isUpdated}
      ${IfNot} ${Silent}
        MessageBox MB_OKCANCEL|MB_ICONEXCLAMATION "「Shade 壹匣 - 内网」正在运行。$\r$\n安装程序会先关闭客户端与后台核心（含 U 盘代理）再继续，用户数据与计划任务保留。$\r$\n$\r$\n点「确定」继续安装，点「取消」退出。" /SD IDCANCEL IDOK uvmp_confirm_${UVMP_CONFIRM_ID}
        Quit
      ${EndIf}
    ${EndIf}
    ; 标签也只能在非卸载构建中生成，否则 makensis 将未使用标签警告视为错误。
    uvmp_confirm_${UVMP_CONFIRM_ID}:
  !endif
  !insertmacro UVMP_RELEASE_PROCESSES
!macroend

!macro customInstall
  DetailPrint "清理旧版按分钟触发的计划任务（UsbAgent 登录任务稍后重建）"
  nsExec::ExecToLog '"$SYSDIR\schtasks.exe" /delete /tn "UVMP-Toolkit-DailyExport" /f'
  Pop $R9
  nsExec::ExecToLog '"$SYSDIR\schtasks.exe" /delete /tn "UVMP-Toolkit-TreeSyncTick" /f'
  Pop $R9
  nsExec::ExecToLog '"$SYSDIR\schtasks.exe" /delete /tn "UVMP-Toolkit-TreeSyncNightly" /f'
  Pop $R9
  nsExec::ExecToLog '"$SYSDIR\schtasks.exe" /delete /tn "UVMP-Toolkit-CrossingSyncTick" /f'
  Pop $R9
  nsExec::ExecToLog '"$SYSDIR\schtasks.exe" /delete /tn "UVMP-Toolkit-CrossingSyncNightly" /f'
  Pop $R9
  DetailPrint "重建 U 盘代理登录任务（UVMP-Toolkit-UsbAgent）"
  ; 任务指向安装目录内的隐藏启动脚本：wscript 以隐藏窗口运行 cmd → uvmp-core usb-agent，
  ; 不会闪出控制台窗口；agent 自身用 state/usb-agent.lock 保证单实例，因此只需这一个任务。
  nsExec::ExecToLog '"$SYSDIR\schtasks.exe" /create /tn "UVMP-Toolkit-UsbAgent" /f /sc onlogon /tr "wscript.exe \"$INSTDIR\resources\core\run-usb-agent.vbs\""'
  Pop $R9
  ; 回收阶段结束了旧的 usb-agent，登录任务要到下次登录才触发；这里立即跑一次，
  ; 否则本次登录期间 U 盘指令无人消费。agent 自带 state/usb-agent.lock 单实例锁，
  ; 与客户端启动时拉起的那份互斥，不会变成两个 agent。
  nsExec::ExecToLog '"$SYSDIR\schtasks.exe" /run /tn "UVMP-Toolkit-UsbAgent"'
  Pop $R9
  ; Client version reported to the server (core/version.py reads version.txt next to uvmp-core.exe).
  ; Linux writes it in the deb postinst; Windows had no equivalent, so the client reported an empty version.
  FileOpen $0 "$INSTDIR\resources\core\version.txt" w
  FileWrite $0 "${VERSION}"
  FileClose $0
!macroend

!macro customUnInstall
  DetailPrint "删除本客户端的计划任务"
  nsExec::ExecToLog '"$SYSDIR\schtasks.exe" /delete /tn "UVMP-Toolkit-UsbAgent" /f'
  Pop $R9
  nsExec::ExecToLog '"$SYSDIR\schtasks.exe" /delete /tn "UVMP-Toolkit-DailyExport" /f'
  Pop $R9
  nsExec::ExecToLog '"$SYSDIR\schtasks.exe" /delete /tn "UVMP-Toolkit-TreeSyncTick" /f'
  Pop $R9
  nsExec::ExecToLog '"$SYSDIR\schtasks.exe" /delete /tn "UVMP-Toolkit-TreeSyncNightly" /f'
  Pop $R9
  nsExec::ExecToLog '"$SYSDIR\schtasks.exe" /delete /tn "UVMP-Toolkit-CrossingSyncTick" /f'
  Pop $R9
  nsExec::ExecToLog '"$SYSDIR\schtasks.exe" /delete /tn "UVMP-Toolkit-CrossingSyncNightly" /f'
  Pop $R9
!macroend
