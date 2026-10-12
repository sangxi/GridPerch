package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sangxi/OpsMonitor/internal/config"
	"github.com/sangxi/OpsMonitor/internal/license"
	"github.com/sangxi/OpsMonitor/internal/store"
	"github.com/sangxi/OpsMonitor/internal/version"
)

/* ============================================================
   与授权系统（更新与授权中心）同步
   ------------------------------------------------------------
   平台代理拉取：配置 -hub 与 -hub-token 后，平台按间隔调用授权系统
   GET /api/v1/check，一次性拿到：
     · 本机授权码      -> 校验通过后自动导入，无需人工粘贴
     · 采集端最新发布物 -> 下载并缓存到本地 releases/，再分发给内网采集端
     · 平台最新发布物   -> 记录待升级信息，供控制台「在线升级」页使用
   内网采集端始终只与平台通信，不需要能访问公网。
   ============================================================ */

// HubStatus 平台与授权系统的同步状态，供控制台「在线升级」「授权购买」页展示。
type HubStatus struct {
	Configured    bool              `json:"configured"`
	URL           string            `json:"url"`
	Machine       string            `json:"machine"`
	LastCheck     string            `json:"lastCheck"`
	LastError     string            `json:"lastError"`
	Current       string            `json:"current"`
	ServerLatest  *hubRel           `json:"serverLatest"`
	AgentCached   map[string]string `json:"agentCached"`
	LicenseState  string            `json:"licenseState"`
	LicenseMsg    string            `json:"licenseMsg"`
	LicensePulled string            `json:"licensePulled"`
	IntervalSec   int               `json:"intervalSec"`

	// TokenSource 只说明令牌来自哪里（builtin / custom / none），
	// 永远不回显令牌内容本身 —— 这个结构体会整个序列化给前端。
	TokenSource string `json:"tokenSource"`

	// AutoUpdate 服务端自身自动更新状态
	AutoUpdate     string `json:"autoUpdate"`     // check / download / off
	ServerUpgraded string `json:"serverUpgraded"` // 已就位、待重启生效的版本
	ServerNote     string `json:"serverNote"`     // 最近一次处理结果的说明
	ServerFail     int    `json:"serverFail"`     // 连续失败次数
	NextRetrySec   int    `json:"nextRetrySec"`   // 授权端不可达时的下次重试倒计时
}

// hubRel 授权系统下发的发布物信息。
type hubRel struct {
	Kind      string `json:"kind"`
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Notes     string `json:"notes"`
	Download  string `json:"download"`
	HasUpdate bool   `json:"hasUpdate"`
}

// hubLic 授权系统下发的授权信息。
type hubLic struct {
	Code     string `json:"code"`
	Customer string `json:"customer"`
	Edition  string `json:"edition"`
	MaxHosts int    `json:"maxHosts"`
	MaxUsers int    `json:"maxUsers"`
	NotAfter string `json:"notAfter"`
}

type hubCheckResp struct {
	OK           bool    `json:"ok"`
	Hub          string  `json:"hub"`
	Time         string  `json:"time"`
	Agent        *hubRel `json:"agent"`
	Server       *hubRel `json:"server"`
	License      *hubLic `json:"license"`
	LicenseState string  `json:"licenseState"`
}

var (
	hubHTTP         = &http.Client{Timeout: 30 * time.Second}
	hubDownloadHTTP = &http.Client{Timeout: 20 * time.Minute}
)

// 授权端「还没签发」类提示语。授权已经在本机生效时，这类消息属于过期口径，
// 会被本机的真实授权状态覆盖，避免页面上「已授权」与「等待签发」自相矛盾。
const (
	licMsgPending = "授权系统已收到本机申请，等待签发授权"
	licMsgNone    = "授权系统尚未为本机签发授权"
)

// HubStatus 返回当前同步状态快照。
func (s *Server) hubState() HubStatus {
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	hs := s.hub
	hs.Configured = strings.TrimSpace(s.cfg.HubURL) != ""
	hs.URL = s.cfg.HubURL
	hs.Machine = s.licenseMachine()
	hs.Current = version.Version
	hs.AutoUpdate = s.cfg.HubAutoUpdate
	hs.TokenSource = s.hubTokenSource()
	if s.cfg.HubInterval > 0 {
		hs.IntervalSec = int(s.cfg.HubInterval / time.Second)
	}
	if hs.AgentCached == nil {
		hs.AgentCached = map[string]string{}
	}
	return hs
}

