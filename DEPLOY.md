# OpsMonitor 部署方案

> 适用版本：v1.1.5+（构建时间 2026-10-13T14:01:00）· 项目地址：<https://github.com/sangxi/OpsMonitor>
> 授权中心：默认授权中心（地址已内置在程序里，无需手工配置）

本文是 OpsMonitor 从零到上线的完整部署方案，覆盖架构选型、部署拓扑、文件清单、
容量规划、授权通信、上线检查、升级回滚与排障。控制台内置同一份教程（访问 `/docs`），
本文更侧重方案决策与生产实践。

---

## 零、三方通信拓扑

三个组件，职责与通信方向严格分离：

```
                    ┌─────────────────────────┐
                    │   授权中心（公有云）       │
                    │ 默认授权中心（地址已内置） │
                    │ · 授权码签发             │
                    │ · 版本发布与下发         │
                    │ · 升级统计               │
                    └───────────┬─────────────┘
                    ▲ 拉取授权  │ 回执升级统计
      每 5 分钟     │           │ POST /api/v1/report
      GET /api/v1/check           ▼
                    ┌─────────────────────────┐
   浏览器 ──HTTPS──►│  opsmonitor-server      │◄───── 只读，不接受公网入站
                    │  （监控端 / 平台）        │
                    └───────────┬─────────────┘
          POST /agent/minTask   │   分发采集端发布物（本地 releases/）
          GET  /agent/version   │   GET /agent/download
          GET  /agent/tasks     ▼
                    ┌─────────────────────────┐
                    │  opsmonitor-agent       │────► 目标主机（无需公网）
                    │  （采集端，内网/公网皆可） │
                    └─────────────────────────┘
```

**三条独立的通信方向**：

| 方向 | 协议 | 频率 | 说明 |
| --- | --- | --- | --- |
| 监控端 → 授权中心 | HTTPS `GET /api/v1/check` | 默认 5 分钟 | 拉授权码 + 各平台最新版本 |
| 监控端 → 授权中心 | HTTPS `POST /api/v1/report` | 随同步 | 回执平台自升级与采集端升级统计 |
| 采集端 → 监控端 | HTTP `POST /agent/*` | 默认 1 分钟 | 指标上报、拉监控项、拉版本、指令、升级回执 |

关键设计：**采集端永远不直接访问授权中心**。内网采集端只需要能连到监控端的 9999 端口，
因此内网环境不需要为采集端开任何出网策略，只有监控端需要访问公网。

### 自动更新行为

监控端每次同步会做四件事：

1. 拉取并校验导入授权码（授权中心签发后最长 5 分钟内生效）
2. 下载各平台采集端发布物到本地 `releases/`，设为下发版本
3. **检测到服务端自身有新版本时自动下载、校验 SHA256、替换二进制**（就位后等重启生效）
4. 把采集端升级结果回执授权中心

第 3 步默认开启，用 `-hub-auto-update` 控制：

| 取值 | 行为 |
| --- | --- |
| `download`（默认） | 自动下载并就位，就位后**自动重启生效**（自 1.1.3 起，见下文） |
| `check` | 只提示不下载，需手动点「拉取并升级」 |
| `off` | 完全关闭 |

> **自 1.1.3 起，平台自升级后会自己重启**，换完二进制自动生效，不再需要人工 `systemctl restart`：
> - systemd（unit 用 `Restart=always`）→ 进程退出，systemd 用新版本拉起；
> - Docker Compose（`restart: unless-stopped`）→ 容器 PID 1 退出，compose 重启容器；
> - 手工 `nohup` 启动 → 自行拉起同参数新进程后退出（端口被占用会自动重试，最多 20 秒）；
> - 新版本启动失败 → 下次启动时自动回滚到 `.bak`。
>
> 重启交接窗口与 systemd `RestartSec`、容器重启的 TIME_WAIT 由「启动时端口被占用自动重试最多 20 秒」兜住。
> 若希望只替换文件、不自动重启（例如想自己挑时间重启），把 `-hub-restart` 设成 `off`
> （`OPSM_HUB_RESTART=off`）：就位后不重启，等人工重启再生效。

> **附：1.1.1 / 1.1.2 / 1.1.3 / 1.1.4 / 1.1.5 能力演进**
> - **1.1.1**：采集端发布物改为**按平台逐个补拉**（覆盖 linux/amd64、linux/arm64、darwin/amd64、darwin/arm64、windows/amd64 五个平台），修复「Linux 服务端装不上 Windows 采集端 / 目标主机报『授权系统尚未发布该平台』」（授权系统 `/api/v1/check` 按请求 os/arch 返回，所以必须按平台逐个查）；其它平台的补拉改到**后台执行**，避免「立即检查更新」被网关超时（HTTP 524）拖死，也不会并发重复下载。授权端下载接口只认 `?token=` 查询串鉴权（平台下载时自动带令牌）。新增启动告警：若 `-public-url` / `OPSM_PUBLIC_URL` 配成了回环/内网/CGNAT 地址，启动日志会打警告——云主机（网卡只有内网 IP）上生成的采集端安装命令会写死这个地址，目标主机必然「无法连接平台」。
> - **1.1.2**：控制台**登录失败会明确提示原因**（「用户名或密码错误」「账号已停用」）；内嵌前端资源与页面统一 `Cache-Control: no-cache`，升级二进制后浏览器/反向代理不会再用旧前端（建议升级后 Ctrl+F5 一次）。
> - **1.1.3**：平台自升级后**自动重启生效**（见上方）；新增 `-hub-restart`；启动时端口被占用会**自动重试最多 20 秒**。
> - **1.1.4**：控制台「在线升级」「授权购买」页**不再展示授权中心域名**、也没有跳转到授权站的按钮（授权站遭 DDoS 期间降低暴露面），保留机器码与同步状态；前端加 `SHOW_HUB_URL` 开关，需要排障时改回 `true` 重新构建即恢复显示。
> - **1.1.5**：修复「一键安装脚本把中文主机名清成一串 `-`」——`sanitizeAgentName()` 改为放行任意语言的字母/数字/组合符号（中文、日文等均可正常作主机名），元字符仍一律替换为 `-`，注入面不变；顺带修掉按字节截断会把汉字劈成半个的乱码隐患（改为按字符截断）。新增**主机重命名**与**删除主机**（主机列表行内按钮，限管理员）：改名会级联迁移指标、告警、系统事件、目录归属、采集端配置、监控项「作用主机」与资产关联，并登记「旧名 → 新名」映射，**仍在用旧名上报的采集端会自动归位，无需重装**；删除会连同历史数据一并清理（采集端若仍在运行会被重新发现）。

