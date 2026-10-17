// Package model 定义服务端与采集端之间传输的数据结构。
// 字段命名沿用原 wgcloud 的 JSON 协议（驼峰），保证旧版 agent 也能直接上报。
package model

// Report 是 agent 每次上报的完整报文，对应原 wgcloud 的 /agent/minTask 请求体。
type Report struct {
	WgToken      string        `json:"wgToken"`
	Hostname     string        `json:"hostname"`
	SystemInfo   *SystemInfo   `json:"systemInfo,omitempty"`
	CpuState     *CpuState     `json:"cpuState,omitempty"`
	MemState     *MemState     `json:"memState,omitempty"`
	SysLoadState *SysLoadState `json:"sysLoadState,omitempty"`
	NetIoState   *NetIoState   `json:"netIoState,omitempty"`
	DiskIoState  *DiskIoState  `json:"diskIoState,omitempty"`
	DeskStates   []DeskState   `json:"deskStateList,omitempty"`
	Processes    []HostProcess `json:"processList,omitempty"`
	// AgentVersion 采集端自身版本号，服务端据此判断该主机是否需要升级。
	AgentVersion string `json:"agentVersion,omitempty"`
	// SysEvents 采集端读到的系统事件（当前仅 Windows 事件查看器有内容），
	// 服务端会把警告/错误级别转成告警，因此在主机详情页可见。
	SysEvents []SysEvent `json:"sysEventList,omitempty"`
	// HostExtra 硬件与运行时扩展指标。全部为可选，采集不到就不上报，
	// 服务端按主机覆盖写入 host_extra（最新一次有效值）。
	HostExtra *HostExtra `json:"hostExtra,omitempty"`
}

// HostExtra 硬件与运行时扩展指标。
//
// 这一组指标在虚拟化与云主机上常常缺失（无温度传感器、SMART 需 root、
// 云盘不暴露 SMART），因此除 ConnCount 外全部允许为零值，
// 服务端与前端都不能把「取不到」当成「异常」。
type HostExtra struct {
	Hostname        string  `json:"hostname"`
	CpuTemp         float64 `json:"cpuTemp"`         // CPU 温度，摄氏度，0 表示未采集到
	SmartStatus     string  `json:"smartStatus"`     // pass / warn / fail，空串表示未采集到
	ConnCount       int     `json:"connCount"`       // TCP 连接总数
	ConnEstablished int     `json:"connEstablished"` // TCP 已建立连接数
	MacAddr         string  `json:"macAddr"`
	IpAddr          string  `json:"ipAddr"`
	CollectTime     string  `json:"collectTime"`
}

// SysEvent 采集端读到的一条系统事件。
// EventID 的 JSON 键是 id，与 PowerShell ConvertTo-Json 输出的字段名保持一致。
type SysEvent struct {
	Time     string `json:"time"`
	Source   string `json:"source"`
	Level    string `json:"level"`
	EventID  int    `json:"id"`
	Category string `json:"category"`
	Content  string `json:"content"`
}

// HostProcess 单个进程的资源占用快照。
type HostProcess struct {
	Hostname   string  `json:"hostname"`
	PID        int32   `json:"pid"`
	Name       string  `json:"name"`
	CPUPer     float64 `json:"cpuPer"`
	MemPer     float64 `json:"memPer"`
	Cmdline    string  `json:"cmdline"`
	CreateTime string  `json:"createTime,omitempty"`
}

// SystemInfo 主机基本信息与总览指标。
type SystemInfo struct {
	Hostname      string  `json:"hostname"`
	IP            string  `json:"ip"`
	Version       string  `json:"version"`
	VersionDetail string  `json:"versionDetail"`
	CpuPer        float64 `json:"cpuPer"`
	MemPer        float64 `json:"memPer"`
	CpuCoreNum    string  `json:"cpuCoreNum"`
	CpuXh         string  `json:"cpuXh"`
	State         string  `json:"state"`
	CreateTime    string  `json:"createTime"`
}

