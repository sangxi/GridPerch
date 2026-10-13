# OpsMonitor 监控端 · Linux 部署速查

面向「把监控端（server）部署到一台 Linux 服务器」的场景。监控端一个进程同时提供
**Web 控制台 + API + 服务端主动探测**（Ping/TCP/DNS/HTTP/SNMP/数据库…）。

> 完整方案见 `DEPLOY.md`（§3.4 Linux 二进制 + systemd）。本文是它的**可照抄速查版**，
> 路径、参数与当前构建产物对齐。

---

## 0. 先搞清楚三件事

| 事项 | 说明 |
|---|---|
| 依赖 | **零外部依赖**。SQLite 用的是纯 Go 实现的 `modernc.org/sqlite`，**不需要 glibc 之外的东西、不需要装 sqlite3、不需要 CGO**。一个静态二进制 + 一个可写目录就能跑 |
| 数据库 | **只有 SQLite**（`store.Open` 里固定 `sql.Open("sqlite", …)`），单文件 `data/opsmonitor.db`，WAL 模式。`go.mod` 里的 MySQL 驱动是给**探针探测 MySQL 目标**用的，不是本程序自身的存储 |
| 端口 | 默认 `:9999`（HTTP）。生产建议前面挂 Nginx/OpenResty 做 TLS 与域名 |

架构对应关系：`amd64` = x86_64，`arm64` = aarch64（树莓派 4/鲲鹏/Apple Silicon 的 Linux）。

---

## 1. 上传二进制

在**本地开发机**（Windows，本仓库根目录）执行：

```bash
scp dist/opsmonitor-server-linux-amd64 root@<监控端IP>:/tmp/opsmonitor-server
```

当前产物校验值（传完可核对）：

```
# md5
dd2ec410604b12b38e0642c60e4d796f  opsmonitor-server-linux-amd64
5e6d0063a4bdb75317eeda426e1e3c93  opsmonitor-agent-linux-amd64      # 采集端，见 §7

# sha256
ca300e62de113f9324d3877f92ff479736cbfdcabbb7e8289b2add8e4345495e  opsmonitor-server-linux-amd64
b66bab0d7a314874e62b27a9bfbbc365e370f8c4bee29b391f4f87d79d79da1b  opsmonitor-agent-linux-amd64
```

> 需要别的架构（arm64 等）时在本仓库根目录执行 `./build.sh all`，一次产出 5 个平台 × 2 角色
> 共 10 个文件到 `dist/`，命名形如 `opsmonitor-{server,agent}-{goos}-{goarch}[.exe]`
> （与 `release.sh` 上传授权平台时使用的名字一致）。

---

## 2. 建立目录并前台试跑

```bash
sudo mkdir -p /opt/opsmonitor/data
sudo mv /tmp/opsmonitor-server /opt/opsmonitor/
sudo chmod +x /opt/opsmonitor/opsmonitor-server
cd /opt/opsmonitor && ./opsmonitor-server -version
```

前台跑一次，**看到下面这几行才算对**（`Ctrl+C` 退出）：

```bash
./opsmonitor-server -addr :9999 -db /opt/opsmonitor/data/opsmonitor.db
```

```
opsmonitor-server v1.1.5，控制台账号：admin / opsmonitor（首次启动后请在启动参数中修改）
授权验签公钥指纹：278D-4852-7191-E7CE（应与授权中心启动日志一致）
服务端启动：监听 :9999，数据库 /opt/opsmonitor/data/opsmonitor.db，数据保留 30 天
控制台访问地址：http://<本机IP>:9999
```

⚠️ **指纹必须是 `278D-4852-7191-E7CE`**。如果打印的是别的值，说明你拿到的二进制不是当前
源码编出来的（旧版曾内置错误的公钥，会导致授权码一律验不过）——重新取最新构建。

---

## 3. 低权限用户 + 环境文件

密码、通信密钥走环境文件，不落命令行、不进 `ps`：

```bash
sudo useradd -r -s /sbin/nologin opsmonitor 2>/dev/null || true
sudo mkdir -p /etc/opsmonitor

sudo tee /etc/opsmonitor/opsmonitor.env >/dev/null <<'EOF'
OPSM_ADDR=:9999
OPSM_DB=/opt/opsmonitor/data/opsmonitor.db
OPSM_TOKEN=换成一段强密钥
OPSM_ADMIN_USER=admin
OPSM_ADMIN_PASS=换成强密码
OPSM_PUBLIC_URL=http://<监控端IP>:9999
OPSM_RETENTION=30
EOF

sudo chmod 600 /etc/opsmonitor/opsmonitor.env
sudo chown -R opsmonitor:opsmonitor /opt/opsmonitor
```

- `OPSM_TOKEN`：**采集端要与它一致**（采集端用 `-token` 或 `-token-hash`）。全部改掉默认值。
- `OPSM_PUBLIC_URL`：写对外可访问的地址；一键安装脚本、采集端更新下载都会用它。会在反向代理后面时写外网/内网真实入口。

