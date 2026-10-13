package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/sangxi/OpsMonitor/internal/store"
)

/* ---------------- 采集端配置下发 ---------------- */

// AgentConfig 下发给采集端的运行配置。
//
// 安装命令因此只需要带 -server / -token-hash / -name 三个必要参数，
// 采集间隔、执行指令开关、跳过磁盘、更新策略这些「行为配置」全部由
// 平台统一下发，改配置不需要登录每台主机改 systemd 单元。
type AgentConfig struct {
	Hostname       string   `json:"hostname"`
	Interval       int      `json:"interval"`       // 指标上报间隔（秒）
	AllowCommand   bool     `json:"allowCommand"`   // 是否允许执行平台下发的指令
	SkipDisk       []string `json:"skipDisk"`       // 跳过采集的挂载点关键字
	UpdateMode     string   `json:"updateMode"`     // auto / check / off
	UpdateInterval int      `json:"updateInterval"` // 版本检查间隔（分钟）
	Refresh        int      `json:"refresh"`        // 本配置的刷新间隔（秒）
	Revision       string   `json:"revision"`       // 配置指纹，变化即代表需要重新应用
	Source         string   `json:"source"`         // global（全局默认）/ override（主机级覆盖）
}

// agentConfigDefaults 全局默认值，与 settings 中的键一一对应。
const (
	defAgentInterval       = 60
	defAgentUpdateInterval = 30
	defAgentRefresh        = 300
)

// agentConfigFor 计算某台主机最终生效的采集端配置：全局默认 + 主机级覆盖。
func (s *Server) agentConfigFor(hostname string) AgentConfig {
	settings, _ := s.store.Settings()

	cfg := AgentConfig{
		Hostname:       hostname,
		Interval:       intValue(settings["agentInterval"], defAgentInterval),
		AllowCommand:   settings["agentAllowCommand"] != "false",
		SkipDisk:       splitList(settings["agentSkipDisk"]),
		UpdateMode:     normalizeUpdateMode(settings["agentUpdateMode"]),
		UpdateInterval: intValue(settings["agentUpdateInterval"], defAgentUpdateInterval),
		Refresh:        intValue(settings["agentRefreshSec"], defAgentRefresh),
		Source:         "global",
	}
	// 刷新间隔过短会让几千台主机把服务端压出高频小请求，做了下限保护
	if cfg.Refresh < 30 {
		cfg.Refresh = 30
	}

	if hostname != "" {
		if o, err := s.store.GetAgentOverride(hostname); err == nil && o != nil {
			applyOverride(&cfg, *o)
		}
	}
	cfg.Revision = configRevision(cfg)
	return cfg
}

// applyOverride 用主机级覆盖项替换全局值，零值表示沿用全局。
func applyOverride(cfg *AgentConfig, o store.AgentOverride) {
	applied := false
	if o.Interval > 0 {
		cfg.Interval = o.Interval
		applied = true
	}
	if o.AllowCommand != "" {
		cfg.AllowCommand = o.AllowCommand == "true"
		applied = true
	}
	if o.SkipDisk != "" {
		cfg.SkipDisk = splitList(o.SkipDisk)
		applied = true
	}
	if o.UpdateMode != "" {
		cfg.UpdateMode = normalizeUpdateMode(o.UpdateMode)
		applied = true
	}
	if o.UpdateInterval > 0 {
		cfg.UpdateInterval = o.UpdateInterval
		applied = true
	}
	if applied {
		cfg.Source = "override"
	}
}

// configRevision 由配置内容算出指纹。
// 采集端据此判断「服务端配置变了没有」，避免每次拉取都重载定时器。
func configRevision(cfg AgentConfig) string {
	cfg.Revision = ""
	cfg.Hostname = ""
	cfg.Source = ""
	raw, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:12]
}

// normalizeUpdateMode 收敛更新策略取值。
func normalizeUpdateMode(v string) string {
	switch strings.TrimSpace(v) {
	case "auto", "check", "off":
		return v
	default:
		return "auto"
	}
}