// CpuState CPU 使用率明细。
type CpuState struct {
	Hostname string  `json:"hostname"`
	User     string  `json:"user"`
	Sys      float64 `json:"sys"`
	Idle     float64 `json:"idle"`
	Iowait   float64 `json:"iowait"`
	Irq      string  `json:"irq"`
	Soft     string  `json:"soft"`
	DateStr  string  `json:"dateStr"`
}

// MemState 内存使用情况，单位为 MB 字符串。
type MemState struct {
	Hostname string  `json:"hostname"`
	Total    string  `json:"total"`
	Used     string  `json:"used"`
	Free     string  `json:"free"`
	UsePer   float64 `json:"usePer"`
	DateStr  string  `json:"dateStr"`
}

// DeskState 磁盘分区使用情况。
type DeskState struct {
	Hostname   string `json:"hostname"`
	FileSystem string `json:"fileSystem"`
	Size       string `json:"size"`
	Used       string `json:"used"`
	Avail      string `json:"avail"`
	UsePer     string `json:"usePer"`
	DateStr    string `json:"dateStr"`
}

// NetIoState 网卡流量与包量。
type NetIoState struct {
	Hostname string `json:"hostname"`
	Rxpck    string `json:"rxpck"`
	Txpck    string `json:"txpck"`
	Rxbyt    string `json:"rxbyt"`
	Txbyt    string `json:"txbyt"`
	Rxcmp    string `json:"rxcmp"`
	Txcmp    string `json:"txcmp"`
	Rxmcst   string `json:"rxmcst"`
	DateStr  string `json:"dateStr"`
}

// SysLoadState 系统负载与登录用户数。
type SysLoadState struct {
	Hostname    string  `json:"hostname"`
	OneLoad     float64 `json:"oneLoad"`
	FiveLoad    float64 `json:"fiveLoad"`
	FifteenLoad float64 `json:"fifteenLoad"`
	Users       string  `json:"users"`
	DateStr     string  `json:"dateStr"`
}

// Host 是主机列表接口返回的行，聚合了最新快照。
type Host struct {
	Hostname string `json:"hostname"`
	IP       string `json:"ip"`
	Version  string `json:"version"`
	// OS 目标主机操作系统代号（linux / windows / darwin），来自采集端上报的 version_detail。
	// 供工具箱按系统过滤「主机本地执行」类命令；空表示采集端未上报。
	OS         string  `json:"os"`
	State      string  `json:"state"`
	CpuPer     float64 `json:"cpuPer"`
	MemPer     float64 `json:"memPer"`
	CpuCoreNum string  `json:"cpuCoreNum"`
	CpuXh      string  `json:"cpuXh"`
	LastSeen   string  `json:"lastSeen"`
	UptimeDesc string  `json:"uptimeDesc"`
	// AgentVersion 该主机采集端版本，空表示旧版采集端未上报。
	AgentVersion string `json:"agentVersion"`
	// CatalogID / CatalogName 该主机所属目录，空表示未分组。
	CatalogID   string `json:"catalogId"`
	CatalogName string `json:"catalogName"`
}

// Alert 告警记录。
//
// 告警是「按次数入库」的（同一故障每个冷却窗口插一行），所以认领状态记在行上：
// 只认领不代表故障已消失，只表示「已经有人看过、正在处理」。
type Alert struct {
	ID         string `json:"id"`
	Hostname   string `json:"hostname"`
	Type       string `json:"type"`
	Level      string `json:"level"`
	Content    string `json:"content"`
	Value      string `json:"value"`
	CreateTime string `json:"createTime"`
	State      string `json:"state"`
	// AckBy / AckTime 认领人与认领时间，空表示未认领。
	AckBy   string `json:"ackBy"`
	AckTime string `json:"ackTime"`
	// Note 处理备注，最多 500 字。
	Note string `json:"note"`
	// Silenced 生成这条告警时是否命中静默规则（仅展示用，不落库）。
	Silenced bool `json:"silenced"`
}