func (s *Server) setHub(fn func(*HubStatus)) {
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if s.hub.AgentCached == nil {
		s.hub.AgentCached = map[string]string{}
	}
	fn(&s.hub)
}

// hubSync 执行一次与授权系统的同步：授权 + 采集端发布物 + 服务端自身发布物。
//
// 连续失败时按指数退避拉长下次间隔，避免授权端宕机期间把日志刷爆；
// 一旦成功立刻恢复常规间隔。
func (s *Server) hubSync() {
	if strings.TrimSpace(s.cfg.HubURL) == "" {
		return
	}
	// 还在退避窗口内则跳过这一轮
	s.hubMu.Lock()
	if s.hub.NextRetrySec > 0 {
		s.hub.NextRetrySec--
		s.hubMu.Unlock()
		return
	}
	s.hubMu.Unlock()

	resp, err := s.hubCheck()
	if err != nil {
		// 先脱敏再落库/落日志：err 里可能带着含令牌的完整 URL。
		msg := s.redactHubToken(err.Error())
		s.setHub(func(h *HubStatus) {
			h.LastCheck = time.Now().Format("2006-01-02 15:04:05")
			h.LastError = msg
			h.ServerFail++
			h.NextRetrySec = backoffSeconds(h.ServerFail)
		})
		log.Printf("授权系统同步失败（%d 分钟后重试）：%s", backoffSeconds(s.hubState().ServerFail)/60, msg)
		return
	}
	s.setHub(func(h *HubStatus) {
		h.LastCheck = time.Now().Format("2006-01-02 15:04:05")
		h.LastError = ""
		h.ServerFail = 0
		h.NextRetrySec = 0
		h.LicenseState = resp.LicenseState
		if resp.Server != nil {
			h.ServerLatest = resp.Server
		}
	})

	// 1) 授权：拿到就校验并导入
	if resp.License != nil && strings.TrimSpace(resp.License.Code) != "" {
		s.applyRemoteLicense(resp.License)
	} else {
		s.setHub(func(h *HubStatus) {
			h.LicensePulled = time.Now().Format("2006-01-02 15:04:05")
			if h.LicenseMsg == "" {
				switch resp.LicenseState {
				case "pending":
					h.LicenseMsg = licMsgPending
				case "none":
					h.LicenseMsg = licMsgNone
				case "":
					h.LicenseMsg = ""
				}
			}
		})
	}

	// 本机已经持有有效授权时以本机为准：授权端那侧的「申请中」往往只是它自己的
	// 记账没有翻篇（授权其实早已下发、本机也已验签通过），此时若继续沿用会让
	// 页面顶部的档位卡片显示「旗舰版/已授权」、下面却提示「等待签发」，自相矛盾。
	if st := s.licenseStatus(); st.Valid {
		s.setHub(func(h *HubStatus) {
			h.LicenseState = "active"
			if h.LicenseMsg == "" || h.LicenseMsg == licMsgPending || h.LicenseMsg == licMsgNone {
				h.LicenseMsg = "授权已生效：" + st.Summary()
			}
		})
	}

	// 2) 采集端发布物：下载并缓存到本地，供内网采集端升级
	if resp.Agent != nil {
		if err := s.cacheAgentRelease(resp.Agent); err != nil {
			log.Printf("缓存采集端发布物失败：%s", s.redactHubToken(err.Error()))
		}
	}

	// 3) 服务端自身：检测到新版本就自动拉取并就位
	s.autoUpdateSelf(resp.Server)

	// 4) 把采集端的升级结果回执授权端（异步，不拖慢本轮同步）
	s.reportAgentUpdates()
}

// agentUpdateStat 汇总一批采集端升级结果。
type agentUpdateStat struct {
	Version   string         `json:"version"`
	Total     int            `json:"total"`
	OK        int            `json:"ok"`
	Failed    int            `json:"failed"`
	Hosts     []string       `json:"hosts"`
	FailedMsg map[string]int `json:"failedMsg"`
}