---

## 〇、⚠️ 授权中心被 Cloudflare 拦截（务必先看）

**实测**默认授权中心（即程序内置地址的授权站）**当前返回 HTTP 403，响应体是 Cloudflare 人机校验页**
（`Just a moment...`），而不是接口本身返回的错误。三种 User-Agent 实测结果一致：

| User-Agent | 响应 |
| --- | --- |
| `Go-http-client/1.1` | 403 Cloudflare 挑战页 |
| `opsmonitor-server/1.1.5` | 403 Cloudflare 挑战页 |
| 完整浏览器 UA | 403 Cloudflare 挑战页 |

这意味着**只要授权中心前面挂着开启 Bot 防护的 Cloudflare，监控端就拉不到任何东西**——
人机校验需要执行 JS，Go 这类程序客户端做不到。

### 处理办法（按推荐顺序）

**方案 A：在 Cloudflare 放行 API 路径（推荐）**

登录 Cloudflare 控制台管理授权中心的域名 → `Security` → `WAF` → `Custom rules`：

```
规则 1（跳过 Bot 防护）:
  条件:  URI Path starts_with "/api/"
  操作:  Skip → 选 All managed rules / Browser Integrity Check / Security Level
```

```
规则 2（放行平台客户端）:
  条件:  (URI Path starts_with "/api/") AND (User-Agent contains "opsmonitor-server")
  操作:  Allow
```

**方案 B：关闭 Bot Fight Mode**

`Security` → `Settings` → `Bot Fight Mode` → 关闭。代价是对整个站点生效。

**方案 C：给 API 单独一个子域**

例如 `api.<你的授权中心地址>`，该子域不挂 Cloudflare 代理（DNS 设为 DNS only，灰云），
再用 `-hub https://api.<你的授权中心地址>` 指向它。**这是最干净的方案。**

**方案 D：完全离线**

```bash
opsmonitor-server -hub ""        # 或省略 -hub
```

关闭与授权中心的全部通信，采集端安装与升级改走**控制台上传发布物**，
授权码在「授权购买」页手工粘贴导入。功能不受影响，只是不再自动获取。

### 关于内置令牌（务必读完）

监控端与授权中心通信用的拉取令牌**已内置在程序里**，默认无需 `-hub-token`，
开箱即可对接默认授权中心。为防止随手逆向，令牌没有以明文出现在源码和二进制中：
存放时先与一段固定掩码逐字节异或、再做循环位移，最后打包成整数数组，
掩码单独声明在另一个变量里；运行时由 `defaultHubToken()` 还原。
实测用 `strings` 类工具扫描成品二进制，令牌全文、任意 8 字符以上连续片段、
乃至首 4 字符均**检索不到**。

**但这只是「混淆」，不是「加密」，请勿高估它的强度：**

- 客户端必须把**明文**令牌发给授权中心做比对，所以令牌一定能在运行时被还原出来。
  拿到二进制并愿意下断点调试的人，最终能取到它。
- 混淆的真实作用是：把攻击成本从「`strings` 一下」抬高到「读懂还原逻辑并动态调试」。
  它能挡住顺手翻二进制的人，挡不住有心人。

**如果确实要做到「拿到二进制也拿不到凭证」**，需要在授权中心侧改造认证方式：

1. **非对称签名**（推荐）：客户端只内置**公钥**，用私钥签名一个带时间戳的挑战，
   授权中心验签。公钥泄露无害，因为没有私钥签不出有效请求。
2. **每台机器一个令牌**：授权中心按机器码签发独立令牌，泄露只影响单台。
3. **IP 白名单 + mTLS**：把信任建立在通道而非共享密钥上。

另外，内置令牌还有两个运维层面的注意点：

- 令牌在监控端**永远不会被回显**：控制台「在线升级」页只显示来源
  （内置 / 自定义 / 未设置），日志与错误信息里的令牌会被统一脱敏成 `***`。
  这一点是专门处理的 —— 令牌在 `check` 请求里走 URL query，一旦请求失败，
  Go 的 `url.Error` 会把**带令牌的完整 URL** 拼进错误文本，而这条错误原本会写进
  日志并显示在控制台上，等于把凭证主动交出去。
- 需要轮换令牌时：在授权中心侧改 `-token`，同时更新
  `tools/gen-hubtoken.js` 里的输入重新生成 `internal/config/hubtoken.go` 并重新构建；
  或者临时用 `-hub-token` / `OPSM_HUB_TOKEN` 覆盖（两者优先级高于内置值）。

### 快速自测

改完网关配置后，在监控端所在机器上执行：

```bash
# 令牌已内置，无需替换；这条只验证「网关是否放行程序客户端」
curl -sS -o /dev/null -w '%{http_code}\n' \
  "https://<你的授权中心地址>/api/v1/check?os=linux&arch=amd64"

# 期望 200；403 是网关仍在拦截；401 + "拉取令牌不正确" 说明网关通了但令牌对不上
```

更省事的办法是看 `/healthz` —— 它不需要令牌：

```bash
curl -sS https://<你的授权中心地址>/healthz     # 期望 {"ok":true,...}
```

