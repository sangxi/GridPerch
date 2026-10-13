// Package server 提供 HTTP 服务端：agent 数据接收、REST API 与 Web 控制台。
package server

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sangxi/OpsMonitor/internal/config"
	"github.com/sangxi/OpsMonitor/internal/license"
	"github.com/sangxi/OpsMonitor/internal/model"
	"github.com/sangxi/OpsMonitor/internal/notify"
	"github.com/sangxi/OpsMonitor/internal/poller"
	"github.com/sangxi/OpsMonitor/internal/store"
	"github.com/sangxi/OpsMonitor/internal/version"
)

//go:embed all:web
var webFS embed.FS

const (
	sessionCookie = "opsm_session"
	sessionTTL    = 12 * time.Hour
)

// session 记录登录会话归属，用于权限判断。
type session struct {
	username string
	expire   time.Time
}

// Server 承载 HTTP 路由与会话管理。
type Server struct {
	cfg   *config.Server
	store *store.Store

	mu       sync.Mutex
	sessions map[string]session
	stopCh   chan struct{}

	// 授权状态缓存：上报接口每次都要校验配额，避免每条上报都读库+验签
	licMu    sync.RWMutex
	licCache *license.Status
	// licVer 授权码校验器：Ed25519 公钥编译在二进制里，运行时无法替换
	licVer license.Verifier

	// 与授权系统（更新与授权中心）的同步状态
	hubMu sync.Mutex
	hub   HubStatus
}

// New 创建服务端实例。
func New(cfg *config.Server, st *store.Store) *Server {
	s := &Server{
		cfg: cfg, store: st, sessions: map[string]session{}, stopCh: make(chan struct{}),
		hub: HubStatus{AgentCached: map[string]string{}},
	}
	pub, err := license.ParsePublicKey(config.LicensePublicKey)
	if err != nil {
		log.Printf("警告：内置授权验签公钥无法解析（%v），新格式授权码将无法校验", err)
	}
	if over := config.LicenseKeyFingerprint; over != "" && license.Fingerprint(pub) != over {
		log.Printf("警告：内置公钥指纹为 %s，与 LicenseKeyFingerprint 声明的 %s 不一致，请检查 licensekey.go",
			license.Fingerprint(pub), over)
	}
	s.licVer = license.NewVerifier(pub, cfg.LicenseSecret)
	s.importStartupLicense()
	return s
}

