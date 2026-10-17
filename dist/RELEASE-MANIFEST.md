# Strix 发版清单

- 版本号：**1.2.1**（服务端与采集端共用，取自 `internal/version/version.go`）
- 主题：**告警闭环 —— 认领 / 备注 / 静默 / 催办**
- 构建时间：**2026-10-18T02:24:23**（由 `build.sh` 注入 `version.BuildTime`，10 个正式产物一致）
- 构建方式：`bash build.sh all`（`CGO_ENABLED=0` 静态交叉编译，纯 Go SQLite，无动态库依赖）
- 自检：`gofmt -l internal cmd` 无输出 · `go vet ./...` 干净 · `go test ./...` 全过 ·
  `sha256sum -c SHA256SUMS.txt` 通过 · 服务端产物可 `grep` 到真公钥 `uZhJVTm2`（指纹 `278D-4852-7191-E7CE`）

## 本次更新内容

### 1. 告警认领与处理备注
- `alert` 表新增 `ack_by` / `ack_time` / `note` 列，`migrateExtra()` 自动补列，**老库打开即升级，无需手工迁移**
- 告警中心每行提供「认领 / 释放 / 备注」操作。认领人取当前会话账号（昵称优先），不接受前端传值
- 重复认领返回「该告警已被 XXX 认领」，避免两个人同时查同一件事
- 备注上限 500 字（按 rune 截断，不会劈坏中文），显示在告警内容下方，导出 CSV/Excel 一并带出

### 2. 告警静默规则（新增表 `alert_silence`）
- 匹配口径为 **主机 + 监控项 + 级别 + 时间段**，任一字段留空表示不限
- **静默期内告警照常入库留痕**，只是不推通知 —— 事后仍能在告警记录里查到这段时间发生过什么
- 过滤点接在邮件与机器人两条推送链路上（`pushAlertMail` / `pushAlertWebhook`），不是只做面板装饰
- 到期自动失效；也可在「静默规则」弹窗里随时删除，删除即刻恢复通知
- 静默规则的主机名已纳入 `hostnameRefTables`，**主机改名时规则跟着迁移**（回归用例 `TestRenameHostMovesSilenceRules`）

### 3. 未认领催办
- 告警产生后超过 `alertEscalateMin`（默认 30 分钟，`0` 表示关闭）仍无人认领，再推一轮通知，标题带 `【未认领催办·N分钟】` 前缀
- 去重靠 `alert.escalated` 列而不是时间游标 —— 游标已被常规推送推过去了，催办查不到数据
- 静默期内不发催办，但**照常打标记**：否则静默一结束，攒下的历史告警会集中爆一轮催办
- 设置入口：`系统设置 → 告警与提醒 → 未认领催办（分钟）`

### 4. 告警中心界面
- 顶部新增四张指标卡：未恢复告警 / 待认领 / 处理中 / 静默生效中
- 新增「处理状态」筛选（未认领 / 处理中 / 已静默）与「处理状态」列
- 认领、释放、备注、标记恢复均为**局部重绘**，不再整页 `render()`，筛选条件与滚动位置不会被冲掉
- 弹窗支持宽版（`.modal-box.wide`），静默规则列表 8 列不再被截断
- `datetime-local` / `date` / `time` 类输入补进 `.form-grid` 样式，日期控件不再挤成一小截

### 5. 顺带修掉的老问题
- 使用教程页 `/docs` 的目录导航里有个 `<a class="grp">运维手册</a>` 漏写 `href`，会让整段滚动高亮脚本抛
  `Cannot read properties of null (reading 'slice')`。已改为 `<div class="grp">`，并给脚本加了跳过无 `href` 项的防御
- 教程页新增「告警闭环怎么用」小节（4 个动作 + 静默匹配口径说明）
- 自定义 Webhook 的密钥请求头补发 `X-Strix-Secret`，**同时保留旧的 `X-Opsm-Secret`**（更名遗留：
  现网接收端可能已按旧头校验，单独改名会让它们全部失败；接收方改好后再择机去掉旧头）

## 兼容性

- **数据库**：无需迁移脚本。新列由 `migrateExtra()` 在启动时补齐，新表由 `schemaExtra()` 建好
- **接口**：`/api/alerts` 只增字段（`ackBy` / `ackTime` / `note` / `silenced`），不删不改，旧前端不受影响
- **通知**：静默与催办都是「多发 / 少发」的增量逻辑，未配置静默规则时行为与 1.2.0 完全一致
- **升级方式**：替换 `strix-server` 单个二进制并重启即可；采集端无需升级（本版未改采集端逻辑）

## 产物（11 个）

| 角色 | 平台 | 文件 |
| --- | --- | --- |
| server | linux/amd64 · linux/arm64 · darwin/amd64 · darwin/arm64 · windows/amd64 | `strix-server-*` |
| agent | linux/amd64 · linux/arm64 · darwin/amd64 · darwin/arm64 · windows/amd64 | `strix-agent-*` |
| agent | windows/amd64（Win7 / Server 2008 R2 兼容版，Go 1.20.14 构建） | `strix-agent-windows-amd64-win7.exe` |

> Win7 兼容版**不参与 `release.sh` 上传**，需手工分发。部署时必须加 `-remote-config off -update off`，
> 否则平台下发的全局 `updateMode=auto` 会让它在 30 分钟内把自己换回 Go 1.23 版本，再次闪退。