// agentUpdates 缓存尚未回执授权端的采集端升级结果。
var agentUpdates struct {
	mu      sync.Mutex
	pending []map[string]string
}

// noteAgentUpdate 记录一次采集端升级结果，等待下一轮同步时统一回执。
func (s *Server) noteAgentUpdate(hostname, to, result string) {
	if hostname == "" || to == "" {
		return
	}
	agentUpdates.mu.Lock()
	defer agentUpdates.mu.Unlock()
	// 同一主机的重复上报只保留最后一次，避免网络重试把统计放大
	for i, p := range agentUpdates.pending {
		if p["hostname"] == hostname {
			agentUpdates.pending[i] = map[string]string{
				"hostname": hostname, "to": to, "result": result,
				"time": time.Now().Format("2006-01-02 15:04:05"),
			}
			return
		}
	}
	agentUpdates.pending = append(agentUpdates.pending, map[string]string{
		"hostname": hostname, "to": to, "result": result,
		"time": time.Now().Format("2006-01-02 15:04:05"),
	})
	// 兜底上限：极端情况下主机反复重试也不让内存无限增长
	if len(agentUpdates.pending) > 5000 {
		agentUpdates.pending = agentUpdates.pending[len(agentUpdates.pending)-5000:]
	}
}

// reportAgentUpdates 把缓存的采集端升级结果回执授权端，回执成功后清空缓存。
//
// 没配 -hub-token 时直接丢弃缓存：此时没有可用的回执通道，
// 留着只会一直堆积。
func (s *Server) reportAgentUpdates() {
	if strings.TrimSpace(s.cfg.HubURL) == "" || strings.TrimSpace(s.cfg.HubToken) == "" {
		agentUpdates.mu.Lock()
		agentUpdates.pending = nil
		agentUpdates.mu.Unlock()
		return
	}
	agentUpdates.mu.Lock()
	batch := agentUpdates.pending
	agentUpdates.pending = nil
	agentUpdates.mu.Unlock()
	if len(batch) == 0 {
		return
	}

	byVer := map[string]*agentUpdateStat{}
	for _, p := range batch {
		st, ok := byVer[p["to"]]
		if !ok {
			st = &agentUpdateStat{Version: p["to"], FailedMsg: map[string]int{}}
			byVer[p["to"]] = st
		}
		st.Total++
		if p["result"] == "ok" {
			st.OK++
		} else {
			st.Failed++
		}
		if len(st.Hosts) < 50 {
			st.Hosts = append(st.Hosts, p["hostname"])
		}
	}

	payload, err := json.Marshal(map[string]any{
		"machine": s.licenseMachine(), "hostname": s.hostnameForHub(),
		"action": "agent-update", "server": version.Version,
		"time":  time.Now().Format("2006-01-02 15:04:05"),
		"stats": byVer,
	})
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(s.cfg.HubURL, "/")+"/api/v1/report", bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	s.applyHubHeaders(req)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("回执采集端升级结果失败（本次丢弃）：%s", s.redactHubToken(err.Error()))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	for v, st := range byVer {
		log.Printf("已回执授权系统：采集端升级到 %s —— 成功 %d 台，失败 %d 台", v, st.OK, st.Failed)
	}
}

