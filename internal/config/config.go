// Package config 提供命令行与环境变量配置，以及告警阈值。
package config

import (
	"crypto/md5"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Server 服务端配置。
type Server struct {
	Addr          string
	DBPath        string
	Token         string
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

	ReleaseDir      string // 采集端发布包的存放目录
	PublicURL       string // 对外访问地址，安装脚本与更新下载用它拼接绝对地址
	LicenseSecret   string // 旧格式（HMAC）授权码的校验密钥；现仅供兼容存量授权码
	LicenseKey      string // 启动时直接导入的授权码（可选）
	LicenseMachine  string // 强制指定的机器码，默认自动取本机
	LicenseFreeHost int    // 未授权时允许接入的主机数
	LicenseFreeUser int    // 未授权时允许创建的控制台账号数

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
// 平台默认就指向官方授权中心：授权导入与版本更新是这套系统的固有环节，
// 不该要求每个部署都手工加一个 -hub。内网离线环境用 -hub "" 显式关闭。
const DefaultHubURL = "https://1999091.xyz"

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

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// ParseServer 解析服务端启动参数，环境变量优先级低于命令行。
func ParseServer() *Server {
	cfg := &Server{
		Addr:         env("OPSM_ADDR", ":9999"),
		DBPath:       env("OPSM_DB", "./data/opsmonitor.db"),
		Token:        env("OPSM_TOKEN", "opsmonitor"),
		AdminUser:    env("OPSM_ADMIN_USER", "admin"),
		AdminPass:    env("OPSM_ADMIN_PASS", "opsmonitor"),
		Retention:    envInt("OPSM_RETENTION", 30),
		OfflineSec:   envInt("OPSM_OFFLINE_SEC", 180),
		PurgeSec:     envInt("OPSM_PURGE_INTERVAL", 3600),
		ReadConns:    envInt("OPSM_READ_CONNS", 8),
		WriteQueue:   envInt("OPSM_WRITE_QUEUE", 8192),
		ProbeWorkers: envInt("OPSM_PROBE_WORKERS", 64),
		LoginAlert:   env("OPSM_LOGIN_ALERT", "true"),
		CPUAlert:     envFloat("OPSM_CPU_ALERT", 90),
		MemAlert:     envFloat("OPSM_MEM_ALERT", 90),
		DiskAlert:    envFloat("OPSM_DISK_ALERT", 90),
		LoadAlert:    envFloat("OPSM_LOAD_ALERT", 10),
	}
	cfg.AlertCoolDown = time.Duration(envInt("OPSM_ALERT_COOLDOWN_MIN", 10)) * time.Minute
	cfg.ReleaseDir = env("OPSM_RELEASE_DIR", "")
	cfg.PublicURL = env("OPSM_PUBLIC_URL", "")
	cfg.LicenseSecret = env("OPSM_LICENSE_SECRET", "OpsMonitor-license")
	cfg.LicenseKey = env("OPSM_LICENSE_KEY", "")
	cfg.LicenseMachine = env("OPSM_LICENSE_MACHINE", "")
	cfg.LicenseFreeHost = envInt("OPSM_LICENSE_FREE_HOSTS", 10)
	cfg.LicenseFreeUser = envInt("OPSM_LICENSE_FREE_USERS", 3)
	cfg.HubURL = env("OPSM_HUB", DefaultHubURL)
	cfg.HubAutoUpdate = env("OPSM_HUB_AUTO_UPDATE", "download")
	cfg.HubRestart = env("OPSM_HUB_RESTART", "auto")
	cfg.HubUserAgent = env("OPSM_HUB_UA", "")
	// 令牌优先级：-hub-token 参数 > OPSM_HUB_TOKEN 环境变量 > 内置令牌。
	// 内置令牌不以明文写在代码里，见 hubtoken.go 的 defaultHubToken。
	hubTokenFromEnv := env("OPSM_HUB_TOKEN", "")
	hubMin := envInt("OPSM_HUB_INTERVAL_MIN", 5)

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
	flag.StringVar(&cfg.HubURL, "hub", cfg.HubURL, "授权系统（更新与授权中心）地址，默认 "+DefaultHubURL+"，留空则完全离线运行")
	flag.StringVar(&cfg.HubToken, "hub-token", hubTokenFromEnv,
		"授权系统拉取令牌；留空则使用内置令牌（默认），仅当改用自建授权中心时才需覆盖")
	flag.IntVar(&hubMin, "hub-interval", hubMin, "从授权系统拉取更新与授权的间隔（分钟）")
	flag.StringVar(&cfg.HubAutoUpdate, "hub-auto-update", cfg.HubAutoUpdate,
		"服务端自身的自动更新策略：download 自动下载并就位（重启后生效）/ check 只提示 / off 关闭")
	flag.StringVar(&cfg.HubRestart, "hub-restart", cfg.HubRestart,
		"平台自升级就位后是否自动重启：auto 自动重启生效（默认）/ off 只替换不重启，需人工重启")
	flag.StringVar(&cfg.HubUserAgent, "hub-ua", cfg.HubUserAgent,
		"访问授权系统时的 User-Agent，留空用默认 opsmonitor-server/<版本>；被网关按 UA 拦截时可改")
	flag.BoolVar(&cfg.PrintVersion, "version", false, "打印版本号后退出")
	flag.Parse()
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
	return cfg
}

// ParseAgent 解析采集端启动参数。
func ParseAgent() *Agent {
	hostname, _ := os.Hostname()
	cfg := &Agent{
		ServerURL:    env("OPSM_SERVER", "http://127.0.0.1:9999"),
		Token:        env("OPSM_TOKEN", "opsmonitor"),
		Hostname:     env("OPSM_HOSTNAME", hostname),
		AllowCommand: env("OPSM_ALLOW_COMMAND", "true") != "false",
		UpdateMode:   env("OPSM_UPDATE", "auto"),
		InstallDir:   env("OPSM_INSTALL_DIR", ""),
		RemoteConfig: env("OPSM_REMOTE_CONFIG", "auto"),
	}
	intervalSec := envInt("OPSM_INTERVAL", 60)
	updateMin := envInt("OPSM_UPDATE_INTERVAL_MIN", 30)

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
