# Strix 发版清单

- 版本号：**1.2.3**（服务端与采集端共用，取自 `internal/version/version.go`）
- 主题：**旧系统兼容 + 同步诊断 —— 采集端双产物通道 / Windows Server 2012 R2 可用 / 502 不再误报地址写错**
- 构建时间：**2026-10-20T17:51:46**（由 `build.sh` 注入 `version.BuildTime`，10 个正式产物一致）
- 构建方式：`bash build.sh all`（`CGO_ENABLED=0` 静态交叉编译，纯 Go SQLite，无动态库依赖）
  \+ `bash build-win7.sh`（Go 1.20.14 + `go120.mod`，出旧系统兼容版）
- 自检：`gofmt -l internal cmd` 无输出 · `go vet ./...` 干净 · `go test ./...` 六个包全过 ·
  `sha256sum -c SHA256SUMS.txt` 11/11 通过 · 服务端产物可 `grep` 到真公钥 `uZhJVTm2`（指纹 `278D-4852-7191-E7CE`）
- 兼容版自报：`strix-agent 1.2.3 (windows/amd64) 旧系统兼容版 构建于 2026-10-20T17:52:22`
  正式版自报：`strix-agent 1.2.3 (windows/amd64) 构建于 2026-10-20T17:51:46`

## 本次更新内容

> 1.2.3 有**库结构变更**（`release` 表新增 `variant` 列，唯一索引由 3 列扩到 4 列），
> 但由 `migrateExtra()` 在启动时自动补列并重建索引，老库直接沿用、无需手工迁移。

### 1. 采集端「旧系统兼容版」独立发布通道

**要解决的问题**：Go 1.21 起官方不再支持 Windows 7 / 8 / 8.1 / Server 2008 R2 / 2012 / 2012 R2。
这些机器上双击正式版采集端**没有任何窗口也没有任何日志**——进程在 runtime 初始化阶段就退出了
（随机数源改用 `ProcessPrng`，依赖 Win8/S2012 才有的 `bcryptprimitives.dll`；同时砍掉了
`WSASocket`、`LoadLibraryEx` 的旧系统回退）。最后能编出可运行产物的工具链是 **Go 1.20**。

在此之前，兼容产物只能靠手工分发，而且**平台下发的全局 `updateMode=auto` 会在 30 分钟内
把它换回 Go 1.23 的正式版，于是又开始闪退**——必须加 `-remote-config off -update off` 才稳。
1.2.3 从协议层面解决了这件事：

- **`release` 表新增 `variant` 列**：`''` = 正式版，`'legacy'` = 旧系统兼容版。
  唯一索引从 `(version, goos, goarch)` 扩到 `(version, goos, goarch, variant)`，
  于是**同一个版本号下正式版与兼容版并存**，互不覆盖，各有各的 `is_latest`
- **控制台「版本发布」页新增本地上传面板**：「版本号 / 系统 / 架构 / 文件 / 说明 /
  旧系统兼容版 / 设为最新」。以前只能从授权中心同步，现在离线环境也能自建发布物
- **兼容版自报身份**：`build-win7.sh` 通过 `-ldflags -X version.LegacyWindows=1` 注入标记，
  采集端查版本与自升级时自动带 `legacy=1`；平台据此**只回兼容版**，
  所以「自动升级把兼容版换回正式版」这个坑从协议上消失
- **一键安装脚本按系统版本自动选**：PowerShell 里读 `[Environment]::OSVersion.Version`，
  `Major < 10` 即判定旧系统，拼上 `&legacy=1`，并在控制台打印「将安装『旧系统兼容版』采集端」
- **`-version` 会打印「旧系统兼容版」**，运维一眼能看出手上跑的是哪一套
- 平台只有正式版、旧系统来一键安装时，会明确提示「尚未发布该平台的『旧系统兼容版』采集端」，
  而不是静默装一个跑不起来的包

> **从 1.2.3 起，旧系统采集端不再需要 `-remote-config off -update off`。**
> 仍在跑 1.2.2 及更早兼容版的机器，升级到 1.2.3 的兼容版后即可去掉这两个参数。

