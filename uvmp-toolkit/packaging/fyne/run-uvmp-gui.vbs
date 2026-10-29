' Shade toolkit (Fyne track) GUI launcher - starts the client without a console window.
'
' Why this file exists:
'   uvmp-toolkit.exe is built as a console-subsystem binary (build_fyne_core.ps1
'   without UVMP_FYNE_WINDOWED=1), so double-clicking it - or pointing a shortcut /
'   the HKCU Run autostart value straight at it - flashes a black console window.
'   WScript.Shell.Run with window style 0 hides that console, and this launcher also
'   prepends <install dir>\native to PATH so ONNX Runtime (onnxruntime.dll) resolves
'   at runtime, exactly like uvmp-toolkit-fyne.cmd and run-uvmp.vbs do for uvmp-core.
'
' Used by (see packaging/fyne/fyne-installer.nsi):
'   shortcuts  : wscript.exe "<install dir>\run-uvmp-gui.vbs"
'   autostart  : HKCU\Software\Microsoft\Windows\CurrentVersion\Run -> same command
'                (written by the app itself, platform.AutoStart; main.go rewrites
'                 the target to this launcher when installed)
'
' Security: the peer instance lock is handled by the app - a second launch wakes the
' running window and exits by itself, so this script never waits (Run ..., False).
Option Explicit

Dim fso, sh, env, baseDir, nativeDir, exePath, args, i, cmd

Set fso = CreateObject("Scripting.FileSystemObject")
Set sh = CreateObject("WScript.Shell")

baseDir = fso.GetParentFolderName(WScript.ScriptFullName)
exePath = fso.BuildPath(baseDir, "uvmp-toolkit.exe")
If Not fso.FileExists(exePath) Then
  MsgBox "Cannot find " & exePath & vbCrLf & _
         "Please reinstall the client.", vbCritical, "Shade Toolkit"
  WScript.Quit 1
End If

' Inject <install dir>\native (ONNX Runtime) into PATH: child processes inherit it;
' when the folder is missing the environment is left untouched.
nativeDir = fso.BuildPath(baseDir, "native")
If fso.FolderExists(nativeDir) Then
  Set env = sh.Environment("PROCESS")
  env("PATH") = nativeDir & ";" & env("PATH")
End If

' Pass through command line arguments (the GUI has no argument contract yet; kept for
' troubleshooting and for the second-launch "wake the running window" path).
args = ""
For i = 0 To WScript.Arguments.Count - 1
  args = args & " " & QuoteArg(WScript.Arguments(i))
Next

cmd = Chr(34) & exePath & Chr(34) & args
' 0 = hidden window (hides the console of the console-subsystem exe); False = no wait
sh.Run cmd, 0, False

Function QuoteArg(s)
  If InStr(s, " ") > 0 Or InStr(s, vbTab) > 0 Then
    QuoteArg = Chr(34) & s & Chr(34)
  Else
    QuoteArg = s
  End If
End Function