// Handler 返回注册好全部路由的处理器。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// agent 上报接口，与原 wgcloud 路径保持一致
	mux.HandleFunc("POST /agent/minTask", s.handleMinTask)
	mux.HandleFunc("GET /agent/tasks", s.handleAgentTasks)
	mux.HandleFunc("POST /agent/monitor", s.handleAgentMonitorResult)
	mux.HandleFunc("GET /agent/commands", s.handleAgentCommands)
	mux.HandleFunc("POST /agent/command", s.handleAgentCommandResult)
	// 采集端升级通道：查询最新版本、下载二进制、回执升级结果
	mux.HandleFunc("GET /agent/version", s.handleAgentVersion)
	mux.HandleFunc("GET /agent/download", s.handleAgentDownload)
	mux.HandleFunc("POST /agent/update", s.handleAgentUpdateReport)
	// 采集端配置拉取：安装命令执行后由采集端定期取回行为配置
	mux.HandleFunc("GET /agent/config", s.handleAgentConfig)
	// 一键安装脚本：脚本内容由服务端动态生成，目标主机直接 curl | bash 从平台拉取
	mux.HandleFunc("GET /install.sh", s.handleInstallScript)
	mux.HandleFunc("GET /install.ps1", s.handleInstallScript)

	// 控制台 API
	// 写操作统一经 s.audit 包裹，记录操作审计（登录成功与失败都会留痕）
	mux.Handle("POST /api/login", s.audit(http.HandlerFunc(s.handleLogin)))
	mux.Handle("POST /api/logout", s.audit(http.HandlerFunc(s.handleLogout)))
	mux.HandleFunc("GET /api/settings/public", s.handlePublicSettings)
	mux.Handle("GET /api/profile", s.auth(http.HandlerFunc(s.handleProfile)))
	mux.Handle("GET /api/dashboard", s.auth(http.HandlerFunc(s.handleDashboard)))
	mux.Handle("GET /api/hosts", s.auth(http.HandlerFunc(s.handleHosts)))
	mux.Handle("GET /api/hosts/{host}/disks", s.auth(http.HandlerFunc(s.handleDisks)))
	mux.Handle("GET /api/hosts/{host}/processes", s.auth(http.HandlerFunc(s.handleHostProcesses)))
	mux.Handle("GET /api/hosts/{host}/sys-events", s.auth(http.HandlerFunc(s.handleHostSysEvents)))
	// 主机目录：名称支持中文，用于按业务/机房/环境归类
	mux.Handle("GET /api/host-catalogs", s.auth(http.HandlerFunc(s.hostCatalogs)))
	mux.Handle("POST /api/host-catalogs", s.auth(s.admin(s.audit(http.HandlerFunc(s.hostCatalogs)))))
	mux.Handle("PUT /api/host-catalogs/{id}", s.auth(s.admin(s.audit(http.HandlerFunc(s.hostCatalogItem)))))
	mux.Handle("DELETE /api/host-catalogs/{id}", s.auth(s.admin(s.audit(http.HandlerFunc(s.hostCatalogItem)))))
	mux.Handle("GET /api/host-catalogs/{id}/members", s.auth(http.HandlerFunc(s.hostCatalogMembers)))
	mux.Handle("POST /api/hosts/{host}/catalog", s.auth(s.adminOrOperator(http.HandlerFunc(s.hostCatalogAssign))))
	// 添加主机：下发采集端接入参数（通信密钥、服务端地址）
	mux.Handle("GET /api/agent/install", s.auth(http.HandlerFunc(s.handleAgentInstall)))
	mux.Handle("GET /api/metrics", s.auth(http.HandlerFunc(s.handleMetrics)))
	mux.Handle("GET /api/metrics/overview", s.auth(http.HandlerFunc(s.handleMetricOverview)))
	mux.Handle("GET /api/alerts", s.auth(http.HandlerFunc(s.handleAlerts)))
	mux.Handle("POST /api/alerts/{id}/resolve", s.auth(s.audit(http.HandlerFunc(s.handleResolve))))
	mux.Handle("GET /api/login-events", s.auth(http.HandlerFunc(s.handleLoginEvents)))

	// 监控项管理
	mux.Handle("GET /api/tasks", s.auth(http.HandlerFunc(s.handleTasks)))
	mux.Handle("POST /api/tasks", s.auth(s.audit(http.HandlerFunc(s.handleTasks))))
	mux.Handle("GET /api/tasks/types", s.auth(http.HandlerFunc(s.handleTaskTypes)))
	mux.Handle("GET /api/tasks/{id}", s.auth(http.HandlerFunc(s.handleTaskItem)))
	mux.Handle("PUT /api/tasks/{id}", s.auth(s.audit(http.HandlerFunc(s.handleTaskItem))))
	mux.Handle("DELETE /api/tasks/{id}", s.auth(s.audit(http.HandlerFunc(s.handleTaskItem))))
	mux.Handle("GET /api/monitor/states", s.auth(http.HandlerFunc(s.handleMonitorStates)))
	mux.Handle("GET /api/monitor/events", s.auth(http.HandlerFunc(s.handleMonitorEvents)))
	mux.Handle("GET /api/monitor/summary", s.auth(http.HandlerFunc(s.handleMonitorSummary)))
	mux.Handle("GET /api/screen", s.auth(http.HandlerFunc(s.handleScreen)))

	// 指令下发
	mux.Handle("GET /api/commands", s.auth(http.HandlerFunc(s.handleCommands)))
	mux.Handle("POST /api/commands", s.auth(s.audit(http.HandlerFunc(s.handleCommands))))
	mux.Handle("GET /api/commands/{id}", s.auth(http.HandlerFunc(s.handleCommandGet)))
	mux.Handle("DELETE /api/commands/{id}", s.auth(s.audit(http.HandlerFunc(s.handleCommandItem))))

	// 工具箱：按需网络探测（服务端发起，与目标主机系统无关）
	mux.Handle("POST /api/tools/probe", s.auth(s.adminOrOperator(s.audit(http.HandlerFunc(s.handleToolProbe)))))

	// 资产台账与设备账号
	mux.Handle("GET /api/assets", s.auth(http.HandlerFunc(s.handleAssets)))
	mux.Handle("POST /api/assets", s.auth(s.audit(http.HandlerFunc(s.handleAssets))))
	mux.Handle("GET /api/assets/stats", s.auth(http.HandlerFunc(s.handleAssetStats)))
	mux.Handle("GET /api/assets/export", s.auth(http.HandlerFunc(s.handleAssetExport)))
	mux.Handle("POST /api/assets/batch", s.auth(s.audit(http.HandlerFunc(s.handleAssetBatch))))
	mux.Handle("PUT /api/assets/{id}", s.auth(s.audit(http.HandlerFunc(s.handleAssetItem))))
	mux.Handle("DELETE /api/assets/{id}", s.auth(s.audit(http.HandlerFunc(s.handleAssetItem))))
	mux.Handle("GET /api/device-accounts", s.auth(http.HandlerFunc(s.handleDeviceAccounts)))
	mux.Handle("POST /api/device-accounts", s.auth(s.audit(http.HandlerFunc(s.handleDeviceAccounts))))
	mux.Handle("PUT /api/device-accounts/{id}", s.auth(s.audit(http.HandlerFunc(s.handleDeviceAccountItem))))
	mux.Handle("DELETE /api/device-accounts/{id}", s.auth(s.audit(http.HandlerFunc(s.handleDeviceAccountItem))))
	mux.Handle("POST /api/device-accounts/{id}/test", s.auth(s.audit(http.HandlerFunc(s.handleDeviceAccountTest))))

	// 巡检报告
	mux.Handle("GET /api/inspections", s.auth(http.HandlerFunc(s.handleInspections)))
	mux.Handle("POST /api/inspections", s.auth(s.audit(http.HandlerFunc(s.handleInspections))))
	mux.Handle("GET /api/inspections/{id}", s.auth(http.HandlerFunc(s.handleInspectionItem)))
	mux.Handle("GET /api/inspections/{id}/export", s.auth(http.HandlerFunc(s.handleInspectionExport)))
	mux.Handle("DELETE /api/inspections/{id}", s.auth(s.audit(http.HandlerFunc(s.handleInspectionItem))))

	// 数据导出
	mux.Handle("GET /api/export/{kind}", s.auth(http.HandlerFunc(s.handleExport)))

	// 采集端版本发布与升级进度
	mux.Handle("GET /api/releases", s.auth(s.adminOrOperator(http.HandlerFunc(s.handleReleases))))
	mux.Handle("POST /api/releases", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleReleases)))))
	mux.Handle("DELETE /api/releases/{id}", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleReleaseItem)))))
	mux.Handle("POST /api/releases/{id}/latest", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleReleaseSetLatest)))))
	mux.Handle("GET /api/host-upgrades", s.auth(http.HandlerFunc(s.handleHostUpgradeStatus)))

	// 采集端配置：全局默认值（管理员/运维可读，仅管理员可改）与主机级覆盖
	mux.Handle("GET /api/agent-config", s.auth(s.adminOrOperator(http.HandlerFunc(s.handleAgentConfigAPI))))
	mux.Handle("PUT /api/agent-config", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleAgentConfigAPI)))))
	mux.Handle("GET /api/agent-config/revision", s.auth(s.adminOrOperator(http.HandlerFunc(s.handleAgentConfigRevision))))
	mux.Handle("GET /api/hosts/{host}/agent-config", s.auth(s.adminOrOperator(http.HandlerFunc(s.handleHostAgentConfig))))
	mux.Handle("PUT /api/hosts/{host}/agent-config", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleHostAgentConfig)))))
	mux.Handle("DELETE /api/hosts/{host}/agent-config", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleHostAgentConfig)))))
	// 主机改名 / 删除：会级联改动历史数据，只开放给管理员
	mux.Handle("PUT /api/hosts/{host}/rename", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleHostRename)))))
	mux.Handle("DELETE /api/hosts/{host}", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleHostDelete)))))

	// Webhook 机器人：地址本身即凭据，只开放给管理员
	mux.Handle("GET /api/webhooks", s.auth(s.admin(http.HandlerFunc(s.handleWebhooks))))
	mux.Handle("POST /api/webhooks", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleWebhooks)))))
	mux.Handle("PUT /api/webhooks/{id}", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleWebhookItem)))))
	mux.Handle("DELETE /api/webhooks/{id}", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleWebhookItem)))))
	mux.Handle("POST /api/webhooks/{id}/test", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleWebhookTest)))))

	// 授权管理：授权详情含机器码与客户信息，只开放给管理员
	mux.Handle("GET /api/license", s.auth(s.admin(http.HandlerFunc(s.handleLicense))))
	mux.Handle("POST /api/license", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleLicense)))))
	mux.Handle("DELETE /api/license", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleLicense)))))

	// 授权系统（更新与授权中心）：平台从授权系统拉取最新版本与本机授权码
	mux.Handle("GET /api/hub/status", s.auth(s.adminOrOperator(http.HandlerFunc(s.handleHubStatus))))
	mux.Handle("POST /api/hub/check", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleHubCheck)))))
	mux.Handle("POST /api/hub/pull", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleHubPull)))))

	// 系统管理（管理员专属）
	mux.Handle("GET /api/members", s.auth(s.admin(http.HandlerFunc(s.handleMembers))))
	mux.Handle("POST /api/members", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleMembers)))))
	mux.Handle("PUT /api/members/{username}", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleMemberItem)))))
	mux.Handle("DELETE /api/members/{username}", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleMemberItem)))))
	mux.Handle("GET /api/tokens", s.auth(s.admin(http.HandlerFunc(s.handleTokens))))
	mux.Handle("POST /api/tokens", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleTokens)))))
	mux.Handle("DELETE /api/tokens/{id}", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleTokenItem)))))
	mux.Handle("GET /api/settings", s.auth(s.admin(http.HandlerFunc(s.handleSettings))))
	mux.Handle("PUT /api/settings", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleSettings)))))
	mux.Handle("POST /api/test-mail", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleTestMail)))))

	// 审计日志与邮件发送记录
	mux.Handle("GET /api/audit", s.auth(s.admin(http.HandlerFunc(s.handleAudit))))
	mux.Handle("DELETE /api/audit", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleAudit)))))
	mux.Handle("GET /api/audit/stats", s.auth(s.admin(http.HandlerFunc(s.handleAuditStats))))
	mux.Handle("GET /api/audit/export", s.auth(s.admin(http.HandlerFunc(s.handleAuditExport))))
	mux.Handle("GET /api/mail/logs", s.auth(s.admin(http.HandlerFunc(s.handleMailLogs))))
	mux.Handle("DELETE /api/mail/logs", s.auth(s.admin(s.audit(http.HandlerFunc(s.handleMailLogs)))))

	// 数据开放接口，使用 API Key 鉴权
	mux.HandleFunc("GET /openapi/v1/hosts", s.openAPI("hosts", s.openHosts))
	mux.HandleFunc("GET /openapi/v1/alerts", s.openAPI("alerts", s.openAlerts))
	mux.HandleFunc("GET /openapi/v1/metrics", s.openAPI("metrics", s.openMetrics))
	mux.HandleFunc("GET /openapi/v1/monitors", s.openAPI("monitors", s.openMonitors))
	mux.HandleFunc("GET /openapi/v1/assets", s.openAPI("assets", s.openAssets))

	// 静态资源与页面
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("内嵌资源加载失败: %v", err)
	}
	// 前端界面是编译进二进制的：升级后如果浏览器或反向代理仍拿旧的 app.js，
	// 就会出现「二进制换了、面板还是老样子 / 按钮点了没反应」的假象。
	// 统一加 no-cache（可缓存但每次必须回源校验），确保升级即时生效。
	mux.Handle("GET /assets/", noCache(http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /screen", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, static, "screen.html")
	})
	// 每套大屏模板都有独立地址（/screen/1 ~ /screen/8），便于给不同屏幕固定一套版式，
	// 也方便在导航大屏、监控电视这类场景里直接写死链接。
	mux.HandleFunc("GET /screen/{n}", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, static, "screen.html")
	})
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, static, "docs.html")
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, static, "index.html")
	})
	return mux
}

