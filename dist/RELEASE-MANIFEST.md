# OpsMonitor 发版清单

- 版本号：**1.1.7**（服务端与采集端共用，取自 `internal/version/version.go`）
- 构建时间：**2026-10-14T14:35:25**（由 `build.sh` 注入 `version.BuildTime`，10 个产物一致）
- 构建方式：`bash build.sh all`（`CGO_ENABLED=0` 静态交叉编译，纯 Go SQLite，无动态库依赖）
- 产物目录：`D:\c\xin`（源 `D:\c\opsmonitor\dist`）
- 自检：`gofmt -l internal cmd` 无输出 · `go vet ./...` 干净 · `go test ./...` 全过 ·
  `sha256sum -c SHA256SUMS.txt` **10/10 OK** · 服务端产物可 `grep` 到真公钥 `uZhJVTm2`

## 本次更新内容

### 1. 修复：平台自升级「版本号永远不变 + 服务反复重启」（本次重点）

**现象**：控制台「在线升级」一直显示「可升级到 X」，平台版本号却永远停在原值；
服务按同步间隔（默认 5 分钟）反复重启；手动点「拉取并升级」经常弹出 **Failed to fetch**。

**根因**：授权端上登记的 1.1.7 **其实就是 1.1.6 的二进制**（`--skip-build` 复用了上一版
`dist/`，却用 `-v 1.1.7` 登记了新版本号）——两者 SHA256 完全一致（server
`aeb5a126…`、agent `34ce099f…`）。于是自升级陷入死循环：

1. 同步发现「登记版本 1.1.7 > 本机 1.1.6」→ 下载、替换、重启；
2. 重启后的二进制自报仍是 1.1.6，`confirmSelfUpgrade` 视作「已生效」清掉 pending；
3. 下一轮同步再次判定「有新版本」→ 重复 1~2，**每 5 分钟一轮**；
4. 手动点击时正好撞上重启窗口，连接被重置 —— 浏览器只看到 `Failed to fetch`。

**修复**：

- `hubPullSelf` 在替换前**先把新二进制跑一遍 `-version` 自检**：自报版本不高于当前运行版本
  就**拒绝替换**并给出可操作的报错（`授权系统下发的 X 自报版本为 Y，并不高于当前运行的 Z，
  已拒绝替换。授权端很可能把旧产物登记成了新版本号…`）。把「静默死循环」变成一条看得懂的报错；
- **采集端自更新同款守卫**：`internal/agent/update.go` 的 `applyUpdate` 在替换前也执行新二进制
  `-version` 自检，拒绝理由与平台侧一致。否则平台缓存了「假 1.1.7」的采集端包后，1.1.6 的采集端
  会每隔一个检查周期（默认 30 分钟）下载 6.7 MB + 重启一次，同样永不收敛；
  （服务端只在 `IsNewer(发布版本, 本机版本)` 为真时才下发升级，因此这道自检不会误伤正常升级）
- **自动路径与控制台手动点击共用一把 `TryLock`**：已有一轮在下载时，手动点击立刻返回
  「已有一轮升级正在下载并替换，请稍候再试」，不再并发抢同一个 `.new`/`.new.tmp` 文件，
  也不会被对方的重启掐断在途请求；
- 内嵌「使用教程」的授权中心排障表新增一条对应条目。

### 2. 发布流程防呆（防止事故复发）

- `build.sh all` 现在会把本次构建的版本号写入 **`dist/VERSION`**；
- `release.sh` 上传前比对 `dist/VERSION` 与本次 `-v`（默认取 `version.go`），不一致**直接中止**
  并提示「去掉 `--skip-build` 重新构建，或把 `-v` 改成实际版本号」。
  没有 `dist/VERSION`（旧产物）时跳过检查，保持向后兼容。

### 3. 版本号 1.1.6 → 1.1.7

## 升级方式

