# ============================================================================
#  GridPerch 采集端 —— Windows 旧系统兼容版 离线安装脚本
#
#  适用系统（本包内置的二进制是 Go 1.20 构建的「旧系统兼容版」）：
#    Windows Server 2008 R2 / 2012 / 2012 R2 / 2016
#    Windows 7 SP1 / 8 / 8.1 / 10 / 11（这些系统也能跑，只是不需要兼容版）
#
#  ⚠️ 为什么必须用兼容版
#    Go 1.21 起编译出的程序要求 Windows 10 / Server 2016 以上。在 Server 2008 R2 /
#    2012 / 2012 R2 上运行时，runtime 初始化阶段就崩（0xc0000005），现象是「窗口一
#    闪就没」——没有日志、没有报错框。原因是 Go 1.21.5 把随机数源换成了
#    bcryptprimitives.dll!ProcessPrng，而该 DLL 只有 Windows 8 / Server 2012 起才有。
#    所以旧系统只能用 Go 1.20 构建的这份兼容版，不是补丁能解决的。
#
#  ⚠️ 本脚本必须兼容 PowerShell 2.0
#    Server 2008 R2 自带的是 PowerShell 2.0，没有 $PSScriptRoot、没有 Get-FileHash、
#    没有 New-ScheduledTask* 系列 cmdlet、没有 Invoke-WebRequest。下面凡是依赖这些
#    的地方都做了回落，改动时请保持这一点。
#
#  用法：
#    1) 直接双击 install.bat —— 本包已预置平台地址与通信密钥，全程无需输入。
#    2) 换平台/换口令后再打包，或临时覆盖：在「管理员」命令提示符里执行
#         install.bat -Server http://10.0.0.5:9999 -Token 平台口令
#    3) 直接调：  powershell -ExecutionPolicy Bypass -File install.ps1 -Server ... -Token ...
# ============================================================================
param(
  [string]$Server     = '',
  [string]$Token      = '',
  [string]$TokenHash  = '',
  [string]$Name       = '',
  [string]$InstallDir = 'C:\GridPerch'
)

# ---------------------------------------------------------------------------
# 预置配置 —— 换监控平台或换通信口令，只改下面两行，然后重新打包即可
# ---------------------------------------------------------------------------
# 平台地址：就是浏览器打开控制台用的地址，带 http:// 和端口
$BuiltinServer = 'http://192.168.40.111:9999'

# 通信密钥：平台「添加主机」页安装命令里 wgToken 的值，32 位十六进制（口令的 MD5）
$BuiltinTokenHash = 'ffb977235c34019e5a27f12639a21126'

# 参数优先于预置值：命令行显式传了 -Server / -Token / -TokenHash 就用传进来的
if (-not $Server) { $Server = $BuiltinServer }
if (-not $TokenHash -and -not $Token) { $TokenHash = $BuiltinTokenHash }

$ErrorActionPreference = 'Stop'
$TaskName = 'gridperch-agent'

# ---------------------------------------------------------------------------
# 小工具
# ---------------------------------------------------------------------------
function Write-Step([string]$text) {
  Write-Host ''
  Write-Host "==> $text" -ForegroundColor Cyan
}

function Write-Ok([string]$text) {
  Write-Host "    $text" -ForegroundColor Green
}

function Write-Warn([string]$text) {
  Write-Host "    $text" -ForegroundColor Yellow
}

function Write-Fail([string]$text) {
  Write-Host "    $text" -ForegroundColor Red
}

# MD5 十六进制小写。必须与 Go 端 config.MD5Token 完全一致：
#   fmt.Sprintf("%x", md5.Sum([]byte(token)))
# 即「UTF-8 字节 + 小写 hex」，少一位或多位都会导致平台判定通信密钥不匹配。
function Get-Md5Hex([string]$text) {
  $md5   = [System.Security.Cryptography.MD5]::Create()
  $bytes = [System.Text.Encoding]::UTF8.GetBytes($text)
  $hash  = $md5.ComputeHash($bytes)
  return (($hash | ForEach-Object { $_.ToString('x2') }) -join '')
}

# PowerShell 2.0 没有 Get-FileHash，用 .NET 自己算
function Get-Sha256Hex([string]$path) {
  $sha = New-Object System.Security.Cryptography.SHA256Managed
  $fs  = [System.IO.File]::OpenRead($path)
  try {
    $hash = $sha.ComputeHash($fs)
  } finally {
    $fs.Close()
  }
  return (($hash | ForEach-Object { $_.ToString('x2') }) -join '')
}