### 2. 授权中心同步的报错口径重做

**要解决的问题**：反向代理返回 502 时，旧代码先看 `Content-Type` 再看状态码，
一看是 `text/html` 就判定「`-hub` 指向的可能不是接口地址。正确地址应形如……」（而且那句示例是空的）。
网关挂了被说成用户地址填错，排查方向直接被带偏。

现在按下面的顺序判定，并且把证据一并打出来：

1. **Cloudflare 挑战页** → 提示换 UA / 放行
2. **401 / 403** → token 不对
3. **502 / 503 / 504 → 网关错误**：「请求已经到达反向代理/CDN，但它没能从上游的授权中心拿到应答 ——
   **这不是 `-hub` 地址写错，地址本身是通的**」，并给出四处排查点：
   ① 授权中心进程是否在跑 ② `proxy_pass` 端口是否一致
   ③ 上游是 HTTPS 时是否加了 `proxy_ssl_server_name on;` ④ 中心侧日志有没有收到请求
4. **200 + HTML** → 才是「地址落到了静态站点上」
5. **404 + HTML** → 路径不对
6. 其余 → 兜底

同时附带**响应页面标题**、`Server` / `Via` 头、**本次请求 URL**（`token` 脱敏成 `token=***`），
据此能直接看出卡在哪一层。另外 `-hub` 误填了 `/api/v1/check`、`/api/v1/report`、`/api/v1`
这类后缀会被自动削掉——这本来就是程序自己拼的，填了也不会错，但以前会真的拼成
`.../api/v1/check/api/v1/check` 然后返回 404。

### 3. 细节

- 启动日志里的版本行、`-version` 输出与页面页脚版本号统一取 `internal/version`，三处不会再对不上
- 授权中心上传发布物时，同一 `(版本, 平台, 架构, 通道)` 重复上传走 `ON CONFLICT DO UPDATE`，
  重传即覆盖，不会报唯一键冲突

## 兼容性

- **数据库**：`release` 表新增 `variant` 列，唯一索引 `uniq_release` 由 3 列重建为 4 列。
  全部由 `migrateExtra()` 启动时自动完成（旧行 `variant` 视为 `''` = 正式版），**老库直接沿用**
- **接口**：`/agent/version`、`/agent/download` 新增可选参数 `legacy=1`；
  不带该参数时行为与 1.2.2 完全一致（返回正式版），**旧采集端不受影响**
- **前端缓存**：前端是编译进二进制的，换完二进制请**强刷 Ctrl+F5**，否则浏览器/反向代理可能还在用旧 `app.js`
- **升级方式**：替换 `strix-server` 单个二进制并重启即可。
  采集端**不必强制升级**，但旧系统上的机器建议升到 1.2.3 兼容版以摆脱两个启动开关

## 产物（11 个）

| 角色 | 平台 | 文件 |
| --- | --- | --- |
| server | linux/amd64 · linux/arm64 · darwin/amd64 · darwin/arm64 · windows/amd64 | `strix-server-*` |
| agent | linux/amd64 · linux/arm64 · darwin/amd64 · darwin/arm64 · windows/amd64 | `strix-agent-*` |
| agent | windows/amd64（Win7 / 8.1 / Server 2008 R2 / 2012 / 2012 R2 兼容版，Go 1.20.14 构建） | `strix-agent-windows-amd64-win7.exe` |

> **兼容版不参与 `release.sh` 自动上传**（授权端按 `(kind, version, goos, goarch)` 登记，只存一份产物）。
> 兼容版走控制台「版本发布 → 采集端发布物（本地上传）」，填**与正式版相同的版本号**、
> 勾选「旧系统兼容版」上传一次即可，两个通道并存。

## 上一版（1.2.2）要点

全站界面收束（纯前端）：各列表操作列文字按钮改图标按钮（悬停有中文提示），主机表操作列 300px → 219px；
空白列表改为虚线提示条；仪表盘 6 张指标卡排满一行。后端接口、库结构、采集端逻辑与 1.2.1 完全一致。
