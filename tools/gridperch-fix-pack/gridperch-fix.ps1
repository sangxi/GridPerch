param([switch]$FixOnly)
# GridPerch offline repair pack - Windows Server 2008 R2 / 2012 R2.
# All-ASCII source: Chinese host names live in hosts.txt (UTF-8 BOM).
# PowerShell 2.0 compatible. Two modes:
#   install.bat  -> FULL: replace exe, rebuild the task via COM, start agent
#   fix-task.bat -> -FixOnly: only rewrite the existing task settings/principal
# Why COM: on 2012 R2, Register-ScheduledTask silently ignores -Settings and
# -Principal (task ends up PT72H + InteractiveToken), and schtasks /Create /XML
# rejects hand-written XML. ITaskDefinition via COM is the only reliable path.

$ErrorActionPreference='Continue'
# Some hosts have a broken PATH with no System32 - that is exactly why the
# .bat fails with "'powershell.exe' is not recognized". Repair PATH for this
# process so schtasks.exe / netsh.exe / route.exe resolve as well.
$sysRoot=$env:SystemRoot
if(-not $sysRoot){$sysRoot='C:\Windows'}
$env:PATH="$sysRoot\System32;$sysRoot\System32\WindowsPowerShell\v1.0;$env:PATH"
$root=Split-Path -Parent $MyInvocation.MyCommand.Path
$tn='gridperch-agent'
$dir='C:\GridPerch'
$exe=Join-Path $dir 'gridperch-agent.exe'
$log=Join-Path $dir 'logs\agent.log'
$srv='http://192.168.40.111:9999'
$th='ffb977235c34019e5a27f12639a21126'

$wp=New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if(-not $wp.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)){Write-Host 'FATAL: run as Administrator (right click -> Run as administrator)';exit 1}

# ---- local IPv4 addresses ----
$ips=@()
Get-WmiObject Win32_NetworkAdapterConfiguration -EA SilentlyContinue | Where-Object{$_.IPEnabled} | ForEach-Object{
  foreach($a in @($_.IPAddress)){if($a -match '^\d+\.\d+\.\d+\.\d+$'){$ips+=$a}}
}

