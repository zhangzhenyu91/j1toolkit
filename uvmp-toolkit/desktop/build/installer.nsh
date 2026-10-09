; electron-builder NSIS 钩子（全 ASCII，无编码问题）。
; 每日派车单同步计划任务：每分钟触发 wscript 隐藏窗口脚本（due-check 判定在核心里）。
; 注意：不要在安装目录写用户配置——升级卸载会清空 $INSTDIR；配置在用户目录（config.py 自动迁移）。
!macro customInstall
  ExecWait 'schtasks /create /tn "UVMP-Toolkit-DailyExport" /f /sc minute /mo 1 /tr "wscript.exe \"$INSTDIR\resources\core\run-daily-export.vbs\""'
!macroend

!macro customUnInstall
  ExecWait 'schtasks /delete /tn "UVMP-Toolkit-DailyExport" /f'
!macroend