func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		http.Error(w, "页面不存在", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", detectType(name))
	// 页面入口同样禁止强缓存，保证升级后立刻拿到引用最新资源的新版 HTML。
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

// noCache 给内嵌静态资源加上 no-cache：允许缓存但每次必须回源校验，
// 避免升级二进制后浏览器/反向代理仍在使用旧的前端文件。
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func detectType(name string) string {
	switch {
	case len(name) >= 4 && name[len(name)-4:] == ".css":
		return "text/css; charset=utf-8"
	case len(name) >= 3 && name[len(name)-3:] == ".js":
		return "application/javascript; charset=utf-8"
	case len(name) >= 5 && name[len(name)-5:] == ".html":
		return "text/html; charset=utf-8"
	default:
		return http.DetectContentType([]byte(name))
	}
}

// Run 启动 HTTP 服务并拉起后台维护任务。
func (s *Server) Run() error {
	// 先核对上一次的自动升级是否真的生效：新版本起不来就立刻回滚，
	// 避免 systemd 的 Restart=always 把它变成无限重启循环。
	s.confirmSelfUpgrade()
	go s.maintenance()
	// 配置了授权系统就先同步一次，授权与发布物不必等第一个间隔
	if strings.TrimSpace(s.cfg.HubURL) != "" {
		go s.hubSync()
	}
	go poller.New(s.cfg, s.store).Run(s.stopCh)
	log.Printf("服务端启动：监听 %s，数据库 %s，数据保留 %s", s.cfg.Addr, s.cfg.DBPath, retentionDesc(s.cfg.Retention))
	urls := accessURLs(s.cfg.Addr)
	for _, u := range urls {
		log.Printf("控制台访问地址：%s", u)
	}
	// 只在回环上监听时，采集端装到别的机器上必然连不上，这里主动提示改用监听全部网卡
	if len(urls) == 1 && strings.Contains(urls[0], "127.0.0.1") {
		log.Printf("提示：当前只监听本机地址，其他主机无法访问。要让局域网内主机接入，请改用 -addr :9999 启动")
	}
	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return listenAndServeWithRetry(srv)
}

// listenAndServeWithRetry 在端口被占用时重试一段时间再放弃。
//
// 自动升级的重启顺序是「先拉起新进程、旧进程再退出」，新进程启动时端口可能还
// 被旧进程占着；systemd 的 RestartSec 与容器重启也可能撞上 TIME_WAIT。
// 给一个短暂重试窗口，可把升级过程中的服务中断降到最低，避免
// 「升级后端口占用导致新版本起不来」进而被 confirmSelfUpgrade 误判回滚。
func listenAndServeWithRetry(srv *http.Server) error {
	const retryWindow = 20 * time.Second
	const retryStep = 500 * time.Millisecond
	deadline := time.Now().Add(retryWindow)
	for {
		err := srv.ListenAndServe()
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return err
		}
		if !isAddrInUse(err) || time.Now().After(deadline) {
			return err
		}
		log.Printf("监听 %s 暂不可用（%v），%v 后重试……", srv.Addr, err, retryStep)
		time.Sleep(retryStep)
	}
}

