# GridPerch 发版清单

- 版本号：**1.2.6**（监控端与采集端共用，取自 `internal/version/version.go`）
- 主题：**修复授权码验签全部失败（平台内置公钥与授权中心不是一对）**
- 构建时间：**2026-10-23T11:07:19**（由 `build.sh` 注入 `version.BuildTime`，10 个正式产物一致）
- 构建方式：`bash build.sh all`（`CGO_ENABLED=0` 静态交叉编译，纯 Go SQLite，无动态库依赖）
  \+ `bash build-win7.sh`（Go 1.20.14 + `go120.mod`，出旧系统兼容版）
- 自检：`gofmt -l internal cmd` 无输出 · `go vet ./...` 干净 · `go test -p 1 ./...` 六个包全过 ·
  `sha256sum -c SHA256SUMS.txt` 11/11 通过
- 兼容版自报：`gridperch-agent 1.2.6 (windows/amd64) 旧系统兼容版 构建于 2026-10-23T11:08:00`
  正式版自报：`gridperch-agent 1.2.6 (windows/amd64) 构建于 2026-10-23T11:07:19`

## 一、修复：授权码导入报「签名校验失败」

**现象**：在「授权管理 → 手工导入授权码」粘贴授权中心签发的 `OPSM2-` 码，
提示 `导入失败：授权码验签未通过：授权码签名校验失败，可能不是本平台签发或已被篡改`。

**根因**：**两边的密钥对不是一对**。授权中心（授权端）当前签发用的私钥，对应公钥指纹是
`A9CB-C5B2-C14D-B5D9`；而平台二进制里编进去的是更名前那套授权中心的公钥 `278D-4852-7191-E7CE`。
Ed25519 验签就是用内置公钥验证签名，钥匙不配对必然失败 —— 跟授权码本身、跟机器码绑定都无关。

指纹可直接核对（`sha256(公钥)[:8]`，按 4 位分组）：

| 公钥 | 指纹 | 来源 |
| --- | --- | --- |
| `FRrqtqc2rshOGQDw63ctoh0NiWLSoGKa7E2wUYrgeuo=` | `A9CB-C5B2-C14D-B5D9` | 授权中心当前在用（本次填入） |
| `uZhJVTm2UXxTuOmL3RXvbcokxdU+AIpxqC8PU799I2U=` | `278D-4852-7191-E7CE` | 更名前那套授权中心，转为历史公钥 |

**修法**：

1. `internal/config/licensekey.go` 的 `LicensePublicKey` 换成授权中心当前那把（`A9CB…`）
2. **不删旧公钥**，改为放进新增的 `LicenseLegacyPublicKeys` —— 平台支持**多把公钥**，
   验签时逐把试，任意一把通过即认。这样用旧私钥签发的存量授权码不会因为换钥匙而集体作废
3. `license.Verifier` 由单公钥扩成公钥列表（`NewMultiVerifier` / `NewVerifierFromStrings`），
   第一把是「现行」公钥，指纹与页面展示用它
4. 启动日志把**全部**指纹打出来，换钥匙后一眼能判断是不是配对问题：
   `授权验签公钥指纹：A9CB-C5B2-C14D-B5D9（首要公钥…；另有 1 把历史公钥继续生效：278D-4852-7191-E7CE）`
5. 新增 `TestMultiKeyVerifier`：换新公钥后旧码仍验得过、只留新公钥时旧码必须失败

> ⚠️ 以后换授权中心密钥时，**只能把旧公钥挪进 `LicenseLegacyPublicKeys`，不能直接覆盖**，
> 否则客户手上那批还有效的授权码会一夜之间全部导入失败。

## 二、上一版（1.2.5）要点：Windows 旧系统一键安装

`Register-ScheduledTask` 报「参数错误 0x80070057」：`New-ScheduledTaskPrincipal -UserId 'SYSTEM'`
少了 `-LogonType ServiceAccount`；同时补齐 `schtasks.exe` 兜底，让只有 2.0 版脚本宿主的
Win7 / Server 2008 R2 也能注册开机自启。

## 兼容性

- **数据库**：**无变更**，老库直接沿用
- **接口**：无变更。授权码格式、绑定规则、配额计算都没动，只是「信任哪把钥匙」变宽了
- **前端缓存**：前端编译进二进制，换完二进制请**强刷 Ctrl+F5**
- **升级方式**：替换 `gridperch-server` 单个二进制并重启。采集端**不必升级**（本次无采集端改动）
- **已授权的部署**：换二进制不影响已生效的授权；用旧公钥（278D…）签发的码仍能验过

## 产物（11 个）

| 角色 | 平台 | 文件 |
| --- | --- | --- |
| server | linux/amd64 · linux/arm64 · darwin/amd64 · darwin/arm64 · windows/amd64 | `gridperch-server-*` |
| agent | linux/amd64 · linux/arm64 · darwin/amd64 · darwin/arm64 · windows/amd64 | `gridperch-agent-*` |
| agent | windows/amd64（Win7 / 8.1 / Server 2008 R2 / 2012 / 2012 R2 兼容版，Go 1.20.14 构建） | `gridperch-agent-windows-amd64-win7.exe` |

> **兼容版不参与 `release.sh` 自动上传**。走控制台「版本发布 → 采集端发布物（本地上传）」，
> 填**与正式版相同的版本号**、勾选「旧系统兼容版」上传一次即可，两个通道并存。