// splitList 拆分逗号分隔的列表，去空去重并保持顺序稳定。
func splitList(v string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// handleAgentConfig 向采集端下发配置。
// 这是采集端部署后唯一需要主动拉取的接口：安装命令执行完就不再需要任何本地参数调整。
func (s *Server) handleAgentConfig(w http.ResponseWriter, r *http.Request) {
	if !s.agentTokenOK(r) {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"ok": false, "msg": "通信密钥校验失败"})
		return
	}
	hostname := strings.TrimSpace(r.URL.Query().Get("hostname"))
	if hostname == "" {
		hostname = strings.TrimSpace(r.Header.Get("X-Opsm-Hostname"))
	}
	writeJSON(w, s.agentConfigFor(s.resolveAgentHost(hostname)))
}

/* ---------------- 采集端配置的控制台接口 ---------------- */

// handleAgentConfigAPI 控制台读写采集端配置（全局默认值）。
func (s *Server) handleAgentConfigAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := s.store.Settings()
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		overrides, _ := s.store.ListAgentOverrides()
		writeJSON(w, map[string]any{
			"ok":             true,
			"interval":       settings["agentInterval"],
			"allowCommand":   settings["agentAllowCommand"],
			"skipDisk":       settings["agentSkipDisk"],
			"updateMode":     settings["agentUpdateMode"],
			"updateInterval": settings["agentUpdateInterval"],
			"refreshSec":     settings["agentRefreshSec"],
			"defaults": map[string]any{
				"interval":       defAgentInterval,
				"updateInterval": defAgentUpdateInterval,
				"refreshSec":     defAgentRefresh,
			},
			"overrides": overrides,
		})
	case http.MethodPut:
		kv := map[string]string{}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&kv); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
			return
		}
		// 只接受白名单键，避免控制台接口被用来写任意设置项
		allowed := map[string]string{}
		for _, k := range []string{"agentInterval", "agentAllowCommand", "agentSkipDisk",
			"agentUpdateMode", "agentUpdateInterval", "agentRefreshSec"} {
			if v, ok := kv[k]; ok {
				allowed[k] = v
			}
		}
		if v, ok := allowed["agentInterval"]; ok && intValue(v, 0) <= 0 {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "上报间隔必须是正整数秒"})
			return
		}
		if v, ok := allowed["agentUpdateInterval"]; ok && intValue(v, 0) <= 0 {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "版本检查间隔必须是正整数分钟"})
			return
		}
		if err := s.store.SaveSettings(allowed); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "msg": "已保存，采集端将在下个刷新周期内生效"})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

// handleHostAgentConfig 读写单台主机的覆盖配置。
func (s *Server) handleHostAgentConfig(w http.ResponseWriter, r *http.Request) {
	hostname := strings.TrimSpace(r.PathValue("host"))
	if hostname == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "缺少主机名"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		global := s.agentConfigFor("")
		override, err := s.store.GetAgentOverride(hostname)
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		effective := s.agentConfigFor(hostname)
		writeJSON(w, map[string]any{
			"ok":        true,
			"hostname":  hostname,
			"global":    global,
			"override":  override,
			"effective": effective,
		})
	case http.MethodPut:
		req := struct {
			Interval       int    `json:"interval"`
			AllowCommand   string `json:"allowCommand"`
			SkipDisk       string `json:"skipDisk"`
			UpdateMode     string `json:"updateMode"`
			UpdateInterval int    `json:"updateInterval"`
		}{}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
			return
		}
		if req.AllowCommand != "" && req.AllowCommand != "true" && req.AllowCommand != "false" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "执行指令开关取值只能是 true / false 或留空继承"})
			return
		}
		if req.UpdateMode != "" && req.UpdateMode != "auto" && req.UpdateMode != "check" && req.UpdateMode != "off" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "更新策略取值只能是 auto / check / off 或留空继承"})
			return
		}
		err := s.store.SaveAgentOverride(store.AgentOverride{
			Hostname:       hostname,
			Interval:       req.Interval,
			AllowCommand:   req.AllowCommand,
			SkipDisk:       req.SkipDisk,
			UpdateMode:     req.UpdateMode,
			UpdateInterval: req.UpdateInterval,
		})
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "msg": "已保存，该主机将在下个刷新周期内生效"})
	case http.MethodDelete:
		if err := s.store.DeleteAgentOverride(hostname); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "msg": "已恢复为全局默认配置"})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

// handleAgentConfigRevision 返回当前全局配置的指纹，控制台据此展示「是否有未生效的改动」。
func (s *Server) handleAgentConfigRevision(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "revision": s.agentConfigFor("").Revision})
}