# 纯 TCP 探测平台端口。不用 Test-NetConnection（PS 4.0+ 才有），也不用 ICMP
# （很多内网把 ping 挡了，但 9999 是通的，用 ping 判断会误报）。
function Test-TcpPort([string]$url) {
  try {
    $uri   = New-Object System.Uri($url)
    $host_ = $uri.Host
    $port  = $uri.Port
    if ($port -le 0) { $port = 80 }
    $client = New-Object System.Net.Sockets.TcpClient
    $iar    = $client.BeginConnect($host_, $port, $null, $null)
    $done   = $iar.AsyncWaitHandle.WaitOne(4000, $false)
    $client.Close()
    return $done
  } catch {
    return $false
  }
}

# ---------------------------------------------------------------------------
# 0. 环境自检
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host '============================================================' -ForegroundColor White
Write-Host '   GridPerch 采集端安装程序（Windows 旧系统兼容版）' -ForegroundColor White
Write-Host '============================================================' -ForegroundColor White

Write-Step '检查运行权限'

# 注册计划任务、写 C:\GridPerch 都要求管理员。提前拦住，别让后面抛晦涩的 HRESULT。
$wp = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $wp.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  Write-Fail '当前不是管理员权限，安装无法继续。'
  Write-Host ''
  Write-Host '  请改用「右键 install.bat → 以管理员身份运行」。' -ForegroundColor Yellow
  Write-Host '  或右键「Windows PowerShell」→「以管理员身份运行」后执行本脚本。' -ForegroundColor Yellow
  throw '安装中止：权限不足。'
}
Write-Ok '管理员权限：OK'

Write-Step '检查系统版本'

# OSVersion 在没装 manifest 的 PowerShell 里可能报告为 6.2（Win8.1/2012 R2 的兼容
# 模式上限）。这里只用来判断「属于哪个年代」，够用，不要去精确比对 build 号。
$osVer = [Environment]::OSVersion.Version
$osName = "$($osVer.Major).$($osVer.Minor).$($osVer.Build)"
Write-Ok "Windows 版本 $osName"

if ($osVer.Major -lt 6) {
  Write-Fail "不支持的 Windows 版本（$osName）。本包最低要求 Windows Server 2008 R2 / Windows 7 SP1。"
  throw '安装中止：系统版本过低。'
}
if ($osVer.Major -eq 6) {
  Write-Ok '属于旧版 Windows（2008 R2 / 2012 / 2012 R2 / Win7 / Win8.1），本兼容包正合适。'
} else {
  Write-Warn "本机是 Windows 10 / Server 2016 及以上，其实用正式版采集端即可；"
  Write-Warn '兼容版同样能正常运行，装上不影响使用。'
}

# ---------------------------------------------------------------------------
# 1. 定位安装源
# ---------------------------------------------------------------------------
Write-Step '检查安装包完整性'

# ⚠️ PowerShell 2.0 没有 $PSScriptRoot，只能用 $MyInvocation 反推脚本所在目录
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
$srcExe    = Join-Path $scriptDir 'gridperch-agent.exe'

if (-not (Test-Path $srcExe)) {
  Write-Fail "安装包里找不到 gridperch-agent.exe：$srcExe"
  Write-Fail '请确认解压后 install.ps1 与 gridperch-agent.exe 在同一目录，不要只拷 bat/ps1。'
  throw '安装中止：安装包不完整。'
}

$srcSize = (Get-Item $srcExe).Length
if ($srcSize -lt 1000000) {
  Write-Fail "采集端程序只有 $srcSize 字节，明显不完整（正常约 6 MB），可能拷贝时被截断。"
  throw '安装中止：二进制文件损坏。'
}
Write-Ok "采集端程序 $([math]::Round($srcSize / 1MB, 2)) MB"

try {
  $srcVer = (& $srcExe -version 2>&1 | Select-Object -First 1)
  # 采集端打的是 UTF-8 中文，而旧系统控制台代码页是 936(GBK)，直接回显会变成
  # 「鏃х郴缁熷吋瀹圭増」这种乱码。这里只把版本号数字抠出来显示，其余丢掉。
  if ("$srcVer" -match '(\d+\.\d+\.\d+)') {
    Write-Ok "版本：$($matches[1])"
  } else {
    Write-Warn '未读到版本号，继续安装。'
  }
} catch {
  Write-Warn '无法读取版本号，继续安装。'
}

