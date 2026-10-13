# OpsMonitor 发版清单

- 版本号：**1.1.5**（服务端与采集端共用，取自 `internal/version/version.go`）
- 构建时间：**2026-10-13T14:09:30**（由 `build.sh` 注入 `version.BuildTime`，10 个产物一致）
- 构建方式：`bash build.sh all`（`CGO_ENABLED=0` 静态交叉编译，纯 Go SQLite，无动态库依赖）
- 产物目录：`D:\c\xin`（源 `D:\c\opsmonitor\dist`）

## 本次更新内容

### 1. 修复：一键安装脚本把中文主机名清成一串 `-`（本次重点）
**现象**：在「添加主机」里给主机起中文名，接入后控制台里主机名列显示成一串 `-`（看起来像「……」），
像是「不支持中文」。

**根因**：`internal/server/release.go` 的 `sanitizeAgentName()` 只放行 ASCII `a-z A-Z 0-9 - _ .`，
**其余字符一律替换成 `-`**（本意是防脚本注入），且按**字节**截断。于是「测试主机-中文」在安装脚本里
变成 `HOST_NAME="-------"`，采集端据此上报，库里存的就是连字符。

**修复**：
- 放行「任意语言的字母 / 数字 / 组合符号」（`unicode.IsLetter` / `IsDigit` / `IsMark`），
  中文、日文、俄文等都能正常作主机名；
- 引号、`$`、反引号、分号、换行、管道、全角标点等元字符**仍一律替换为 `-`**，注入面不变；
- 截断改为**按字符（rune）**，不再把多字节汉字劈成半个、显示成乱码；
- 新增 `internal/server/release_test.go`（11 个用例锁定行为）。

> 只有「一键安装」这条路会清洗主机名；「手动安装」本来就不清洗，所以此前手动接入不受影响。

### 2. 新增：主机重命名（管理员）
主机名由采集端 `-name` 启动参数决定，平台无法远端修改已部署主机上的服务参数。
所以平台侧的做法是两步：

1. **把该主机名下的全部数据级联迁移到新名字**——指标、告警、系统事件、目录归属、
   采集端配置（`agent_config`）、监控项的「作用主机」名单、指令历史、资产管理的「关联监控主机」；
2. **登记「旧名 → 新名」映射**（`host_rename` 表）。采集端后续仍以旧名上报时，
   平台在所有采集端入口（上报、任务、指令、运行配置）自动归位到新名字。

因此**改名后不需要重装或改动采集端**，也不会冒出第二台「旧名」主机。支持链式改名（A→B→C）。

### 3. 新增：删除主机（管理员）
连同全部历史数据一起清理。**注意**：若该主机的采集端仍在运行，它会在下次上报时被重新发现
（有活着的采集端就是在线主机）；要彻底不再出现，需先在目标主机上停用/卸载采集端。

### 4. UI 与审计
- 「主机监控」列表每行新增 **重命名** / **删除** 按钮（仅管理员可见），均带说明性确认弹窗；
- 操作审计新增 `重命名主机` / `删除主机`（`host` 分类），操作对象记录为主机名；
- 顺带修正 `/api/hosts/{host}/agent-config`、`/api/hosts/{host}/rename` 的审计对象
  误记为路径末段（`agent-config` / `rename`）的问题，现在都记录真正的主机名；
- 控制台内置教程新增「主机重命名与删除」章节（`/docs`）。

### 5. 沿用：控制台不暴露授权中心域名（1.1.4）
「在线升级」「授权购买」页不展示授权中心域名、无跳转按钮；前端 `SHOW_HUB_URL` 开关可临时恢复显示。

### 6. 沿用：平台自升级后自动重启（1.1.3）
换完二进制自动重启生效；端口被占用时最多重试 20 秒兜住交接竞态；`-hub-restart` 可关闭自重启；
新版本启动失败自动回滚 `.bak`。

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

