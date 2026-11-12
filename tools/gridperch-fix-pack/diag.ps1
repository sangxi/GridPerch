# GridPerch offline pack - connectivity diagnostic.
# All-ASCII, PowerShell 2.0 compatible (no Test-NetConnection / Invoke-WebRequest / Get-CimInstance).
# Run on the machine that still shows offline after the pack succeeded.
$ErrorActionPreference='Continue'
# Repair PATH so netsh.exe / route.exe / schtasks.exe resolve on hosts where
# System32 was dropped from PATH (the reason powershell.exe was not found).
$sysRoot=$env:SystemRoot
if(-not $sysRoot){$sysRoot='C:\Windows'}
$env:PATH="$sysRoot\System32;$sysRoot\System32\WindowsPowerShell\v1.0;$env:PATH"
$srv='192.168.40.111'
"== AGENT =="
$procs=@(Get-Process gridperch-agent -EA SilentlyContinue)
"AGENT_PROCS=$($procs.Count)"
Get-WmiObject Win32_Process -Filter "Name='gridperch-agent.exe'" -EA SilentlyContinue | ForEach-Object{"  PID=$($_.ProcessId) CMD=$($_.CommandLine)"}
"== AGENT LOG (last 15) =="
$lp='C:\GridPerch\logs\agent.log'
if(Test-Path $lp){Get-Content $lp | Select-Object -Last 15 | ForEach-Object{'  '+$_}}else{"  NO LOG FILE: $lp"}
"== TCP TO SERVER =="
foreach($p in @(22,80,443,9999)){$c=New-Object Net.Sockets.TcpClient;try{$a=$c.BeginConnect($srv,$p,$null,$null);$ok=$a.AsyncWaitHandle.WaitOne(1500,$false);"  PORT $p = $($ok -and $c.Connected)"}catch{"  PORT $p = FAIL $($_.Exception.Message)"}finally{$c.Close()}}
"== FIREWALL PROFILES =="
& netsh.exe advfirewall show allprofiles state
"== OUTBOUND BLOCK / GridPerch RULES =="
& netsh.exe advfirewall firewall show rule name=all dir=out | Select-String -Pattern 'Block|Blockieren|Bloquer|Rule Name|GridPerch|9999' | ForEach-Object{'  '+$_.Line.Trim()}
"== AV / EDR =="
Get-WmiObject -Namespace 'root\SecurityCenter2' -Class AntiVirusProduct -EA SilentlyContinue | ForEach-Object{'  '+$_.displayName}
"== ROUTE =="
& route.exe print $srv
"== TASK =="
& schtasks.exe /Query /TN gridperch-agent /XML 2>&1 | Select-String -Pattern 'ExecutionTimeLimit|UserId' | ForEach-Object{'  '+$_.Line.Trim()}
"== DONE =="
