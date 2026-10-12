<#
.SYNOPSIS
  OpsMonitor 监控端（server）Windows 一键部署。

.DESCRIPTION
  把 opsmonitor-server.exe 装成开机自启的 Windows 服务（用系统自带「计划任务」实现，
  不依赖 NSSM 等第三方工具），并放行防火墙端口。

.EXAMPLE
  .\install.ps1
.EXAMPLE
  .\install.ps1 -Token '你的密钥' -AdminPass '你的控制台密码'
.EXAMPLE
  .\install.ps1 -File D:\tmp\opsmonitor-server-windows-amd64.exe -Addr :8000
.EXAMPLE
  .\install.ps1 -Uninstall
#>
[CmdletBinding()]
param(
  [string]$Version   = '1.1.0',
  [string]$Prefix    = 'C:\OpsMonitor',
  [string]$Addr      = ':9999',
  [string]$Token     = '',
  [string]$AdminUser = 'admin',
  [string]$AdminPass = '',
  [string]$PublicUrl = '',
  [int]$Retention    = 30,
  [string]$File      = '',
  [switch]$NoFirewall,
  [switch]$Uninstall
)

$ErrorActionPreference = 'Stop'
$App      = 'opsmonitor-server'
$Exe      = Join-Path $Prefix "$App.exe"
$DataDir  = Join-Path $Prefix 'data'
$StartCmd = Join-Path $Prefix 'start-server.cmd'
$TaskName = 'opsmonitor'

function Say([string]$m)  { Write-Host "==> $m" -ForegroundColor Green }
function Warn([string]$m) { Write-Host "[!] $m" -ForegroundColor Yellow }
function Die([string]$m)  { Write-Host "[x] $m" -ForegroundColor Red; exit 1 }

# ---- 管理员检查 ----
$wp = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $wp.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  Die '需要管理员权限。请右键「Windows PowerShell」→「以管理员身份运行」，再重新执行。'
}

# ---- 卸载 ----
if ($Uninstall) {
  Say '停止并移除计划任务'
  if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
    Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
  }
  Get-Process $App -ErrorAction SilentlyContinue | Stop-Process -Force
  Warn "数据目录 $DataDir 与配置 $StartCmd 已保留（含数据库）。确认不再需要时手工删除。"
  Say '卸载完成'
  exit 0
}

# ---- 取二进制 ----
Say '准备程序文件'
New-Item -ItemType Directory -Force -Path $Prefix, $DataDir | Out-Null

if ($File -ne '') {
  if (-not (Test-Path $File)) { Die "找不到指定的文件：$File" }
  $src = $File
} else {
  $here = Split-Path -Parent $MyInvocation.MyCommand.Path
  $cands = @(
    (Join-Path $here 'opsmonitor-server-windows-amd64.exe'),
    (Join-Path $here '..\opsmonitor-server-windows-amd64.exe'),
    (Join-Path $here '..\..\dist\opsmonitor-server-windows-amd64.exe'),
    (Join-Path $here "..\..\release\$Version\opsmonitor-server-windows-amd64.exe")
  )
  $src = $cands | Where-Object { Test-Path $_ } | Select-Object -First 1
  if (-not $src) {
    Die "没找到 opsmonitor-server-windows-amd64.exe。请把它与脚本放同一目录，或用 -File <路径> 指定。"
  }
  Say "使用本地二进制：$src"
}

# 校验确实是 Windows PE 文件（别误拷了 Linux/macOS 产物）
$bytes = [System.IO.File]::ReadAllBytes($src)[0..1]
if (-not ($bytes[0] -eq 0x4D -and $bytes[1] -eq 0x5A)) {
  Die '该文件不是 Windows 可执行文件（PE），请确认用的是 opsmonitor-server-windows-amd64.exe'
}

# 先停掉旧进程，避免文件占用
Get-Process $App -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Seconds 1
Copy-Item $src $Exe -Force
Say "已安装到 $Exe"