# ---- host name: hosts.txt first (IP,Name), then the running agent's own command line ----
$name=''
$hf=Join-Path $root 'hosts.txt'
if(Test-Path $hf){
  foreach($line in [IO.File]::ReadAllLines($hf,[Text.Encoding]::UTF8)){
    if($line -match '^\s*#' -or $line.Trim() -eq ''){continue}
    $parts=$line.Split(',',2)
    if($parts.Length -eq 2 -and ($ips -contains $parts[0].Trim())){$name=$parts[1].Trim();break}
  }
}
$agentArgs=''
$p=Get-WmiObject Win32_Process -Filter "Name='gridperch-agent.exe'" -EA SilentlyContinue | Select-Object -First 1
if($p -and $p.CommandLine){
  $agentArgs=[regex]::Replace($p.CommandLine,'^\s*"?.*?gridperch-agent\.exe"?\s*','')
  if($name -eq ''){
    $m=[regex]::Match($agentArgs,'-name\s+"([^"]+)"')
    if($m.Success){$name=$m.Groups[1].Value}
  }
}
if($name -eq ''){$name=$env:COMPUTERNAME;Write-Host "WARN: IP not listed in hosts.txt; using COMPUTERNAME=$name"}
if($agentArgs -eq ''){$agentArgs="-server $srv -token-hash $th -name `"$name`" -log `"$log`" -update off -remote-config off"}
$mode='FULL'; if($FixOnly){$mode='FIX-ONLY'}
"MODE=$mode"
"NAME=$name"
"IPS=$($ips -join ',')"
"ARGS=$agentArgs"

if(-not $FixOnly){
  "== STOP OLD PROCESS =="
  Get-Process -EA SilentlyContinue | Where-Object{$_.ProcessName -match 'gridperch|strix|opsmonitor'} | Stop-Process -Force -EA SilentlyContinue
  "== DELETE OLD TASK =="
  if(Get-Command Get-ScheduledTask -EA SilentlyContinue){
    Get-ScheduledTask -EA SilentlyContinue | Where-Object{$_.TaskName -match 'gridperch|strix|opsmonitor'} | Unregister-ScheduledTask -Confirm:$false -EA SilentlyContinue
  }
  & schtasks.exe /Delete /TN $tn /F 2>&1 | Out-Null
  $td='C:\Windows\System32\Tasks'
  if(Test-Path $td){
    Get-ChildItem $td -EA SilentlyContinue | Where-Object{-not $_.PSIsContainer -and $_.Name -match 'gridperch|strix|opsmonitor'} | ForEach-Object{Remove-Item $_.FullName -Force -EA SilentlyContinue;"  removed $($_.Name)"}
  }
  "== INSTALL EXE =="
  if(-not (Test-Path $dir)){New-Item -ItemType Directory -Path $dir -Force | Out-Null}
  if(-not (Test-Path (Split-Path -Parent $log))){New-Item -ItemType Directory -Path (Split-Path -Parent $log) -Force | Out-Null}
  $src=Join-Path $root 'agent.exe'
  if(Test-Path $src){Copy-Item $src $exe -Force;"  copied agent.exe -> $exe"}else{"  WARN: agent.exe missing in pack, keeping existing exe"}
}

"== REGISTER VIA COM =="
$ok=$false
$svc=$null
try{
  $svc=New-Object -ComObject Schedule.Service
  $svc.Connect()
  $folder=$svc.GetFolder('\')
  if($FixOnly){
    $def=$folder.GetTask($tn).Definition
  }else{
    $def=$svc.NewTask(0)
    $def.RegistrationInfo.Description='GridPerch Agent'
    $tr=$def.Triggers.Create(8)
    $tr.Enabled=$true
    $act=$def.Actions.Create(0)
    $act.Path=$exe
    $act.Arguments=$agentArgs
    $act.WorkingDirectory=$dir
  }
  $def.Settings.ExecutionTimeLimit='PT0S'
  $def.Settings.StartWhenAvailable=$true
  $def.Settings.RestartCount=999
  $def.Settings.RestartInterval='PT1M'
  $def.Settings.MultipleInstances=2
  $def.Principal.UserId='S-1-5-18'
  $def.Principal.LogonType=5
  $def.Principal.RunLevel=1
  foreach($u in @('SYSTEM','S-1-5-18',$null)){
    if(-not $ok){
      try{$folder.RegisterTaskDefinition($tn,$def,6,$u,$null,5)|Out-Null;"  COM=OK userId=$u";$ok=$true}
      catch{"  COM_TRY_FAIL userId=$u : $($_.Exception.Message)"}
    }
  }
}catch{"  COM=FAIL $($_.Exception.Message)"}

if(-not $ok){
  "== FALLBACK: Task XML via schtasks =="
  # Same XML as the online install.ps1 (validated on 2008 R2): NO LogonType
  # element, PT0S, StartWhenAvailable, RestartOnFailure.
  $xe=[Security.SecurityElement]::Escape($exe)
  $xa=[Security.SecurityElement]::Escape($agentArgs)
  $xd=[Security.SecurityElement]::Escape($dir)
  $x='<?xml version="1.0" encoding="UTF-16"?><Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><RegistrationInfo><Description>GridPerch Agent</Description></RegistrationInfo><Triggers><BootTrigger><Enabled>true</Enabled></BootTrigger></Triggers><Principals><Principal id="Author"><UserId>S-1-5-18</UserId><RunLevel>HighestAvailable</RunLevel></Principal></Principals><Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><AllowHardTerminate>true</AllowHardTerminate><StartWhenAvailable>true</StartWhenAvailable><RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable><IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings><AllowStartOnDemand>true</AllowStartOnDemand><Enabled>true</Enabled><Hidden>false</Hidden><RunOnlyIfIdle>false</RunOnlyIfIdle><WakeToRun>false</WakeToRun><ExecutionTimeLimit>PT0S</ExecutionTimeLimit><Priority>7</Priority><RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure></Settings><Actions Context="Author"><Exec><Command>__EXE__</Command><Arguments>__ARGS__</Arguments><WorkingDirectory>__DIR__</WorkingDirectory></Exec></Actions></Task>'
  $x=$x.Replace('__EXE__',$xe).Replace('__ARGS__',$xa).Replace('__DIR__',$xd)
  $xp=Join-Path $env:TEMP 'gp-pack-task.xml'
  [IO.File]::WriteAllText($xp,$x,[Text.Encoding]::Unicode)
  & schtasks.exe /Create /TN $tn /XML $xp /F 2>&1
  "  XML_EXIT=$LASTEXITCODE"
}

if(-not $FixOnly){
  "== START =="
  Start-Sleep -Seconds 1
  if(@(Get-Process gridperch-agent -EA SilentlyContinue).Count -eq 0){
    try{$svc.GetFolder('\').GetTask($tn).Run($null)|Out-Null;"  TASK_RUN=OK"}
    catch{Start-Process -FilePath $exe -ArgumentList $agentArgs -WindowStyle Hidden;"  FALLBACK_STARTPROCESS=OK"}
  }else{"  already running"}
}

# Self-heal: if the COM "NewTask" path did not persist the settings on this
# particular build, fall back to the path proven on .94 - take the EXISTING
# definition, rewrite its properties, and register it again.
$chk=(& schtasks.exe /Query /TN $tn /XML 2>&1 | Out-String)
if($chk -notmatch 'PT0S'){
  "== SELF-HEAL: rewrite existing definition =="
  try{
    $svc2=New-Object -ComObject Schedule.Service
    $svc2.Connect()
    $f2=$svc2.GetFolder('\')
    $d2=$f2.GetTask($tn).Definition
    $d2.Settings.ExecutionTimeLimit='PT0S'
    $d2.Settings.StartWhenAvailable=$true
    $d2.Settings.RestartCount=999
    $d2.Settings.RestartInterval='PT1M'
    $d2.Principal.UserId='S-1-5-18'
    $d2.Principal.LogonType=5
    $d2.Principal.RunLevel=1
    $f2.RegisterTaskDefinition($tn,$d2,6,'SYSTEM',$null,5)|Out-Null
    "  HEAL=OK"
  }catch{"  HEAL=FAIL $($_.Exception.Message)"}
}

"== VERIFY =="
$out=(& schtasks.exe /Query /TN $tn /XML 2>&1 | Out-String)
[regex]::Matches($out,'<(ExecutionTimeLimit|UserId|LogonType|Command|Arguments)>[^<]*</\1>') | ForEach-Object{$_.Value}
"AGENT_PROCS=$(@(Get-Process gridperch-agent -EA SilentlyContinue).Count)"
if($out -match 'PT0S'){"RESULT=OK: unlimited runtime, no 72h kill"}else{"RESULT=FAIL: still not PT0S, send this output back"}
"== DONE =="