// isAddrInUse 判断是否为「端口被占用」类错误（跨平台，含容器内代理的文案差异）。
func isAddrInUse(err error) bool {
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "only one usage of each socket address")
}

// accessURLs 列出控制台可能的访问地址，帮助用户在「添加主机」时填对地址，
// 避免在服务端本机打开控制台、却把带 127.0.0.1 的命令拿到别的机器上执行。
func accessURLs(addr string) []string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	var out []string
	add := func(h string) {
		out = append(out, "http://"+net.JoinHostPort(h, port))
	}
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		// 只绑回环：其他机器访问不到
		add("127.0.0.1")
		return out
	}
	if host != "" && host != "0.0.0.0" && host != "::" {
		// 绑定在某个具体网卡地址上
		add(host)
		return out
	}
	// 绑定全部网卡：逐个列出非回环 IPv4 地址
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() {
				continue
			}
			add(ip4.String())
		}
	}
	return out
}

// Close 通知后台任务退出。
func (s *Server) Close() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

func retentionDesc(days int) string {
	if days <= 0 {
		return "无限制"
	}
	return strconv.Itoa(days) + " 天"
}

// maintenance 定期刷新主机在线状态、执行 WAL 检查点、清理过期数据与推送告警邮件。
func (s *Server) maintenance() {
	refresh := time.NewTicker(30 * time.Second)
	purge := time.NewTicker(time.Duration(maxInt(s.cfg.PurgeSec, 60)) * time.Second)
	checkpoint := time.NewTicker(5 * time.Minute)
	mailer := time.NewTicker(60 * time.Second)
	hooker := time.NewTicker(60 * time.Second)
	hubEvery := s.cfg.HubInterval
	if hubEvery < time.Minute {
		hubEvery = 5 * time.Minute
	}
	hubber := time.NewTicker(hubEvery)
	defer refresh.Stop()
	defer purge.Stop()
	defer checkpoint.Stop()
	defer mailer.Stop()
	defer hooker.Stop()
	defer hubber.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-refresh.C:
			if err := s.store.RefreshHostStates(); err != nil {
				log.Printf("刷新主机状态失败: %v", err)
			}
			if n := s.store.TimeoutCommands(2 * time.Minute); n > 0 {
				log.Printf("标记 %d 条下发指令为超时", n)
			}
		case <-checkpoint.C:
			if err := s.store.Checkpoint(); err != nil {
				log.Printf("WAL 检查点失败: %v", err)
			}
		case <-purge.C:
			if err := s.store.Purge(); err != nil {
				log.Printf("清理历史数据失败: %v", err)
			} else {
				log.Printf("已清理超过 %s 的历史数据", retentionDesc(s.cfg.Retention))
			}
		case <-mailer.C:
			s.pushAlertMail()
		case <-hooker.C:
			s.pushAlertWebhook()
		case <-hubber.C:
			s.hubSync()
		}
	}
}

