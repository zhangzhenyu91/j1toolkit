' 以隐藏窗口方式运行 uvmp-core（Windows 计划任务每分钟触发本脚本，参数即 uvmp-core 子命令与开关）。
' 用法: wscript.exe run-uvmp.vbs <uvmp-core 参数...>
'   例: wscript.exe run-uvmp.vbs daily-export --if-due
' 计划任务注册见 packaging/fyne/register-tasks.cmd（由 fyne-installer.nsi 调用，5 个 UVMP-Toolkit-* 任务与旧版同名升级替换）。
' 日志按子命令分文件（与旧版 packaging/run-daily-export.vbs 同口径）。
' 注意：WScript.Shell.Run 不经 cmd，重定向必须显式 cmd /c（已踩坑：>> 会被当成参数）
Set ws = CreateObject("WScript.Shell")
Set fso = CreateObject("Scripting.FileSystemObject")
basedir = fso.GetParentFolderName(WScript.ScriptFullName)

If WScript.Arguments.Count < 1 Then
  WScript.Echo "用法: wscript.exe run-uvmp.vbs <uvmp-core 参数...>"
  WScript.Quit 1
End If

Function Q(s)
  Q = """" & s & """"
End Function

' 参数拼接（含空格的参数补引号）
argstr = ""
For i = 0 To WScript.Arguments.Count - 1
  a = WScript.Arguments(i)
  If InStr(a, " ") > 0 Then a = Q(a)
  If i > 0 Then argstr = argstr & " "
  argstr = argstr & a
Next

' 日志名按子命令映射（未知命令归 uvmp-core.log）
Select Case LCase(WScript.Arguments(0))
  Case "daily-export": logname = "daily-export.log"
  Case "tree-sync": logname = "tree-sync.log"
  Case "crossing-sync": logname = "crossing-sync.log"
  Case Else: logname = "uvmp-core.log"
End Select

' 与安装器启动器同口径：ONNX 运行时库走 native\（PATH 注入），子进程继承
Set env = ws.Environment("PROCESS")
env("PATH") = basedir & "\native;" & env("PATH")
env("PYTHONIOENCODING") = "utf-8"

cmd = "cmd /c ""set PYTHONIOENCODING=utf-8&& " & Q(basedir & "\uvmp-core.exe") _
      & " " & argstr & " >> " & Q(basedir & "\" & logname) & " 2>&1"""
ws.Run cmd, 0, True   ' 0=隐藏窗口；True=等待完成，防止分钟级周期重叠执行
