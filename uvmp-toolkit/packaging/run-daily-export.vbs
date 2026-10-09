' 以隐藏窗口方式运行每日派车单同步（Windows 计划任务每分钟触发本脚本）。
' 由 CI 随核心包分发（与 uvmp-core.exe 同目录）。
' 注意：WScript.Shell.Run 不经 cmd，重定向必须显式 cmd /c（已踩坑：>> 会被当成参数）
Set ws = CreateObject("Wscript.Shell")
Set fso = CreateObject("Scripting.FileSystemObject")
basedir = fso.GetParentFolderName(WScript.ScriptFullName)

Function Q(s)
  Q = """" & s & """"
End Function

cmd = "cmd /c ""set PYTHONIOENCODING=utf-8&& " & Q(basedir & "\uvmp-core.exe") _
      & " daily-export --if-due >> " & Q(basedir & "\daily-export.log") & " 2>&1"""
ws.Run cmd, 0, True   ' 0=隐藏窗口；True=等待完成，防止分钟级周期重叠执行
