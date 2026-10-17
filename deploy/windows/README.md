# 监控端（server）· Windows 部署

需要 64 位 Windows。**官方支持 Windows 10 / 11、Windows Server 2016 及以上。**

> 工具链的硬性下限其实是 **Windows 8 / Server 2012**（Go 1.21.5 起 runtime 依赖
> `bcryptprimitives.dll`，该 DLL 自 Windows 8 起才有），但这几个版本不在官方支持列表内，
> 只作参考。**Windows 7 SP1 / Server 2008 R2 无法运行当前产物** —— 采集端的兼容方案
> 见文末「采集端（agent）」一章。

## 快速开始

在**管理员 PowerShell** 里执行（脚本会自己提权检查，权限不足会直接拦住并给出提示）：

```powershell
cd C:\你的目录\deploy\windows
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass -Force   # 仅本次会话放开脚本执行
.\install.ps1
```

脚本会自动：

1. 把 `strix-server.exe` 装到 `C:\Strix\`（自动从脚本上级目录找二进制）
2. 生成 `C:\Strix\start-server.cmd`，把端口、密钥、账号写进去
3. 注册**开机自启计划任务** `strix`（SYSTEM 权限、失败自动重启 3 次）
4. 放行防火墙端口
5. 打印**只显示一次**的控制台账号与通信密钥

> 为什么用「计划任务」而不是 Windows 服务：不依赖 NSSM 等第三方工具，系统原生、零下载。

## 常用参数

```powershell
.\install.ps1 -Addr :8000                          # 换端口
.\install.ps1 -Token '你的密钥' -AdminPass '你的密码'   # 指定密钥与密码
.\install.ps1 -Prefix 'D:\Strix'              # 换安装目录
.\install.ps1 -File D:\tmp\strix-server-windows-amd64.exe
.\install.ps1 -NoFirewall                          # 不动防火墙
.\install.ps1 -Uninstall                           # 卸载（保留数据）
.\uninstall.ps1 -Purge                             # 卸载并删除数据
```

## 目录规划

| 路径 | 用途 |
|---|---|
| `C:\Strix\strix-server.exe` | 主程序（自动更新会替换，旧版存为 `.bak`） |
| `C:\Strix\start-server.cmd` | 启动脚本（配置在这里，权限受限） |
| `C:\Strix\data\strix.db` | SQLite 数据库（备份拷这个目录） |
| `C:\Strix\logs\server.log` | 程序输出日志 |
| `C:\Strix\releases\` | 采集端发布物缓存 |

## 日常运维

```powershell
# 状态
Get-ScheduledTask -TaskName strix | Select-Object State
Get-Process strix-server -ErrorAction SilentlyContinue

# 看日志（跟随输出）
Get-Content C:\Strix\logs\server.log -Tail 40 -Wait

# 重启
Stop-ScheduledTask -TaskName strix; Start-ScheduledTask -TaskName strix

# 开机自启开关
Disable-ScheduledTask -TaskName strix
Enable-ScheduledTask  -TaskName strix
```

## 手工部署（不用脚本）

```powershell
New-Item -ItemType Directory -Force C:\Strix\data, C:\Strix\logs
Copy-Item strix-server-windows-amd64.exe C:\Strix\strix-server.exe

# 前台试跑，确认能起来（Ctrl+C 退出）
C:\Strix\strix-server.exe -addr :9999 -db C:\Strix\data\strix.db -token 你的密钥

# 注册开机自启
$action  = New-ScheduledTaskAction -Execute 'C:\Strix\strix-server.exe' `
  -Argument '-addr :9999 -db C:\Strix\data\strix.db -token 你的密钥' `
  -WorkingDirectory 'C:\Strix'
$trigger = New-ScheduledTaskTrigger -AtStartup
$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -RunLevel Highest
Register-ScheduledTask -TaskName strix -Action $action -Trigger $trigger -Principal $principal -Force