---

## 4. 注册 systemd

```bash
sudo tee /etc/systemd/system/opsmonitor.service >/dev/null <<'EOF'
[Unit]
Description=OpsMonitor 运维监控服务端
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
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/opt/opsmonitor/data /opt/opsmonitor/releases

[Install]
WantedBy=multi-user.target
EOF

sudo mkdir -p /opt/opsmonitor/releases
sudo systemctl daemon-reload
sudo systemctl enable --now opsmonitor
systemctl status opsmonitor --no-pager
```

常用运维命令：

```bash
journalctl -u opsmonitor -f          # 实时日志
systemctl restart opsmonitor         # 重启（改完 env 必须重启）
sudo systemctl stop opsmonitor       # 停止
```

---

## 5. 放行端口

```bash
# CentOS / RHEL / 欧拉
sudo firewall-cmd --permanent --add-port=9999/tcp && sudo firewall-cmd --reload

# Ubuntu / Debian
sudo ufw allow 9999/tcp

# 宝塔 / 1Panel：在面板「安全」里再放行一次 9999
```

云服务器（阿里云/腾讯云）**还要在安全组放行**，否则面板通了也进不去。

放行后验证：

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:9999/api/health   # 期望 200
```

---

## 6. 打开控制台并导入授权

浏览器访问 `http://<监控端IP>:9999`，账号 `admin / <OPSM_ADMIN_PASS>`。

进「**系统 → 授权购买**」：

1. 页面会显示**本机机器码**（形如 `C317-6E8B-BAE5-A8A6`）；
2. 优先点「**立即从授权系统拉取授权**」——监控端每 5 分钟自动同步一次，授权中心签发的码会自动装上；
3. 走不通时，把授权中心的授权码整串粘到「**手动导入授权码**」→「导入并校验」。

成功后页面顶部应显示档位（如 **旗舰版**）与到期日，且下方「同步状态」为**已获取授权**。

坑：

- **机器码会变**：换网卡、换主机名、容器重建都可能变 → 授权失效。多网卡/容器场景用
  `-license-machine <固定值>` 把机器码钉死（加进 `opsmonitor.env`：`OPSM_LICENSE_MACHINE=xxx`）。
- 监控端**只验签、不签发**。二进制里只有公钥，反编译也造不出授权码。
- 完全离线（不与授权中心通信）：用启动参数 `-hub=`（等号后留空）。**注意不能用环境变量
  `OPSM_HUB=` 来关**——`config.env()` 对空串会回落到默认授权中心地址（程序内置的授权站）。
  例如把单元文件的 `ExecStart=` 改成：
  `/opt/opsmonitor/opsmonitor-server -hub=`（此时授权码只能手动导入）。

---

## 7. 接入采集端

### 7.1 一键安装（推荐）

控制台「**监控 → 添加主机**」→ 选系统 `linux`、架构 `amd64`、填主机名 → 复制命令到目标主机执行：

```bash
curl -fsSL "http://<监控端IP>:9999/install.sh?wgToken=<MD5>&name=web-01" | sudo bash
```

脚本会自动：识别系统架构 → 从平台拉取对应二进制 → **校验 SHA256** → 装到
`/usr/local/bin/opsmonitor-agent` → 注册 `opsmonitor-agent.service` 开机自启。
通信密钥以 **MD5 指纹**下发，目标主机的命令行历史和单元文件里都不留明文口令。

⚠️ **前提**：平台里必须已发布过 `linux/amd64` 的采集端。当前授权系统的下载接口返回 401，
自动拉取发布物不生效，需要**手工上传**：控制台「系统 → 在线升级」→ 上传
`dist/opsmonitor-agent-linux-amd64` → 版本号填 **1.1.5**（必须与二进制内嵌版本一致）→
选 `linux` / `amd64` → 设为该平台下发版本。

### 7.2 手动安装（不依赖平台下载）

```bash
scp dist/opsmonitor-agent-linux-amd64 root@<目标主机>:/tmp/opsmonitor-agent
```

```bash
sudo install -m 0755 /tmp/opsmonitor-agent /usr/local/bin/opsmonitor-agent

sudo tee /etc/systemd/system/opsmonitor-agent.service >/dev/null <<'EOF'
[Unit]
Description=OpsMonitor 采集端
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/opsmonitor-agent -server http://<监控端IP>:9999 -token 换成同一段强密钥 -name web-01
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload && sudo systemctl enable --now opsmonitor-agent
journalctl -u opsmonitor-agent -f     # 应看到上报成功
```

说明：

