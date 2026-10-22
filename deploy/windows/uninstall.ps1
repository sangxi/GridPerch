<#
.SYNOPSIS
  卸载 GridPerch 监控端（Windows）。
.DESCRIPTION
  等价于 install.ps1 -Uninstall：停止并删除计划任务与进程，保留数据与配置。
  加 -Purge 会连同数据目录一起删除（不可恢复，请谨慎）。
.EXAMPLE
  .\uninstall.ps1
.EXAMPLE
  .\uninstall.ps1 -Purge
#>
[CmdletBinding()]
param(
  [string]$Prefix  = 'C:\GridPerch',
  [string]$TaskName = 'gridperch',
  [switch]$Purge
)

$ErrorActionPreference = 'Stop'
$App = 'gridperch-server'

$wp = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $wp.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  Write-Host '[x] 需要管理员权限。' -ForegroundColor Red; exit 1
}

Write-Host '==> 停止并移除计划任务' -ForegroundColor Green
if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
  Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
  Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
}
Get-Process $App -ErrorAction SilentlyContinue | Stop-Process -Force

Write-Host '==> 移除防火墙规则' -ForegroundColor Green
Get-NetFirewallRule -DisplayName 'GridPerch' -ErrorAction SilentlyContinue |
  Remove-NetFirewallRule -ErrorAction SilentlyContinue

if ($Purge) {
  $ans = Read-Host "确认删除 $Prefix 下的全部数据（含数据库）？输入 yes 继续"
  if ($ans -eq 'yes') {
    Remove-Item -Recurse -Force $Prefix
    Write-Host '==> 已删除数据目录' -ForegroundColor Green
  } else {
    Write-Host '[!] 已跳过数据删除'
  }
} else {
  Write-Host "[!] 数据目录 $Prefix 与配置 start-server.cmd 已保留（含数据库）。" -ForegroundColor Yellow
  Write-Host "    确认不再需要时：Remove-Item -Recurse -Force $Prefix"
}

Write-Host '==> 卸载完成' -ForegroundColor Green