// backoffSeconds 按连续失败次数返回退避秒数：1m → 2m → 4m → … 上限 30 分钟。
//
// 授权系统短暂不可用时不必每次都重试，但也不能无限期退避——
// 授权可能正好在这期间被签发，退避太久会让用户以为没生效。
func backoffSeconds(failCount int) int {
	if failCount <= 0 {
		return 0
	}
	const base = 60
	maxBackoff := 1800
	d := base << uint(min(failCount-1, 5)) // 最多 32 倍
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// autoUpdateSelf 检测授权系统上的服务端新版本，按策略自动下载并就位。
//
// 这里刻意不自动重启：重启会影响所有在途上报与控制台会话，
// 因此就位后交由 systemd / Docker 的重启策略生效（那正是无人值守该有的行为），
// 同时在控制台明确提示「已就位，重启后生效」。
func (s *Server) autoUpdateSelf(r *hubRel) {
	if r == nil || strings.TrimSpace(r.Version) == "" {
		return
	}
	if !version.IsNewer(r.Version, version.Version) {
		// 当前已是最新；清掉可能残留的「待重启」提示
		s.setHub(func(h *HubStatus) {
			if h.ServerUpgraded == r.Version {
				h.ServerUpgraded = ""
				h.ServerNote = "已运行最新版本 " + version.Version
			}
		})
		return
	}
	switch s.cfg.HubAutoUpdate {
	case "off":
		s.setHub(func(h *HubStatus) {
			h.ServerNote = "发现新版本 " + r.Version + "，但自动更新已关闭（-hub-auto-update off）"
		})
		return
	case "check":
		s.setHub(func(h *HubStatus) {
			h.ServerNote = "发现新版本 " + r.Version + "，可在「在线升级」页手动拉取"
		})
		return
	}
	// download：已经就位过同一版本则不再重复下载
	if s.pendingSelfVersion() == r.Version {
		s.setHub(func(h *HubStatus) {
			h.ServerUpgraded = r.Version
			h.ServerNote = "版本 " + r.Version + " 已就位，重启服务后生效"
		})
		return
	}
	msg, err := s.hubPullSelf()
	if err != nil {
		// ServerNote 会显示在控制台，回执也会出网，两处都先脱敏
		em := s.redactHubToken(err.Error())
		s.setHub(func(h *HubStatus) {
			h.ServerNote = "自动升级失败：" + em
		})
		log.Printf("服务端自动升级失败：%s", em)
		s.hubReport("self-update", version.Version, r.Version, "fail", em)
		return
	}
	s.setHub(func(h *HubStatus) {
		h.ServerUpgraded = r.Version
		h.ServerNote = "已自动升级：" + msg
	})
	s.hubReport("self-update", version.Version, r.Version, "ok", msg)
}

// hubReport 把平台侧的动作回执给授权系统，形成双向通信。
//
// 授权端据此可以看到各平台的授权使用与升级分布。回执失败不影响主流程——
// 代拉已经成功了，为一条统计信息重试反而会拖慢同步。
func (s *Server) hubReport(action, from, to, result, message string) {
	if strings.TrimSpace(s.cfg.HubURL) == "" || strings.TrimSpace(s.cfg.HubToken) == "" {
		return
	}
	payload, err := json.Marshal(map[string]string{
		"machine": s.licenseMachine(), "hostname": s.hostnameForHub(),
		"action": action, "from": from, "to": to,
		"result": result, "message": message,
		"server": version.Version, "time": time.Now().Format("2006-01-02 15:04:05"),
	})
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(s.cfg.HubURL, "/")+"/api/v1/report", bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	s.applyHubHeaders(req)
	// 短超时：回执是尽力而为，不能拖慢同步节奏
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("向授权系统回执 %s 失败（忽略）：%s", action, s.redactHubToken(err.Error()))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		log.Printf("授权系统拒绝回执 %s：HTTP %d", action, resp.StatusCode)
	}
}

// hubCheck 调用授权系统 check 接口。
func (s *Server) hubCheck() (*hubCheckResp, error) {
	q := url.Values{}
	q.Set("token", s.cfg.HubToken)
	q.Set("machine", s.licenseMachine())
	q.Set("os", runtime.GOOS)
	q.Set("arch", runtime.GOARCH)
	q.Set("version", version.Version)
	q.Set("hostname", s.hostnameForHub())

	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(s.cfg.HubURL, "/")+"/api/v1/check?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	s.applyHubHeaders(req)
	resp, err := hubHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, s.describeHubStatus(resp, body)
	}
	var out hubCheckResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("授权系统响应无法解析（content-type=%s）：%w",
			firstNonEmpty(resp.Header.Get("Content-Type"), "未提供"), err)
	}
	if !out.OK {
		return nil, fmt.Errorf("授权系统校验未通过")
	}
	return &out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// describeHubStatus 把非 200 响应翻译成人能照着改的结论。