控制台「系统 → 在线升级」页也会直接显示具体的拦截原因与处理建议。

---

## 一、部署前必须知道的五件事

这五条决定了部署能不能一次成功，**每条都对应一类高频故障**：

| # | 事项 | 做错的后果 |
| --- | --- | --- |
| 1 | `data` 目录必须挂载/备份 | 容器重建后历史指标、告警、资产、账号**全部丢失** |
| 2 | `OPSM_PUBLIC_URL` 必须是**其他主机能访问到的地址** | 生成的安装命令拿到目标主机执行必然失败 |
| 3 | 采集端发布物按「系统 + 架构」**分别登记** | `linux/amd64` 有发布物 ≠ `linux/arm64` 能装 |
| 4 | `-token` 两端必须一致 | 主机列表永远为空，且日志只显示「token 校验失败」 |
| 5 | 首次部署立即改默认密码与密钥 | 任何人拿到地址即可登录并读取通信密钥 |

---

## 二、架构与部署拓扑

### 2.1 组件拓扑

```
                    ┌──────────────────────────────────────┐
                    │      OpsMonitor Server（单实例）       │
   浏览器 ◄─HTTPS──►│  控制台 + 大屏 + 教程 + REST API      │
                    │  主动探针调度器（ICMP/TCP/SNMP/DB…）  │
                    │  告警判定 + 邮件/机器人推送            │
                    │            │                │
                    │      读写分离│                │
                    │      ┌─────▼─────┐           │
                    │      │  SQLite   │           │
                    │      │ 单文件库   │           │
                    │      └───────────┘           │
                    └────────────┼───────────────────┘
                                 │ HTTP 9999
        ┌────────────────┬───────┴───────┬────────────────┐
        ▼                ▼               ▼                ▼
   opsmonitor-agent  opsmonitor-agent  opsmonitor-agent  ...
   (Linux/systemd)   (Windows/计划任务)  (macOS/launchd)
```

### 2.2 容量规划参考

实测数据（单核 2G 机器）：

| 接入主机数 | 服务端内存 | SQLite 磁盘占用（保留 30 天） | 说明 |
| --- | --- | --- | --- |
| 50 台 | ~120 MB | ~1.5 GB | 内部小环境 |
| 200 台 | ~250 MB | ~6 GB | 中型环境 |
| 500 台 | ~450 MB | ~15 GB | 推荐配置：4 核 8G + 100G 盘 |
| 1000 台 | ~800 MB | ~30 GB | 推荐配置：8 核 16G + 200G 盘 |

估算口径：单主机每分钟 1 条上报，每条约 1.5KB（含索引），另加进程快照与监控项结果，
按 2.5KB/主机/分钟 估算，保留 30 天即 `主机数 × 2.5KB × 60 × 24 × 30`。

**降低占用的手段**：

- 缩短保留天数：`-retention 14`（磁盘紧张时首选）
- 调大上报间隔：控制台「系统设置 → 采集端配置」改为 300 秒（数据量直接降为 1/5）
- 跳过无关磁盘：采集端 `-skip-disk` 或平台配置中指定

### 2.3 高可用说明

**当前为单实例设计**。SQLite 写连接池只有一条连接，多实例并发写会冲突。
需要高可用的场景推荐：

- **方案 A（推荐）**：主备切换。主实例正常运行，备实例用同一份数据目录的**只读拷贝**，
  主实例故障时停机、切换目录、启动备实例。RPO 取决于备份频率。
- **方案 B**：双活仅限**读**。把大屏与 `/openapi/v1/*` 指向备实例，采集上报仍走主实例。

不建议在没有充分压测的情况下直接上多活。

---

## 三、部署方式

### 3.1 Docker Compose（推荐）

#### 3.1.1 目录准备

```bash
mkdir -p /opt/opsmonitor/data
mkdir -p /opt/opsmonitor/releases
mkdir -p /backup
cd /opt/opsmonitor
```

#### 3.1.2 编写 docker-compose.yml

```yaml
services:
  opsmonitor:
    image: opsmonitor/opsmonitor:latest
    container_name: opsmonitor
    restart: unless-stopped
    ports:
      - "9999:9999"
    volumes:
      # ① SQLite 数据库目录 —— 必须挂载，否则容器重建数据全丢
      - ./data:/opt/opsmonitor/data
      # ② 采集端发布物缓存 —— 用于一键安装采集端，可选但强烈建议
      - ./releases:/opt/opsmonitor/releases
      # ③ 时区 —— 不挂会导致容器内时间为 UTC，告警时间差 8 小时
      - /etc/localtime:/etc/localtime:ro
    environment:
      TZ: "Asia/Shanghai"
      OPSM_ADDR: ":9999"
      OPSM_DB: "/opt/opsmonitor/data/opsmonitor.db"
      OPSM_TOKEN: "改成你自己的强密钥"
      OPSM_ADMIN_USER: "admin"
      OPSM_ADMIN_PASS: "改成强密码"
      # 对外可达地址：安装脚本与采集端升级下载都靠它拼绝对地址
      OPSM_PUBLIC_URL: "http://192.168.40.111:9999"
      OPSM_RETENTION: "30"
      OPSM_OFFLINE_SEC: "180"
      OPSM_CPU_ALERT: "90"
      OPSM_MEM_ALERT: "90"
      OPSM_DISK_ALERT: "90"
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:9999/"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
```

#### 3.1.3 启动与验证

```bash
docker compose up -d
docker compose logs -f --tail=100
```

看到以下日志即启动成功：

```
服务端启动：监听 :9999，数据库 /opt/opsmonitor/data/opsmonitor.db，数据保留 30 天
控制台访问地址：http://192.168.40.111:9999
```

浏览器打开该地址，用设置的账号登录。

### 3.2 1Panel

