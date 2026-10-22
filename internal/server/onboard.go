package server

import (
	"net/http"
	"sort"
	"strings"

	"github.com/sangxi/gridperch/internal/config"
	"github.com/sangxi/gridperch/internal/store"
	"github.com/sangxi/gridperch/internal/version"
)

// agentPlatform 是「添加主机」页做前置校验用的平台摘要。
//
// 安装脚本的第一步就是拿目标平台去 /agent/version 问版本，没发布就直接中止；
// 把同一份信息提前给页面，用户选完系统与架构当场就能看到"这个平台还没发布"，
// 而不是把命令拷到目标主机上执行才失败。
type agentPlatform struct {
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Version string `json:"version"`
	Size    int64  `json:"size"`
	// Legacy 是否已上传旧系统兼容版（Windows 7 / 8.1 / Server 2012 R2 等）。
	// 兼容版与正式版并存，页面上提示「这些系统请用兼容版安装命令」。
	Legacy        bool   `json:"legacy"`
	LegacyVersion string `json:"legacyVersion"`
}

// publishedPlatforms 汇总当前有可下发采集端的平台。
// 判定完全复用 LatestRelease，与 /agent/version 的取法保持一致，
// 避免页面显示"已发布"而脚本实际拿不到版本。
func (s *Server) publishedPlatforms() []agentPlatform {
	list, err := s.store.ListReleases()
	if err != nil {
		return []agentPlatform{}
	}
	seen := make(map[string]bool, len(list))
	out := make([]agentPlatform, 0, len(list))
	for _, r := range list {
		if r.Archived {
			continue
		}
		key := r.Goos + "/" + r.Goarch
		if seen[key] {
			continue
		}
		seen[key] = true
		rel, err := s.store.LatestRelease(r.Goos, r.Goarch, "")
		if err != nil {
			continue
		}
		lg, _ := s.store.LatestRelease(r.Goos, r.Goarch, store.VariantLegacy)
		if rel == nil && lg == nil {
			continue
		}
		p := agentPlatform{OS: r.Goos, Arch: r.Goarch}
		if rel != nil {
			p.Version = rel.Version
			p.Size = rel.Size
		}
		if lg != nil {
			p.Legacy = true
			p.LegacyVersion = lg.Version
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OS != out[j].OS {
			return out[i].OS < out[j].OS
		}
		return out[i].Arch < out[j].Arch
	})
	return out
}

// handleAgentInstall 返回采集端接入所需参数，供控制台「添加主机」页生成安装命令。
//
// 通信密钥是采集端与服务端之间的共享口令，一旦泄露即可伪造上报数据，
// 因此仅管理员与运维可读取，只读账号会被拒绝。
func (s *Server) handleAgentInstall(w http.ResponseWriter, r *http.Request) {
	m := s.currentUser(r)
	if m == nil {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"ok": false, "msg": "未登录"})
		return
	}
	if m.Role != RoleAdmin && m.Role != RoleOperator {
		writeJSONStatus(w, http.StatusForbidden, map[string]any{"ok": false, "msg": "仅管理员与运维可获取接入密钥"})
		return
	}
	writeJSON(w, map[string]any{
		"ok":    true,
		"token": s.cfg.Token,
		// tokenHash 是 MD5(token)，采集端上报与脚本拉取都只用它，
		// 生成的命令与安装脚本里因此不会出现口令明文。
		"tokenHash": config.MD5Token(s.cfg.Token),
		"server":    s.baseURL(r),
		"interval":  60,
		"version":   version.Version,
		// platforms 让「添加主机」页在生成命令前就能判断目标平台有没有可下发的采集端
		"platforms": s.publishedPlatforms(),
	})
}

// requestBaseURL 反推浏览器访问服务端所用的基地址，作为采集端 -server 的默认值。
// 反向代理下优先采用 X-Forwarded-* 头，保证给采集端的是对外可达地址。
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := firstHeaderValue(r.Header.Get("X-Forwarded-Proto")); v != "" {
		scheme = v
	}
	host := r.Host
	if v := firstHeaderValue(r.Header.Get("X-Forwarded-Host")); v != "" {
		host = v
	}
	return scheme + "://" + host
}

// firstHeaderValue 取逗号分隔代理头中的第一个值。
func firstHeaderValue(v string) string {
	if v == "" {
		return ""
	}
	return strings.TrimSpace(strings.Split(v, ",")[0])
}