// mailLastRun 记录上次聚合发送时间，用于按配置的聚合间隔节流。
var mailLastRun time.Time

// pushAlertMail 将新增告警按邮件配置推送。
//
// 依次应用：启用开关 → 聚合间隔 → 免打扰时段 → 告警级别过滤 → 恢复通知；
// 无论成功、失败还是被跳过，都会写入邮件发送记录，便于事后确认。
func (s *Server) pushAlertMail() {
	settings, err := s.store.Settings()
	if err != nil {
		return
	}
	if settings["mailEnabled"] != "true" {
		return
	}
	interval := intValue(settings["mailIntervalSec"], 60)
	if interval < 15 {
		interval = 15
	}
	if !mailLastRun.IsZero() && time.Since(mailLastRun) < time.Duration(interval)*time.Second {
		return
	}
	mailLastRun = time.Now()

	cfg := s.mailConfigFrom(settings)
	if !cfg.Enabled() {
		return
	}
	// 免打扰时段内不发送，且不推进游标，时段结束后会补发这段时间的告警
	if inQuietHours(settings["mailQuietFrom"], settings["mailQuietTo"], time.Now()) {
		return
	}

	alertItems, err := s.store.AlertsSince(settings["mailLastSent"])
	if err != nil {
		log.Printf("查询待推送告警失败: %v", err)
		return
	}
	levels := levelFilter(settings["mailLevels"])
	list := make([]notify.Alert, 0, len(alertItems))
	maxTime := settings["mailLastSent"]
	for _, a := range alertItems {
		if a.CreateTime > maxTime {
			maxTime = a.CreateTime
		}
		if !levels[a.Level] {
			continue
		}
		list = append(list, notify.Alert{Hostname: a.Hostname, Type: a.Type, Level: a.Level,
			Content: a.Content, CreateTime: a.CreateTime})
	}
	// 恢复通知：把状态由异常转正常的监控项一并发出
	if settings["mailRecover"] == "true" {
		events, eerr := s.store.RecentRecoveriesSince(settings["mailLastSent"])
		if eerr != nil {
			log.Printf("查询恢复事件失败: %v", eerr)
		}
		for _, e := range events {
			if e.CheckTime > maxTime {
				maxTime = e.CheckTime
			}
			content := fmt.Sprintf("监控项 %s 已恢复正常（%s）", e.Name, e.Msg)
			list = append(list, notify.Alert{Hostname: e.Hostname, Type: e.Type, Level: "info",
				Content: content, CreateTime: e.CheckTime})
		}
	}
	if len(list) == 0 {
		// 有新增告警但都被级别过滤掉了：同样推进游标，否则这批被过滤的告警
		// 会一直占住查询窗口，把后面真正需要发送的告警挡在外面。
		if maxTime != settings["mailLastSent"] {
			_ = s.store.SaveSettings(map[string]string{"mailLastSent": maxTime})
		}
		return
	}
	subject := cfg.Subject(list[0])
	if err := notify.Send(cfg, list); err != nil {
		log.Printf("告警邮件推送失败: %v", err)
		s.store.AppendMailLog(model.MailLog{Kind: "alert", Subject: subject, Recipients: cfg.To,
			ItemCount: len(list), Status: "fail", Error: err.Error()})
		_ = s.store.SaveSettings(map[string]string{"mailLastError": err.Error(), "mailLastTime": timeNowString()})
		return
	}
	s.store.AppendMailLog(model.MailLog{Kind: "alert", Subject: subject, Recipients: cfg.To,
		ItemCount: len(list), Status: "ok"})
	_ = s.store.SaveSettings(map[string]string{
		"mailLastSent":  maxTime,
		"mailLastTime":  timeNowString(),
		"mailLastError": "",
		"mailLastCount": strconv.Itoa(len(list)),
	})
}