// AlertSilence 告警静默规则。
//
// 匹配口径是「主机 + 监控项」，不是单条告警 ID —— 因为同一故障会反复入库，
// 按 ID 静默只能挡住一条，挡不住后续每一次。Host / Type 留空表示全部匹配。
type AlertSilence struct {
	ID         string `json:"id"`
	Hostname   string `json:"hostname"`
	Type       string `json:"type"`
	Level      string `json:"level"`
	Reason     string `json:"reason"`
	StartTime  string `json:"startTime"`
	EndTime    string `json:"endTime"`
	CreateBy   string `json:"createBy"`
	CreateTime string `json:"createTime"`
	// Active 当前是否处于静默生效期内（仅展示用，不落库）。
	Active bool `json:"active"`
}

// Dashboard 仪表盘聚合数据。
type Dashboard struct {
	HostTotal   int     `json:"hostTotal"`
	HostOnline  int     `json:"hostOnline"`
	HostOffline int     `json:"hostOffline"`
	AlertActive int     `json:"alertActive"`
	AvgCpu      float64 `json:"avgCpu"`
	AvgMem      float64 `json:"avgMem"`
	Hosts       []Host  `json:"hosts"`
	Alerts      []Alert `json:"alerts"`
}

// Series 图表时序数据。
type Series struct {
	Name   string  `json:"name"`
	Unit   string  `json:"unit"`
	Points []Point `json:"points"`
}

// DiskIoState 磁盘 IO 读写速率。
type DiskIoState struct {
	Hostname  string  `json:"hostname"`
	ReadKB    float64 `json:"readKb"`
	WriteKB   float64 `json:"writeKb"`
	ReadIOPS  float64 `json:"readIops"`
	WriteIOPS float64 `json:"writeIops"`
	DateStr   string  `json:"dateStr"`
}

// MonitorResult agent 上报的单项检查结果。status：1 正常，2 异常。
type MonitorResult struct {
	TaskID    string `json:"taskId"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Value     string `json:"value"`
	Msg       string `json:"msg"`
	CheckTime string `json:"checkTime"`
}

// MonitorResultBatch agent 上报的一批监控项结果。
type MonitorResultBatch struct {
	WgToken  string          `json:"wgToken"`
	Hostname string          `json:"hostname"`
	Results  []MonitorResult `json:"results"`
}

// MonitorTask 监控项配置。
// Runner 决定由谁执行：agent 表示下发到采集端，server 表示服务端主动探测。
type MonitorTask struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Name         string `json:"name"`
	Target       string `json:"target"`
	IntervalSec  int    `json:"intervalSec"`
	TimeoutSec   int    `json:"timeoutSec"`
	Enabled      bool   `json:"enabled"`
	Hosts        string `json:"hosts"`
	AlertContent string `json:"alertContent"`
	Runner       string `json:"runner"`
	// HealCmd 异常时自动执行的恢复命令（进程下线自动恢复、日志自动处理、文件篡改自动还原）。
	HealCmd    string `json:"healCmd"`
	AutoHeal   bool   `json:"autoHeal"`
	CreateTime string `json:"createTime"`
	UpdateTime string `json:"updateTime"`
}

// TaskAssignment 下发给 agent 的监控项。
type TaskAssignment struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Target      string `json:"target"`
	IntervalSec int    `json:"intervalSec"`
	TimeoutSec  int    `json:"timeoutSec"`
	AutoHeal    bool   `json:"autoHeal"`
	HealCmd     string `json:"healCmd"`
}

// Asset 资产设备台账条目。
type Asset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Type        string `json:"type"`
	IP          string `json:"ip"`
	Mac         string `json:"mac"`
	OS          string `json:"os"`
	Spec        string `json:"spec"`
	Vendor      string `json:"vendor"`
	SN          string `json:"sn"`
	Location    string `json:"location"`
	Owner       string `json:"owner"`
	Department  string `json:"department"`
	BuyDate     string `json:"buyDate"`
	ExpireDate  string `json:"expireDate"`
	Status      string `json:"status"`
	MonitorBind string `json:"monitorBind"`
	Remark      string `json:"remark"`
	CreateTime  string `json:"createTime"`
	UpdateTime  string `json:"updateTime"`
}

// DeviceAccount 设备/系统的账号凭据，密码以密文保存且接口不回传明文。
type DeviceAccount struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	AssetID    string `json:"assetId"`
	Protocol   string `json:"protocol"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	Secret     string `json:"secret,omitempty"`
	HasSecret  bool   `json:"hasSecret"`
	Remark     string `json:"remark"`
	CreateTime string `json:"createTime"`
	UpdateTime string `json:"updateTime"`
}