//
// 授权端通常挂在 CDN / WAF 后面，这些网关会拦掉一切非浏览器客户端，
// 返回的却是一段 HTML 挑战页。若只把状态码原样抛出来，
// 运维看到「HTTP 403」根本无从下手，因此这里单独识别并给出处理建议。
func (s *Server) describeHubStatus(resp *http.Response, body []byte) error {
	status := resp.StatusCode
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 120 {
		snippet = snippet[:120]
	}

	// Cloudflare 的人机校验页
	if strings.Contains(snippet, "Just a moment") || strings.Contains(snippet, "cf-browser-verification") ||
		strings.EqualFold(resp.Header.Get("Cf-Ray"), "") && strings.Contains(strings.ToLower(resp.Header.Get("Server")), "cloudflare") {
		return fmt.Errorf(
			"授权系统返回 HTTP %d，但响应是 Cloudflare 人机校验页（%s），不是接口本身拒绝。"+
				"平台作为程序客户端过不了浏览器挑战，请在 Cloudflare 控制台对 %s 放行："+
				"在 WAF/速率限制里为 /api/* 配置跳过规则（Skip rules），或加一条允许规则 "+
				"User-Agent 匹配 opsmonitor-server/* 且来源IP固定",
			status, snippet, s.cfg.HubURL)
	}

	// 令牌不对
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("授权系统返回 HTTP %d（拉取令牌被拒）：%s。"+
			"本机当前使用%s，请核对授权中心启动参数 -token 是否与之相同；"+
			"若中心侧配的就是别的值，可用 -hub-token 或 OPSM_HUB_TOKEN 显式覆盖内置令牌",
			status, snippet, hubTokenSourceText(s.hubTokenSource()))
	}

	// 返回了 HTML 而不是 JSON：通常是地址指到了网页而不是接口
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "text/html") {
		return fmt.Errorf("授权系统返回 HTTP %d 且内容为网页（%s），说明 -hub 指向的可能不是接口地址。"+
			"正确地址应形如 https://域名（不含 /api/v1/check）", status, ct)
	}

	if snippet == "" {
		return fmt.Errorf("授权系统返回 HTTP %d", status)
	}
	return fmt.Errorf("授权系统返回 %d：%s", status, snippet)
}

// applyHubHeaders 统一设置访问授权系统的请求头。
//
// User-Agent 默认带产品名便于授权端识别客户端来源；
// 若授权端前置了按 UA 拦截的网关，可用 -hub-ua 覆盖成带浏览器特征的 UA。
// Accept 里带上 JSON 是为了让返回 HTML 挑战页时能被下面的状态诊断区分出来。
func (s *Server) applyHubHeaders(req *http.Request) {
	ua := strings.TrimSpace(s.cfg.HubUserAgent)
	if ua == "" {
		ua = "opsmonitor-server/" + version.Version
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if t := strings.TrimSpace(s.cfg.HubToken); t != "" {
		req.Header.Set("X-Opsm-Hub-Token", t)
	}
}

// redactHubToken 把文本里可能出现的授权中心令牌抹掉。
//
// 令牌在 check 请求里既走 query 参数也走请求头。一旦请求失败，Go 的
// url.Error 会把「带 query 的完整 URL」拼进错误文本，形如：
//
//	Get "https://hub/api/v1/check?token=xxxx&machine=...": dial tcp ...
//
// 而这条错误会写进日志，也会原样显示在控制台「在线升级」页，
// 等于把凭证主动交给运维和日志采集系统。所以凡是可能落日志或回给前端的
// 授权系统错误，都必须先过一遍这里。
//
// 放在这里而不是各调用点，是为了新增调用点时不会漏。
func (s *Server) redactHubToken(msg string) string {
	t := strings.TrimSpace(s.cfg.HubToken)
	if t == "" || msg == "" {
		return msg
	}
	msg = strings.ReplaceAll(msg, t, "***")
	// 令牌若含特殊字符，URL 里是转义后的形式，一并抹掉
	if enc := url.QueryEscape(t); enc != "" && enc != t {
		msg = strings.ReplaceAll(msg, enc, "***")
	}
	return msg
}

// hubTokenSource 归类当前令牌的来源，只返回 builtin / custom / none。
//
// 单独抽出来是为了让「显示给运维的提示」和「接口返回的字段」用同一套判断，
// 避免两处逻辑漂移。注意任何分支都不返回令牌内容。
func (s *Server) hubTokenSource() string {
	switch {
	case strings.TrimSpace(s.cfg.HubToken) == "":
		return "none"
	case s.cfg.UsesBuiltinHubToken():
		return "builtin"
	default:
		return "custom"
	}
}

// hubTokenSourceText 把来源代号翻成给运维看的中文，供错误提示拼接。
func hubTokenSourceText(src string) string {
	switch src {
	case "builtin":
		return "内置令牌"
	case "custom":
		return "自定义令牌（-hub-token / OPSM_HUB_TOKEN）"
	default:
		return "未设置的令牌"
	}
}

// hostnameForHub 返回上报给授权系统的本机名。
func (s *Server) hostnameForHub() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return ""
}