# 放行防火墙
New-NetFirewallRule -DisplayName 'Strix' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 9999
```

## 升级

升级只需替换 `C:\Strix\strix-server.exe` 这一个二进制（前端已编译在内）。替换后重启计划任务使新版本生效：

```powershell
Stop-ScheduledTask -TaskName strix; Start-ScheduledTask -TaskName strix
```

## 排障

| 现象 | 处理 |
|---|---|
| 计划任务显示「已就绪」但没有进程 | 看 `C:\Strix\logs\server.log`；常见是端口被占或数据目录无权限 |
| 无法执行脚本，提示脚本被禁用 | 用上面那句 `Set-ExecutionPolicy -Scope Process Bypass`，或 `powershell -ExecutionPolicy Bypass -File .\install.ps1` |
| 本机能开、别的机器打不开 | 防火墙规则没生效，或安全组/上层设备未放行 |
| 日志乱码 | 用 `Get-Content -Encoding Default` 读取，或直接用记事本打开 |
| 启动日志指纹不是 `278D-4852-7191-E7CE` | 二进制不是本版本产物 |
| 采集端连不上 | 检查 `-token` 是否与监控端 `STRIX_TOKEN` 一致，以及 9999 是否可达 |

---

# 采集端（agent）· Windows 部署

采集端**没有**独立的安装脚本文件。正常路径是在平台控制台「添加主机」页面生成一键命令
（服务端动态吐出 `/install.ps1`），在 Windows 10 / Server 2016 及以上可直接使用。

## 系统要求（与监控端同源，边界要记清）

| 系统 | 能否运行当前产物 |
|---|---|
| Windows 10 / 11、Server 2016 / 2019 / 2022 | ✅ 官方支持 |
| Windows 8 / 8.1、Server 2012 / 2012 R2 | ⚠️ 不在官方支持列表，但实际可运行（`bcryptprimitives.dll` 自 Windows 8 起存在） |
| **Windows 7 SP1 / Server 2008 R2** | ❌ **跑不了**，见下一节 |

原因：Go **1.21.5** 起，runtime 的随机数源从 `RtlGenRandom`（`advapi32!SystemFunction036`，
XP 起就有）换成了 `ProcessPrng`（`bcryptprimitives.dll`）。Windows 7 / Server 2008 R2 上没有
这个 DLL，Go 加载失败后拿到的函数指针是 `NULL`，runtime 初始化阶段一调用就触发
`0xc0000005` 退出。崩在 runtime 里、早于任何业务日志，所以现象是
**双击后黑窗口一闪就没了，既没有报错框也没有日志文件**。

快速判断手上这个 exe 属于哪一类（在**开发机**上对二进制执行）：

```bash
grep -ac bcryptprimitives  strix-agent-windows-amd64.exe        # >0 → 跑不了旧系统
grep -ac SystemFunction036 strix-agent-windows-amd64-win7.exe   # >0 → 可以跑旧系统
```

## Windows 7 SP1 / Server 2008 R2 专用部署

用 Go 1.20.14（官方最后一个支持这两个系统的版本）单独构建的兼容产物：

```
strix-agent-windows-amd64-win7.exe
```

在源码根执行 `./build-win7.sh` 生成（依赖 `go120.mod` 这份裁剪版 modfile）。
该产物**不参与 `release.sh` 上传**，平台的在线升级与一键安装都不会用到它，
也不会覆盖 `dist/` 里的正式产物。

### ⚠️ 必须关掉自动升级（最容易漏的一步）

平台下发的「更新策略」是**全局设置**且默认 `auto`。不关掉的话，兼容版会在 30 分钟内向平台
下载正常（Go 1.23）构建的采集端并替换自己 —— **换完又闪退**。所以启动参数必须带
`-remote-config off -update off`。

`-remote-config off` 只掐掉「拉取平台配置」这一条链路，指标上报、指令下发、监控项采集照常工作。

### 部署步骤

Windows Server 2008 R2 自带的 PowerShell 只有 **2.0**，**没有 `Register-ScheduledTask` /
`Get-ScheduledTask`**，平台「添加主机」页给的一键命令在它上面必然失败。改用系统自带的
`schtasks.exe`：

```cmd
:: 1) 先验证二进制能在本机跑起来（应打印 strix-agent 1.2.0 (windows/amd64) …）
C:\Strix\strix-agent-windows-amd64-win7.exe -version

:: 2) 把本目录的 start-agent.cmd 与 exe 放在同一目录，用记事本改开头那 4 项
::    （平台地址 / 密钥MD5 / 主机名 / 上报间隔）—— 其余参数已在脚本里固化

:: 3) 双击 start-agent.cmd 试跑。日志里应出现
::    「采集端启动：版本 1.2.0 …，更新策略 off」以及「已上报 CPU …」，即已连上平台

:: 4) 注册开机自启（管理员 cmd，注意末尾 log 参数让输出写进 agent.log）
schtasks /create /tn strix-agent /tr "C:\Strix\start-agent.cmd log" /sc onstart /ru SYSTEM /rl HIGHEST /f
schtasks /run /tn strix-agent

:: 5) 查看状态与日志
schtasks /query /tn strix-agent
type C:\Strix\agent.log
```

`密钥MD5` 就是平台「添加主机」页面生成命令里的 `wgToken` 值（= 监控端 `token` 的 MD5）。
`-name` 建议用纯英文；不写则用系统主机名。

日志出现 `采集端启动：版本 1.2.0，本机名称 …，服务端 …，更新策略 off` 即成功，
随后控制台「主机监控」里会出现这台机器。

### 升级这个兼容版

版本号变更后，在**开发机**上重新构建，再把新 exe 手工拷到目标机替换：

```bash
./build-win7.sh          # 产物：dist/strix-agent-windows-amd64-win7.exe
```

```cmd
:: 在 2008 R2 上
schtasks /end /tn strix-agent
:: 覆盖 exe 之后再启动
schtasks /run /tn strix-agent
```

### 排障

| 现象 | 处理 |
|---|---|
| 双击闪退、无任何输出 | 就是系统兼容问题，换 `-win7` 兼容版 |
| `-version` 正常但注册任务后平台看不到主机 | 看 `C:\Strix\agent.log`；多半是 `-server` 写成了 `127.0.0.1`，或平台 IP/端口不可达 |
| 稳定运行一段时间后突然又闪退 | 检查启动参数是否漏了 `-remote-config off -update off`，被平台下发的 `auto` 换成了正常版 |
| 不确定手上的 exe 是哪个构建 | `-version` 看构建时间；或 `grep -ac bcryptprimitives`，结果为 0 才是兼容版 |