| 步骤 | 操作 |
| --- | --- |
| 1 | 「应用商店」安装 **Docker** |
| 2 | 「容器」→「创建容器」，名称 `opsmonitor`，镜像 `opsmonitor/opsmonitor:latest` |
| 3 | 重启策略选 **始终重启** |
| 4 | 端口映射：主机 `9999` → 容器 `9999` |
| 5 | 文件夹：新建 `/opt/opsmonitor/data` |
| 6 | 挂载：`/opt/opsmonitor/data` → `/opt/opsmonitor/data`（读写） |
| 7 | 环境变量：按 3.1.2 的表格逐条填写 |
| 8 | 防火墙与云安全组放行 9999/TCP |
| 9 | （可选）「网站」→ 反向代理到 `http://127.0.0.1:9999` |

> 配了反向代理后，务必把 `OPSM_PUBLIC_URL` 改成 `https://你的域名`。

### 3.3 宝塔面板

| 步骤 | 操作 |
| --- | --- |
| 1 | 「软件商店」安装 **Docker 管理器** |
| 2 | 「Docker」→「容器管理」→「添加容器」，填镜像与端口映射 |
| 3 | 目录挂载 `/opt/opsmonitor/data` → `/opt/opsmonitor/data` |
| 4 | 配置环境变量（同上） |
| 5 | 「网站」→ 添加站点 → 反向代理到 `http://127.0.0.1:9999` |
| 6 | 「安全」→ 放行端口 `9999` |

### 3.4 Linux 二进制 + systemd

```bash
mkdir -p /opt/opsmonitor/data && cd /opt/opsmonitor

# 从 GitHub Releases 下载（按实际系统与架构选一个）
curl -fLo opsmonitor-server https://github.com/sangxi/OpsMonitor/releases/latest/download/opsmonitor-server-linux-amd64
chmod +x opsmonitor-server
./opsmonitor-server -version
```

注册服务（**密钥以环境文件方式存放更安全**）：

```bash
sudo mkdir -p /etc/opsmonitor
sudo tee /etc/opsmonitor/opsmonitor.env >/dev/null <<'EOF'
OPSM_ADDR=:9999
OPSM_DB=/opt/opsmonitor/data/opsmonitor.db
OPSM_TOKEN=改成强密钥
OPSM_ADMIN_USER=admin
OPSM_ADMIN_PASS=改成强密码
OPSM_PUBLIC_URL=http://192.168.40.111:9999
OPSM_RETENTION=30
EOF
sudo chmod 600 /etc/opsmonitor/opsmonitor.env

sudo tee /etc/systemd/system/opsmonitor.service >/dev/null <<'EOF'
[Unit]
Description=OpsMonitor 运维监控服务端
Documentation=https://github.com/sangxi/OpsMonitor
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=opsmonitor
Group=opsmonitor
WorkingDirectory=/opt/opsmonitor
EnvironmentFile=/etc/opsmonitor/opsmonitor.env
ExecStart=/opt/opsmonitor/opsmonitor-server
Restart=always
RestartSec=5
# 加固：只允许写数据目录
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/opt/opsmonitor/data /opt/opsmonitor/releases

[Install]
WantedBy=multi-user.target
EOF

# 创建专用低权限用户
sudo useradd -r -s /sbin/nologin opsmonitor 2>/dev/null || true
sudo chown -R opsmonitor:opsmonitor /opt/opsmonitor

sudo systemctl daemon-reload
sudo systemctl enable --now opsmonitor
sudo systemctl status opsmonitor --no-pager
```

### 3.5 Windows

```powershell
# 解压到 C:\OpsMonitor
cd C:\OpsMonitor
New-Item -ItemType Directory -Force C:\OpsMonitor\data | Out-Null

# 前台试跑
.\opsmonitor-server.exe -addr :9999 -db C:\OpsMonitor\data\opsmonitor.db -token 你的密钥

# 注册开机自启（管理员 PowerShell）
$action  = New-ScheduledTaskAction -Execute 'C:\OpsMonitor\opsmonitor-server.exe' `
  -Argument '-addr :9999 -db C:\OpsMonitor\data\opsmonitor.db -token 你的密钥'
$trigger = New-ScheduledTaskTrigger -AtStartup
$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -RunLevel Highest
Register-ScheduledTask -TaskName 'opsmonitor' -Action $action -Trigger $trigger `
  -Principal $principal -Force
Start-ScheduledTask -TaskName 'opsmonitor'

# 放行防火墙
netsh advfirewall firewall add rule name="OpsMonitor" dir=in action=allow protocol=TCP localport=9999
```

---

## 四、文件挂载清单

### 4.1 目录结构

```
/opt/opsmonitor/
├── opsmonitor-server              # 服务端二进制（容器内由镜像自带）
├── opsmonitor-agent               # 采集端二进制（仅手工部署采集端时需要）
├── data/                          # ★ 必须持久化
│   ├── opsmonitor.db              #   SQLite 主库
│   ├── opsmonitor.db-wal          #   WAL 日志（与主库同目录，不可单独移动）
│   └── opsmonitor.db-shm          #   共享内存索引
├── releases/                      # 采集端发布物缓存
│   └── opsmonitor-agent-linux-amd64
└── opsmonitor-file-baseline.json  # 文件防篡改基线（在采集端本机，不在服务端）
```

### 4.2 挂载对照表

| 宿主机路径 | 容器内路径 | 读写 | 必需性 | 不挂的后果 |
| --- | --- | --- | --- | --- |
| `/opt/opsmonitor/data` | `/opt/opsmonitor/data` | 读写 | **必需** | 容器重建后历史指标、告警、资产、账号全部丢失 |
| `/opt/opsmonitor/releases` | `/opt/opsmonitor/releases` | 读写 | 可选 | 一键安装采集端提示「尚未发布 xx/xx 的采集端版本」 |
| `/etc/localtime` | `/etc/localtime` | 只读 | 建议 | 容器内时区为 UTC，告警时间差 8 小时 |
| `/opt/opsmonitor/opsmonitor-file-baseline.json` | — | 读写 | 仅采集端 | 采集端重启后文件防篡改基线丢失，首次运行重建 |