// applyRemoteLicense 把授权系统下发的授权码校验后导入。
func (s *Server) applyRemoteLicense(lv *hubLic) {
	code := strings.TrimSpace(lv.Code)
	now := time.Now().Format("2006-01-02 15:04:05")

	if strings.TrimSpace(s.store.LicenseKey()) == code {
		s.setHub(func(h *HubStatus) {
			h.LicenseState = "active"
			h.LicensePulled = now
			h.LicenseMsg = "授权已是最新"
		})
		return
	}
	st := license.Resolve(code, s.licenseVerifier(), s.licenseMachine(), s.freeQuota())
	if !st.Valid {
		s.setHub(func(h *HubStatus) {
			h.LicensePulled = now
			h.LicenseMsg = "授权系统下发的授权校验失败：" + st.Reason
		})
		log.Printf("授权系统下发的授权校验失败：%s", st.Reason)
		return
	}
	if err := s.store.SaveLicense(code, st.Customer, st.Edition); err != nil {
		s.setHub(func(h *HubStatus) {
			h.LicensePulled = now
			h.LicenseMsg = "保存授权失败：" + err.Error()
		})
		return
	}
	s.invalidateLicense()
	s.setHub(func(h *HubStatus) {
		h.LicenseState = "active"
		h.LicensePulled = now
		h.LicenseMsg = "已从授权系统导入授权：" + st.Summary()
	})
	log.Printf("已从授权系统导入授权：%s", st.Summary())
}

// cacheAgentRelease 下载授权系统的采集端发布物并登记为本地下发版本。
func (s *Server) cacheAgentRelease(r *hubRel) error {
	if r.OS == "" || r.Arch == "" || r.Version == "" {
		return nil
	}
	// 本地已有同平台同版本则跳过，避免每次同步重复下载
	if cur, _ := s.store.LatestRelease(r.OS, r.Arch); cur != nil && cur.Version == r.Version {
		s.markAgentCached(r)
		return nil
	}
	ext := ""
	if r.OS == "windows" {
		ext = ".exe"
	}
	name := fmt.Sprintf("opsmonitor-agent-%s-%s%s", r.OS, r.Arch, ext)
	dir := filepath.Join(s.releasesDir(), r.Version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, name)
	if err := s.hubDownloadTo(r, dst); err != nil {
		return err
	}
	fi, err := os.Stat(dst)
	if err != nil {
		return err
	}
	rel := &store.Release{
		Version: r.Version, Goos: r.OS, Goarch: r.Arch,
		Filename: name, Path: dst, Size: fi.Size(), SHA256: r.SHA256,
		Notes: r.Notes + "（来自授权系统）",
	}
	if err := s.store.AddRelease(rel); err != nil {
		return err
	}
	if err := s.store.SetReleaseLatest(rel.ID); err != nil {
		return err
	}
	s.markAgentCached(r)
	log.Printf("已从授权系统缓存采集端发布物：%s/%s v%s", r.OS, r.Arch, r.Version)
	return nil
}

func (s *Server) markAgentCached(r *hubRel) {
	s.setHub(func(h *HubStatus) {
		if h.AgentCached == nil {
			h.AgentCached = map[string]string{}
		}
		h.AgentCached[r.OS+"/"+r.Arch] = r.Version
	})
}

