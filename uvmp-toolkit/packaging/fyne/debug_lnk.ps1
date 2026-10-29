# TEMPORARY debug script: why does WScript.Shell read an empty TargetPath from the
# NSIS-created shortcuts on the GitHub windows runner while it works on dev machines?
# ASCII-only on purpose (PS 5.1 reads BOM-less ps1 as ANSI; keep every literal safe).
# Deletes itself from the repo along with debug-lnk.yml after the root cause is found.
param()
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..\..")

function HexDump([string]$Path, [int]$Bytes) {
  $b = [System.IO.File]::ReadAllBytes($Path)
  $n = [Math]::Min($Bytes, $b.Length)
  for ($off = 0; $off -lt $n; $off += 16) {
    $len = [Math]::Min(16, $n - $off)
    $hex = ""
    $asc = ""
    for ($i = 0; $i -lt $len; $i++) {
      $v = $b[$off + $i]
      $hex += $v.ToString("x2") + " "
      if ($v -ge 0x20 -and $v -le 0x7e) { $asc += [char]$v } else { $asc += "." }
    }
    Write-Host ("{0:x8}  {1} {2}" -f $off, $hex.PadRight(48), $asc)
  }
}

function Find-Bytes([byte[]]$Haystack, [byte[]]$Needle) {
  for ($i = 0; $i -le $Haystack.Length - $Needle.Length; $i++) {
    $ok = $true
    for ($j = 0; $j -lt $Needle.Length; $j++) {
      if ($Haystack[$i + $j] -ne $Needle[$j]) { $ok = $false; break }
    }
    if ($ok) { return $i }
  }
  return -1
}

Write-Host "===== ENV FACTS ====="
Write-Host "TEMP               = $env:TEMP"
Write-Host "OS                 = $([Environment]::OSVersion.VersionString)"
Write-Host "PSVersion          = $($PSVersionTable.PSVersion)"
Write-Host "ANSI codepage      = $([System.Text.Encoding]::Default.CodePage) ($([System.Text.Encoding]::Default.WebName))"
Write-Host "GetFolderPath Programs = $([Environment]::GetFolderPath('Programs'))"
Write-Host "GetFolderPath Desktop  = $([Environment]::GetFolderPath('Desktop'))"
Write-Host "System32 wscript   = $(Test-Path 'C:\Windows\System32\wscript.exe')"
Write-Host "SysWOW64 wscript   = $(Test-Path 'C:\Windows\SysWOW64\wscript.exe')"

# ---------- 1. real installer: silent install with 8.3-form TEMP dir (same as smoke) ----------
$setup = Resolve-Path "dist-fyne/windows-amd64/uvmp-toolkit-fyne-Setup-*.exe" | Select-Object -First 1
$instDir = Join-Path $env:TEMP ("uvmp-debug-" + [guid]::NewGuid().ToString("N"))
Write-Host "===== REAL INSTALLER -> $instDir ====="
$p = Start-Process -FilePath $setup -ArgumentList "/S", "/D=$instDir" -PassThru -Wait
Write-Host "install exit: $($p.ExitCode)"
if ($p.ExitCode -ne 0) { throw "silent install failed with $($p.ExitCode)" }

$un = Get-ItemProperty "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\UvmpToolkitFyne"
$dn = $un.DisplayName
Write-Host "DisplayName        = [$dn] len=$($dn.Length)"
Write-Host ("DisplayName codes  = " + (($dn.ToCharArray() | ForEach-Object { [int]$_ | ForEach-Object { $_.ToString("x4") } }) -join " "))

$wsh = New-Object -ComObject WScript.Shell
$programs = [Environment]::GetFolderPath("Programs")
Write-Host "===== LNKS MATCHING Shade/Fyne IN PROGRAMS ====="
Get-ChildItem $programs -Filter "*.lnk" | Where-Object { $_.Name -match "Shade|Fyne|fyne" } | ForEach-Object {
  Write-Host ("  found: [{0}] size={1}" -f $_.Name, $_.Length)
}
$smLnk = Join-Path $programs "$dn.lnk"
Write-Host "expected lnk       = $smLnk"
Write-Host "Test-Path          = $(Test-Path $smLnk)"
$lnkItem = Get-Item $smLnk -ErrorAction SilentlyContinue
if ($null -eq $lnkItem) { throw "lnk missing: $smLnk" }
Write-Host "lnk size           = $($lnkItem.Length)"

$sc = $wsh.CreateShortcut($smLnk)
Write-Host "===== WSCRIPT READ (real lnk) ====="
Write-Host "TargetPath         = [$($sc.TargetPath)]"
Write-Host "Arguments          = [$($sc.Arguments)]"
Write-Host "IconLocation       = [$($sc.IconLocation)]"
Write-Host "WorkingDirectory   = [$($sc.WorkingDirectory)]"
Write-Host "TargetPath len     = $($sc.TargetPath.Length)"

Write-Host "===== HEXDUMP (first 0x300 bytes of real lnk) ====="
HexDump $smLnk 0x300
$raw = [System.IO.File]::ReadAllBytes($smLnk)
$asciiWscript = Find-Bytes $raw ([System.Text.Encoding]::ASCII.GetBytes("wscript"))
$utf16Wscript = Find-Bytes $raw ([System.Text.Encoding]::Unicode.GetBytes("wscript"))
Write-Host "raw 'wscript' ascii offset = $asciiWscript / utf16 offset = $utf16Wscript"