- **已部署的 1.1.6**：下一轮同步（≤5 分钟）自动拉取本版本、就位并重启生效；
  也可在「在线升级」页点「拉取并升级」。
- **手工部署**：只替换 `opsmonitor-server` 一个二进制后重启服务即可
  （Linux 默认 `/opt/opsmonitor/opsmonitor-server` + `systemctl restart opsmonitor`）。

> 前端内嵌资源有更新，升级后浏览器**强刷一次（Ctrl+F5）**。

## 本次验证（mock 授权中心 + 真二进制，`D:\c\_opstest5\pulltest\`）

| 用例 | 场景 | 结果 |
|---|---|---|
| A | 平台：标签 1.1.8、下发文件其实是 1.1.7 | 日志报「自报版本为 1.1.7，并不高于当前运行的 1.1.7，已拒绝替换」，exe 未动、无 `.new`/pending 残留 |
| B | 平台：标签 1.1.8、下发的确是 1.1.8 | 正常就位（exe 自报 1.1.8、`.bak`=旧版、pending=1.1.8） |
| C | 平台：慢速下载中手动点「拉取并升级」 | 返回「已有一轮升级正在下载并替换，请稍候再试」，随后自动路径正常收敛 |
| D | 采集端：平台缓存了假标签的「采集端 1.1.8」 | 采集端「发现新版本 1.1.8…开始自动升级」后**拒绝替换**，自身仍是 1.1.7、无 `.old` 残留 |

---

## 附：Windows 7 SP1 / Server 2008 R2 专用采集端

**背景（2026-10-14 追加）**：在 Windows Server 2008 R2 上直接运行
`opsmonitor-agent-windows-amd64.exe` 会「闪退一下就没有了」——没有窗口、没有报错框、
没有日志文件。

**根因**：Go **1.21.5** 起，runtime 的随机数源从 `RtlGenRandom`
（`advapi32.dll` 的 `SystemFunction036`，Windows XP 起就有）换成了 `ProcessPrng`
（**`bcryptprimitives.dll`，只有 Windows 8 / Server 2012 及以上才有**）。
在 2008 R2 上 Go 用 `LoadLibraryExW` 加载它失败，拿到的函数指针是 `NULL`，
runtime 初始化阶段一调用就触发 `0xc0000005` 访问违规，进程当场终止 ——
崩在 runtime 里，早于任何一行业务日志，所以看不到任何提示。
官方口径：Go 1.21 起最低要求 Windows 10 / Server 2016；最后一个支持并在
Server 2008 R2 上实际测试过的版本是 **Go 1.20**。

**判据**（直接对二进制 `grep` 即可判定，无需上机）：

| 特征字符串 | Go 1.23.9 正式产物 | Go 1.20.14 兼容产物 |
|---|---|---|
| `bcryptprimitives` | 2 | **0** |
| `ProcessPrng` | 3 | **0** |
| `SystemFunction036` | 0 | **1** |
| `RtlGenRandom` | 0 | **1** |

**产物**：`opsmonitor-agent-windows-amd64-win7.exe`，6,360,064 字节，
sha256 `83d831c8cb64e19e240fdda0b384781da252b3c103c4957048edcbaaef67bb3f`，
自报 `opsmonitor-agent 1.1.7 (windows/amd64)`。

**构建**：`./build-win7.sh` —— Go 1.20.14（`D:\c\opsmonitor-buildtools\goroot120`）
+ 裁剪版 modfile `go120.mod`（去掉声明 `go 1.21` 的 sqlite/mysql，采集端本就不依赖它们；
间接依赖全部对齐主 go.mod 的版本，让产物差异只来自工具链）。
**该产物不参与 `release.sh` 上传**，平台下发的正式采集端版本不受影响。

**部署要点**：启动参数必须带 `-remote-config off -update off`。
否则平台下发的 `updateMode=auto` 会让它按正常的 30 分钟周期下载平台上的
Go 1.23 版采集端并替换自己 —— 换完又闪退。
