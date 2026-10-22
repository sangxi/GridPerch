# 监控端（server）· Windows 部署

需要 64 位 Windows。**官方支持 Windows 10 / 11、Windows Server 2016 及以上。**

> 这个限制来自工具链本身：Go 1.21 起编译出的程序最低要求 Windows 10 / Server 2016，
> 之前的 Win7 / 8 / 8.1 与 Server 2008 R2 / 2012 / 2012 R2 全部不再支持。
> **监控端（server）同样受此限制** —— 旧系统上跑不了监控端，请把监控端放在
> Linux / Docker / Windows 10+ 上；被监控的旧系统装**采集端兼容版**即可（见文末一章）。

## 快速开始

在**管理员 PowerShell** 里执行（脚本会自己提权检查，权限不足会直接拦住并给出提示）：

```powershell
cd C:\你的目录\deploy\windows
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass -Force   # 仅本次会话放开脚本执行
.\install.ps1
```

脚本会自动：

1. 把 `gridperch-server.exe` 装到 `C:\GridPerch\`（自动从脚本上级目录找二进制）
2. 生成 `C:\GridPerch\start-server.cmd`，把端口、密钥、账号写进去
3. 注册**开机自启计划任务** `gridperch`（SYSTEM 权限、失败自动重启 3 次）
4. 放行防火墙端口
5. 打印**只显示一次**的控制台账号与通信密钥

> 为什么用「计划任务」而不是 Windows 服务：不依赖 NSSM 等第三方工具，系统原生、零下载。

## 常用参数

```powershell
.\install.ps1 -Addr :8000                          # 换端口
.\install.ps1 -Token '你的密钥' -AdminPass '你的密码'   # 指定密钥与密码
.\install.ps1 -Prefix 'D:\GridPerch'              # 换安装目录
.\install.ps1 -File D:\tmp\gridperch-server-windows-amd64.exe
.\install.ps1 -NoFirewall                          # 不动防火墙
.\install.ps1 -Uninstall                           # 卸载（保留数据）
.\uninstall.ps1 -Purge                             # 卸载并删除数据
```

## 目录规划

| 路径 | 用途 |
|---|---|
| `C:\GridPerch\gridperch-server.exe` | 主程序（自动更新会替换，旧版存为 `.bak`） |
| `C:\GridPerch\start-server.cmd` | 启动脚本（配置在这里，权限受限） |
| `C:\GridPerch\data\gridperch.db` | SQLite 数据库（备份拷这个目录） |
| `C:\GridPerch\logs\server.log` | 程序输出日志 |
| `C:\GridPerch\releases\` | 采集端发布物缓存 |

## 日常运维

```powershell
# 状态
Get-ScheduledTask -TaskName gridperch | Select-Object State
Get-Process gridperch-server -ErrorAction SilentlyContinue

# 看日志（跟随输出）
Get-Content C:\GridPerch\logs\server.log -Tail 40 -Wait

# 重启
Stop-ScheduledTask -TaskName gridperch; Start-ScheduledTask -TaskName gridperch

# 开机自启开关
Disable-ScheduledTask -TaskName gridperch
Enable-ScheduledTask  -TaskName gridperch
```

## 手工部署（不用脚本）

```powershell
New-Item -ItemType Directory -Force C:\GridPerch\data, C:\GridPerch\logs
Copy-Item gridperch-server-windows-amd64.exe C:\GridPerch\gridperch-server.exe

# 前台试跑，确认能起来（Ctrl+C 退出）
C:\GridPerch\gridperch-server.exe -addr :9999 -db C:\GridPerch\data\gridperch.db -token 你的密钥

# 注册开机自启
$action  = New-ScheduledTaskAction -Execute 'C:\GridPerch\gridperch-server.exe' `
  -Argument '-addr :9999 -db C:\GridPerch\data\gridperch.db -token 你的密钥' `
  -WorkingDirectory 'C:\GridPerch'
$trigger = New-ScheduledTaskTrigger -AtStartup
$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -RunLevel Highest
Register-ScheduledTask -TaskName gridperch -Action $action -Trigger $trigger -Principal $principal -Force

