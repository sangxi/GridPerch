# 监控端（server）· Windows 部署

支持 Windows 10 / 11、Windows Server 2012 R2 及以上（64 位）。

## 快速开始

在**管理员 PowerShell** 里执行（脚本会自己提权检查，权限不足会直接拦住并给出提示）：

```powershell
cd C:\你的目录\deploy\windows
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass -Force   # 仅本次会话放开脚本执行
.\install.ps1
```

脚本会自动：

1. 把 `opsmonitor-server.exe` 装到 `C:\OpsMonitor\`（自动从脚本上级目录找二进制）
2. 生成 `C:\OpsMonitor\start-server.cmd`，把端口、密钥、账号写进去
3. 注册**开机自启计划任务** `opsmonitor`（SYSTEM 权限、失败自动重启 3 次）
4. 放行防火墙端口
5. 打印**只显示一次**的控制台账号与通信密钥

> 为什么用「计划任务」而不是 Windows 服务：不依赖 NSSM 等第三方工具，系统原生、零下载。

## 常用参数

```powershell
.\install.ps1 -Addr :8000                          # 换端口
.\install.ps1 -Token '你的密钥' -AdminPass '你的密码'   # 指定密钥与密码
.\install.ps1 -Prefix 'D:\OpsMonitor'              # 换安装目录
.\install.ps1 -File D:\tmp\opsmonitor-server-windows-amd64.exe
.\install.ps1 -NoFirewall                          # 不动防火墙
.\install.ps1 -Uninstall                           # 卸载（保留数据）
.\uninstall.ps1 -Purge                             # 卸载并删除数据
```

## 目录规划

| 路径 | 用途 |
|---|---|
| `C:\OpsMonitor\opsmonitor-server.exe` | 主程序（自动更新会替换，旧版存为 `.bak`） |
| `C:\OpsMonitor\start-server.cmd` | 启动脚本（配置在这里，权限受限） |
| `C:\OpsMonitor\data\opsmonitor.db` | SQLite 数据库（备份拷这个目录） |
| `C:\OpsMonitor\logs\server.log` | 程序输出日志 |
| `C:\OpsMonitor\releases\` | 采集端发布物缓存 |

## 日常运维

```powershell
# 状态
Get-ScheduledTask -TaskName opsmonitor | Select-Object State
Get-Process opsmonitor-server -ErrorAction SilentlyContinue

# 看日志（跟随输出）
Get-Content C:\OpsMonitor\logs\server.log -Tail 40 -Wait

# 重启
Stop-ScheduledTask -TaskName opsmonitor; Start-ScheduledTask -TaskName opsmonitor

# 开机自启开关
Disable-ScheduledTask -TaskName opsmonitor
Enable-ScheduledTask  -TaskName opsmonitor
```

## 手工部署（不用脚本）

```powershell
New-Item -ItemType Directory -Force C:\OpsMonitor\data, C:\OpsMonitor\logs
Copy-Item opsmonitor-server-windows-amd64.exe C:\OpsMonitor\opsmonitor-server.exe

# 前台试跑，确认能起来（Ctrl+C 退出）
C:\OpsMonitor\opsmonitor-server.exe -addr :9999 -db C:\OpsMonitor\data\opsmonitor.db -token 你的密钥

# 注册开机自启
$action  = New-ScheduledTaskAction -Execute 'C:\OpsMonitor\opsmonitor-server.exe' `
  -Argument '-addr :9999 -db C:\OpsMonitor\data\opsmonitor.db -token 你的密钥' `
  -WorkingDirectory 'C:\OpsMonitor'
$trigger = New-ScheduledTaskTrigger -AtStartup
$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -RunLevel Highest
Register-ScheduledTask -TaskName opsmonitor -Action $action -Trigger $trigger -Principal $principal -Force

# 放行防火墙
New-NetFirewallRule -DisplayName 'OpsMonitor' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 9999
```

## 排障

| 现象 | 处理 |
|---|---|
| 计划任务显示「已就绪」但没有进程 | 看 `C:\OpsMonitor\logs\server.log`；常见是端口被占或数据目录无权限 |
| 无法执行脚本，提示脚本被禁用 | 用上面那句 `Set-ExecutionPolicy -Scope Process Bypass`，或 `powershell -ExecutionPolicy Bypass -File .\install.ps1` |
| 本机能开、别的机器打不开 | 防火墙规则没生效，或安全组/上层设备未放行 |
| 日志乱码 | 用 `Get-Content -Encoding Default` 读取，或直接用记事本打开 |
| 启动日志指纹不是 `278D-4852-7191-E7CE` | 二进制不是本版本产物 |
| 采集端连不上 | 检查 `-token` 是否与监控端 `OPSM_TOKEN` 一致，以及 9999 是否可达 |