以 `SHA256SUMS.txt` 为准（`sha256sum -c SHA256SUMS.txt` 校验，本次 10/10 通过）。

## 部署（Linux · systemd）

前端与自升级逻辑都编译进二进制，**只替换 `opsmonitor-server` 一个文件 + 重启即可**。

```bash
cp /opt/opsmonitor/opsmonitor-server /opt/opsmonitor/opsmonitor-server.bak.$(date +%F-%H%M)
rm -f /opt/opsmonitor/.opsmonitor-server.pending     # 关键：否则启动会回滚到 .bak
install -m 0755 /tmp/opsmonitor-server-linux-amd64 /opt/opsmonitor/opsmonitor-server
systemctl restart opsmonitor
/opt/opsmonitor/opsmonitor-server -version           # 应显示 1.1.5
```

> 采集端二进制**不需要**跟着换：本次的改名/删除能力全在服务端。
> 只有当你想让某台主机的**采集端自身名字**也彻底改掉（而不依赖平台映射）时，
> 才需要在目标主机上重跑一次 1.1.5 的安装命令。

## 部署（Docker Compose）

```bash
docker compose cp opsmonitor-server-linux-amd64 opsmonitor:/usr/local/bin/opsmonitor-server
docker compose restart opsmonitor
```
容器内 `/.dockerenv` 被识别为「托管环境」，后续自升级会以退出→compose 重启的方式生效，
因此 **compose 里必须有 `restart: unless-stopped`（本仓库模板已带）**。

## 在线升级（授权端）

1.1.5 > 授权端现有版本。要让面板的在线升级链可用，把 1.1.5 登记到授权系统：

```bash
./release.sh --with-server -s <你的授权中心地址> -v 1.1.5 -n "修复中文主机名；新增主机重命名与删除"
```

> 登记版本号必须等于二进制内嵌版本（`1.1.5`），否则会「已是最新拉不下来」或每 5 分钟反复重下。

### 自升级行为速查

| 场景 | 行为 |
|---|---|
| systemd（`Restart=always`） | 进程 `exit(0)` → systemd 用新版本拉起 |
| Docker Compose（`restart: unless-stopped`） | 容器 PID 1 退出 → compose 重启容器 |
| 手工 `nohup` 启动 | 自拉新进程（端口被占用会自动重试）后退出 |
| 新版本启动失败 | 启动时 `confirmSelfUpgrade` 比对不上 → 自动回滚 `.bak` |
| `-hub-restart off` | 只替换文件，不重启，需人工重启 |

## 发版前自检要点

- 服务端 5 个产物都必须内嵌授权验签公钥，启动日志指纹应为 **`278D-4852-7191-E7CE`**（本次已核）。
- 上传时的版本号必须与二进制内嵌版本一致（`1.1.5`）。
- 采集端按「系统 + 架构」分别登记，漏掉某个平台的表现是该平台主机安装采集端时提示「尚未发布」。

## 本次验证记录

- `gofmt -l internal/ cmd/` 无输出；`go vet ./...` 干净；`go test ./...` 全过
  （含新增 `internal/store/hostadmin_test.go` 5 个用例、`internal/server/release_test.go` 11 个用例）。
- 端到端（真实二进制 + 真实采集端，端口 18081）：
  1. `GET /install.sh?name=测试主机-中文` → `HOST_NAME="测试主机-中文"`（旧版本此处是一串 `-`）；
  2. 采集端以中文名接入 → 列表显示 `测试主机-中文`；
  3. 重命名为 `数据库主库-01` → 列表只剩新名字，主机级采集端配置（`interval=77`）随迁；
  4. **旧名继续上报** → 自动归位到 `数据库主库-01`，IP/最后上报时间刷新，**未产生第二台主机**；
  5. 删除 → 列表清空；审计记录 `删除主机 | target=数据库主库-01 | ok`；
  6. 采集端仍在运行时以自身名字重新出现（符合设计，文档已写明）。
