# GridPerch Plan A - batch reinstall for the 11 Windows Server 2012 R2 hosts
#
# Run this from an ELEVATED Windows PowerShell 5.1 on a management workstation,
# logged in with an account that is local administrator on the target hosts
# (a domain admin account works). No password is stored in this file.
#
#   .\reinstall-all.ps1
#   .\reinstall-all.ps1 -Credential (Get-Credential)   # supply domain admin explicitly
#   .\reinstall-all.ps1 -Only '192.168.40.161'        # single-host trial first
#
# Channel: WMI Win32_Process.Create (TCP 135 + 445, verified open on all 11 targets).
# Each target re-downloads install.ps1 from the platform, re-downloads the legacy
# agent, re-registers gridperch-agent with ExecutionTimeLimit=PT0S, and starts it.
# That removes the default PT72H kill-timer that caused the 3-day outage cycle.
#
# Verified on the server before generating this file:
#   install.ps1 returns charset=utf-8 (Chinese host names stay intact)
#   OSVersion.Major < 10 -> legacy=1 -> "-update off -remote-config off"
#   /agent/download?legacy=1 -> 200, 6368768 bytes, sha256 verified by installer
#
# IMPORTANT
#   * ReturnValue=0 means "remote process launched", NOT "install succeeded".
#     Check the console ~5 minutes later (or ask for a /api/hosts status check).
#   * 192.168.40.168 has outbound :9999 blocked - its reinstall will fail.
#     Use tools/fix-gridperch-task.ps1 locally on that host instead.
#   * 192.168.40.159 is powered off and is excluded.
#   * If WMI is blocked by firewall/EDR, copy the matching line from
#     tools/reinstall-commands.txt and paste it on that host directly.
#   * Per-host install output is left at %TEMP%\gridperch-reinstall.log on the target.

param(
  [System.Management.Automation.PSCredential] $Credential = $null,
  [string] $Only = ''
)

$ErrorActionPreference = 'Continue'
$hash = 'ffb977235c34019e5a27f12639a21126'
$base = 'http://192.168.40.111:9999/install.ps1'

# name= values are URL-encoded; keep them verbatim so the console host name matches.
$targets = @(
  @{ ip='192.168.40.161'; name='BL_ETL' },
  @{ ip='192.168.40.162'; name='BL%E6%95%B0%E6%8D%AE%E5%BA%93' },
  @{ ip='192.168.40.163'; name='ERP%E5%8C%BB%E7%96%97%E5%99%A8%E6%A2%B0%E6%95%B0%E6%8D%AE%E5%BA%931' },
  @{ ip='192.168.40.99';  name='%E5%8D%8E%E6%8B%9B' },
  @{ ip='192.168.40.128'; name='%E5%90%AF%E8%BD%AF%E6%B5%8B%E8%AF%95%E5%BA%93' },
  @{ ip='192.168.40.170'; name='OA%E6%96%B0%E6%8E%A5%E5%8F%A3%E6%9C%8D%E5%8A%A1%E5%99%A8' },
  @{ ip='192.168.40.140'; name='%E5%A4%9A%E4%BB%93OMS%E6%8E%A5%E5%8F%A3' },
  @{ ip='192.168.40.94';  name='%E6%A5%9A%E9%9B%84%E8%99%B9%E5%9F%8E%E6%9C%8D%E5%8A%A1%E5%99%A8' },
  @{ ip='192.168.40.149'; name='%E7%94%B5%E5%AD%90%E7%9B%91%E7%AE%A1%E7%A0%81%E6%9C%8D%E5%8A%A1%E5%99%A8' },
  @{ ip='192.168.40.216'; name='%E6%A5%9A%E9%9B%84%E8%BF%9C%E7%A8%8B%E5%AE%A1%E6%96%B9%E6%9C%8D%E5%8A%A1%E5%99%A8' },
  @{ ip='192.168.40.168'; name='%E6%96%B0%E9%9B%B6%E5%94%AE%E5%BA%94%E7%94%A8%E6%95%B0%E6%8D%AE%E6%9C%8D%E5%8A%A1%E5%99%A8' }
)

Write-Host '== GridPerch Plan A batch reinstall (11 hosts) =='
Write-Host ''

foreach ($t in $targets) {
  if ($Only -and $t.ip -ne $Only) { continue }
  $url   = $base + '?wgToken=' + $hash + '&name=' + $t.name
  $inner = '$ErrorActionPreference=''Stop''; Invoke-Expression (Invoke-WebRequest -UseBasicParsing ''' + $url + ''').Content *> "$env:TEMP\gridperch-reinstall.log"'
  $cmd   = 'powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -Command "' + $inner + '"'

  $status = 'LAUNCHED'
  $detail = ''
  try {
    $wmiArgs = @{ Class='Win32_Process'; Name='Create'; ComputerName=$t.ip; ArgumentList=$cmd; ErrorAction='Stop' }
    if ($Credential) { $wmiArgs['Credential'] = $Credential }
    $r = Invoke-WmiMethod @wmiArgs
    if ($r.ReturnValue -eq 0) {
      $detail = 'pid=' + $r.ProcessId
    } else {
      $status = 'FAILED'
      $detail = 'WMI ReturnValue=' + $r.ReturnValue
    }
  } catch {
    $status = 'FAILED'
    $detail = $_.Exception.Message
  }
  Write-Host ($t.ip.PadRight(16) + $status.PadRight(10) + $detail)
}

Write-Host ''
Write-Host 'Done. Wait ~5 minutes, then verify in the console (or request an /api/hosts check).'
Write-Host 'For any FAILED row, paste that host''s line from tools/reinstall-commands.txt.'
Write-Host 'On 192.168.40.168 use tools/fix-gridperch-task.ps1 instead (outbound :9999 blocked).'