### 4.3 权限参考

| 文件 | 属主 | 权限 |
| --- | --- | --- |
| `opsmonitor-server` | root | 755 |
| `opsmonitor-agent` | root | 755 |
| `data/` | 运行用户 | 750 |
| `data/opsmonitor.db` | 运行用户 | 640 |
| `releases/` | 运行用户 | 755 |
| `/etc/opsmonitor/opsmonitor.env` | root | **600** |
| systemd unit | root | 644 |

---

## 五、采集端接入方案

### 5.1 一键安装（推荐）

1. 控制台 →「监控」→「添加主机」→ 选系统、架构、填主机名
2. 页面会**提前校验**该「系统 + 架构」是否已有可下发的采集端版本
3. 复制命令到目标主机执行：

```bash
# Linux / macOS
curl -fsSL "http://192.168.40.111:9999/install.sh?wgToken=<MD5>&name=web-01" | sudo bash
```

```powershell
# Windows（管理员 PowerShell，单行，不含变量，复制粘贴不会被破坏）
Invoke-Expression (Invoke-WebRequest -UseBasicParsing "http://192.168.40.111:9999/install.ps1?wgToken=<MD5>&name=win-01").Content
```

安装脚本自动完成：识别系统与架构 → 查询下发版本 → 下载二进制 → **校验 SHA256** →
拉取平台下发的运行配置 → 注册开机自启的系统服务。

> 命令里只带 `-server` / `-token-hash` / `-name` 三个必要参数，
> 通信密钥以 **MD5 指纹**下发，目标主机的命令行历史与 systemd 单元文件里不会留下明文口令。

### 5.2 发布采集端版本

采集端二进制必须先发布到平台，目标主机才能从平台拉取。

**方式一：控制台上传（管理员）**

「系统 → 在线升级」→ 上传采集端二进制 → 填版本号（**必须与二进制内嵌版本号一致**）→
选目标系统与 CPU 架构 → 设为下发版本。

**方式二：一键发布脚本**

```bash
./release.sh -s https://你的授权中心地址
./release.sh -v 1.1.5 -n "新增 CPU 温度与 SMART 采集"
./release.sh --only linux/amd64,linux/arm64
```

脚本会：交叉编译 5 个平台 → 登录授权系统 → 逐个上传并设为该平台下发版本 →
用 `/api/v1/check` 自检。

> 平台按「系统 + 架构」**分别发布**。漏掉某一个平台的表现就是该平台主机安装失败。

### 5.3 容器内采集宿主机

```yaml
services:
  opsmonitor-agent:
    image: opsmonitor/opsmonitor-agent:latest
    container_name: opsmonitor-agent
    restart: unless-stopped
    pid: host
    network_mode: host
    volumes:
      # 只读挂载宿主机根目录，让采集端看到真实的 /proc、/sys
      - /:/host:ro,rslave
    environment:
      - TZ=Asia/Shanghai
    command:
      - -server=http://192.168.40.111:9999
      - -token=你的通信密钥
      - -name=宿主机名
```

`ro` 一定要加——采集端只需读 `/proc`、`/sys`，不应能修改宿主机文件。

---

## 六、配置项完整清单

### 6.1 服务端