// hubDownloadTo 从授权系统下载发布物到指定路径，并校验 SHA256。
func (s *Server) hubDownloadTo(r *hubRel, dst string) error {
	u := strings.TrimSpace(r.Download)
	if u == "" {
		return fmt.Errorf("授权系统未提供下载地址")
	}
	// 相对地址补全为绝对地址
	if strings.HasPrefix(u, "/") {
		u = strings.TrimRight(s.cfg.HubURL, "/") + u
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	s.applyHubHeaders(req)
	resp, err := hubDownloadHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("授权系统下载返回 %d", resp.StatusCode)
	}
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	sum := fmt.Sprintf("%x", h.Sum(nil))
	if want := strings.TrimSpace(r.SHA256); want != "" && !strings.EqualFold(want, sum) {
		_ = os.Remove(tmp)
		return fmt.Errorf("下载文件校验和不一致（期望 %s，实际 %s）", cut12(want), cut12(sum))
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(dst, 0o755)
}

// pendingFile 是记录「已就位待重启版本」的标记文件名，与可执行文件同目录。
const pendingFile = ".opsmonitor-server.pending"

// pendingSelfVersion 返回已下载就位、但还没通过重启生效的版本号。
//
// 存在标记文件说明上一次升级已经把新二进制换好了，只是还没重启。
// 这里不比对可执行文件本身——Windows 上运行中的 exe 无法可靠读取，
// 用标记文件判断既能区分「已就位」与「已生效」，又不受平台限制。
func (s *Server) pendingSelfVersion() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	exe, _ = filepath.EvalSymlinks(exe)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(exe), pendingFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// markPendingSelf 记录已就位的版本号，供下次同步跳过重复下载。
func (s *Server) markPendingSelf(version string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)
	_ = os.WriteFile(filepath.Join(filepath.Dir(exe), pendingFile), []byte(version), 0o644)
}

// clearPendingSelf 重启成功后清掉标记。
func (s *Server) clearPendingSelf() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)
	_ = os.Remove(filepath.Join(filepath.Dir(exe), pendingFile))
}

// confirmSelfUpgrade 在启动时核对上一次的自动升级是否真正生效。
//
// 标记里的版本与当前运行的版本一致才说明重启成功；
// 不一致说明新二进制没能起来（启动即崩溃），需要立刻把旧版本换回去，
// 否则服务会陷入「反复启动失败—反复下载—反复崩溃」的循环。
func (s *Server) confirmSelfUpgrade() {
	pending := s.pendingSelfVersion()
	if pending == "" {
		return
	}
	if version.IsNewer(pending, version.Version) || strings.TrimSpace(pending) == version.Version {
		s.clearPendingSelf()
		log.Printf("服务端已升级到 %s 并生效", version.Version)
		return
	}
	// 新版本没跑起来：回滚到 .bak
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)
	bak := exe + ".bak"
	if _, err := os.Stat(bak); err != nil {
		log.Printf("自动升级到 %s 未生效，且找不到备份 %s，请人工介入", pending, filepath.Base(bak))
		return
	}
	if err := os.Rename(bak, exe); err != nil {
		log.Printf("自动升级到 %s 未生效，回滚失败：%v，请人工介入", pending, err)
		return
	}
	s.clearPendingSelf()
	log.Printf("自动升级到 %s 未生效，已回滚到备份版本，请重新启动服务", pending)
}

