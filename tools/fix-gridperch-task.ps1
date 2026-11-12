# GridPerch agent scheduled-task repair
# Target: Windows Server 2012 R2 (PowerShell 4.0+). MUST run as Administrator.
# Rebuilds ONLY the scheduled-task settings. Agent exe, host name and token are preserved.
# Fixes three defects of the pre-2026-11-06 installer (module path):
#   1. ExecutionTimeLimit defaults to PT72H -> Task Scheduler kills the agent every 3 days.
#   2. StartWhenAvailable defaults to false -> task never starts again after a failed boot.
#   3. No RestartOnFailure -> a crashed agent stays offline forever.
# Safe to re-run. A backup of the original task XML is saved before every change.

$ErrorActionPreference = 'Stop'
$taskName = 'gridperch-agent'
$dir      = 'C:\GridPerch'

$wp = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $wp.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  throw 'ERROR: run this in an elevated PowerShell (Run as Administrator).'
}

Write-Host '== GridPerch agent task repair =='

# 1. Backup the current task definition (contains exe path, host name and token hash)
$cur = Get-ScheduledTask -TaskName $taskName -ErrorAction Stop
$oldXml = Export-ScheduledTask -TaskName $taskName
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$backup = Join-Path $dir ('gridperch-task-backup-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '.xml')
$oldXml | Out-File -FilePath $backup -Encoding Unicode
Write-Host ('Backup saved: ' + $backup)

# 2. Re-register with unlimited runtime + start-when-available + restart-on-failure
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit (New-TimeSpan -Seconds 0)
Register-ScheduledTask -TaskName $taskName -Action $cur.Actions -Trigger $cur.Triggers -Principal $cur.Principal -Settings $settings -Description 'GridPerch Agent' -Force | Out-Null
Write-Host 'Task re-registered with unlimited runtime.'

# 3. Remove orphan instances, then start the task
Get-Process -Name 'gridperch-agent' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2
Start-ScheduledTask -TaskName $taskName
Start-Sleep -Seconds 6

# 4. Verify and print an unambiguous result
$xml = Export-ScheduledTask -TaskName $taskName
$limit = '(missing)'
$m = [regex]::Match($xml, '<ExecutionTimeLimit>([^<]*)</ExecutionTimeLimit>')
if ($m.Success) { $limit = $m.Groups[1].Value }
$swa = '(missing)'
$m2 = [regex]::Match($xml, '<StartWhenAvailable>([^<]*)</StartWhenAvailable>')
if ($m2.Success) { $swa = $m2.Groups[1].Value }
$proc = @(Get-Process -Name 'gridperch-agent' -ErrorAction SilentlyContinue)
$info = Get-ScheduledTaskInfo -TaskName $taskName
Write-Host ('ExecutionTimeLimit = ' + $limit)
Write-Host ('StartWhenAvailable = ' + $swa)
Write-Host ('TaskState          = ' + (Get-ScheduledTask -TaskName $taskName).State)
Write-Host ('AgentProcesses     = ' + $proc.Count)
Write-Host ('LastRunTime        = ' + $info.LastRunTime + '  LastResult=' + $info.LastTaskResult)
if ($limit -eq 'PT0S' -and $proc.Count -gt 0) {
  Write-Host 'RESULT: OK - agent is running with unlimited runtime.'
} else {
  Write-Host 'RESULT: CHECK - send the lines above back for review.'
}