// inQuietHours 判断当前是否落在免打扰时段内，支持跨零点（如 22:00-07:00）。
func inQuietHours(from, to string, now time.Time) bool {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" || from == to {
		return false
	}
	cur := now.Format("15:04")
	if from < to {
		return cur >= from && cur < to
	}
	return cur >= from || cur < to
}

// levelFilter 解析告警级别白名单，空配置表示全部级别都发送。
func levelFilter(raw string) map[string]bool {
	out := map[string]bool{}
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out[p] = true
		}
	}
	if len(out) == 0 {
		return map[string]bool{"critical": true, "warning": true, "info": true}
	}
	return out
}

func intValue(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return def
}

// secretSettingKeys 需要加密存储的配置项。
var secretSettingKeys = map[string]bool{"mailPassword": true}

// decryptSetting 解密配置项；为兼容早期明文存储的数据，解密失败时原样返回。
func (s *Server) decryptSetting(v string) string {
	if v == "" {
		return ""
	}
	if plain := s.store.DecryptSecret(v); plain != "" {
		return plain
	}
	return v
}

// prepareSettings 处理设置写入：口令类字段做加密，值为空表示保持原值不变。
func (s *Server) prepareSettings(kv map[string]string) error {
	old, err := s.store.Settings()
	if err != nil {
		return err
	}
	for k := range kv {
		if !secretSettingKeys[k] {
			continue
		}
		if strings.TrimSpace(kv[k]) == "" {
			// 前端不再回显口令，留空即「不修改」
			if v, ok := old[k]; ok {
				kv[k] = v
			} else {
				delete(kv, k)
			}
			continue
		}
		enc, eerr := s.store.EncryptSecret(kv[k])
		if eerr != nil {
			return fmt.Errorf("口令加密失败: %w", eerr)
		}
		kv[k] = enc
	}
	return nil
}