// hubPullSelf 下载授权系统最新的平台版本并就地替换自身二进制。
// 替换后需要重启服务进程才会真正生效。
func (s *Server) hubPullSelf() (string, error) {
	hs := s.hubState()
	if !hs.Configured {
		return "", fmt.Errorf("尚未配置授权系统地址")
	}
	// 重新拉一次，确保拿到最新信息
	resp, err := s.hubCheck()
	if err != nil {
		return "", err
	}
	if resp.Server == nil || strings.TrimSpace(resp.Server.Version) == "" {
		return "", fmt.Errorf("授权系统尚未发布 %s/%s 的平台版本", runtime.GOOS, runtime.GOARCH)
	}
	r := resp.Server
	// 已经是最新就别再替换一次，避免无谓的重启
	if !version.IsNewer(r.Version, version.Version) && s.pendingSelfVersion() != r.Version {
		s.clearPendingSelf()
		return fmt.Sprintf("当前已是最新版本 %s", version.Version), nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("无法定位当前程序路径：%w", err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	newFile := exe + ".new"
	if err := s.hubDownloadTo(r, newFile); err != nil {
		return "", err
	}
	// Windows 上运行中的可执行文件不能被覆盖，但可以改名：
	// 先把当前程序改名为 .bak，再把新文件改名为当前程序名。
	bak := exe + ".bak"
	_ = os.Remove(bak)
	if err := os.Rename(exe, bak); err != nil {
		_ = os.Remove(newFile)
		return "", fmt.Errorf("备份当前版本失败：%w", err)
	}
	if err := os.Rename(newFile, exe); err != nil {
		// 回滚，避免目录里没有可执行文件
		_ = os.Rename(bak, exe)
		_ = os.Remove(newFile)
		return "", fmt.Errorf("替换新版本失败：%w", err)
	}
	_ = os.Chmod(exe, 0o755)
	s.markPendingSelf(r.Version)
	s.setHub(func(h *HubStatus) {
		h.LicensePulled = time.Now().Format("2006-01-02 15:04:05")
	})
	msg := fmt.Sprintf("已就位版本 %s，重启服务后生效（旧版本备份为 %s）", r.Version, filepath.Base(bak))
	log.Printf("平台升级：%s", msg)
	return msg, nil
}

/* ---------------- HTTP 接口 ---------------- */

// handleHubStatus 返回与授权系统的同步状态，供控制台两个页面使用。
func (s *Server) handleHubStatus(w http.ResponseWriter, r *http.Request) {
	st := s.licenseStatus()
	writeJSON(w, map[string]any{
		"ok":       true,
		"hub":      s.hubState(),
		"license":  st,
		"summary":  st.Summary(),
		"machine":  s.licenseMachine(),
		"server":   version.Version,
		"build":    version.BuildTime,
		"editions": license.Editions,
		"free":     s.freeQuota(),
	})
}

// handleHubCheck 立即执行一次同步。
func (s *Server) handleHubCheck(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(s.cfg.HubURL) == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{
			"ok": false,
			"msg": "当前为完全离线模式（-hub 留空）。如需启用授权与在线更新，" +
				"请去掉 -hub 参数或指定为 " + config.DefaultHubURL,
		})
		return
	}
	// 手工点「立即检查」时清掉退避倒计时，否则用户会看到「刚点了却什么都没发生」
	s.setHub(func(h *HubStatus) { h.NextRetrySec = 0 })
	s.hubSync()
	hs := s.hubState()
	if hs.LastError != "" {
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"ok": false, "msg": "同步失败：" + hs.LastError, "hub": hs})
		return
	}
	msg := "已与授权系统 " + s.cfg.HubURL + " 同步"
	if hs.ServerUpgraded != "" {
		msg += "；检测到服务端新版本 " + hs.ServerUpgraded + "，已就位，重启服务后生效"
	}
	writeJSON(w, map[string]any{"ok": true, "msg": msg, "hub": hs})
}

// handleHubPull 拉取并安装平台自身的最新版本（手动触发，忽略自动更新策略）。
func (s *Server) handleHubPull(w http.ResponseWriter, r *http.Request) {
	msg, err := s.hubPullSelf()
	if err != nil {
		// 这个 msg 会直接回给浏览器，必须脱敏
		em := s.redactHubToken(err.Error())
		s.hubReport("self-update", version.Version, "", "fail", em)
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": em})
		return
	}
	s.hubReport("self-update", version.Version, s.pendingSelfVersion(), "ok", msg)
	writeJSON(w, map[string]any{"ok": true, "msg": msg, "hub": s.hubState()})
}

// cut12 截取字符串前 12 位，用于展示校验和等长串。
func cut12(v string) string {
	if len(v) > 12 {
		return v[:12]
	}
	return v
}
