// Package config 提供命令行与环境变量配置，以及告警阈值。
package config

import (
	"crypto/md5"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Server 服务端配置。
type Server struct {
	Addr   string
	DBPath string
	Token  string
	// TokenLegacy 更名前的默认通信密钥。仅在本次仍使用默认密钥时填充，
	// 用于让「没改过密钥的老采集端」在服务端升级后不至于立刻断连。
	TokenLegacy   string
	AdminUser     string
	AdminPass     string
	Retention     int // 历史数据保留天数
	OfflineSec    int // 超过该秒数未上报视为离线
	PurgeSec      int // 清理任务执行间隔
	CPUAlert      float64
	MemAlert      float64
	DiskAlert     float64
	LoadAlert     float64
	ReadConns     int    // SQLite 读连接池大小
	WriteQueue    int    // 上报写队列容量
	ProbeWorkers  int    // 服务端主动探测并发度
	LoginAlert    string // 是否开启主机登录提醒：true/false
	AlertCoolDown time.Duration

	ReleaseDir    string // 采集端发布包的存放目录
	PublicURL     string // 对外访问地址，安装脚本与更新下载用它拼接绝对地址
	LicenseSecret string // 旧格式（HMAC）授权码的校验密钥；现仅供兼容存量授权码
	// LicenseSecretLegacy 更名前使用的旧密钥默认值。存量 HMAC 授权码是用它签的，
	// 换了新默认值后若不再接受旧值，已激活的授权会当场失效，所以一并带上。
	LicenseSecretLegacy string
	LicenseKey          string // 启动时直接导入的授权码（可选）
	LicenseMachine      string // 强制指定的机器码，默认自动取本机
	LicenseFreeHost     int    // 未授权时允许接入的主机数
	LicenseFreeUser     int    // 未授权时允许创建的控制台账号数

	// 授权系统（更新与授权中心）：配置后平台定期从授权系统拉取最新版本与本机授权码
	HubURL      string        // 授权系统地址，默认 DefaultHubURL，留空表示完全离线
	HubToken    string        // 授权系统拉取令牌：授权中心内置的那串明文令牌
	HubInterval time.Duration // 拉取间隔
	// HubAutoUpdate 服务端自身的自动更新策略：
	// check 只在控制台提示 / download 自动下载并就位（默认，重启后生效）/ off 关闭
	HubAutoUpdate string
	// HubRestart 平台自升级「就位后是否自动重启」：
	// auto 自动重启让新版本立即生效（默认）/ off 只替换文件、等人工重启。
	// 关掉它会退回「换了文件但版本号不变」的旧行为，一般无需修改。
	HubRestart string
	// HubUserAgent 访问授权系统时的 User-Agent。
	// 部分网关（如 Cloudflare 的 Bot Fight Mode）会按 UA 拦截程序客户端，
	// 这时可改成带浏览器特征的 UA 绕过；但更根本的做法是在网关上放行。
	HubUserAgent string

	PrintVersion bool // 只打印版本号后退出
}

// DefaultHubURL 内置的授权系统地址。
//
// 平台默认就指向授权中心：授权导入与版本更新是这套系统的固有环节，
// 不该要求每个部署都手工加一个 -hub。内网离线环境用 -hub "" 显式关闭。
//
// 声明为 var（而非 const）是为了能在构建时用 -ldflags -X 覆盖，便于换授权中心
// 或产出「默认离线」的构建：
//
//	go build -ldflags "-X github.com/sangxi/strix/internal/config.DefaultHubURL=https://你的授权中心"
//	go build -ldflags "-X github.com/sangxi/strix/internal/config.DefaultHubURL="   # 默认离线
//
// ⚠️ 这个地址是**功能必需**的：平台要「开箱即能拉更新与授权码」，它就必须存在于
// 二进制里（只是不再出现在任何提示文案/前端/文档中）。真要连它都不留，只能按上面的
// 方式传空值构建，代价是默认离线、需显式 -hub 才启用。
var DefaultHubURL = "https://1999091.xyz"

// Agent 采集端配置。
type Agent struct {
	ServerURL string
	Token     string
	// TokenHash 通信口令的 MD5。一键安装脚本用 -token-hash 下发，
	// 目标主机的命令行历史与 systemd 单元文件里都不会出现口令明文。
	TokenHash    string
	Interval     time.Duration
	Hostname     string
	SkipDisk     []string
	AllowCommand bool // 是否允许执行服务端下发的指令

	UpdateMode     string        // 更新策略：auto 自动升级 / check 只检查 / off 关闭
	UpdateInterval time.Duration // 版本检查间隔
	InstallDir     string        // 安装目录，更新时替换自身二进制用
	PrintVersion   bool          // 只打印版本号后退出
	// RemoteConfig 是否接受服务端下发的运行配置：auto 接受（默认）/ off 只用本地参数。
	// 一键安装脚本生成的命令只带必要参数，其余配置由平台统一管理。
	RemoteConfig string
}

// MD5Token 返回原始 token 的 MD5，与原 wgcloud 的鉴权方式一致。
func MD5Token(token string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(token)))
}