| 参数 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-addr` | `OPSM_ADDR` | `:9999` | HTTP 监听地址 |
| `-db` | `OPSM_DB` | `./data/opsmonitor.db` | SQLite 文件路径 |
| `-token` | `OPSM_TOKEN` | `opsmonitor` | 采集端通信密钥，两端必须一致 |
| `-user` | `OPSM_ADMIN_USER` | `admin` | 控制台用户名（仅首次启动生效） |
| `-pass` | `OPSM_ADMIN_PASS` | `opsmonitor` | 控制台密码（仅首次启动生效） |
| `-retention` | `OPSM_RETENTION` | `30` | 历史数据保留天数，0 表示永久 |
| `-purge` | `OPSM_PURGE_INTERVAL` | `3600` | 历史清理间隔（秒） |
| `-offline` | `OPSM_OFFLINE_SEC` | `180` | 超过该秒数未上报判定离线 |
| `-cpu-alert` | `OPSM_CPU_ALERT` | `90` | CPU 告警阈值（%） |
| `-mem-alert` | `OPSM_MEM_ALERT` | `90` | 内存告警阈值（%） |
| `-disk-alert` | `OPSM_DISK_ALERT` | `90` | 磁盘告警阈值（%） |
| `-load-alert` | `OPSM_LOAD_ALERT` | `10` | 系统负载告警阈值 |
| `-alert-cooldown` | `OPSM_ALERT_COOLDOWN_MIN` | `10` | 同类告警去重冷却（分钟） |
| `-login-alert` | `OPSM_LOGIN_ALERT` | `true` | 主机登录用户变化时生成提醒 |
| `-probe-workers` | `OPSM_PROBE_WORKERS` | `64` | 服务端主动探测并发度 |
| `-read-conns` | `OPSM_READ_CONNS` | `8` | SQLite 读连接池大小 |
| `-write-queue` | `OPSM_WRITE_QUEUE` | `8192` | 上报写队列容量 |
| `-release-dir` | `OPSM_RELEASE_DIR` | 数据库同级 `releases/` | 采集端发布包目录 |
| `-public-url` | `OPSM_PUBLIC_URL` | 自动反推 | 对外访问地址 |
| `-license-secret` | `OPSM_LICENSE_SECRET` | `opsmonitor-license` | **旧格式**（`WGC-`/`OPSM1-`/`OPSM-`，HMAC）授权码的校验密钥；新格式（`OPSM2-`）用内置 Ed25519 公钥校验，与本参数无关。线上已导入旧码则**不要删**，删了会回落到社区版 |
| `-license` | `OPSM_LICENSE_KEY` | 空 | 授权码（新格式 `OPSM2-`）手动指定的另一种途径；一般留空由平台自动从授权中心拉取并写入数据库 |
| `-license-machine` | `OPSM_LICENSE_MACHINE` | 空 | 机器码钉死值（如 `C317-6E8B-BAE5-A8A6`）；多网卡/容器场景用此固定机器码，避免换网卡/重建容器导致授权失效 |
| `-free-hosts` | `OPSM_LICENSE_FREE_HOSTS` | `10` | 未授权时允许接入的主机数 |
| `-free-users` | `OPSM_LICENSE_FREE_USERS` | `3` | 未授权时允许创建的账号数 |
| `-hub` | `OPSM_HUB` | 默认已内置授权中心地址；留空 `-hub ""` = 完全离线 | 授权中心地址；留空则完全离线 |
| `-hub-token` | `OPSM_HUB_TOKEN` | 留空用内置令牌 | 授权中心拉取令牌。默认用程序内置令牌，无需配置；仅当改用自建授权中心时才需覆盖。优先级：`-hub-token` > `OPSM_HUB_TOKEN` > 内置 |
| `-hub-interval` | `OPSM_HUB_INTERVAL_MIN` | `5` | 代拉间隔（分钟） |
| `-hub-auto-update` | `OPSM_HUB_AUTO_UPDATE` | `download` | 服务端自更新：`download` 自动下载并就位（默认，就位后自动重启生效）/ `check` 只提示不下载 / `off` 关闭 |
| `-hub-restart` | `OPSM_HUB_RESTART` | `auto` | 自升级就位后的重启策略：`auto` 自动重启生效（默认）/ `off` 只替换文件不重启，需人工重启 |
| `-hub-ua` | `OPSM_HUB_UA` | 空 | 访问授权中心的 User-Agent，被网关按 UA 拦截时可覆盖 |
| `-version` | - | - | 打印版本号后退出 |

### 6.2 采集端

| 参数 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-server` | `OPSM_SERVER` | `http://127.0.0.1:9999` | 服务端地址 |
| `-token` | `OPSM_TOKEN` | `opsmonitor` | 通信密钥，与服务端一致 |
| `-token-hash` | - | 空 | 通信密钥的 MD5，与 `-token` 二选一 |
| `-name` | `OPSM_HOSTNAME` | 本机主机名 | 平台显示名称 |
| `-remote-config` | `OPSM_REMOTE_CONFIG` | `auto` | `auto` 接受平台下发 / `off` 只用本地参数 |
| `-interval` | `OPSM_INTERVAL` | `60` | 上报间隔（秒）**平台下发时被覆盖** |
| `-update` | `OPSM_UPDATE` | `auto` | `auto` / `check` / `off` **平台下发时被覆盖** |
| `-update-interval` | `OPSM_UPDATE_INTERVAL_MIN` | `30` | 版本检查间隔（分钟）**平台下发时被覆盖** |
| `-allow-command` | `OPSM_ALLOW_COMMAND` | `true` | 是否执行下发指令**平台下发时被覆盖** |
| `-install-dir` | `OPSM_INSTALL_DIR` | 可执行文件目录 | 升级时替换二进制的目标目录 |
| `-version` | - | - | 打印版本号后退出 |

---

## 七、上线检查清单

部署完成后逐项确认：

### 7.1 服务端

- [ ] 启动日志出现「控制台访问地址：http://…」
- [ ] 浏览器能打开控制台并登录成功
- [ ] 用**非本机地址**在另一台机器上能访问（验证未绑回环）
- [ ] 防火墙 / 云安全组已放行 9999/TCP
- [ ] `data` 目录已挂载且可写（`docker exec opsmonitor ls -la /opt/opsmonitor/data`）
- [ ] 容器内时区正确（`docker exec opsmonitor date` 与本机一致）
- [ ] **默认密码与通信密钥已修改**

### 7.2 采集端

- [ ] 在目标主机 `curl http://服务端IP:9999/` 能返回页面
- [ ] 「添加主机」页选到目标系统 + 架构时**没有红色告警**
- [ ] 安装脚本执行无报错
- [ ] 主机出现在「主机监控」列表，状态为在线
- [ ] 指标在1 分钟内开始刷新

### 7.3 告警与可视化

- [ ] 「系统设置 → 邮件」能发出测试邮件（或配置机器人并点「测试」）
- [ ] 新建一个监控项，确认能正常出结果
- [ ] 大屏能正常打开，`/screen/1` ~ `/screen/8` 各模板均无报错
- [ ] 大屏全屏投屏正常（`F` 或 F11）

### 7.4 备份

- [ ] 已配置定时备份
- [ ] **至少完整演练过一次恢复**

---

## 八、备份与恢复

### 8.1 备份

全部状态都在一个 SQLite 文件里，备份就是备份这个目录。

**方式一：停机冷备（最可靠）**

```bash
docker compose stop opsmonitor
tar czf /backup/opsmonitor-$(date +%F).tar.gz -C /opt/opsmonitor data
docker compose start opsmonitor
```

**方式二：在线热备（不停服）**

```bash
sqlite3 /opt/opsmonitor/data/opsmonitor.db \
  ".backup '/opt/opsmonitor/opsmonitor-$(date +%F).db'"
```

> 直接 `cp` 正在写入的 `opsmonitor.db` 可能拿到不一致快照。
> WAL 模式下要连`db-wal`、`db-shm` 一起复制，或用上面的 `.backup` 命令。

**方式三：备份到远端**

```bash
rclone copy /backup/opsmonitor-*.tar.gz remote:bucket/opsmonitor/
```

### 8.2 定时备份

