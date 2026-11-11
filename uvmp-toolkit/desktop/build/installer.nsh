; electron-builder NSIS hooks (ASCII only, no encoding issues).
; USB agent: one logon task runs the hidden agent; the agent consumes server commands.
; Remove legacy per-minute tasks on upgrade to avoid double runs.
; Do not write user config into $INSTDIR: upgrades/uninstall wipe it; config lives in the user profile.
!macro customInstall
  ExecWait 'schtasks /delete /tn "UVMP-Toolkit-DailyExport" /f'
  ExecWait 'schtasks /delete /tn "UVMP-Toolkit-TreeSyncTick" /f'
  ExecWait 'schtasks /delete /tn "UVMP-Toolkit-TreeSyncNightly" /f'
  ExecWait 'schtasks /delete /tn "UVMP-Toolkit-CrossingSyncTick" /f'
  ExecWait 'schtasks /delete /tn "UVMP-Toolkit-CrossingSyncNightly" /f'
  ExecWait 'schtasks /create /tn "UVMP-Toolkit-UsbAgent" /f /sc onlogon /tr "wscript.exe \"$INSTDIR\resources\core\run-usb-agent.vbs\""'
  ; Client version reported to the server (core/version.py reads version.txt next to uvmp-core.exe).
  ; Linux writes it in the deb postinst; Windows had no equivalent, so the client reported an empty version.
  FileOpen $0 "$INSTDIR\resources\core\version.txt" w
  FileWrite $0 "${VERSION}"
  FileClose $0
!macroend

!macro customUnInstall
  ExecWait 'schtasks /delete /tn "UVMP-Toolkit-UsbAgent" /f'
  ExecWait 'schtasks /delete /tn "UVMP-Toolkit-DailyExport" /f'
  ExecWait 'schtasks /delete /tn "UVMP-Toolkit-TreeSyncTick" /f'
  ExecWait 'schtasks /delete /tn "UVMP-Toolkit-TreeSyncNightly" /f'
  ExecWait 'schtasks /delete /tn "UVMP-Toolkit-CrossingSyncTick" /f'
  ExecWait 'schtasks /delete /tn "UVMP-Toolkit-CrossingSyncNightly" /f'
!macroend
