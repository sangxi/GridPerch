# Strix 发版清单

- 版本号：**1.2.0**（服务端与采集端共用，取自 `internal/version/version.go`）
- 主题：**品牌更名 —— OpsMonitor → Strix**
- 构建时间：**2026-10-17T22:18:24**（由 `build.sh` 注入 `version.BuildTime`，10 个正式产物一致）
- 构建方式：`bash build.sh all`（`CGO_ENABLED=0` 静态交叉编译，纯 Go SQLite，无动态库依赖）
- 自检：`gofmt -l internal cmd` 无输出 · `go vet ./...` 干净 · `go test ./...` 全过 ·
  `sha256sum -c SHA256SUMS.txt` 通过 · 服务端产物可 `grep` 到真公钥 `uZhJVTm2`（指纹 `278D-4852-7191-E7CE`）

## 本次更新内容

### 1. 品牌更名：OpsMonitor → Strix（夜枭）

原名与开源圈同名项目过多，检索与品牌识别都受影响。本次把**全部对外标识**统一为 Strix：

| 项目 | 旧 | 新 |
| --- | --- | --- |
| 产品名 | OpsMonitor | **Strix** |
| Go module | `github.com/sangxi/OpsMonitor` | `github.com/sangxi/strix` |
| 二进制 | `opsmonitor-server` / `opsmonitor-agent` | `strix-server` / `strix-agent` |
| 环境变量前缀 | `OPSM_*` | `STRIX_*` |
| 授权码前缀（新签发） | `OPSM2-` | `STRIX2-` |
| Linux 安装路径 | `/opt/opsmonitor` | `/opt/strix` |
| 配置文件 | `/etc/opsmonitor/opsmonitor.env` | `/etc/strix/strix.env` |
| systemd 单元 | `opsmonitor.service` | `strix.service` |
| 运行用户/组 | `opsmonitor` | `strix` |
| 默认数据库 | `opsmonitor.db` | `strix.db` |
| 会话 Cookie | `opsm_session` | `strix_session` |
| 默认账号密码 | `admin / opsmonitor` | `admin / strix` |

### 2. 旧标识全部保留兼容（升级不需要换授权、不需要迁数据）

| 旧标识 | 兼容方式 |
| --- | --- |
| `OPSM_*` 环境变量 | 新变量未设置时自动回落，老 `EnvironmentFile` / `.env` 原样可用（`config.env`） |
| `OPSM2-` 授权码 | 仍按 Ed25519 验签（`prefixLegacyV2`），**客户手里的授权码继续有效** |
| `OPSM1-` / `OPSM-` / `WGC-` 授权码 | 仍按 HMAC 验签；新旧两个默认密钥都会试（`Verifier.legacy` 为列表） |
| 旧库 `opsmonitor.db` | 新默认路径找不到文件时自动沿用旧库，启动日志会明确提示（`config.ResolveLegacyDBPath`） |
| 旧通信密钥默认值 `opsmonitor` | 仅在本次仍使用默认密钥时接受；已改强密钥的部署不多留口令（`config.TokenOK`） |
| **机器码** | 盐值 `opsmonitor-server` **刻意保持不变** —— 改了会让所有绑机授权集体失效 |
| **设备口令加密** | 盐值 `opsmonitor-device-secret` 同样保持不变，否则已入库口令解不开 |

### 3. 部署脚本增强

- `deploy/linux/install.sh`：新增旧版本（`/opt/opsmonitor` + `opsmonitor.service`）并存检测，避免两套抢同一端口；
  支持 `--force` 跳过。前缀支持 `OPSM_PREFIX` 回落。
- `deploy/docker/docker-compose.yml`：服务名 / 容器名 / 镜像名改为 `strix`，`STRIX_DB` 指向 `strix.db` 并注明旧库自动沿用。

## 升级须知

1. **服务端**：只需替换二进制为 `strix-server`，旧标识会自动兼容，不需要重新导入授权。
2. **采集端**：平台下发的发布物名已变为 `strix-agent-*`；老采集端（`opsmonitor-agent`）仍能上报，
   但建议随后重装为 `strix-agent`，否则进程名与新发布物不一致（自动升级会替换为 `strix-agent`）。
3. **systemd 老部署**：旧单元名 `opsmonitor.service` 与新单元 `strix.service` 不能并存，
   升级时先 `systemctl disable --now opsmonitor` 再装新单元。
4. **容器部署**：镜像名改为 `strix/strix-server:1.2.0`，需要重新 build 或改 tag；
   数据卷里的 `opsmonitor.db` 会被自动识别沿用。
5. **授权签发侧**：授权中心若要签发新前缀 `STRIX2-` 的码，需同步修改签发端；在此之前签发的
   `OPSM2-` 码依然可用（本版本两种前缀都认）。