# 放行防火墙
New-NetFirewallRule -DisplayName 'GridPerch' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 9999
```

## 升级

升级只需替换 `C:\GridPerch\gridperch-server.exe` 这一个二进制（前端已编译在内）。替换后重启计划任务使新版本生效：

```powershell
Stop-ScheduledTask -TaskName gridperch; Start-ScheduledTask -TaskName gridperch
```

## 排障

| 现象 | 处理 |
|---|---|
| 计划任务显示「已就绪」但没有进程 | 看 `C:\GridPerch\logs\server.log`；常见是端口被占或数据目录无权限 |
| 无法执行脚本，提示脚本被禁用 | 用上面那句 `Set-ExecutionPolicy -Scope Process Bypass`，或 `powershell -ExecutionPolicy Bypass -File .\install.ps1` |
| 本机能开、别的机器打不开 | 防火墙规则没生效，或安全组/上层设备未放行 |
| 日志乱码 | 用 `Get-Content -Encoding Default` 读取，或直接用记事本打开 |
| 启动日志指纹不是 `278D-4852-7191-E7CE` | 二进制不是本版本产物 |
| 采集端连不上 | 检查 `-token` 是否与监控端 `GRIDPERCH_TOKEN` 一致，以及 9999 是否可达 |

---

# 采集端（agent）· Windows 部署

采集端**没有**独立的安装脚本文件。正常路径是在平台控制台「添加主机」页生成一键命令
（服务端动态吐出 `/install.ps1`，脚本自己判断该装哪一套产物）。

## 系统要求：两套产物，按系统版本分流

| 系统 | 用哪套产物 | 一键命令能否用 |
|---|---|---|
| Windows 10 / 11、Server 2016 / 2019 / 2022 | 正式版（当前工具链） | ✅ 直接用 |
| **Windows 8 / 8.1、Server 2012 / 2012 R2** | **旧系统兼容版**（Go 1.20 构建） | ✅ 脚本自动识别并选兼容版 |
| Windows 7 SP1 / Server 2008 R2 | 旧系统兼容版 | ❌ 系统只带 PowerShell 2.0，见下文手工部署 |

### 为什么旧系统非要兼容版

Go **1.21 起，编译出的程序最低要求 Windows 10 / Server 2016**，官方明确不再支持
Windows 7 / 8 / 8.1 与 Server 2008 R2 / 2012 / 2012 R2。落到代码上是两处硬伤：

- Go 1.21.5 起 runtime 的随机数源从 `RtlGenRandom`（`advapi32!SystemFunction036`）换成
  `ProcessPrng`（`bcryptprimitives.dll`）—— Windows 7 / Server 2008 R2 上根本没有这个 DLL；
- 即使在 2012 R2 上这个 DLL 存在，Go 1.21+ 还砍掉了 `WSASocket` 的旧回退、
  `LoadLibraryEx` 的回退和旧版控制台句柄处理，在 6.x 内核上同样起不来。

崩点都在 **runtime 初始化阶段**，早于任何一行业务日志，所以现象是
**双击后黑窗口一闪就没了，既没有报错框、也没有日志文件**。

快速判断手上这个 exe 属于哪一类（在**开发机**上对二进制执行）：

```bash
grep -ac bcryptprimitives  gridperch-agent-windows-amd64.exe        # >0 → 正式版，跑不了旧系统
grep -ac SystemFunction036 gridperch-agent-windows-amd64-win7.exe   # >0 → 兼容版，旧系统可用
```

## 两套产物从哪来

| 产物 | 构建命令 | 文件名 |
|---|---|---|
| 正式版 | `./build.sh all` | `dist/gridperch-agent-windows-amd64.exe` |
| 旧系统兼容版 | `./build-win7.sh` | `dist/gridperch-agent-windows-amd64-win7.exe` |

兼容版用 Go 1.20.14 + 裁剪过的 `go120.mod` 单独构建（采集端不依赖 sqlite/mysql，
所以能绕开「这两个库要求 go 1.21」的限制）。构建时注入了
`version.LegacyWindows=1`，它从此**自己表明身份**，见下面「自动升级」一节。

### 上传到平台（关键一步）

授权系统那条线只存一份产物，**兼容版只能从平台侧本地上传**：

1. 控制台 → **版本发布** → 拉到最下面的「采集端发布物（本地上传 / 兼容版）」
2. 版本号填**与正式版完全一致**的号（如 `1.2.3`），系统选 `Windows`，架构选 `amd64`
3. 勾选 **旧系统兼容版**，选文件，上传

记录靠 `variant` 区分，同一个版本号下正式版与兼容版**并存**，互不覆盖。
「设为最新」也只影响各自那一类，不会把对方顶下来。

## 自动升级不会再把兼容版换回正式版

以前这是个坑：平台下发的更新策略是全局 `auto`，兼容版会在 30 分钟内把自己换成正式版，
**换完就闪退**，所以老文档要求启动参数必须带 `-remote-config off -update off`。

1.2.3 起从协议上解决了：

- 兼容版查版本、拉更新时都带 `legacy=1`，平台**只回兼容版**；
- 平台没有兼容版记录时返回空版本 → 采集端判定「无更新」，**什么都不做**（安全兜底）；
- 安装脚本也会按系统版本自动带 `legacy=1`，不需要人工选。

所以在 1.2.3 及以上，**不再需要** `-remote-config off -update off`。
仍在跑 1.2.2 及更早兼容版的机器，升级到 1.2.3 的兼容版后即可去掉这两个参数。

## 一键安装（Windows 8.1 / Server 2012 / 2012 R2）

在**管理员 PowerShell** 里直接粘贴控制台「添加主机」页给的命令即可。脚本会先打印一行：

```
系统版本 6.3，属于旧版 Windows，将安装「旧系统兼容版」采集端
```

`6.3` 就是 Server 2012 R2 / Windows 8.1。看到这行说明分流正确，后面照常走完。

> 2012 R2 自带 PowerShell 4.0，`Register-ScheduledTask`、`Get-FileHash`、
> `Invoke-RestMethod` 都有，脚本能完整跑完；TLS 1.2 也在脚本里显式打开了
> （这些系统默认只启用 TLS 1.0，不打开会报「基础连接已经关闭」）。

## 手工部署（Windows 7 SP1 / Server 2008 R2）

这两个系统只有 **PowerShell 2.0**，没有 `Register-ScheduledTask` / `Get-ScheduledTask`，
平台给的一键命令在上面必然失败，改用系统自带的 `schtasks.exe`：

```cmd
:: 1) 先验证二进制能在本机跑起来（应打印 gridperch-agent 1.2.3 (windows/amd64) …）
C:\GridPerch\gridperch-agent-windows-amd64-win7.exe -version

