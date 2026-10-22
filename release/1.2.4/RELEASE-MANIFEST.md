# GridPerch 发版清单

- 版本号：**1.2.4**（监控端与采集端共用，取自 `internal/version/version.go`）
- 主题：**品牌更名为 GridPerch**（原名 Strix）+ **大屏名称可自定义**
- 构建时间：**2026-10-22T13:44:36**（由 `build.sh` 注入 `version.BuildTime`，10 个正式产物一致）
- 构建方式：`bash build.sh all`（`CGO_ENABLED=0` 静态交叉编译，纯 Go SQLite，无动态库依赖）
  \+ `bash build-win7.sh`（Go 1.20.14 + `go120.mod`，出旧系统兼容版）
- 自检：`gofmt -l internal cmd` 无输出 · `go vet ./...` 干净 · `go test -p 1 ./...` 六个包全过 ·
  `sha256sum -c SHA256SUMS.txt` 11/11 通过 · 服务端产物可 `grep` 到真公钥 `uZhJVTm2`（指纹 `278D-4852-7191-E7CE`）
- 兼容版自报：`gridperch-agent 1.2.4 (windows/amd64) 旧系统兼容版 构建于 2026-10-22T13:45:31`
  正式版自报：`gridperch-agent 1.2.4 (windows/amd64) 构建于 2026-10-22T13:44:36`

## 一、品牌更名（OpsMonitor → Strix → GridPerch）

### 变了什么

| 项目 | 新（1.2.4） | 旧（1.2.3 及更早） |
| --- | --- | --- |
| 二进制 | `gridperch-server` / `gridperch-agent` | `strix-server` / `strix-agent` |
| Go module | `github.com/sangxi/gridperch` | `github.com/sangxi/strix` |
| 环境变量前缀 | `GRIDPERCH_*` | `STRIX_*`（更早 `OPSM_*`） |
| 授权码前缀 | `GRIDPERCH2-` | `STRIX2-`（更早 `OPSM2-`） |
| 默认端口 / 默认口令 | `:9999` / `admin : gridperch` | `:9999` / `admin : strix` |
| Linux 部署目录 | `/opt/gridperch`、`/etc/gridperch/gridperch.env`、`gridperch.service` | `/opt/strix`、`/etc/strix/strix.env`、`strix.service` |
| 默认库文件 | `data/gridperch.db` | `data/strix.db` |
| 界面品牌 | 侧栏、登录页、页脚、大屏默认标题 | Strix |

### 哪些**刻意没变**（改了会出事）

- **机器码盐值 `"opsmonitor-server"`**（`internal/license/license.go`）与
  **设备口令盐值 `"opsmonitor-device-secret"`**（`internal/store/manage.go`）——
  两处历史常量永久冻结。改掉会让所有已绑定机器的授权码集体失效、已存设备口令全部解不开
- **Ed25519 验签公钥**（指纹 `278D-4852-7191-E7CE`）不动，已签发的授权码继续有效
- **数据协议版本 `Protocol = 1`** 与库结构：1.2.4 **无任何表结构变更**，老库直接沿用

### 向后兼容（升级不用改配置）

- **环境变量**：`GRIDPERCH_ADDR` 找不到时依次回落 `STRIX_ADDR`、`OPSM_ADDR`，
  老 `strix.env` / 老启动参数不用改（`internal/config/config.go` 的 `envLegacySuffixes`）
- **库文件**：默认 `gridperch.db` 不存在时，依次试 `strix.db`、`opsmonitor.db`，
  老库原地沿用，**不会新建空库**
- **授权码**：`GRIDPERCH2-` / `STRIX2-` / `OPSM2-` 三种前缀同时验签
- **HMAC 旧密钥**：`GRIDPERCH_LICENSE_SECRET_STRIX` 默认值仍是 `Strix-license`，
  存量 HMAC 授权码照旧能验
- **旧采集端**：1.2.3 的 `strix-agent` 仍可上报；平台侧按主机名登记，改名不影响存量主机
- **systemd**：unit 名换成 `gridperch.service`。旧机器升级请先
  `systemctl disable --now strix.service` 再装新的，避免两套服务抢 `:9999`

> ⚠️ 升级注意：默认管理员口令随品牌改为 `gridperch`，但 `EnsureUser()` **只在账号不存在时建号**，
> 已有部署的 admin 密码不会被重置。只有全新安装才是 `admin / gridperch`。

## 二、大屏标题可自定义