// env 读取环境变量：先找新前缀 STRIX_ 的同名变量，再回落到上一代前缀 OPSM_，
// 最后才用默认值。
//
// 品牌从 OpsMonitor 更名为 Strix 后，变量前缀统一改成 STRIX_；而已经部署的机器
// （systemd 的 EnvironmentFile、docker-compose 的 .env）里写的仍是 OPSM_*。
// 这里做一次回落，让老部署只换二进制就能继续跑，不必同步改配置。
func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if old := legacyEnvKey(key); old != "" {
		if v := os.Getenv(old); v != "" {
			return v
		}
	}
	return def
}

// legacyEnvKey 把 STRIX_XXX 映射回上一代前缀的 OPSM_XXX；非 STRIX_ 前缀返回空。
func legacyEnvKey(key string) string {
	const newPrefix, oldPrefix = "STRIX_", "OPSM_"
	if strings.HasPrefix(key, newPrefix) {
		return oldPrefix + key[len(newPrefix):]
	}
	return ""
}

func envInt(key string, def int) int {
	if v := env(key, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := env(key, ""); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// ResolveLegacyDBPath 兼容更名前后的数据库文件名。
//
// 默认库文件从 data/opsmonitor.db 改成了 data/strix.db。按新默认值找不到文件、
// 而同级目录下存在上一代的 opsmonitor.db 时，沿用旧文件——否则升级后会在旁边
// 新建一个空库，现象就是「数据全丢了」。返回 true 表示用了旧路径。
func ResolveLegacyDBPath(path string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		return path, false
	}
	if _, err := os.Stat(path); err == nil {
		return path, false
	}
	if !strings.EqualFold(filepath.Base(path), "strix.db") {
		return path, false
	}
	old := filepath.Join(filepath.Dir(path), "opsmonitor.db")
	if _, err := os.Stat(old); err == nil {
		return old, true
	}
	return path, false
}

// ParseServer 解析服务端启动参数，环境变量优先级低于命令行。
func ParseServer() *Server {
	cfg := &Server{
		Addr:         env("STRIX_ADDR", ":9999"),
		DBPath:       env("STRIX_DB", "./data/strix.db"),
		Token:        env("STRIX_TOKEN", "strix"),
		AdminUser:    env("STRIX_ADMIN_USER", "admin"),
		AdminPass:    env("STRIX_ADMIN_PASS", "strix"),
		Retention:    envInt("STRIX_RETENTION", 30),
		OfflineSec:   envInt("STRIX_OFFLINE_SEC", 180),
		PurgeSec:     envInt("STRIX_PURGE_INTERVAL", 3600),
		ReadConns:    envInt("STRIX_READ_CONNS", 8),
		WriteQueue:   envInt("STRIX_WRITE_QUEUE", 8192),
		ProbeWorkers: envInt("STRIX_PROBE_WORKERS", 64),
		LoginAlert:   env("STRIX_LOGIN_ALERT", "true"),
		CPUAlert:     envFloat("STRIX_CPU_ALERT", 90),
		MemAlert:     envFloat("STRIX_MEM_ALERT", 90),
		DiskAlert:    envFloat("STRIX_DISK_ALERT", 90),
		LoadAlert:    envFloat("STRIX_LOAD_ALERT", 10),
	}
	cfg.AlertCoolDown = time.Duration(envInt("STRIX_ALERT_COOLDOWN_MIN", 10)) * time.Minute
	cfg.ReleaseDir = env("STRIX_RELEASE_DIR", "")
	cfg.PublicURL = env("STRIX_PUBLIC_URL", "")
	cfg.LicenseSecret = env("STRIX_LICENSE_SECRET", "Strix-license")
	// 旧密钥默认值与新默认值一起交给验签器：存量授权码是用旧的签的，两个都得认。
	cfg.LicenseSecretLegacy = env("STRIX_LICENSE_SECRET_LEGACY", "OpsMonitor-license")
	cfg.LicenseKey = env("STRIX_LICENSE_KEY", "")
	cfg.LicenseMachine = env("STRIX_LICENSE_MACHINE", "")
	cfg.LicenseFreeHost = envInt("STRIX_LICENSE_FREE_HOSTS", 10)
	cfg.LicenseFreeUser = envInt("STRIX_LICENSE_FREE_USERS", 3)
	cfg.HubURL = env("STRIX_HUB", DefaultHubURL)
	cfg.HubAutoUpdate = env("STRIX_HUB_AUTO_UPDATE", "download")
	cfg.HubRestart = env("STRIX_HUB_RESTART", "auto")
	cfg.HubUserAgent = env("STRIX_HUB_UA", "")
	// 令牌优先级：-hub-token 参数 > STRIX_HUB_TOKEN 环境变量 > 内置令牌。
	// 内置令牌不以明文写在代码里，见 hubtoken.go 的 defaultHubToken。
	hubTokenFromEnv := env("STRIX_HUB_TOKEN", "")
	hubMin := envInt("STRIX_HUB_INTERVAL_MIN", 5)

	flag.StringVar(&cfg.Addr, "addr", cfg.Addr, "HTTP 监听地址，例如 :9999")
	flag.StringVar(&cfg.DBPath, "db", cfg.DBPath, "SQLite 数据库文件路径")
	flag.StringVar(&cfg.Token, "token", cfg.Token, "agent 通信密钥，agent 需配置相同值")
	flag.StringVar(&cfg.AdminUser, "user", cfg.AdminUser, "Web 控制台用户名")
	flag.StringVar(&cfg.AdminPass, "pass", cfg.AdminPass, "Web 控制台密码")
	flag.IntVar(&cfg.Retention, "retention", cfg.Retention, "监控历史数据保留天数")
	flag.IntVar(&cfg.OfflineSec, "offline", cfg.OfflineSec, "超过该秒数未上报判定主机离线")
	flag.Float64Var(&cfg.CPUAlert, "cpu-alert", cfg.CPUAlert, "CPU 使用率告警阈值(%)")
	flag.Float64Var(&cfg.MemAlert, "mem-alert", cfg.MemAlert, "内存使用率告警阈值(%)")
	flag.Float64Var(&cfg.DiskAlert, "disk-alert", cfg.DiskAlert, "磁盘使用率告警阈值(%)")
	flag.Float64Var(&cfg.LoadAlert, "load-alert", cfg.LoadAlert, "系统负载告警阈值")
	flag.IntVar(&cfg.ReadConns, "read-conns", cfg.ReadConns, "SQLite 读连接池大小，大规模部署可调大")
	flag.IntVar(&cfg.WriteQueue, "write-queue", cfg.WriteQueue, "上报写队列容量，突发流量大时调大")
	flag.IntVar(&cfg.ProbeWorkers, "probe-workers", cfg.ProbeWorkers, "服务端主动探测（SNMP/PING/数据库等）并发度")
	cooldownMin := int(cfg.AlertCoolDown.Minutes())
	loginAlert := cfg.LoginAlert == "true"
	flag.IntVar(&cfg.PurgeSec, "purge", cfg.PurgeSec, "历史数据清理间隔（秒）")
	flag.IntVar(&cooldownMin, "alert-cooldown", cooldownMin, "同类告警去重冷却时间（分钟）")
	flag.BoolVar(&loginAlert, "login-alert", loginAlert, "主机登录用户变化时是否生成提醒")
	flag.StringVar(&cfg.ReleaseDir, "release-dir", cfg.ReleaseDir, "采集端发布包存放目录，默认在数据库同级的 releases/ 下")
	flag.StringVar(&cfg.PublicURL, "public-url", cfg.PublicURL, "对外访问地址（反向代理场景），安装脚本与更新下载使用")
	flag.StringVar(&cfg.LicenseSecret, "license-secret", cfg.LicenseSecret, "旧格式授权码(HMAC)的校验密钥；新格式(Ed25519)授权码用内置公钥校验，与本参数无关")
	flag.StringVar(&cfg.LicenseKey, "license", cfg.LicenseKey, "启动时直接导入授权码")
	flag.StringVar(&cfg.LicenseMachine, "license-machine", cfg.LicenseMachine, "机器码，默认按本机主机名与网卡自动生成")
	flag.IntVar(&cfg.LicenseFreeHost, "free-hosts", cfg.LicenseFreeHost, "未授权（社区版）允许接入的主机数")
	flag.IntVar(&cfg.LicenseFreeUser, "free-users", cfg.LicenseFreeUser, "未授权（社区版）允许创建的控制台账号数")
	// -hub 特意用**空默认值**注册：Go 的 flag 包会把非空默认值回显成 (default "...")，
	// 那样内置的授权系统地址就会出现在 -h 帮助里。是否使用内置地址改为解析后判断。
	hubFlag := ""
	flag.StringVar(&hubFlag, "hub", hubFlag, "授权系统（更新与授权中心）地址。不传则使用内置地址；传空（-hub=）则完全离线运行")
	flag.StringVar(&cfg.HubToken, "hub-token", hubTokenFromEnv,
		"授权系统拉取令牌；留空则使用内置令牌（默认），仅当改用自建授权中心时才需覆盖")
	flag.IntVar(&hubMin, "hub-interval", hubMin, "从授权系统拉取更新与授权的间隔（分钟）")
	flag.StringVar(&cfg.HubAutoUpdate, "hub-auto-update", cfg.HubAutoUpdate,
		"服务端自身的自动更新策略：download 自动下载并就位（重启后生效）/ check 只提示 / off 关闭")
	flag.StringVar(&cfg.HubRestart, "hub-restart", cfg.HubRestart,
		"平台自升级就位后是否自动重启：auto 自动重启生效（默认）/ off 只替换不重启，需人工重启")
	flag.StringVar(&cfg.HubUserAgent, "hub-ua", cfg.HubUserAgent,
		"访问授权系统时的 User-Agent，留空用默认 strix-server/<版本>；被网关按 UA 拦截时可改")
	flag.BoolVar(&cfg.PrintVersion, "version", false, "打印版本号后退出")
	flag.Parse()
	// 未显式传 -hub 时保留配置里的值（来自 STRIX_HUB 环境变量，或内置默认地址）；
	// 显式传入才覆盖——传空值（-hub=）即「完全离线」，与原语义一致。
	hubPassed := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "hub" {
			hubPassed = true
		}
	})
	if hubPassed {
		cfg.HubURL = hubFlag
	}
	cfg.AlertCoolDown = time.Duration(cooldownMin) * time.Minute
	// 参数与环境变量都没给令牌时，回落到内置令牌，使平台开箱即可与授权中心通信。
	// 在线离线部署（显式 -hub ""）不受影响：HubURL 为空时同步逻辑整体短路。
	if strings.TrimSpace(cfg.HubToken) == "" {
		cfg.HubToken = defaultHubToken()
	}
	cfg.HubURL = strings.TrimRight(strings.TrimSpace(cfg.HubURL), "/")
	if hubMin < 1 {
		hubMin = 5
	}
	cfg.HubInterval = time.Duration(hubMin) * time.Minute
	switch cfg.HubAutoUpdate {
	case "check", "download", "off":
	default:
		// 未识别的值按「只提示」处理，不擅自替换正在运行的服务端二进制
		cfg.HubAutoUpdate = "check"
	}
	// 只有显式写 off/false/no/manual 才关闭自动重启，其余（含空值）都按 auto：
	// 默认必须能自升级，否则会退回「换了文件却不生效」的老问题。
	switch strings.ToLower(strings.TrimSpace(cfg.HubRestart)) {
	case "off", "false", "no", "0", "manual", "none":
		cfg.HubRestart = "off"
	default:
		cfg.HubRestart = "auto"
	}
	if loginAlert {
		cfg.LoginAlert = "true"
	} else {
		cfg.LoginAlert = "false"
	}
	// 更名前默认密钥是 opsmonitor，最新默认是 strix。只有在没改过密钥、仍用默认值时
	// 才把旧值留作兼容：已改成强密钥的部署不该平白多出一个可用口令。
	if cfg.Token == "strix" {
		cfg.TokenLegacy = "opsmonitor"
	}
	return cfg
}