try {
  $sha = Get-Sha256Hex $srcExe
  Write-Ok "SHA256：$sha"
  Write-Host '    （可与包内 SHA256SUMS.txt 对照，确认文件未在拷贝过程中损坏）' -ForegroundColor DarkGray
} catch {
  Write-Warn '计算校验和失败，跳过。'
}

# ---------------------------------------------------------------------------
# 2. 收集连接参数
# ---------------------------------------------------------------------------
Write-Step '监控平台连接'

# 正常情况下这里什么都不问：地址和密钥在脚本顶部「预置配置」里已经写死了。
# 只有当有人把预置值清空、或者想临时指到别的平台时，才会走到下面的询问分支。
if (-not $Server) {
  Write-Host '  本包没有内置平台地址，请手动输入，形如 http://192.168.1.10:9999' -ForegroundColor Gray
  $Server = Read-Host '  平台地址'
}
$Server = $Server.Trim()
if ($Server -eq '') {
  throw '安装中止：平台地址不能为空。'
}
# 用户常只输 IP，这里补协议头；末尾斜杠去掉，否则拼出来的 URL 会出现 //api
if ($Server -notmatch '^[a-zA-Z]+://') { $Server = 'http://' + $Server }
$Server = $Server.TrimEnd('/')
Write-Ok "平台地址 $Server（内置）"

if (-not $TokenHash -and -not $Token) {
  Write-Host '  本包没有内置通信密钥，请手动输入 wgToken（32 位十六进制）。' -ForegroundColor Gray
  $Token = Read-Host '  通信密钥'
}
$Token = "$Token".Trim()
$TokenHash = "$TokenHash".Trim()

if ($TokenHash -ne '') {
  if ($TokenHash.Length -ne 32 -or $TokenHash -notmatch '^[0-9a-fA-F]{32}$') {
    Write-Fail "通信密钥不是 32 位十六进制：$TokenHash"
    throw '安装中止：通信密钥格式不对。'
  }
  $TokenHash = $TokenHash.ToLower()
  Write-Ok "通信密钥 MD5 $TokenHash（内置）"
} else {
  if ($Token -eq '') { throw '安装中止：通信密钥不能为空。' }
  $TokenHash = Get-Md5Hex $Token
  Write-Ok "通信密钥 MD5 $TokenHash（由口令算出）"
}

# ---------------------------------------------------------------------------
# 3. 探测平台连通性
# ---------------------------------------------------------------------------
Write-Step '探测与平台的连通性'

if (Test-TcpPort $Server) {
  Write-Ok "可以连通 $Server"
} else {
  Write-Warn "TCP 连不上 $Server，但仍继续安装。"
  Write-Host '    首次部署建议按下面几条依次排查，否则装完控制台看不到主机：' -ForegroundColor Gray
  Write-Host '      1) 平台服务端是否已启动，监听端口是否为 9999；' -ForegroundColor Gray
  Write-Host '      2) 本机到平台的 9999/TCP 是否被防火墙拦截；' -ForegroundColor Gray
  Write-Host '      3) 地址是否填成了平台本机的 127.0.0.1，或云主机的内网 IP（10.x / 192.168.x），' -ForegroundColor Gray
  Write-Host '         这类地址别的机器连不上；' -ForegroundColor Gray
  Write-Host '      4) 平台若在反向代理后面，需开启 WebSocket 与长连接透传。' -ForegroundColor Gray
}

# ---------------------------------------------------------------------------
# 4. 停止已在运行的旧实例
# ---------------------------------------------------------------------------
Write-Step '准备安装目录'

# 正在运行的进程会锁住 exe，不先停掉，后面的 Copy-Item 必然报「文件正被另一进程使用」
$running = Get-Process 'gridperch-agent' -ErrorAction SilentlyContinue
if ($running) {
  Write-Ok "发现已在运行的采集端（PID $($running.Id -join ', ')），先停止"
  $running | Stop-Process -Force
  Start-Sleep -Milliseconds 800
}

if (-not (Test-Path $InstallDir)) {
  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
  Write-Ok "已创建 $InstallDir"
} else {
  Write-Ok "安装目录 $InstallDir 已存在"
}

$exe = Join-Path $InstallDir 'gridperch-agent.exe'

# ---------------------------------------------------------------------------
# 5. 部署二进制
# ---------------------------------------------------------------------------
Write-Step '部署采集端程序'

