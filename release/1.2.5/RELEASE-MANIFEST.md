# GridPerch 发版清单

- 版本号：**1.2.5**（监控端与采集端共用，取自 `internal/version/version.go`）
- 主题：**修复 Windows 采集端在旧系统上一键安装失败（注册计划任务报 0x80070057）**
- 构建时间：**2026-10-22T16:19:06**（由 `build.sh` 注入 `version.BuildTime`，10 个正式产物一致）
- 构建方式：`bash build.sh all`（`CGO_ENABLED=0` 静态交叉编译，纯 Go SQLite，无动态库依赖）
  \+ `bash build-win7.sh`（Go 1.20.14 + `go120.mod`，出旧系统兼容版）
- 自检：`gofmt -l internal cmd` 无输出 · `go vet ./...` 干净 · `go test -p 1 ./...` 六个包全过 ·
  `sha256sum -c SHA256SUMS.txt` 11/11 通过 · 服务端产物可 `grep` 到真公钥 `uZhJVTm2`（指纹 `278D-4852-7191-E7CE`）·
  生成的 `/install.ps1` 经 PowerShell 解析器校验语法通过（166 行）
- 兼容版自报：`gridperch-agent 1.2.5 (windows/amd64) 旧系统兼容版 构建于 2026-10-22T16:20:09`
  正式版自报：`gridperch-agent 1.2.5 (windows/amd64) 构建于 2026-10-22T16:19:06`

## 一、修复：一键安装在旧版 Windows 上装不上

**现象**：在 Windows 8.1 / Server 2012 R2（`[Environment]::OSVersion` = 6.3）上执行一键安装，
前面的步骤全绿 —— 检测环境、连通性、查版本、下载采集端、拉取配置 —— 唯独卡在最后一步：

```
==> 注册开机自启计划任务
Register-ScheduledTask : 参数错误。
+ CategoryInfo : InvalidArgument: (PS_ScheduledTask:...)，CimException
+ FullyQualifiedErrorId : HRESULT 0x80070057,Register-ScheduledTask
```

结果采集端二进制已经落在 `C:\GridPerch\` 却不会开机自启，等于「装了一半」。

**根因**：`New-ScheduledTaskPrincipal -UserId 'SYSTEM' -RunLevel Highest` **没有指定 `-LogonType`**。
旧系统上 SYSTEM 主体配上默认登录类型会被判定为非法参数，注册阶段直接 `0x80070057`。
（`-ExecutionTimeLimit` 置 0 那条是清白的，实测 `PT0S` 合法。）

**修法**（`internal/server/release.go` 的 `installPS1`）：

1. 主体补 `-LogonType ServiceAccount` —— 这一条就解决 6.3 上的报错
2. 整段注册包 `try/catch`，失败自动回落 `schtasks.exe /Create /SC ONSTART /RU SYSTEM /RL HIGHEST`
3. `Start-ScheduledTask` / `Get-ScheduledTask` 也逐个用 `Get-Command` 判断 ——
   **PowerShell 2.0（Win7 / Server 2008 R2）连 `New-ScheduledTaskAction` 都没有**，
   以前这类机器会死在更早的一行；现在整条链路都能落到 `schtasks`，不再依赖 ScheduledTasks 模块
4. 结尾的手工运维提示统一改成 `schtasks /Run|/Query|/Delete`，任何版本都能用

> 只影响 **Windows 采集端安装**，采集端与监控端的运行逻辑一行没动。
> Linux / macOS 安装脚本、采集端上报、授权、大屏等均无变化。

## 二、上一版（1.2.4）要点：品牌更名 + 大屏标题可自定义

- **品牌更名 OpsMonitor → Strix → GridPerch**：module `github.com/sangxi/gridperch`、
  二进制 `gridperch-{server,agent}`、环境变量 `GRIDPERCH_*`、授权码前缀 `GRIDPERCH2-`、
  部署 `/opt/gridperch` + `/etc/gridperch/gridperch.env` + `gridperch.service`、
  默认库 `data/gridperch.db`、默认口令 `admin / gridperch`
- **向后兼容**：env 按 `GRIDPERCH_X → STRIX_X → OPSM_X` 回落；库文件按
  `gridperch.db → strix.db → opsmonitor.db` 查找；`STRIX2-` / `OPSM2-` 旧授权码仍验签；
  机器码盐 `opsmonitor-server`、设备口令盐 `opsmonitor-device-secret`、Ed25519 公钥三处**永久冻结**
- **大屏标题可自定义**：新增 `screenTitle`，优先级 显式值 > 系统名称+运维监控中心 > 兜底；
  `/api/screen` 与 `/api/settings/public` 共用 `screenTitleOf()`，前端 10 秒热跟随、超长省略

## 兼容性

- **数据库**：**无变更**。`screenTitle` 存在通用键值表 `settings` 里，1.2.5 未动表结构，老库直接沿用
- **接口**：无变更。只有 `/install.ps1` 的内容变了（它由服务端动态生成，换二进制即生效）
- **前端缓存**：前端是编译进二进制的，换完二进制请**强刷 Ctrl+F5**
- **升级方式**：替换 `gridperch-server` 单个二进制并重启即可。采集端**不必升级**
  （本次没有任何采集端逻辑改动，1.2.5 的采集端与 1.2.4 行为一致，仅版本号随动）

## 产物（11 个）

| 角色 | 平台 | 文件 |
| --- | --- | --- |
| server | linux/amd64 · linux/arm64 · darwin/amd64 · darwin/arm64 · windows/amd64 | `gridperch-server-*` |
| agent | linux/amd64 · linux/arm64 · darwin/amd64 · darwin/arm64 · windows/amd64 | `gridperch-agent-*` |
| agent | windows/amd64（Win7 / 8.1 / Server 2008 R2 / 2012 / 2012 R2 兼容版，Go 1.20.14 构建） | `gridperch-agent-windows-amd64-win7.exe` |

> **兼容版不参与 `release.sh` 自动上传**（授权端按 `(kind, version, goos, goarch)` 登记，只存一份产物）。
> 兼容版走控制台「版本发布 → 采集端发布物（本地上传）」，填**与正式版相同的版本号**、
> 勾选「旧系统兼容版」上传一次即可，两个通道并存。