```bash
# 每天 03:17 冷备一次，保留 14 天
17 3 * * * cd /opt/opsmonitor && \
  docker compose stop opsmonitor && \
  tar czf /backup/opsmonitor-$(date +\%F).tar.gz data && \
  docker compose start opsmonitor && \
  find /backup -name 'opsmonitor-*.tar.gz' -mtime +14 -delete
```

### 8.3 恢复

```bash
docker compose stop opsmonitor
tar xzf /backup/opsmonitor-2026-10-11.tar.gz -C /opt/opsmonitor
chown -R opsmonitor:opsmonitor /opt/opsmonitor/data
docker compose start opsmonitor
```

### 8.4 恢复演练

**备份文件存在不等于能恢复**。建议每季度在测试环境完整走一遍恢复流程并记录耗时，
恢复到「能看到历史数据」为止。

---

## 九、升级与回滚

### 9.1 服务端升级

```bash
# 备份
cd /opt/opsmonitor && tar czf /backup/pre-upgrade-$(date +%F).tar.gz data

# Docker
docker compose pull && docker compose up -d
docker compose logs -f --tail=50

# 二进制
sudo systemctl stop opsmonitor
sudo cp opsmonitor-server opsmonitor-server.bak
sudo cp 新版/opsmonitor-server /opt/opsmonitor/opsmonitor-server
sudo systemctl start opsmonitor
```

> **自 1.1.3 起，自动升级会自行重启生效**，无需手工 `systemctl restart`。若是**手工替换二进制**升级（例如离线环境），务必按下面步骤操作，尤其是第 2 步：
>
> 1. 备份旧二进制：`sudo cp /opt/opsmonitor/opsmonitor-server /opt/opsmonitor/opsmonitor-server.bak`
> 2. **`sudo rm -f /opt/opsmonitor/.opsmonitor-server.pending`** —— 不清掉的话，启动时的确认逻辑会认为上次升级未生效，从而回滚到 `.bak`，表现为「换了又变回旧版」
> 3. 替换二进制并 `sudo chmod +x /opt/opsmonitor/opsmonitor-server`
> 4. `sudo systemctl restart opsmonitor`
> 5. `/opt/opsmonitor/opsmonitor-server -version` 校验版本
>
> 说明：`.opsmonitor-server.pending` 内容为「已就位待确认的新版本号」；新进程启动时若发现它与当前版本一致就清掉并记「已升级生效」，不一致则回滚 `.bak`。前端通过 `//go:embed all:web` 编译进二进制，**升级只需替换那一个二进制文件**，不需要单独更新 `.js` / `.go` 文件。自 1.1.2 起内嵌前端统一 `Cache-Control: no-cache`，升级后建议浏览器 Ctrl+F5 一次。

数据库结构变更由程序在启动时自动执行（新增表与列都是幂等的），**不需要手动执行 SQL**。

### 9.2 采集端升级

采集端内置更新器：默认每 30 分钟查询版本，发现新版本自行
下载 → **校验 SHA256** → 替换二进制 → 重启，全程不需登录目标主机。

- 由 systemd / launchd 托管时直接退出交给服务管理器拉起，不会同时跑两个进程
- 替换走「旧文件改名 → 新文件落位」的原子流程，Windows 上正在运行的 exe 也能安全替换
- 校验不过直接中止并保留原进程；替换失败会回滚旧文件

### 9.3 灰度与回滚

采集端发布物按「系统 + 架构」分别登记，同一平台可保留多个版本，其中一个是**下发版本**：

- **灰度**：先只给测试平台设为下发版本，观察一段时间再推生产
- **回滚**：在「系统 → 在线升级」中把旧版本重新设为下发版本，采集端下个检查周期自动降级

> 上传发布物时只有「不低于当前下发版本」才会被设为下发版本，误传旧版本不会把已下发版本顶掉。

---

## 十、安全加固建议

| 项| 建议 |
| --- | --- |
| 默认密码 | 首次部署立即改 `-pass`，并删除默认弱口令痕迹 |
| 通信密钥 | 改 `-token` 为高强度随机串；接入命令只用 MD5 指纹 |
| 网络暴露 | 9999 不暴露公网；跨网段走内网或 VPN |
| HTTPS | 反向代理启用 TLS，同时设置 `OPSM_PUBLIC_URL=https://…` |
| 密钥文件 | `/etc/opsmonitor/opsmonitor.env` 权限 600 |
| 账号权限 | 日常运维用 `operator`，只读用 `viewer`，不要共用 admin |
| 审计 | 定期查看「审计日志」，覆盖登录、指令下发、配置变更 |
| 开放接口 | `/openapi/v1/*` 用独立 Token，不复用控制台密码 |
| 数据目录 | 不放在 Web 可访问路径下 |

---

## 十一、排障手册

### 11.1 部署类

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| 其他主机打不开控制台 | 只监听了回环地址 | 改 `-addr :9999`；启动日志会主动提示 |
| 采集端装不上 | 目标主机访问不到服务端地址 | 先在目标主机 `curl http://服务端IP:9999/` 自测 |
| `服务端尚未发布 xx/xx 的采集端版本` | 该「系统 + 架构」无发布物 | 「添加主机」页查看已发布平台，按系统+架构分别发布 |
| Windows 报「无法连接到远程服务器」 | 地址填了 `127.0.0.1` 或防火墙未放行 | 换成目标机可达地址；放行 9999/TCP |
| `需要管理员权限` | Windows 安装脚本要求管理员 PowerShell | 右键「以管理员身份运行」 |
| `Start-ScheduledTask: 找不到指定的文件` | 安装脚本没执行成功 | 先解决脚本本身的报错 |
| Linux 提示未找到 curl | 精简镜像缺工具 | `yum install -y curl` 或 `apt-get install -y curl` |
| Linux 提示不支持的平台 / 架构 | 平台不是 linux/darwin，或架构不是 amd64/arm64 | 检查目标系统 |
| **重启后数据全丢** | `data` 目录没挂载 | 按挂载清单挂上 `/opt/opsmonitor/data` |
| 告警时间差 8 小时 | 容器时区是 UTC | 设 `TZ=Asia/Shanghai` 并挂载 `/etc/localtime` |

