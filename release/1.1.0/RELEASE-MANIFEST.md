# OpsMonitor 发版清单

- 版本号：**1.1.0**（服务端与采集端共用，取自 `internal/version/version.go`）
- 构建时间：2026-10-12T10:49–10:55（各文件不同，由 `build.sh` 注入 `version.BuildTime`）
- 构建方式：`./build.sh all`（`CGO_ENABLED=0` 静态交叉编译，纯 Go SQLite，无动态库依赖）
- 产物目录：`dist/`

## 平台对照

| # | 角色 | 目标平台 | 文件名 | 授权平台 goos/goarch |
|---|---|---|---|---|
| 1 | 监控端 server | Linux x86_64 | `opsmonitor-server-linux-amd64` | `linux` / `amd64` |
| 2 | 监控端 server | Linux ARM64（鲲鹏/飞腾/树莓派4） | `opsmonitor-server-linux-arm64` | `linux` / `arm64` |
| 3 | 监控端 server | Windows x86_64 | `opsmonitor-server-windows-amd64.exe` | `windows` / `amd64` |
| 4 | 监控端 server | macOS Intel 芯片 | `opsmonitor-server-darwin-amd64` | `darwin` / `amd64` |
| 5 | 监控端 server | macOS Apple Silicon（M 系列） | `opsmonitor-server-darwin-arm64` | `darwin` / `arm64` |
| 6 | 采集端 agent | Linux x86_64 | `opsmonitor-agent-linux-amd64` | `linux` / `amd64` |
| 7 | 采集端 agent | Linux ARM64 | `opsmonitor-agent-linux-arm64` | `linux` / `arm64` |
| 8 | 采集端 agent | Windows x86_64 | `opsmonitor-agent-windows-amd64.exe` | `windows` / `amd64` |
| 9 | 采集端 agent | macOS Intel 芯片 | `opsmonitor-agent-darwin-amd64` | `darwin` / `amd64` |
| 10 | 采集端 agent | macOS Apple Silicon（M 系列） | `opsmonitor-agent-darwin-arm64` | `darwin` / `arm64` |

> 说明：macOS 的 ARM 芯片是 **Apple M 系列**（M1/M2/M3/M4…），对应 `darwin/arm64`；
> 不存在「A20」这个 Mac 芯片型号，按 ARM64 那一行传即可。

## SHA256

```
725760cfe8c8b6b67f67dbf4a5e023056911773d1db99d8239de25a12c799924  opsmonitor-agent-darwin-amd64
547480d9d9f89c3a4618672d8231589f57c5ab0b2135021ac73d48ac3e357114  opsmonitor-agent-darwin-arm64
b66bab0d7a314874e62b27a9bfbbc365e370f8c4bee29b391f4f87d79d79da1b  opsmonitor-agent-linux-amd64
7bd48fbce3fdce41c2154098cbf1b35832bd5f80b3a16ff30166c03383fbebf4  opsmonitor-agent-linux-arm64
d9a7ad88146c69ba6b50a811105ce3782b6bebb8231be54b9ddd782337363c93  opsmonitor-agent-windows-amd64.exe
4982ac7c65eaa6c2d90c32cfde46dc4f4feef020f7ef7ea4c51150faa5d06c52  opsmonitor-server-darwin-amd64
a6a86b16d07714d93757836e427027a06283cfce3102ddc69610831bd736e37c  opsmonitor-server-darwin-arm64
ca300e62de113f9324d3877f92ff479736cbfdcabbb7e8289b2add8e4345495e  opsmonitor-server-linux-amd64
e385867d08e2f843cc9a533afb077359ea8c379d3556ce29a0a5712d451e4ecf  opsmonitor-server-linux-arm64
d98c97250d09abc53d29b0bec59e1181e43c3fad098b435797d39b583669c4ac  opsmonitor-server-windows-amd64.exe
```

## 上传

```bash
# 采集端 5 平台 + 服务端 5 平台一起发到授权系统
./release.sh --with-server -s https://1999091.xyz -v 1.1.0 -n "更新说明写这里"

# 只发采集端（默认行为）
./release.sh -s https://1999091.xyz -v 1.1.0 -n "更新说明写这里"

# 只发某几个平台
./release.sh --with-server -s https://1999091.xyz --only linux/amd64,windows/amd64
```

`release.sh` 会登录授权系统后台 → 逐个 `POST /api/admin/releases`（`kind/version/goos/goarch/latest=true/notes/minVersion/file`）
→ 再调 `/api/v1/check` 自检每个平台是否已登记。

## 发版前自检要点

- 服务端 5 个产物都必须内嵌授权验签公钥 `uZhJVTm2UXxTuOmL3RXvbcokxdU+AIpxqC8PU799I2U=`，
  启动日志指纹应为 **`278D-4852-7191-E7CE`**（旧版曾内置错误公钥，导致授权码一律验不过）。
- 上传时的版本号必须与二进制内嵌版本一致（本批为 `1.1.0`），否则控制台「在线升级」页会报版本不匹配。
- 采集端按「系统 + 架构」分别登记，漏掉某个平台的表现是该平台主机安装采集端时提示「尚未发布」。