Copy-Item -Path $srcExe -Destination $exe -Force
Write-Ok "已安装到 $exe"

# ---------------------------------------------------------------------------
# 6. 注册开机自启
# ---------------------------------------------------------------------------
Write-Step '注册开机自启任务'

# 只带「定位平台 + 标识本机」的最小参数，其余走默认值。
# 注意：$args 是 PowerShell 自动变量，这里必须用别的名字，否则会覆盖脚本自身的参数集。
$agentArgs = "-server $Server -token-hash $TokenHash"
if ($Name -ne '') {
  # 主机名会原样进计划任务的命令行，带空格必须加引号；同时挡掉能破坏命令行的字符。
  # 中文主机名是支持的，不要做「转成拼音」这类清洗。
  $Name = $Name.Trim()
  if ($Name -match '["`;&|<>%]') {
    Write-Fail "主机名里不能有引号或 & | < > % 这类字符：$Name"
    throw '安装中止：主机名不合法。'
  }
  $agentArgs = $agentArgs + ' -name "' + $Name + '"'
}

# 🔴 两条必须写在参数里的开关，删掉会出事故：
#   -update off        关闭采集端自动升级。平台的「采集端配置」里 updateMode 是
#                      **全局**默认 auto，一旦放开，这台旧系统会在 30 分钟内被换成
#                      Go 1.23 构建的正式版，换完立刻起不来（闪退，无日志）。
#   -remote-config off 不接受平台下发的运行配置，只用这里的本地参数。
#                      两个开关同时用，才能保证「装的时候能跑，之后一直能跑」。
#                      若确实需要在平台上统一调整采集间隔，把 -remote-config off 改成
#                      auto，但 -update off 必须保留。
$agentArgs = $agentArgs + ' -update off -remote-config off'

$registered = $false

# 优先走 ScheduledTasks 模块（Windows 8 / Server 2012 起才有）。两个坑，改之前先看：
#   ① Win8.1 / Server 2012 R2（6.3）上，SYSTEM 主体**不带** -LogonType 时注册会直接报
#      「参数错误 0x80070057」—— 前面全都成功，只卡这最后一步。必须显式写
#      -LogonType ServiceAccount，缺了它这个主体在旧系统上被判定为非法。
#   ② Win7 / Server 2008 R2 只有 PowerShell 2.0，连 New-ScheduledTaskAction 都没有，
#      只能回落 schtasks.exe。所以每条模块命令都要单独判断，不能只判断一次。
if (Get-Command Register-ScheduledTask -ErrorAction SilentlyContinue) {
  try {
    $action    = New-ScheduledTaskAction -Execute $exe -Argument $agentArgs -WorkingDirectory $InstallDir
    $trigger   = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    # ExecutionTimeLimit 置 0 表示不限时：计划任务默认跑满 72 小时会被强杀，常驻进程必须放开
    $settings  = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit (New-TimeSpan -Seconds 0)
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Description 'GridPerch 采集端' -Force | Out-Null
    $registered = $true
    Write-Ok '已通过计划任务 API 注册（SYSTEM / 开机启动 / 不限运行时长）'
  } catch {
    Write-Warn "计划任务 API 注册失败（$($_.Exception.Message)），改用 schtasks.exe 重试"
  }
}