// TokenOK 判断采集端送来的令牌是否匹配本服务端。
// 明文与 MD5 都接受（一键安装脚本下发的是 MD5），并兼容更名前的默认令牌。
func (c *Server) TokenOK(got string) bool {
	if got == "" {
		return false
	}
	if got == c.Token || got == MD5Token(c.Token) {
		return true
	}
	if c.TokenLegacy != "" {
		return got == c.TokenLegacy || got == MD5Token(c.TokenLegacy)
	}
	return false
}

// ParseAgent 解析采集端启动参数。
func ParseAgent() *Agent {
	hostname, _ := os.Hostname()
	cfg := &Agent{
		ServerURL:    env("STRIX_SERVER", "http://127.0.0.1:9999"),
		Token:        env("STRIX_TOKEN", "strix"),
		Hostname:     env("STRIX_HOSTNAME", hostname),
		AllowCommand: env("STRIX_ALLOW_COMMAND", "true") != "false",
		UpdateMode:   env("STRIX_UPDATE", "auto"),
		InstallDir:   env("STRIX_INSTALL_DIR", ""),
		RemoteConfig: env("STRIX_REMOTE_CONFIG", "auto"),
	}
	intervalSec := envInt("STRIX_INTERVAL", 60)
	updateMin := envInt("STRIX_UPDATE_INTERVAL_MIN", 30)

	flag.StringVar(&cfg.ServerURL, "server", cfg.ServerURL, "服务端地址，例如 http://127.0.0.1:9999")
	flag.StringVar(&cfg.Token, "token", cfg.Token, "与服务端一致的通信密钥")
	flag.StringVar(&cfg.Hostname, "name", cfg.Hostname, "本主机在监控平台显示的名称")
	flag.StringVar(&cfg.TokenHash, "token-hash", cfg.TokenHash, "通信密钥的 MD5（与 -token 二选一，一键安装脚本使用）")
	flag.BoolVar(&cfg.AllowCommand, "allow-command", cfg.AllowCommand, "是否允许执行服务端下发的指令")
	flag.IntVar(&intervalSec, "interval", intervalSec, "采集上报间隔（秒）")
	flag.StringVar(&cfg.UpdateMode, "update", cfg.UpdateMode, "自动更新策略：auto 自动升级 / check 只检查不升级 / off 关闭")
	flag.IntVar(&updateMin, "update-interval", updateMin, "版本检查间隔（分钟）")
	flag.StringVar(&cfg.InstallDir, "install-dir", cfg.InstallDir, "安装目录，留空则用可执行文件所在目录")
	flag.StringVar(&cfg.RemoteConfig, "remote-config", cfg.RemoteConfig, "是否接受服务端下发的运行配置：auto 接受 / off 只用本地参数")
	flag.BoolVar(&cfg.PrintVersion, "version", false, "打印版本号后退出")
	flag.Parse()
	cfg.Interval = time.Duration(intervalSec) * time.Second
	cfg.UpdateInterval = time.Duration(updateMin) * time.Minute
	if cfg.TokenHash == "" {
		cfg.TokenHash = MD5Token(cfg.Token)
	}
	switch cfg.UpdateMode {
	case "auto", "check", "off":
	default:
		cfg.UpdateMode = "auto"
	}
	if cfg.RemoteConfig != "off" {
		cfg.RemoteConfig = "auto"
	}
	return cfg
}