// maskSettings 处理设置读取：口令类字段不回传明文，只回传「是否已设置」。
func (s *Server) maskSettings(all map[string]string) map[string]string {
	out := make(map[string]string, len(all)+2)
	for k, v := range all {
		if secretSettingKeys[k] {
			out[k] = ""
			if v != "" {
				out[k+"Set"] = "true"
			}
			continue
		}
		out[k] = v
	}
	return out
}

// mailConfigFrom 将系统设置转换为邮件发送配置，口令在此解密。
func (s *Server) mailConfigFrom(settings map[string]string) notify.MailConfig {
	port, _ := strconv.Atoi(settings["mailPort"])
	return notify.MailConfig{
		Host:     settings["mailHost"],
		Port:     port,
		User:     settings["mailUser"],
		Password: s.decryptSetting(settings["mailPassword"]),
		From:     settings["mailFrom"],
		To:       settings["mailTo"],
		TLS:      settings["mailTLS"] == "true",
		StartTLS: settings["mailStartTLS"] == "true",
		Prefix:   settings["alertPrefix"],
		Sign:     settings["alertSignature"],
	}
}

func timeNowString() string { return time.Now().Format("2006-01-02 15:04:05") }

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// handleMinTask 接收 agent 上报。
func (s *Server) handleMinTask(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		writeJSON(w, map[string]string{"result": "error：仅支持 POST"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeJSON(w, map[string]string{"result": "error：" + err.Error()})
		return
	}
	var report model.Report
	if err := json.Unmarshal(body, &report); err != nil {
		writeJSON(w, map[string]string{"result": "error：" + err.Error()})
		return
	}
	if report.WgToken != config.MD5Token(s.cfg.Token) {
		log.Printf("拒绝上报：token 校验失败，来源 %s", r.RemoteAddr)
		writeJSON(w, map[string]string{"result": "error：token is invalidate"})
		return
	}
	// 主机改名后采集端仍以旧名上报，这里统一归位到当前主机名，避免旧名“复活”成新主机
	report.Hostname = s.resolveAgentHost(report.Hostname)
	report.SystemInfo.Hostname = s.resolveAgentHost(report.SystemInfo.Hostname)
	// 授权配额：只对「新主机」计数，已在册主机继续上报不受影响，
	// 避免换授权/到期时存量主机集体掉线。
	st := s.licenseStatus()
	if st.MaxHosts > 0 && !s.store.HostExists(report.Hostname) && s.store.CountHosts() >= st.MaxHosts {
		log.Printf("拒绝上报：主机数已达授权上限 %d（%s），主机 %s", st.MaxHosts, st.Edition, report.Hostname)
		writeJSON(w, map[string]string{"result": fmt.Sprintf(
			"error：已接入主机数达到授权上限 %d 台，请在控制台「授权管理」中导入更高档位的授权码", st.MaxHosts)})
		return
	}
	if err := s.store.SaveReport(&report); err != nil {
		log.Printf("保存上报数据失败: %v", err)
		writeJSON(w, map[string]string{"result": "error：" + err.Error()})
		return
	}
	writeJSON(w, map[string]string{"result": "success"})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
	if !s.store.CheckUser(req.Username, req.Password) {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"ok": false, "msg": "用户名或密码错误"})
		return
	}
	member, err := s.store.GetMember(req.Username)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	if !member.Enabled {
		writeJSONStatus(w, http.StatusForbidden, map[string]any{"ok": false, "msg": "账号已停用，请联系管理员"})
		return
	}
	token := newToken()
	s.mu.Lock()
	s.sessions[token] = session{username: req.Username, expire: time.Now().Add(sessionTTL)}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
	})
	s.store.TouchLogin(req.Username, clientIP(r))
	writeJSON(w, map[string]any{"ok": true, "username": member.Username, "role": member.Role})
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	m := s.currentUser(r)
	if m == nil {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"ok": false, "msg": "未登录"})
		return
	}
	st := s.licenseStatus()
	writeJSON(w, map[string]any{"ok": true, "username": m.Username, "role": m.Role,
		"nickname": m.Nickname, "menus": MenusOf(m),
		"serverVersion": version.Version,
		// 侧栏徽标只需要档位与有效期；客户名、机器码等详情走管理员专属的 /api/license
		"license": map[string]any{
			"edition": st.Edition, "valid": st.Valid, "community": st.Community,
			"notAfter": st.NotAfter, "daysLeft": st.DaysLeft,
		}})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, map[string]any{"ok": true})
}