- `-token` 与监控端的 `OPSM_TOKEN` 必须一致；也可改用 `-token-hash <token 的 MD5>` 避免明文。
- 采集间隔 `-interval`、是否允许执行指令 `-allow-command`、自动更新策略 `-update` 等，
  默认会被平台「采集端配置」**热更新**覆盖，不用改单元文件。
- 只采集本机指标时用 `-name` 区分主机名。

---

## 8. 数据库说明与调优

监控端**只用 SQLite**，无需装数据库、无需建库授权。数据全在 `data/opsmonitor.db` 一个文件里，
备份/迁移就是拷这个目录（见 §9）。

单机压测经验：几百台主机以内无需调整。上报量大或写入告警时，按需调大这几个参数
（写进 `opsmonitor.env`）：

```
OPSM_READ_CONNS=8          # 读连接池，查询多时调大
OPSM_WRITE_QUEUE=8192      # 上报写队列，突发流量大时调大
OPSM_PURGE_INTERVAL=3600   # 历史数据清理间隔（秒）
OPSM_RETENTION=30          # 历史保留天数，省磁盘就调小
```

> 用 `-db` 只能改 SQLite 文件路径（如放到更大的数据盘），不能切成 MySQL。
> `go.mod` 里的 `go-sql-driver/mysql` 只服务于「数据库监控项」，与被监控端自身的存储无关。

---

## 9. 升级 / 备份 / 回滚

```bash
# 升级：替换二进制后重启（配置在 /etc，不会被覆盖）
sudo systemctl stop opsmonitor
sudo cp -a /opt/opsmonitor/opsmonitor-server /opt/opsmonitor/opsmonitor-server.bak
sudo mv /tmp/opsmonitor-server.new /opt/opsmonitor/opsmonitor-server
sudo systemctl start opsmonitor

# 回滚
sudo cp -a /opt/opsmonitor/opsmonitor-server.bak /opt/opsmonitor/opsmonitor-server
sudo systemctl restart opsmonitor

# 冷备（SQLite 有 WAL，停服后再拷最稳妥）
sudo systemctl stop opsmonitor
sudo tar czf /root/opsmonitor-$(date +%F).tar.gz -C /opt/opsmonitor data
sudo systemctl start opsmonitor
```

> **自 1.1.3 起，自动升级会自行重启生效**，无需手工重启。手工替换二进制升级时务必先清掉待确认标记，否则会回滚成旧版：
> 1. `sudo cp -a /opt/opsmonitor/opsmonitor-server /opt/opsmonitor/opsmonitor-server.bak`
> 2. **`sudo rm -f /opt/opsmonitor/.opsmonitor-server.pending`**（不清会回滚到 `.bak`，表现「换了又变回旧版」）
> 3. `sudo mv /tmp/opsmonitor-server.new /opt/opsmonitor/opsmonitor-server && sudo chmod +x /opt/opsmonitor/opsmonitor-server`
> 4. `sudo systemctl restart opsmonitor`
> 5. `/opt/opsmonitor/opsmonitor-server -version` 校验
>
> 自检就位：`.opsmonitor-server.pending` 写的是「已就位待确认的新版本号」，新进程启动发现与当前版本一致就清掉并记「已升级生效」，不一致则回滚 `.bak`。前端编译进二进制，**只换这一个文件即可**。可用 `-hub-restart off`（`OPSM_HUB_RESTART=off`）关闭自动重启、改为人工重启。

---

## 10. 排障对照表

| 现象 | 排查方向 |
|---|---|
| `address already in use` | `ss -tlnp \| grep 9999` 找出占用进程；换端口用 `-addr :8000` |
| 页面能开但接口 502 / 静态资源 404 | 反向代理少配了 `proxy_set_header Host`；或 `OPSM_PUBLIC_URL` 与实际入口不一致 |
| 启动日志「授权验签公钥指纹」不是 `278D-4852-7191-E7CE` | 二进制不是最新源码构建的 → 重新取产物 |
| 授权码导入报「签名校验失败」 | ①公钥指纹核对；②确认授权码是从授权中心页面**完整**复制（中途截断/带省略号会直接验签失败） |
| 授权码导入成功但仍显示社区版 | 「授权购买」页点「立即从授权系统拉取授权」；确认机器码与授权中心签发时绑定的机器码一致 |
| 启动日志「授权系统同步失败 401」 | 监控端到默认授权中心的网络/DNS 不通，或授权端令牌问题。不影响控制台与采集功能；离线也能跑 |
| 采集端装不上 | 平台是否已发布该「系统 + 架构」的采集端版本（见 §7.1），以及目标主机能否访问监控端 9999 |
| 主机一直「离线」 | 目标主机到监控端 9999 的连通性；`-token` 是否一致；`journalctl -u opsmonitor-agent` |
| SQLite 写入告警 / 上报延迟 | 调大 `OPSM_READ_CONNS`、`OPSM_WRITE_QUEUE`（见 §8）；确认 `data/` 所在磁盘不是慢盘、有足够空间 |