# reader control: WScript.Shell creates its own lnk with the same Chinese name
$ctlLnk = Join-Path $env:TEMP ("uvmp-ctl-" + [guid]::NewGuid().ToString("N") + ".lnk")
$c = $wsh.CreateShortcut($ctlLnk)
$c.TargetPath = "$env:WINDIR\System32\wscript.exe"
$c.Arguments = '"x.vbs"'
$c.Save()
$c2 = $wsh.CreateShortcut($ctlLnk)
Write-Host "===== READER CONTROL (wscript-created lnk) ====="
Write-Host "TargetPath         = [$($c2.TargetPath)]"
Remove-Item $ctlLnk -Force

# ---------- 2. variant matrix with a minimal NSIS script (same CreateShortCut lines) ----------
Write-Host "===== VARIANT MATRIX ====="
$nsiText = [System.IO.File]::ReadAllText("packaging/fyne/fyne-installer.nsi")
if ($nsiText -notmatch '(?m)!define DISPLAYNAME "(.+?)"\r?\n') { throw "DISPLAYNAME not found in nsi" }
$dispName = $Matches[1]
Write-Host "parsed DISPLAYNAME = [$dispName]"

$vDir = Join-Path $env:TEMP ("uvmp-var-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force $vDir | Out-Null
Copy-Item (Join-Path $instDir "icon.ico") (Join-Path $vDir "icon.ico")
Copy-Item (Join-Path $instDir "run-uvmp-gui.vbs") (Join-Path $vDir "run-uvmp-gui.vbs")

$vNsi = @"
Unicode true
!define DISPLAYNAME "$dispName"
OutFile "$vDir\vsetup.exe"
InstallDir "$vDir\app"
RequestExecutionLevel user
Page instfiles
Section
  CreateShortCut "`$SMPROGRAMS\v-ascii-full.lnk" "`$SYSDIR\wscript.exe" '"`$INSTDIR\run-uvmp-gui.vbs"' "`$INSTDIR\icon.ico" 0 SW_SHOWNORMAL
  CreateShortCut "`$SMPROGRAMS\v-ascii-noicon.lnk" "`$SYSDIR\wscript.exe" '"`$INSTDIR\run-uvmp-gui.vbs"'
  CreateShortCut "`$SMPROGRAMS\v-ascii-noparams.lnk" "`$SYSDIR\wscript.exe"
  CreateShortCut "`$SMPROGRAMS\v-syswow-target.lnk" "C:\Windows\SysWOW64\wscript.exe"
  CreateShortCut "`$SMPROGRAMS\`${DISPLAYNAME}.lnk" "`$SYSDIR\wscript.exe" '"`$INSTDIR\run-uvmp-gui.vbs"' "`$INSTDIR\icon.ico" 0 SW_SHOWNORMAL
  SetOutPath "`$INSTDIR"
SectionEnd
"@
$vNsiPath = Join-Path $vDir "variant.nsi"
[System.IO.File]::WriteAllText($vNsiPath, $vNsi, (New-Object System.Text.UTF8Encoding($true)))

$makensis = $env:UVMP_NSIS_MAKENSIS
& $makensis /DVERSION=0 /DSTAGINGDIR=x /DOUTFILE="$vDir\vsetup.exe" /DICONFILE="$vDir\icon.ico" $vNsiPath 2>&1 | Select-Object -Last 3
if ($LASTEXITCODE -ne 0) { throw "variant makensis failed" }
$pv = Start-Process -FilePath (Join-Path $vDir "vsetup.exe") -ArgumentList "/S", "/D=$vDir\app" -PassThru -Wait
Write-Host "variant install exit: $($pv.ExitCode)"

foreach ($name in @("v-ascii-full", "v-ascii-noicon", "v-ascii-noparams", "v-syswow-target", $dispName)) {
  $lnk = Join-Path $programs "$name.lnk"
  if (-not (Test-Path $lnk)) { Write-Host "  [$name] MISSING"; continue }
  $s = $wsh.CreateShortcut($lnk)
  Write-Host ("  [{0}] size={1} target=[{2}] args=[{3}] icon=[{4}]" -f $name, (Get-Item $lnk).Length, $s.TargetPath, $s.Arguments, $s.IconLocation)
  if ($name -eq "v-ascii-full") {
    Write-Host "  hexdump of v-ascii-full lnk:"
    HexDump $lnk 0x120
  }
}

# ---------- 3. cleanup ----------
$u = Join-Path $instDir "Uninstall-uvmp-toolkit-fyne.exe"
if (Test-Path $u) { Start-Process -FilePath $u -ArgumentList "/S" -PassThru -Wait | Out-Null; Write-Host "real installer uninstalled" }
Remove-Item -Recurse -Force $vDir -ErrorAction SilentlyContinue
Remove-Item -Force (Join-Path $programs "v-ascii-full.lnk"), (Join-Path $programs "v-ascii-noicon.lnk"), (Join-Path $programs "v-ascii-noparams.lnk"), (Join-Path $programs "v-syswow-target.lnk") -ErrorAction SilentlyContinue
Remove-Item -Force (Join-Path $programs "$dispName.lnk") -ErrorAction SilentlyContinue
Write-Host "===== DEBUG DONE ====="
