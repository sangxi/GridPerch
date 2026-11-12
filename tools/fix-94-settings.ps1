# GridPerch .94 - repair the task that Register-ScheduledTask created with
# WRONG defaults (PT72H + InteractiveToken). No XML authoring, no cmdlet
# parameter binding: talk to Task Scheduler COM directly and rewrite the
# existing task definition in place. All-ASCII, every line a complete statement.
#
# Why: schtasks /Create /XML rejected the hand-written minimal XML on 2012 R2
# ("XML format is not correct or value out of range"), and
# Register-ScheduledTask silently ignored -Settings/-Principal. Only the COM
# ITaskDefinition path reliably sets ExecutionTimeLimit=PT0S here.

$ErrorActionPreference='Continue'
$tn='gridperch-agent'
"== BEFORE =="
$before=(& schtasks.exe /Query /TN $tn /XML 2>&1 | Out-String)
[regex]::Matches($before,'<(ExecutionTimeLimit|LogonType|UserId|Command|Arguments)>[^<]*</\1>')|ForEach-Object{$_.Value}
"== COM FIX =="
$ok=$false
try{$svc=New-Object -ComObject Schedule.Service;$svc.Connect();$folder=$svc.GetFolder('\');$def=$folder.GetTask($tn).Definition;$def.Settings.ExecutionTimeLimit='PT0S';$def.Settings.StartWhenAvailable=$true;$def.Settings.RestartCount=999;$def.Settings.RestartInterval='PT1M';$def.Principal.UserId='S-1-5-18';$def.Principal.LogonType=5;$def.Principal.RunLevel=1;foreach($u in @('SYSTEM','S-1-5-18',$null)){if(-not $ok){try{$folder.RegisterTaskDefinition($tn,$def,6,$u,$null,5)|Out-Null;"COM=OK userId=$u";$ok=$true}catch{"COM_TRY_FAIL userId=$u $($_.Exception.Message)"}}}}catch{"COM=FAIL $($_.Exception.Message)"}
if(-not $ok){$fix=Join-Path $env:TEMP 'gp-fixed.xml';$x=(& schtasks.exe /Query /TN $tn /XML | Out-String);$x2=[regex]::Replace($x,'<ExecutionTimeLimit>[^<]*</ExecutionTimeLimit>','<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>');$x2=[regex]::Replace($x2,'<\?xml[^>]*\?>','');$x2='<?xml version="1.0" encoding="UTF-16"?>'+$x2.TrimStart();[IO.File]::WriteAllText($fix,$x2,[Text.Encoding]::Unicode);& schtasks.exe /Create /TN $tn /XML $fix /F 2>&1;"XML_FALLBACK_EXIT=$LASTEXITCODE"}
"== VERIFY =="
$after=(& schtasks.exe /Query /TN $tn /XML 2>&1 | Out-String)
[regex]::Matches($after,'<(ExecutionTimeLimit|LogonType|UserId|Command|Arguments)>[^<]*</\1>')|ForEach-Object{$_.Value}
"AGENT_PROCS=$(@(Get-Process gridperch-agent -EA SilentlyContinue).Count)"
">> PT0S + S-1-5-18 means done. Do NOT run schtasks /Run."