# ---- 生成启动脚本（配置写在这里，不进计划任务定义，避免被 Get-ScheduledTask 明文看到）----
if (-not (Test-Path $StartCmd)) {
  Say "生成配置与启动脚本 $StartCmd"
  if ($Token -eq '') {
    $Token = -join ((1..48) | ForEach-Object { '{0:x}' -f (Get-Random -Max 16) })
  }
  if ($AdminPass -eq '') {
    $AdminPass = -join ((48..57) + (65..90) + (97..122) | Get-Random -Count 12 | ForEach-Object { [char]$_ })
  }
  if ($PublicUrl -eq '') {
    $ip = (Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
           Where-Object { $_.IPAddress -notlike '127.*' -and $_.PrefixOrigin -ne 'WellKnown' } |
           Select-Object -First 1 -ExpandProperty IPAddress)
    if (-not $ip) { $ip = '127.0.0.1' }
    $PublicUrl = "http://${ip}$Addr"
  }

  $dbPath = Join-Path $DataDir 'opsmonitor.db'
  $logDir = Join-Path $Prefix 'logs'
  New-Item -ItemType Directory -Force -Path $logDir | Out-Null
  $logFile = Join-Path $logDir 'server.log'
  $lines = @(
    '@echo off',
    'rem OpsMonitor 监控端启动脚本（由 install.ps1 生成）',
    'rem 计划任务没有控制台，程序输出统一追加到 logs\server.log',
    "cd /d `"$Prefix`"",
    "set `"OPSM_ADDR=$Addr`"",
    "set `"OPSM_DB=$dbPath`"",
    "set `"OPSM_TOKEN=$Token`"",
    "set `"OPSM_ADMIN_USER=$AdminUser`"",
    "set `"OPSM_ADMIN_PASS=$AdminPass`"",
    "set `"OPSM_PUBLIC_URL=$PublicUrl`"",
    "set `"OPSM_RETENTION=$Retention`"",
    "`"$Exe`" >> `"$logFile`" 2>&1"
  )
  # 用 OEM(系统本地代码页) 写 ANSI，cmd 才能正确读取中文注释
  $enc = [System.Text.Encoding]::GetEncoding([System.Globalization.CultureInfo]::CurrentCulture.TextInfo.ANSICodePage)
  [System.IO.File]::WriteAllLines($StartCmd, $lines, $enc)
  $NewCred = $true
} else {
  Say "启动脚本已存在，保留原配置：$StartCmd"
  $NewCred = $false
}

# ---- 计划任务 ----
Say '注册开机自启任务（SYSTEM 权限）'
$action  = New-ScheduledTaskAction -Execute 'cmd.exe' -Argument "/c `"$StartCmd`"" -WorkingDirectory $Prefix
$trigger = New-ScheduledTaskTrigger -AtStartup
$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
  -StartWhenAvailable -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) `
  -ExecutionTimeLimit ([TimeSpan]::Zero)

Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
  -Principal $principal -Settings $settings -Force | Out-Null
Start-ScheduledTask -TaskName $TaskName

# ---- 防火墙 ----
$port = $Addr.TrimStart(':')
if (-not $NoFirewall -and $port) {
  Say "放行防火墙端口 $port/tcp"
  if (-not (Get-NetFirewallRule -DisplayName 'OpsMonitor' -ErrorAction SilentlyContinue)) {
    New-NetFirewallRule -DisplayName 'OpsMonitor' -Direction Inbound -Action Allow `
      -Protocol TCP -LocalPort $port | Out-Null
  } else {
    Set-NetFirewallRule -DisplayName 'OpsMonitor' -Enabled True | Out-Null
  }
}

# ---- 结果 ----
Start-Sleep -Seconds 3
$proc = Get-Process $App -ErrorAction SilentlyContinue
if ($proc) { $procState = "运行中 (PID $($proc.Id))" } else { $procState = '未运行，请查看日志' }
Write-Host ''
Say '部署完成'
Write-Host "  程序      : $Exe（版本 $Version）"
Write-Host "  配置      : $StartCmd"
Write-Host "  数据      : $DataDir\opsmonitor.db"
Write-Host "  监听      : $Addr"
Write-Host "  控制台    : $PublicUrl"
Write-Host "  进程状态  : $procState"
if ($NewCred) {
  Write-Host ''
  Write-Host '  ── 控制台账号（只在这里显示一次，请立刻保存）──'
  Write-Host "     用户名  : $AdminUser"
  Write-Host "     密码    : $AdminPass"
  Write-Host "     通信密钥: $Token   （采集端要用同一值）"
}
Write-Host ''
Write-Host "  查看状态  : Get-ScheduledTask -TaskName $TaskName | Select-Object State"
Write-Host "  重启      : Stop-ScheduledTask -TaskName $TaskName; Start-ScheduledTask -TaskName $TaskName"
Write-Host "  日志      : $Prefix\logs\server.log"
Write-Host "              查看：Get-Content '$Prefix\logs\server.log' -Tail 40 -Wait"
Write-Host "  卸载      : .\install.ps1 -Uninstall"
Write-Host ''
Write-Host '  自检      : 启动日志里应出现「授权验签公钥指纹：278D-4852-7191-E7CE」'
Write-Host '              不是这个值说明二进制不是本版本，请重新获取产物。'