func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// auth 校验会话 Cookie，未登录返回 401。
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.currentUser(r) == nil {
			writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"ok": false, "msg": "未登录"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// admin 限制仅管理员可访问的接口。
func (s *Server) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := s.currentUser(r)
		if m == nil {
			writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"ok": false, "msg": "未登录"})
			return
		}
		if m.Role != RoleAdmin {
			writeJSONStatus(w, http.StatusForbidden, map[string]any{"ok": false, "msg": "仅管理员可执行该操作"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminOrOperator 限制仅管理员与运维可访问：版本发布、授权详情这类
// 平台级信息只读账号没必要看到，菜单里也不下发。
func (s *Server) adminOrOperator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := s.currentUser(r)
		if m == nil {
			writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"ok": false, "msg": "未登录"})
			return
		}
		if m.Role != RoleAdmin && m.Role != RoleOperator {
			writeJSONStatus(w, http.StatusForbidden, map[string]any{"ok": false, "msg": "仅管理员与运维可查看该信息"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// currentUser 返回当前会话对应的成员信息，会话无效时返回 nil。
func (s *Server) currentUser(r *http.Request) *model.Member {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	sess, ok := s.sessions[c.Value]
	s.mu.Unlock()
	if !ok || time.Now().After(sess.expire) {
		return nil
	}
	m, err := s.store.GetMember(sess.username)
	if err != nil || !m.Enabled {
		return nil
	}
	return m
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data, err := s.store.Dashboard()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, data)
}

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.store.ListHosts()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, hosts)
}

func (s *Server) handleDisks(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	disks, err := s.store.LatestDisks(host)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, disks)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	minutes, _ := strconv.Atoi(q.Get("minutes"))
	if minutes <= 0 {
		minutes = 60
	}
	typ := q.Get("type")
	if typ == "" {
		typ = "cpu"
	}
	series, err := s.store.Metrics(q.Get("host"), typ, minutes)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, series)
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	alerts, err := s.store.AlertsOf(strings.TrimSpace(r.URL.Query().Get("host")), limit)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, alerts)
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ResolveAlert(r.PathValue("id")); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(normalizeJSON(v))
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(normalizeJSON(v))
}

// normalizeJSON 递归把 nil 切片 / nil map 归一化为空切片 / 空 map。
//
// 空结果集在 Go 里是 nil 切片，直接编码会得到 JSON 字面量 null，
// 前端对列表做 .map/.find/.length 时就会抛 "Cannot read properties of null"。
// 在出口统一归一化，可一次性消除这类问题，也避免调用方逐个初始化。
func normalizeJSON(v any) any {
	return normalizeValue(reflect.ValueOf(v), 0)
}

// maxNormalizeDepth 限制递归深度，避免异常的自引用结构导致栈溢出。
const maxNormalizeDepth = 12

func normalizeValue(rv reflect.Value, depth int) any {
	if depth > maxNormalizeDepth || !rv.IsValid() {
		return nil
	}
	switch rv.Kind() {
	case reflect.Interface, reflect.Pointer:
		if rv.IsNil() {
			return nil
		}
		return normalizeValue(rv.Elem(), depth+1)
	case reflect.Slice:
		if rv.IsNil() {
			return reflect.MakeSlice(rv.Type(), 0, 0).Interface()
		}
		// []byte 交给编码器按 base64 处理，不做元素级替换
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return rv.Interface()
		}
		out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			if nv := normalizeValue(rv.Index(i), depth+1); nv != nil {
				out.Index(i).Set(reflect.ValueOf(nv))
			} else {
				out.Index(i).Set(rv.Index(i))
			}
		}
		return out.Interface()
	case reflect.Map:
		if rv.IsNil() {
			return reflect.MakeMap(rv.Type()).Interface()
		}
		// 只处理字符串键的 map，其余保持原样
		if rv.Type().Key().Kind() != reflect.String {
			return rv.Interface()
		}
		out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			val := iter.Value()
			if nv := normalizeValue(val, depth+1); nv != nil && reflect.TypeOf(nv).AssignableTo(rv.Type().Elem()) {
				out.SetMapIndex(iter.Key(), reflect.ValueOf(nv))
			} else {
				out.SetMapIndex(iter.Key(), val)
			}
		}
		return out.Interface()
	case reflect.Struct:
		// 结构体内部字段（如 Report.Processes）也要保证是数组而非 null
		out := reflect.New(rv.Type()).Elem()
		out.Set(rv)
		for i := 0; i < rv.NumField(); i++ {
			f := rv.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			if fv := out.Field(i); fv.CanSet() {
				switch fv.Kind() {
				case reflect.Slice:
					if fv.IsNil() && fv.Type().Elem().Kind() != reflect.Uint8 {
						fv.Set(reflect.MakeSlice(fv.Type(), 0, 0))
					}
				case reflect.Map:
					if fv.IsNil() {
						fv.Set(reflect.MakeMap(fv.Type()))
					}
				}
			}
		}
		return out.Interface()
	}
	return rv.Interface()
}
