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

' 树障统计表同步（同一唤醒链路）：先按 U盘指令执行（U盘不在秒退），再做 23:55 夜间兜底判定；
' 计划任务全天每分钟触发——00:00 服务端回写 replace 后下一轮 tick 执行（与 linux timer 同口径）。
cmd = "cmd /c ""set PYTHONIOENCODING=utf-8&& " & Q(basedir & "\uvmp-core.exe") _
      & " tree-sync --tick >> " & Q(basedir & "\tree-sync.log") & " 2>&1"""
ws.Run cmd, 0, True
cmd = "cmd /c ""set PYTHONIOENCODING=utf-8&& " & Q(basedir & "\uvmp-core.exe") _
      & " tree-sync --nightly --if-due >> " & Q(basedir & "\tree-sync.log") & " 2>&1"""
ws.Run cmd, 0, True

' 交跨统计表同步（与树障同契约、独立指令文件/成功戳，同一唤醒链路）：先按 U盘指令执行
' （U盘不在秒退），再做 23:55 夜间兜底判定；计划任务全天每分钟触发，00:00 回写窗口不暂停轮询。
cmd = "cmd /c ""set PYTHONIOENCODING=utf-8&& " & Q(basedir & "\uvmp-core.exe") _
      & " crossing-sync --tick >> " & Q(basedir & "\crossing-sync.log") & " 2>&1"""
ws.Run cmd, 0, True
cmd = "cmd /c ""set PYTHONIOENCODING=utf-8&& " & Q(basedir & "\uvmp-core.exe") _
      & " crossing-sync --nightly --if-due >> " & Q(basedir & "\crossing-sync.log") & " 2>&1"""
ws.Run cmd, 0, True