// Command 下发给采集端执行的指令。
type Command struct {
	ID         string `json:"id"`
	Host       string `json:"host"`
	Cmd        string `json:"cmd"`
	TimeoutSec int    `json:"timeoutSec"`
	Source     string `json:"source"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exitCode"`
	Output     string `json:"output"`
	ErrorMsg   string `json:"error"`
	User       string `json:"user"`
	CreateTime string `json:"createTime"`
	FinishTime string `json:"finishTime"`
}

// CommandPull 采集端拉取到的待执行指令。
type CommandPull struct {
	ID         string `json:"id"`
	Cmd        string `json:"cmd"`
	TimeoutSec int    `json:"timeoutSec"`
}

// CommandResult 采集端回传的指令执行结果。
type CommandResult struct {
	WgToken  string           `json:"wgToken"`
	Hostname string           `json:"hostname"`
	Results  []CommandOutcome `json:"results"`
}

// CommandOutcome 单条指令的执行结果。
type CommandOutcome struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output"`
	ErrorMsg string `json:"error"`
}

// Member 控制台成员账号。
type Member struct {
	Username    string `json:"username"`
	Role        string `json:"role"`
	Nickname    string `json:"nickname"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Menus       string `json:"menus"`
	Enabled     bool   `json:"enabled"`
	LastLogin   string `json:"lastLogin"`
	LastLoginIP string `json:"lastLoginIp"`
	LoginCount  int    `json:"loginCount"`
	CreateTime  string `json:"createTime"`
}

// APIToken 数据开放接口凭证。
type APIToken struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Token      string `json:"token"`
	Perms      string `json:"perms"`
	Enabled    bool   `json:"enabled"`
	LastUsed   string `json:"lastUsed"`
	CreateTime string `json:"createTime"`
}

// Inspection 巡检报告。
type Inspection struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Scope      string `json:"scope"`
	Author     string `json:"author"`
	Summary    string `json:"summary"`
	Content    string `json:"content"`
	Score      int    `json:"score"`
	CreateTime string `json:"createTime"`
}

// LoginEvent 主机登录事件，用于主机登录提醒。
type LoginEvent struct {
	ID         string `json:"id"`
	Hostname   string `json:"hostname"`
	Users      string `json:"users"`
	Diff       string `json:"diff"`
	CreateTime string `json:"createTime"`
}

// MonitorState 监控项在某主机上的最新状态。
type MonitorState struct {
	TaskID    string `json:"taskId"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Hostname  string `json:"hostname"`
	Status    string `json:"status"`
	Value     string `json:"value"`
	Msg       string `json:"msg"`
	CheckTime string `json:"checkTime"`
}

// Point 单个时间点取值。
type Point struct {
	Time  string  `json:"time"`
	Value float64 `json:"value"`
}

// AuditLog 操作审计流水，记录谁在什么时间对哪个对象做了什么。
type AuditLog struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	Role       string `json:"role"`
	Action     string `json:"action"`   // 人类可读的动作名，如「新建监控项」
	Category   string `json:"category"` // 归类：auth / monitor / alert / command / asset / device / inspection / member / token / setting / mail / data
	Method     string `json:"method"`
	Path       string `json:"path"`
	Target     string `json:"target"` // 操作对象，如监控项名称、成员账号
	Detail     string `json:"detail"` // 补充说明（截断后的请求要点）
	IP         string `json:"ip"`
	UserAgent  string `json:"userAgent"`
	Result     string `json:"result"` // ok / fail
	StatusCode int    `json:"statusCode"`
	DurationMs int64  `json:"durationMs"`
	CreateTime string `json:"createTime"`
}

// MailLog 邮件发送记录。
type MailLog struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"` // alert（告警推送）/ test（测试邮件）
	Subject    string `json:"subject"`
	Recipients string `json:"recipients"`
	ItemCount  int    `json:"itemCount"`
	Status     string `json:"status"` // ok / fail / skip
	Error      string `json:"error"`
	CreateTime string `json:"createTime"`
}