**要解决的问题**：大屏页顶部的名字以前写死在 `screen.html` 的 `<h1>` 里，
换成自己单位的名字必须改前端源码重新编译。`systemName` 只能改系统名称（侧栏、登录页、页脚），管不到大屏。

现在：

- **新增设置项 `screenTitle`**：系统设置 →「界面定制」→ 大屏标题（最长 32 字符），留空即自动生成
- **取值优先级**（`screenTitleOf()`，`internal/server/manage.go`）：
  显式 `screenTitle` > `systemName + " 运维监控中心"` > 兜底常量 `GridPerch 运维监控中心`。
  默认安装表现与旧版一致；只改过「系统名称」的用户不必再去改一次大屏标题
- **`/api/settings/public` 与 `/api/screen` 共用同一个函数**：`screenTitle` 已加入
  `publicSettingKeys`，未登录的大屏页也能直接拿到算好的标题，前后端不再各兜底一遍
- **`/api/screen` 新增 `title` 字段**：设置读取失败时退回兜底名，不让整块大屏没有标题
- **热改名**：`screen.js` 的 `applyTitle()` 在每次刷新（10 秒一轮）里套用新标题，
  同时改页顶 `h1`、`h1[title]` 与 `document.title`，后台改完不用重开大屏。
  首屏先用 `/api/settings/public` 占位，避免自定义过名称的大屏先闪一下默认名
- **超长标题不挤爆头部**：`.screen-head h1` 改为 `flex: 1 1 auto; min-width: 0` +
  `overflow: hidden; text-overflow: ellipsis`，两侧时钟/模板下拉 `flex: 0 0 auto` 不参与收缩；
  超长省略而非换行（投屏不接受布局跳动），完整名称看 `h1` 的 `title` 悬停提示

## 三、细节

- 新增 `internal/server/screen_title_test.go`，覆盖三级优先级的取值规则
- `.gitattributes` 补齐 `*.go/*.js/*.css/*.html/*.md/*.yml/*.json` 与 `Dockerfile*`、
  `docker-compose*.yml` 的 `text eol=lf`，避免 `core.autocrlf=true` 下检出成 CRLF
  导致 `gofmt -l` 误报、Dockerfile 续行报 "unable to process line continuation"
- 命名空间全面切换后 `gofmt -l` / `go vet` / `go test` 三连干净（`release_test.go` 的断言已跟随改名）

## 兼容性

- **数据库**：**无变更**。`settings` 是通用键值表，`SaveSettings` 不做白名单校验，
  新键直接落库；老库无需迁移，`screenTitle` 不存在时按优先级自动回落
- **接口**：`/api/screen` 响应**新增** `title` 字段，`/api/settings/public` 新增 `screenTitle` 键，
  均为纯增量，旧前端/旧采集端不受影响
- **前端缓存**：前端是编译进二进制的，换完二进制请**强刷 Ctrl+F5**，否则浏览器/反向代理可能还在用旧 `screen.js`
- **升级方式**：替换 `gridperch-server` 单个二进制并重启即可（注意 systemd unit 改名）。

## 产物（11 个）

| 角色 | 平台 | 文件 |
| --- | --- | --- |
| server | linux/amd64 · linux/arm64 · darwin/amd64 · darwin/arm64 · windows/amd64 | `gridperch-server-*` |
| agent | linux/amd64 · linux/arm64 · darwin/amd64 · darwin/arm64 · windows/amd64 | `gridperch-agent-*` |
| agent | windows/amd64（Win7 / 8.1 / Server 2008 R2 / 2012 / 2012 R2 兼容版，Go 1.20.14 构建） | `gridperch-agent-windows-amd64-win7.exe` |

> **兼容版不参与 `release.sh` 自动上传**（授权端按 `(kind, version, goos, goarch)` 登记，只存一份产物）。
> 兼容版走控制台「版本发布 → 采集端发布物（本地上传）」，填**与正式版相同的版本号**、
> 勾选「旧系统兼容版」上传一次即可，两个通道并存。

## 上一版（1.2.3）要点

采集端「旧系统兼容版」独立发布通道（`release` 表新增 `variant` 列，同版本号下正式版与兼容版并存；
兼容版自报 `legacy=1`，自动升级只会拿到兼容版，旧系统不再需要 `-remote-config off -update off`）
\+ 授权中心同步报错口径重做（502/503/504 明确判定为网关错误，不再误报「`-hub` 地址填错」，
并打出行响应标题、`Server`/`Via` 头与脱敏 URL）。
