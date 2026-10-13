# OpsMonitor 发版清单

- 版本号：**1.1.4**（服务端与采集端共用，取自 `internal/version/version.go`）
- 构建时间：**2026-10-13T13:01:08**（由 `build.sh` 注入 `version.BuildTime`，10 个产物一致）
- 构建方式：`bash build.sh all`（`CGO_ENABLED=0` 静态交叉编译，纯 Go SQLite，无动态库依赖）
- 产物目录：`D:\c\xin`（源 `D:\c\opsmonitor\dist`）

## 本次更新内容

### 1. 控制台不再暴露授权中心域名（本次重点）
授权站遭 DDoS 期间，前台「在线升级」「授权购买」两页此前会把授权中心地址直接显示出来，
并带有跳转授权站的按钮。1.1.4 起：

- 「授权系统连接」（在线升级页）与「授权系统授权同步」（授权购买页）**不再展示授权中心域名**；
- **去掉所有跳转到授权站的按钮**；
- **保留机器码与同步状态**（排障仍能看到机器码、是否同步成功）；
- 前端新增总开关 `SHOW_HUB_URL`（`internal/server/web/assets/app.js` 顶部），
  默认 `false`；需要排障时改回 `true` 重新构建即恢复显示，其余功能不受影响。

> 该开关只影响前端展示，**不影响**授权校验、代拉、回执等任何后端逻辑。

### 2. 沿用：平台自升级后自动重启（1.1.3）
「在线升级」换完二进制会**自动重启生效**，不再需要人工 `systemctl restart`：

- systemd / launchd / Docker 托管环境 → 进程退出，由管理器用新二进制拉起；
- 手工 `nohup` 启动 → 自行拉起同参数新进程再退出；
- 新增 `listenAndServeWithRetry`：端口被占用时最多重试 20 秒，兜住端口交接竞态；
- 新增开关 `-hub-restart` / `OPSM_HUB_RESTART`（`auto` 默认 / `off` 只替换不重启）；
- 新版本启动失败 → 下次启动 `confirmSelfUpgrade` 比对不上自动回滚 `.bak`。

### 3. 沿用：登录失败不再「没反应」（1.1.2）
登录接口返回 401 时直接显示「用户名或密码错误」「账号已停用」等后端原因，登录按钮显示「登录中…」。

### 4. 沿用：前端静态资源禁用强缓存 / 多平台补拉（1.1.2 / 1.1.1）
`/assets/` 与页面统一 `Cache-Control: no-cache`；采集端发布物按 5 个平台逐个后台补拉，
`-public-url` 配成内网/CGNAT 地址时启动告警。

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

> macOS 的 ARM 芯片是 **Apple M 系列**（M1/M2/M3/M4…），对应 `darwin/arm64`。

## SHA256

以 `SHA256SUMS.txt` 为准（`sha256sum -c SHA256SUMS.txt` 校验）。

## 部署（Linux · systemd）

前端与自升级逻辑都编译进二进制，**只替换 `opsmonitor-server` 一个文件 + 重启即可**。

```bash
cp /opt/opsmonitor/opsmonitor-server /opt/opsmonitor/opsmonitor-server.bak.$(date +%F-%H%M)
rm -f /opt/opsmonitor/.opsmonitor-server.pending     # 关键：否则启动会回滚到 .bak
install -m 0755 /tmp/opsmonitor-server-linux-amd64 /opt/opsmonitor/opsmonitor-server
systemctl restart opsmonitor
/opt/opsmonitor/opsmonitor-server -version           # 应显示 1.1.4
```

> 换成 1.1.4 之后，**以后再发布新版本就不需要再手工替换了**：
> 平台拉到新版本会自己重启生效（前提是版本号登记正确，见下）。

## 部署（Docker Compose）

```bash
docker compose cp opsmonitor-server-linux-amd64 opsmonitor:/usr/local/bin/opsmonitor-server
docker compose restart opsmonitor
```
容器内 `/.dockerenv` 被识别为「托管环境」，后续自升级会以退出→compose 重启的方式生效，
因此 **compose 里必须有 `restart: unless-stopped`（本仓库模板已带）**。

## 在线升级（授权端）

1.1.4 > 授权端现有版本，所以面板会显示「已是最新」，属正常。
要让面板的在线升级链继续可用，把 1.1.4 登记到授权系统即可（地址填你自己的授权中心）：

```bash
./release.sh --with-server -s <你的授权中心地址> -v 1.1.4 -n "控制台隐藏授权中心域名；平台可自重启升级"
```

> 登记版本号必须等于二进制内嵌版本（`1.1.4`），否则会「已是最新拉不下来」或反复重下。

### 自升级行为速查

| 场景 | 行为 |
|---|---|
| systemd（`Restart=always`） | 进程 `exit(0)` → systemd 用新版本拉起 |
| Docker Compose（`restart: unless-stopped`） | 容器 PID 1 退出 → compose 重启容器 |
| 手工 `nohup` 启动 | 自拉新进程（端口被占用会自动重试）后退出 |
| 新版本启动失败 | 启动时 `confirmSelfUpgrade` 比对不上 → 自动回滚 `.bak` |
| `-hub-restart off` | 只替换文件，不重启，需人工重启 |

## 发版前自检要点

- 服务端 5 个产物都必须内嵌授权验签公钥 `uZhJVTm2UXxTuOmL3RXvbcokxdU+AIpxqC8PU799I2U=`，
  启动日志指纹应为 **`278D-4852-7191-E7CE`**。
- 上传时的版本号必须与二进制内嵌版本一致。
- 采集端按「系统 + 架构」分别登记，漏掉某个平台的表现是该平台主机安装采集端时提示「尚未发布」。