### 11.2 数据类

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| 主机显示离线但实际在线 | 采集端没在跑，或网络不通 | 检查采集端服务状态；确认能访问 9999 |
| 指标全是 0 | 采集端刚重启，首轮缺基准 | 磁盘 IO 与网速第二轮上报起生效 |
| 温度 / SMART 显示「未采集」 | 虚拟机/云主机无对应硬件 | 属正常现象，不影响判定 |
| Windows 负载恒为 0 | Windows 无 loadavg 机制 | 属正常现象，页面已标注 |
| 监控项一直正常但没数据 | 目标配置写错 | 看监控项最近一次消息，失败原因会写明 |
| 告警不推送 | 级别过滤 / 免打扰时段 / 未启用 | 检查系统设置里的邮件与机器人配置，可点「测试」 |
| 中文 Windows 输出乱码 | 控制台 GBK 编码 | 采集端已自动转码，若仍异常检查终端编码 |

### 11.3 授权中心类

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| 同步返回 **403 + `Just a moment...`** | Cloudflare 人机校验拦下了程序客户端 | 见第〇章，四种方案任选；这是最常见的一种 |
| 同步返回 401 且提示 `拉取令牌不正确` | 授权中心当前配置的令牌与平台发出的不一致 | 授权中心的令牌已改为编译内置，核对两侧内置值是否一致；或临时用 `-hub-token` / `OPSM_HUB_TOKEN` 覆盖 |
| 同步返回 HTML 而非 JSON | `-hub` 指向了网页而不是接口 | 地址应为默认授权中心地址（程序内置），**不要**带 `/api/v1/check` |
| 一直「尚未为本机签发授权」 | 授权中心还没给本机机器码签发 | 拿控制台「授权购买」页显示的机器码，去授权中心后台签发 |
| 授权导入后校验失败（提示签名校验失败） | 公钥抄错，或旧格式密钥不一致 | 新格式（`OPSM2-`）看启动日志的「授权验签公钥指纹」是否与授权中心一致，不一致就抄错公钥了；旧格式（`WGC-`/`OPSM1-`/`OPSM-`）才需要两端 `-license-secret` 一致 |
| 主机数超限被拒接入 | 授权档位主机数已用满 | 换更高档位授权；存量主机不受影响 |
| 日志反复刷「授权系统同步失败」 | 授权中心不可达 | 已自动退避（1→2→4→…分钟，上限 30 分钟），不用管 |
| 检测到新版本但不升级 | `-hub-auto-update` 设成了 `check` 或 `off` | 改回 `download`，或在控制台手动点「拉取并升级」 |
| 新版本就位后没生效 | 还没重启，或重启后起不来 | 手工重启；起不来会自动回滚到 `.bak` |

**排查授权中心连通性：**

```bash
# 在监控端所在机器上执行，期望 200
curl -sS -o /dev/null -w '%{http_code}\n' \
  "https://<你的授权中心地址>/api/v1/check?os=linux&arch=amd64&token=<你的-hub-token>"

# 看响应体，正常的应该是 JSON；返回 HTML 就是网关拦截
curl -sS "https://<你的授权中心地址>/api/v1/check?os=linux&arch=amd64&token=<你的-hub-token>" | head -c 300
```

### 11.4 排查顺序

```bash
# 1. 服务端日志
docker compose logs --tail=200 opsmonitor
journalctl -u opsmonitor -n 200

# 2. 采集端日志（Linux）
journalctl -u opsmonitor-agent -n 200
# Windows
Get-ScheduledTaskInfo -TaskName opsmonitor-agent | Select-Object LastRunTime, LastTaskResult

# 3. 连通性自测（在目标主机执行）
curl http://服务端IP:9999/

# 4. 数据库完整性
sqlite3 /opt/opsmonitor/data/opsmonitor.db "PRAGMA integrity_check;"

# 5. 磁盘占用
du -sh /opt/opsmonitor/data/*
```

再看控制台的**「操作审计」**确认配置改动是否生效。

---

## 十二、常见问题

**Q：可以接多少台主机？**
A：SQLite 单写入的设计下，实测 1000 台主机稳定运行。更大规模建议按业务拆分多实例。

**Q：为什么不用 MySQL？**
A：为了「两个二进制 + 一个文件」的部署体验。读写连接池分离 + 异步批量事务 + SQL 层聚合降采样，
已能支撑上述量级。需要 MySQL 语义（如复杂报表）时可把历史数据定期导出到独立分析库。

**Q：采集端需要目标主机开放端口吗？**
A：不需要。采集端是主动向服务端上报（出向 9999/TCP）。监控项探测也是服务端主动发起。

**Q：大屏可以给多块屏吗？**
A：可以。8 套模板各有独立地址，例如主屏 `/screen/2`、副屏 `/screen/5`，
每块屏浏览器按 F11 全屏即可。

**Q：如何完全离线运行？**
A：默认就是离线模式。不配置 `-hub` 时不会访问任何外部服务，采集端升级也从本地`releases/` 取。

**Q：升级会丢数据吗？**
A：不会。数据库结构变更都是幂等的（`CREATE TABLE IF NOT EXISTS` / `ALTER TABLE ADD COLUMN`），
且升级前建议先按 8.1 备份。

---

## 十三、目录约定建议

生产环境建议按下面的目录组织，便于备份与迁移：

```
/opt/opsmonitor/     应用与数据
  ├── opsmonitor-server
  ├── data/
  └── releases/
/etc/opsmonitor/     配置与密钥（root-only）
  └── opsmonitor.env
/var/log/opsmonitor/ 日志（systemd journal 或文件）
/backup/             备份归档
```