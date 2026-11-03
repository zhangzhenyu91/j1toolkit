' 以隐藏窗口运行 U 盘单监听 agent（登录计划任务触发）。
' agent 自身用 state/usb-agent.lock 保证单实例；GUI 启动时也会尝试拉起，重复实例会直接退出。
' 由 CI 随核心包分发（与 uvmp-core.exe 同目录）。
' 注意：WScript.Shell.Run 不经 cmd，重定向必须显式 cmd /c（已踩坑：>> 会被当成参数）
Set ws = CreateObject("Wscript.Shell")
Set fso = CreateObject("Scripting.FileSystemObject")
basedir = fso.GetParentFolderName(WScript.ScriptFullName)

Function Q(s)
  Q = """" & s & """"
End Function

cmd = "cmd /c ""set PYTHONIOENCODING=utf-8&& " & Q(basedir & "\uvmp-core.exe") _
      & " usb-agent --quiet >> " & Q(basedir & "\usb-agent.log") & " 2>&1"""
ws.Run cmd, 0, False   ' 0=隐藏窗口；False=不等待（agent 常驻，由自身重启策略兜底）
