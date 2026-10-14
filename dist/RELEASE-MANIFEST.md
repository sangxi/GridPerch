# OpsMonitor 发版清单

- 版本号：**1.1.6**（服务端与采集端共用，取自 `internal/version/version.go`）
- 构建时间：**2026-10-14T11:07:52**（由 `build.sh` 注入 `version.BuildTime`，10 个产物一致）
- 构建方式：`bash build.sh all`（`CGO_ENABLED=0` 静态交叉编译，纯 Go SQLite，无动态库依赖）
- 产物目录：`D:\c\xin`（源 `D:\c\opsmonitor\dist`）
- 自检：`gofmt -l internal cmd` 无输出 · `go vet ./...` 干净 · `go test ./...` 全过 ·
  `sha256sum -c SHA256SUMS.txt` **10/10 OK**

## 本次更新内容

### 1. 修复：大屏图表「打开约 10 秒后全部空白」（本次重点）
**现象**：任意大屏模板（`/screen/1` ~ `/screen/8`）刚打开时图表正常显示，约 10 秒后
（正好一个自动刷新周期）**KPI 数字仍在跳动、所有图表面板却一起变成空白**。

**根因**：`internal/server/web/assets/screen.js` 里 `render()` 每次刷新都执行
`stage.innerHTML = built.html`，把整块页面 DOM 重建，新旧图表容器是两个**不同的 DOM 节点**；
而 `destroyCharts()` 只在**切换模板**时才调用，`charts[id]` 中的 ECharts 实例被一直复用。
修复前的 `getChart()` 只用 `!c || c.isDisposed()` 判断是否需要重新 init——容器被重建后
实例既没被 dispose、`isDisposed()` 也仍是 `false`，于是新数据被 `setOption` 到
**已经脱离文档的旧 canvas** 上，画面上自然什么都没有。首屏之所以正常，是因为那时
`charts` 还是空的，会 init 到新 DOM 上；`REFRESH_MS = 10000` 正是那个「10 秒」。

**修复**：`getChart()` 先取当前容器 `dom`，只要「实例已销毁」或「容器节点已换人」
（`c.getDom() !== dom`）就先 `dispose()` 再重新 `init()`，保证每次 `setOption` 都作用在
当前在文档里的 canvas 上。全局 `echarts.init` 只有这一处，12 个 `draw*` 函数全部经由
`getChart()`，因此是单点修复、8 套模板同时生效。

> 只影响服务端内嵌的前端资源。换完二进制后浏览器**强刷一次（Ctrl+F5）**，避免仍缓存旧 `screen.js`。

### 2. 授权站地址收口：不再出现在任何提示文案里
- `-hub` 的帮助文案不再打印具体地址，改为「不传则使用内置地址；传空（`-hub=`）则完全离线运行」；
- **顺带堵住一处更隐蔽的泄露**：Go 的 `flag` 包会自动把**非空默认值**回显成
  `(default "…")`，原先 `-hub` 用内置地址作默认值，导致 `-h` 输出里仍然出现该地址。
  现改为「空默认值注册 + 解析后用 `flag.Visit` 判断是否显式传入」，
  语义完全不变（不传=用内置地址，传空=完全离线），但 `-h` 里不再出现任何地址；
- 平台「立即检查」在离线模式下的报错文案同样不再出现具体地址；
- `config.DefaultHubURL` 由 `const` 改为 `var`，构建时可用
  `-ldflags "-X github.com/sangxi/OpsMonitor/internal/config.DefaultHubURL=..."` 覆盖或清空。

> 效果：服务端产物里该地址由 **3 处降为 1 处**（只剩功能上必需的默认值）。
> **说明**：只要平台要「开箱即能拉更新与授权码」，该地址就必须存在于二进制中，
> 无法既默认连授权系统又让二进制里完全没有它。若要连这 1 处也不留，就用上面的
> ldflags 传空值构建（代价：默认离线，必须显式 `-hub` 才启用授权与在线更新）。

### 3. 文档
控制台内置教程（`/docs`）「可视化大屏」章节新增「图表变成空白？升级到 1.1.6」说明。

### 4. 沿用
- 1.1.5：一键安装脚本支持中文等任意语言文字作主机名；新增**主机重命名**与**删除主机**。
- 1.1.4：控制台不暴露授权中心域名。
- 1.1.3：平台自升级后自动重启，失败自动回滚 `.bak`。

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
/opt/opsmonitor/opsmonitor-server -version           # 应显示 1.1.6
```

> 采集端二进制**不需要**跟着换：本次改动全在服务端。

## 部署（Docker Compose）

```bash
docker compose cp opsmonitor-server-linux-amd64 opsmonitor:/usr/local/bin/opsmonitor-server
docker compose restart opsmonitor
```
容器内 `/.dockerenv` 被识别为「托管环境」，后续自升级会以退出→compose 重启的方式生效，
因此 **compose 里必须有 `restart: unless-stopped`（本仓库模板已带）**。

## 在线升级（授权端）

1.1.6 > 授权端现有版本。要让面板的在线升级链可用，把 1.1.6 登记到授权系统：

```bash
./release.sh --with-server -s <你的授权中心地址> -v 1.1.6 -n "修复大屏图表刷新后空白；收口授权站地址提示文案"
```

> 登记版本号必须等于二进制内嵌版本（`1.1.6`），否则会「已是最新拉不下来」或每 5 分钟反复重下。

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
- 上传时的版本号必须与二进制内嵌版本一致（`1.1.6`）。
- 采集端按「系统 + 架构」分别登记，漏掉某个平台的表现是该平台主机安装采集端时提示「尚未发布」。

## 本次验证记录

- `gofmt -l internal cmd` 无输出；`go vet ./...` 干净；`go test ./...` 全过。
- **大屏修复的无头浏览器 A/B 验证**（真实二进制 + 真实采集端上报，模板 2「KPI 矩阵」）：
  1. 修复后 t≈3s 与 t≈28s（跨过 2 次自动刷新）**6 个图表 canvas 全部存在**，尺寸正常；
  2. **反证**：把 `getChart` 换回修复前的旧实现再刷新 → 6 个图表全部 `NO-CANVAS`，
     截图与用户报障的空白画面完全一致；
  3. 还原修复版再刷新 → 6 个图表全部恢复；全程 `pageerror` / `console error` 均为 0。
- 地址收口核验：服务端 5 个产物中 `1999091.xyz` 出现次数由 **3 → 1**（仅保留功能默认值）；
  `-h` 帮助输出中该地址出现 **0 次**；采集端 5 个产物始终为 0。