if (-not $registered) {
  # 🔴 旧系统必须走 Task XML，不能用 schtasks /Create /TR。
  #
  # 现场实测（Server 2008 R2 6.1.7601）：/TR 的写法会失败，报
  #    错误: 无效参数/选项 - '-server'。
  #    注册开机自启失败（schtasks 退出码 -2147467259）
  # 原因：/TR 的值是「exe 路径 + 一串参数」，既含空格又需要内部引号。PowerShell
  # 把它作为单个参数传给原生 exe 时会先加一层转义，schtasks 再按自己的规则解析
  # 一次，结果 /TR 只吃到第一个引号为止，后面的 -server ... 全被当成 schtasks
  # 自己的开关，于是报「无效参数 - '-server'」。
  #
  # 改用 XML：Command / Arguments / WorkingDirectory 是三个独立元素，完全不走
  # 命令行引号规则，路径含空格、参数含特殊字符都不会出问题。
  $xmlPath = Join-Path $env:TEMP 'gridperch-agent-task.xml'
  $xmlExe  = [System.Security.SecurityElement]::Escape($exe)
  $xmlArgs = [System.Security.SecurityElement]::Escape($agentArgs)
  $xmlDir  = [System.Security.SecurityElement]::Escape($InstallDir)

  # ⚠️ here-string 的结束符 "@ 必须顶在第 1 列，前面不能有空格，否则语法报错
  $taskXml = @"
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>GridPerch 采集端</Description>
  </RegistrationInfo>
  <Triggers>
    <BootTrigger>
      <Enabled>true</Enabled>
    </BootTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>S-1-5-18</UserId>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>3</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>$xmlExe</Command>
      <Arguments>$xmlArgs</Arguments>
      <WorkingDirectory>$xmlDir</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
"@

  # schtasks /XML 要求文件是 Unicode（UTF-16 LE 带 BOM），用 -Encoding Unicode 生成
  $taskXml | Out-File -FilePath $xmlPath -Encoding Unicode -Force

  $out = & schtasks.exe /Create /TN $TaskName /XML $xmlPath /F 2>&1
  if ($LASTEXITCODE -ne 0) {
    Write-Fail "注册开机自启失败（schtasks 退出码 $LASTEXITCODE）。"
    Write-Host ('    schtasks 说：' + ($out -join ' ')) -ForegroundColor Gray
    Write-Host "  任务定义已生成在：$xmlPath" -ForegroundColor Gray
    Write-Host ('  可手工注册：schtasks /Create /TN ' + $TaskName + ' /XML "' + $xmlPath + '" /F') -ForegroundColor Gray
    throw '安装中止：无法注册开机自启。'
  }
  Write-Ok '已通过任务 XML 注册（SYSTEM / 开机启动 / 不限运行时长）'
}

# ---------------------------------------------------------------------------
# 7. 立即启动并验证
# ---------------------------------------------------------------------------
Write-Step '启动采集端'

# Start-ScheduledTask / Get-ScheduledTask 同样是模块命令，旧系统要用 schtasks
if (Get-Command Start-ScheduledTask -ErrorAction SilentlyContinue) {
  Start-ScheduledTask -TaskName $TaskName
} else {
  & schtasks.exe /Run /TN $TaskName | Out-Null
}

Start-Sleep -Seconds 3

$proc = Get-Process 'gridperch-agent' -ErrorAction SilentlyContinue
if ($proc) {
  Write-Ok "采集端已启动（PID $($proc.Id -join ', ')）"
} else {
  Write-Warn '没有检测到采集端进程，可能刚启动就退出了。'
  Write-Host '    请在本机执行下面命令看它到底报什么错：' -ForegroundColor Gray
  Write-Host ('      cd ' + $InstallDir) -ForegroundColor Gray
  Write-Host ('      gridperch-agent.exe -server ' + $Server + ' -token-hash ' + $TokenHash) -ForegroundColor Gray
  Write-Host '    若这一步也一闪而过，说明二进制与系统不匹配（例如把 64 位版装到了 32 位系统）。' -ForegroundColor Gray
  Write-Host ('    本机处理器架构：' + $env:PROCESSOR_ARCHITECTURE) -ForegroundColor Gray
}

if (Get-Command Get-ScheduledTask -ErrorAction SilentlyContinue) {
  $taskState = (Get-ScheduledTask -TaskName $TaskName).State
  Write-Ok "计划任务状态：$taskState"
} else {
  Write-Host '    计划任务状态：已注册（旧系统不支持查询，可用 schtasks /Query /TN ' + $TaskName + ' 查看）' -ForegroundColor Gray
}

# ---------------------------------------------------------------------------
# 8. 完成
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host '============================================================' -ForegroundColor White
Write-Host '   安装完成' -ForegroundColor Green
Write-Host '============================================================' -ForegroundColor White
Write-Host ''
Write-Host "  平台地址：$Server"
Write-Host "  安装目录：$InstallDir"
Write-Host "  启动参数：$agentArgs"
Write-Host ''
Write-Host '  请到控制台「主机监控」确认本机是否上线（通常 10 秒内出现）。' -ForegroundColor Yellow
Write-Host '  主机名默认取本机计算机名，改名请在平台里做（会自动级联历史数据）。' -ForegroundColor Gray
Write-Host ''
Write-Host '  常用命令：'
Write-Host "    启动  schtasks /Run /TN $TaskName"
Write-Host "    停止  schtasks /End /TN $TaskName & taskkill /IM gridperch-agent.exe /F"
Write-Host "    状态  schtasks /Query /TN $TaskName /V /FO LIST"
Write-Host "    卸载  运行本包内的 uninstall.bat"
Write-Host ''