:: 2) 把本目录的 start-agent.cmd 与 exe 放在同一目录，用记事本改开头那 4 项
::    （平台地址 / 密钥MD5 / 主机名 / 上报间隔）—— 其余参数已在脚本里固化

:: 3) 双击 start-agent.cmd 试跑。日志里应出现
::    「采集端启动：版本 1.2.3 …」，以及「已上报 CPU …」，即已连上平台

:: 4) 注册开机自启（管理员 cmd，末尾 log 参数让输出写进 agent.log）
schtasks /create /tn gridperch-agent /tr "C:\GridPerch\start-agent.cmd log" /sc onstart /ru SYSTEM /rl HIGHEST /f
schtasks /run /tn gridperch-agent

:: 5) 查看状态与日志
schtasks /query /tn gridperch-agent
type C:\GridPerch\agent.log
```

`密钥MD5` 就是平台「添加主机」页面生成命令里的 `wgToken` 值（= 监控端 `token` 的 MD5）。
`-name` 建议用纯英文；不写则用系统主机名。

> 在 2008 R2 这一类只能用 PowerShell 2.0 的机器上，**平台自升级用不了**
> （自动升级依赖计划任务机制），换版本按下一节手工替换 exe。

## 升级兼容版

版本号变更后，在**开发机**上重新构建，把新 exe 传到平台（勾「旧系统兼容版」），
再让目标机替换：

```bash
./build-win7.sh          # 产物：dist/gridperch-agent-windows-amd64-win7.exe
```

```cmd
:: 在旧系统上手工替换
schtasks /end /tn gridperch-agent
:: 覆盖 exe 之后再启动
schtasks /run /tn gridperch-agent
```

Windows 8.1 / Server 2012 R2 上如果当初用的是一键命令安装的，平台发布新兼容版后
采集端会自己升级（走的仍是 `legacy=1` 那条流）。

## 排障

| 现象 | 处理 |
|---|---|
| 双击闪退、无任何输出 | 系统兼容问题，换兼容版（`-win7.exe`） |
| `-version` 正常但平台看不到主机 | 看 `C:\GridPerch\agent.log`；多半是 `-server` 写成 `127.0.0.1`，或平台 IP/端口不可达 |
| 一键安装报「尚未发布…旧系统兼容版」 | 平台「版本发布」里没有勾选兼容版上传，按上文补一次 |
| 稳定运行后突然又闪退 | 用 `-version` 看版本；若变成了没有 `-win7` 后缀的正式版，说明是被旧平台换掉的（1.2.2 及更早的行为），部署 1.2.3+ 后不再发生 |
| 不确定手上的 exe 是哪个构建 | `-version` 看构建时间；或 `grep -ac bcryptprimitives`，结果为 0 才是兼容版 |
