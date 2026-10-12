package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sangxi/OpsMonitor/internal/config"
	"github.com/sangxi/OpsMonitor/internal/license"
	"github.com/sangxi/OpsMonitor/internal/model"
	"github.com/sangxi/OpsMonitor/internal/store"
	"github.com/sangxi/OpsMonitor/internal/version"
)

/* ================ 一、版本发布 ================ */

// maxReleaseSize 单个发布包上限，防止误传超大文件占满磁盘。
const maxReleaseSize = 200 << 20

// releasesDir 返回发布包的存放目录。默认与数据库同级，便于整目录打包迁移。
func (s *Server) releasesDir() string {
	dir := strings.TrimSpace(s.cfg.ReleaseDir)
	if dir == "" {
		if d := filepath.Dir(s.cfg.DBPath); d != "" && d != "." {
			dir = filepath.Join(d, "releases")
		} else {
			dir = "releases"
		}
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// baseURL 返回对外可达的服务端地址：优先启动参数 public-url，
// 否则按浏览器 / 采集端请求头反推，保证安装脚本里的地址是真实可访问的。
func (s *Server) baseURL(r *http.Request) string {
	if v := strings.TrimRight(strings.TrimSpace(s.cfg.PublicURL), "/"); v != "" {
		return v
	}
	return requestBaseURL(r)
}

// handleReleases 版本发布管理：GET 列表，POST 上传新版本。
func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListReleases()
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		stats, _ := s.store.ReleaseStats()
		hosts, _ := s.store.ListHosts()
		// 统计各平台待升级主机数，页面直接展示「有多少台需要升级」
		stale := map[string]int{}
		latest := map[string]string{}
		for _, rel := range list {
			if rel.IsLatest {
				latest[rel.Goos+"/"+rel.Goarch] = rel.Version
			}
		}
		for _, h := range hosts {
			if h.AgentVersion == "" {
				continue
			}
			for key, ver := range latest {
				if version.IsNewer(ver, h.AgentVersion) {
					stale[key]++
				}
			}
		}
		writeJSON(w, map[string]any{
			"ok":        true,
			"list":      list,
			"stats":     stats,
			"stale":     stale,
			"server":    version.Version,
			"buildTime": version.BuildTime,
			"protocol":  version.Protocol,
		})
	case http.MethodPost:
		s.uploadRelease(w, r)
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

// uploadRelease 接收 multipart 上传的采集端二进制并登记为新版本。
func (s *Server) uploadRelease(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxReleaseSize+4<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "解析上传内容失败：" + err.Error()})
		return
	}
	ver := strings.TrimSpace(r.FormValue("version"))
	goos := strings.TrimSpace(r.FormValue("goos"))
	goarch := strings.TrimSpace(r.FormValue("goarch"))
	notes := strings.TrimSpace(r.FormValue("notes"))
	minVer := strings.TrimSpace(r.FormValue("minVersion"))
	makeLatest := r.FormValue("latest") == "true" || r.FormValue("latest") == "1"

	if ver == "" || goos == "" || goarch == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "版本号、操作系统、CPU 架构均为必填"})
		return
	}
	switch goos {
	case "linux", "windows", "darwin":
	default:
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "操作系统只支持 linux / windows / darwin"})
		return
	}
	switch goarch {
	case "amd64", "arm64", "386", "arm":
	default:
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "CPU 架构只支持 amd64 / arm64 / 386 / arm"})
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "请选择要上传的采集端二进制文件"})
		return
	}
	defer file.Close()

	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	name := fmt.Sprintf("opsmonitor-agent-%s-%s", goos, goarch)
	// 版本目录 + 平台文件名，便于人工核对与回滚
	dir := filepath.Join(s.releasesDir(), ver)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "创建发布目录失败：" + err.Error()})
		return
	}
	// 同名版本重复上传覆盖：先写临时文件，落盘校验通过后再改名，避免半截文件
	dstPath := filepath.Join(dir, name+ext)
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "创建临时文件失败：" + err.Error()})
		return
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(file, maxReleaseSize+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "写入文件失败：" + err.Error()})
		return
	}
	if size > maxReleaseSize {
		_ = os.Remove(tmp.Name())
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "文件超过 200MB 上限"})
		return
	}
	if err := os.Rename(tmp.Name(), dstPath); err != nil {
		_ = os.Remove(tmp.Name())
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "保存发布包失败：" + err.Error()})
		return
	}
	_ = os.Chmod(dstPath, 0o755)

	rel := &store.Release{
		Version: ver, Goos: goos, Goarch: goarch,
		Filename: name + ext, Path: dstPath, Size: size,
		SHA256: hex.EncodeToString(h.Sum(nil)), Notes: notes,
		MinVersion: minVer, IsLatest: makeLatest,
	}
	if err := s.store.AddRelease(rel); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	saved, err := s.store.GetReleaseByVersion(goos, goarch, ver)
	if err != nil || saved == nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "发布记录写入后读取失败"})
		return
	}
	// 勾选了「设为该平台最新版本」才推送；首个版本默认自动成为最新
	if makeLatest || s.isFirstRelease(goos, goarch, saved.ID) {
		_ = s.store.SetReleaseLatest(saved.ID)
	}
	writeJSON(w, map[string]any{"ok": true, "release": saved})
}

// isFirstRelease 判断该平台上是否只有这一条记录，是则自动设为最新。
func (s *Server) isFirstRelease(goos, goarch, id string) bool {
	list, err := s.store.ListReleases()
	if err != nil {
		return false
	}
	n := 0
	for _, r := range list {
		if r.Goos == goos && r.Goarch == goarch {
			n++
		}
	}
	return n == 1
}

// handleReleaseItem 删除某条发布记录，同时清理磁盘文件（同版本其它平台仍在时保留目录）。
func (s *Server) handleReleaseItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "缺少发布记录 ID"})
		return
	}
	rel, err := s.store.DeleteRelease(id)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	if rel != nil && rel.Path != "" {
		if err := os.Remove(rel.Path); err != nil && !os.IsNotExist(err) {
			writeJSON(w, map[string]any{"ok": true, "msg": "记录已删除，但磁盘文件清理失败：" + err.Error()})
			return
		}
		_ = os.Remove(filepath.Dir(rel.Path)) // 目录为空时顺手删掉
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleReleaseSetLatest 把指定发布记录设为该平台的下发版本。
func (s *Server) handleReleaseSetLatest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.SetReleaseLatest(id); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

/* ================ 二、采集端侧：版本查询 / 下载 / 回执 ================ */

// agentTokenOK 校验采集端提交的通信密钥，两种写法都支持：
// 查询串 wgToken（与原 wgcloud 接口一致）与请求头 X-WgToken。
func (s *Server) agentTokenOK(r *http.Request) bool {
	got := strings.TrimSpace(r.URL.Query().Get("wgToken"))
	if got == "" {
		got = strings.TrimSpace(r.Header.Get("X-WgToken"))
	}
	if got == "" {
		return false
	}
	return got == config.MD5Token(s.cfg.Token) || got == s.cfg.Token
}

// handleAgentVersion 采集端查询自己所在平台的最新版本。
// 返回 force=true 时表示当前版本低于该平台设置的最低版本，必须升级。
func (s *Server) handleAgentVersion(w http.ResponseWriter, r *http.Request) {
	if !s.agentTokenOK(r) {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"ok": false, "msg": "通信密钥校验失败"})
		return
	}
	goos := strings.TrimSpace(r.URL.Query().Get("os"))
	goarch := strings.TrimSpace(r.URL.Query().Get("arch"))
	current := strings.TrimSpace(r.URL.Query().Get("current"))
	if goos == "" || goarch == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "缺少 os / arch 参数"})
		return
	}
	rel, err := s.store.LatestRelease(goos, goarch)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	if rel == nil {
		writeJSON(w, map[string]any{
			"ok": true, "version": "", "hasUpdate": false,
			"server": version.Version, "msg": "服务端尚未发布该平台的采集端版本",
		})
		return
	}
	token := config.MD5Token(s.cfg.Token)
	url := fmt.Sprintf("%s/agent/download?os=%s&arch=%s&version=%s&wgToken=%s",
		s.baseURL(r), rel.Goos, rel.Goarch, rel.Version, token)
	force := rel.MinVersion != "" && version.IsNewer(rel.MinVersion, current)
	writeJSON(w, map[string]any{
		"ok":         true,
		"version":    rel.Version,
		"hasUpdate":  version.IsNewer(rel.Version, current),
		"force":      force,
		"url":        url,
		"sha256":     rel.SHA256,
		"size":       rel.Size,
		"notes":      rel.Notes,
		"minVersion": rel.MinVersion,
		"releaseAt":  rel.CreateTime,
		"server":     version.Version,
		"protocol":   version.Protocol,
	})
}

// handleAgentDownload 下发采集端二进制。指定 version 可下载历史版本用于回滚。
func (s *Server) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	if !s.agentTokenOK(r) {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"ok": false, "msg": "通信密钥校验失败"})
		return
	}
	goos := strings.TrimSpace(r.URL.Query().Get("os"))
	goarch := strings.TrimSpace(r.URL.Query().Get("arch"))
	ver := strings.TrimSpace(r.URL.Query().Get("version"))
	var rel *store.Release
	var err error
	if ver != "" {
		rel, err = s.store.GetReleaseByVersion(goos, goarch, ver)
	} else {
		rel, err = s.store.LatestRelease(goos, goarch)
	}
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	if rel == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "服务端没有该平台/版本的采集端程序，请先在「版本发布」中上传"})
		return
	}
	f, err := os.Open(rel.Path)
	if err != nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "发布包文件不存在，可能已被删除：" + err.Error()})
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+rel.Filename)
	w.Header().Set("X-Opsm-Version", rel.Version)
	w.Header().Set("X-Opsm-Sha256", rel.SHA256)
	http.ServeContent(w, r, rel.Filename, info.ModTime(), f)
	_ = s.store.BumpReleaseDownload(rel.ID)
}

// handleAgentUpdateReport 采集端升级完成后回执，写审计便于回溯灰度情况。
//
// 这里是「采集端 → 监控端」方向的最后一环：采集端本身不能访问公网，
// 升级结果先到监控端，由监控端在下一轮同步时连同统计一起回执给授权端，
// 从而在不放开采集端出网的前提下，让授权端掌握各平台的真实升级分布。
func (s *Server) handleAgentUpdateReport(w http.ResponseWriter, r *http.Request) {
	if !s.agentTokenOK(r) {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"ok": false, "msg": "通信密钥校验失败"})
		return
	}
	var req struct {
		Hostname string `json:"hostname"`
		From     string `json:"from"`
		To       string `json:"to"`
		Result   string `json:"result"`
		Message  string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
	result := "ok"
	if req.Result != "ok" && req.Result != "success" {
		result = "fail"
	}
	s.store.AppendAudit(model.AuditLog{
		Username: "agent", Role: "-", Action: "采集端自动更新", Category: "upgrade",
		Target: req.Hostname, Detail: fmt.Sprintf("%s → %s %s", req.From, req.To, truncate(req.Message, 160)),
		IP: clientIP(r), Result: result, Path: r.URL.Path, Method: r.Method,
		CreateTime: time.Now().Format("2006-01-02 15:04:05"),
	})
	s.noteAgentUpdate(req.Hostname, req.To, result)
	writeJSON(w, map[string]any{"ok": true})
}

/* ================ 三、一键接入安装脚本（从平台拉取） ================ */

// handleInstallScript 动态生成安装脚本，脚本内容本身就会从平台拉取二进制，
// 目标主机不需要事先拷贝任何文件，也不需要能访问外网。
func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	// 脚本里会嵌通信口令的指纹，因此取脚本也要鉴权，否则任何人都能拿到接入凭据
	if !s.agentTokenOK(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "通信密钥校验失败：请从控制台「添加主机」页复制完整命令\n")
		return
	}
	script := r.URL.Query().Get("script")
	if script == "" {
		if strings.HasSuffix(r.URL.Path, ".ps1") {
			script = "ps1"
		} else {
			script = "sh"
		}
	}
	// 脚本里不带密钥原文，只带 MD5 后的通信口令，避免明文口令落在目标主机命令行历史里
	tokenHash := config.MD5Token(s.cfg.Token)
	base := s.baseURL(r)
	name := sanitizeAgentName(r.URL.Query().Get("name"))

	if script == "ps1" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, installPS1, base, tokenHash, name)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, installSH, base, tokenHash, name)
}

// sanitizeAgentName 只保留主机名安全字符，避免脚本注入。
func sanitizeAgentName(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		v = "opsmonitor-host"
	}
	var b strings.Builder
	for _, c := range v {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			b.WriteRune(c)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// installSH Linux / macOS 一键安装脚本。
// 脚本里嵌的是通信口令的 MD5，不是明文 token：目标主机的命令行历史与
// systemd 单元文件都不会出现明文口令。
const installSH = `#!/usr/bin/env bash
# OpsMonitor 采集端一键安装：直接从平台拉取二进制并注册为系统服务。
# 本脚本由服务端动态生成，请勿手工修改。
set -euo pipefail

SERVER="%s"
TOKEN_HASH="%s"
HOST_NAME="%s"

echo "==> 检测系统环境"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux)  GOOS=linux ;;
  darwin) GOOS=darwin ;;
  *) echo "不支持的平台：$OS" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) echo "不支持的 CPU 架构：$(uname -m)" >&2; exit 1 ;;
esac
echo "    平台 $GOOS/$GOARCH"

# 优先 curl，其次 wget；两者都没有时给出明确的安装指引，而不是让后续命令报 command not found
if command -v curl >/dev/null 2>&1; then
  fetch()    { curl -fsSL "$1"; }
  download() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch()    { wget -qO- "$1"; }
  download() { wget -qO "$2" "$1"; }
else
  echo "未找到 curl 也未找到 wget，请先安装其中之一（例如：yum install -y curl 或 apt-get install -y curl）。" >&2
  exit 1
fi

echo "==> 检查与平台的连通性"
# 用控制台首页探测：它不需要鉴权，只要服务在跑就会返回 200
if ! fetch "$SERVER/" >/dev/null 2>&1; then
  echo "无法连接平台 $SERVER，请依次检查：" >&2
  echo "  1) 服务端是否已经启动；" >&2
  echo "  2) 服务端启动参数 -addr 是否为 :9999（只绑 127.0.0.1 时，其他机器访问不到）；" >&2
  echo "  3) 本机到服务端 9999 端口是否被防火墙拦截；" >&2
  echo "  4) 命令里的地址是否是本机能访问到的地址——生成命令时可能带上服务端本机的 127.0.0.1，或云主机的内网 IP（10.x / 192.168.x），这些别的机器都连不上；" >&2
  echo "     云主机请在服务端设置 -public-url http://公网IP:9999（或 OPSM_PUBLIC_URL）后重新生成命令。" >&2
  exit 1
fi

echo "==> 查询服务端发布的最新版本"
META=$(fetch "$SERVER/agent/version?os=$GOOS&arch=$GOARCH&wgToken=$TOKEN_HASH&current=0.0.0")
VERSION=$(printf '%%s' "$META" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')
SHA=$(printf '%%s' "$META" | sed -n 's/.*"sha256":"\([^"]*\)".*/\1/p')
if [ -z "$VERSION" ]; then
  echo "服务端尚未发布 $GOOS/$GOARCH 的采集端版本，安装无法继续。" >&2
  echo "" >&2
  echo "  请在服务端控制台的「版本发布」页面上传该平台的采集端程序：" >&2
  echo "    目标系统：$GOOS     CPU 架构：$GOARCH     版本号：与本次构建一致" >&2
  echo "  或在服务端源码目录执行 ./release.sh，会一次性构建并上传全部平台的采集端。" >&2
  echo "  控制台地址：$SERVER" >&2
  exit 1
fi
echo "    最新版本 $VERSION"

echo "==> 从平台下载采集端"
INSTALL_DIR=/usr/local/bin
BIN="$INSTALL_DIR/opsmonitor-agent"
TMP=$(mktemp)
download "$SERVER/agent/download?os=$GOOS&arch=$GOARCH&wgToken=$TOKEN_HASH" "$TMP"

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "$TMP" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL=$(shasum -a 256 "$TMP" | awk '{print $1}')
else
  ACTUAL=""
fi
if [ -n "$SHA" ] && [ -n "$ACTUAL" ] && [ "$SHA" != "$ACTUAL" ]; then
  echo "校验和不一致，文件可能在传输中损坏，已中止安装。" >&2
  rm -f "$TMP"; exit 1
fi

sudo install -m 0755 "$TMP" "$BIN"
rm -f "$TMP"
echo "    已安装到 $BIN"

echo "==> 拉取平台下发的采集端配置"
CONF=$(fetch "$SERVER/agent/config?wgToken=$TOKEN_HASH&hostname=$HOST_NAME" 2>/dev/null || true)
if [ -n "$CONF" ]; then
  echo "    $CONF"
else
  echo "    暂未取到，采集端启动后会自动重试"
fi

# 只带定位服务端与标识本机的最小参数，采集间隔、执行指令开关、
# 跳过磁盘、更新策略都由平台「采集端配置」下发并支持热更新。
ARGS="-server $SERVER -token-hash $TOKEN_HASH -name $HOST_NAME"

if [ "$GOOS" = "darwin" ]; then
  echo "==> 注册 launchd 后台服务"
  PLIST=/Library/LaunchDaemons/opsmonitor-agent.plist
  sudo tee "$PLIST" >/dev/null <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>opsmonitor-agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>$BIN</string><string>-server</string><string>$SERVER</string>
    <string>-token-hash</string><string>$TOKEN_HASH</string>
    <string>-name</string><string>$HOST_NAME</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/var/log/opsmonitor-agent.log</string>
  <key>StandardErrorPath</key><string>/var/log/opsmonitor-agent.log</string>
</dict>
</plist>
EOF
  sudo launchctl unload "$PLIST" 2>/dev/null || true
  sudo launchctl load "$PLIST"
  echo "==> 完成，查看日志：tail -f /var/log/opsmonitor-agent.log"
  exit 0
fi

if command -v systemctl >/dev/null 2>&1; then
  echo "==> 注册 systemd 服务"
  sudo tee /etc/systemd/system/opsmonitor-agent.service >/dev/null <<EOF
[Unit]
Description=OpsMonitor 采集端
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$BIN $ARGS
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
  sudo systemctl daemon-reload
  sudo systemctl enable --now opsmonitor-agent
  sleep 2
  systemctl status opsmonitor-agent --no-pager || true
  echo "==> 完成。日志：journalctl -u opsmonitor-agent -f"
else
  echo "==> 未检测到 systemd，改用 nohup 常驻"
  sudo nohup "$BIN" $ARGS >/var/log/opsmonitor-agent.log 2>&1 &
  echo "==> 已后台启动，日志：/var/log/opsmonitor-agent.log"
fi
`

// installPS1 Windows 一键安装脚本。
const installPS1 = `# OpsMonitor 采集端一键安装（Windows PowerShell）
# 直接从平台拉取二进制并注册为开机自启任务。
# 本脚本由服务端动态生成，请勿手工修改。
$ErrorActionPreference = 'Stop'

$Server     = '%s'
$TokenHash  = '%s'
$HostName   = '%s'
$InstallDir = 'C:\OpsMonitor'
$Exe        = Join-Path $InstallDir 'opsmonitor-agent.exe'

Write-Host '==> 检测系统环境与权限'

# 注册计划任务、写入 C:\OpsMonitor 都要求管理员权限；提前拦住，免得后面报晦涩的 HRESULT
$wp = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $wp.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  Write-Host '需要管理员权限。请右键「Windows PowerShell」选择「以管理员身份运行」，再重新执行本命令。' -ForegroundColor Red
  throw '安装中止：权限不足。'
}

# 旧系统（如 Windows Server 2012 R2）默认可能只启用 TLS 1.0，HTTPS 平台需要显式打开 1.2
try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch { }

$Goarch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'amd64' }
  'ARM64' { 'arm64' }
  'x86'   { '386' }
  default { 'amd64' }
}
Write-Host "    平台 windows/$Goarch"

Write-Host '==> 检查与平台的连通性'
# 先探一次平台：连不通时直接把排查方向说清楚，而不是让后面的下载抛一串 .NET 异常
try {
  $null = Invoke-WebRequest -UseBasicParsing -Uri "$Server/agent/version?os=windows&arch=$Goarch&wgToken=$TokenHash&current=0.0.0" -TimeoutSec 15
} catch {
  # 有 HTTP 响应说明网络本身是通的（例如密钥不匹配返回 401），交给后面的接口去校验
  if (-not $_.Exception.Response) {
    Write-Host ''
    Write-Host "无法连接平台 $Server，请依次检查：" -ForegroundColor Red
    Write-Host '  1) 服务端是否已经启动；'
    Write-Host '  2) 服务端启动参数 -addr 是否为 :9999（只绑 127.0.0.1 时，其他机器访问不到）；'
    Write-Host '  3) 本机到服务端 9999 端口是否被防火墙拦截（服务端可用 netsh advfirewall firewall add rule name=OpsMonitor dir=in action=allow protocol=TCP localport=9999 放行）；'
    Write-Host '  4) 命令里的地址是否是本机能访问到的地址——生成命令时可能带上服务端本机的 127.0.0.1，或云主机的内网 IP（10.x / 192.168.x），这些别的机器都连不上；'
    Write-Host '     云主机请在服务端设置 -public-url http://公网IP:9999（或 OPSM_PUBLIC_URL）后重新生成命令。'
    Write-Host "  原始错误：$($_.Exception.Message)"
    throw '安装中止：无法连接平台。'
  }
}

Write-Host '==> 查询服务端发布的最新版本'
$meta = Invoke-RestMethod -Uri "$Server/agent/version?os=windows&arch=$Goarch&wgToken=$TokenHash&current=0.0.0" -TimeoutSec 15
if (-not $meta.version) {
  Write-Host ''
  Write-Host "服务端尚未发布 windows/$Goarch 的采集端版本，安装无法继续。" -ForegroundColor Red
  Write-Host '  请在服务端控制台的「版本发布」页面上传该平台的采集端程序：'
  Write-Host "    目标系统：Windows     CPU 架构：$Goarch     版本号：与本次构建一致"
  Write-Host '  或在服务端源码目录执行 ./release.sh，会一次性构建并上传全部平台的采集端。'
  Write-Host "  控制台地址：$Server"
  throw '安装中止：服务端尚未发布该平台的采集端版本。'
}
Write-Host "    最新版本 $($meta.version)"

Write-Host '==> 从平台下载采集端'
New-Item -ItemType Directory -Force $InstallDir | Out-Null
$tmp = Join-Path $InstallDir '.opsmonitor-agent.download'
Invoke-WebRequest -UseBasicParsing -Uri "$Server/agent/download?os=windows&arch=$Goarch&wgToken=$TokenHash" -OutFile $tmp -TimeoutSec 300

if ($meta.sha256) {
  $actual = (Get-FileHash $tmp -Algorithm SHA256).Hash.ToLower()
  if ($actual -ne $meta.sha256.ToLower()) {
    Remove-Item $tmp -Force
    throw '校验和不一致，文件可能在传输中损坏，已中止安装。'
  }
}
# 正在运行的旧进程无法直接覆盖，先停掉并等文件句柄释放再替换
Get-Process opsmonitor-agent -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Milliseconds 500
Move-Item $tmp $Exe -Force
Write-Host "    已安装到 $Exe"

Write-Host '==> 拉取平台下发的采集端配置'
try {
  $conf = Invoke-RestMethod -Uri "$Server/agent/config?wgToken=$TokenHash&hostname=$HostName" -TimeoutSec 10
  Write-Host ("    上报间隔 {0}s / 执行指令 {1} / 更新策略 {2}" -f $conf.interval, $conf.allowCommand, $conf.updateMode)
} catch {
  Write-Host '    暂未取到，采集端启动后会自动重试'
}

Write-Host '==> 注册开机自启计划任务'
# 只带定位服务端与标识本机的最小参数，其余配置由平台「采集端配置」下发。
# 注意：$args 是 PowerShell 自动变量，必须换名，否则会覆盖脚本自身的参数集。
$agentArgs = "-server $Server -token-hash $TokenHash -name $HostName"
$action    = New-ScheduledTaskAction -Execute $Exe -Argument $agentArgs -WorkingDirectory $InstallDir
$trigger   = New-ScheduledTaskTrigger -AtStartup
$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -RunLevel Highest
# ExecutionTimeLimit 置 0 表示不限时：计划任务默认执行满 72 小时会被强杀，常驻进程必须放开
$settings  = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit (New-TimeSpan -Seconds 0)
$splat = @{ TaskName = 'opsmonitor-agent'; Action = $action; Trigger = $trigger; Principal = $principal
            Settings = $settings; Description = 'OpsMonitor 采集端'; Force = $true }
Register-ScheduledTask @splat | Out-Null
Start-ScheduledTask -TaskName 'opsmonitor-agent'
Start-Sleep -Seconds 2
$task = Get-ScheduledTask -TaskName 'opsmonitor-agent'
Write-Host "    计划任务状态：$($task.State)"
Write-Host ''
Write-Host '==> 安装完成。采集端已注册为开机自启任务 opsmonitor-agent。'
Write-Host '    数据请到控制台「主机监控」查看。'
Write-Host '    手动重启：Start-ScheduledTask -TaskName opsmonitor-agent'
Write-Host '    查看状态：Get-ScheduledTask -TaskName opsmonitor-agent | Select-Object State'
Write-Host '    卸载任务：Unregister-ScheduledTask -TaskName opsmonitor-agent -Confirm:$false'
`

/* ================ 四、授权管理 ================ */

// licenseStatus 返回当前授权状态。结果缓存在内存中，导入/解除授权时失效重建，
// 这样高频的上报接口不必每条都读库并重算签名。
func (s *Server) licenseStatus() license.Status {
	s.licMu.RLock()
	if s.licCache != nil {
		st := *s.licCache
		s.licMu.RUnlock()
		return st
	}
	s.licMu.RUnlock()

	st := license.Resolve(s.store.LicenseKey(), s.licenseVerifier(), s.licenseMachine(), s.freeQuota())
	s.licMu.Lock()
	s.licCache = &st
	s.licMu.Unlock()
	return st
}

// invalidateLicense 授权变更后清掉缓存。
func (s *Server) invalidateLicense() {
	s.licMu.Lock()
	s.licCache = nil
	s.licMu.Unlock()
}

// licenseVerifier 返回授权码校验器。公钥编译在二进制里，这里只是取出来用。
func (s *Server) licenseVerifier() license.Verifier { return s.licVer }

// licenseMachine 返回本机机器码，可用 -license-machine 覆盖（容器/漂移 IP 场景）。
func (s *Server) licenseMachine() string {
	if v := strings.TrimSpace(s.cfg.LicenseMachine); v != "" {
		return v
	}
	return license.MachineCode()
}

// freeQuota 返回未授权（社区版）的配额上限，来自启动参数。
func (s *Server) freeQuota() license.FreeQuota {
	return license.FreeQuota{MaxHosts: s.cfg.LicenseFreeHost, MaxUsers: s.cfg.LicenseFreeUser}
}

// importStartupLicense 支持用启动参数 -license 直接注入授权码。
func (s *Server) importStartupLicense() {
	key := strings.TrimSpace(s.cfg.LicenseKey)
	if key == "" {
		return
	}
	st := license.Resolve(key, s.licenseVerifier(), s.licenseMachine(), s.freeQuota())
	if !st.Valid {
		return
	}
	_ = s.store.SaveLicense(key, st.Customer, st.Edition)
	s.invalidateLicense()
}

// handleLicense 授权状态查询 / 导入 / 解除。
func (s *Server) handleLicense(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		st := s.licenseStatus()
		hosts := s.store.CountHosts()
		users := s.store.CountMembers()
		writeJSON(w, map[string]any{
			"ok":       true,
			"status":   st,
			"summary":  st.Summary(),
			"hosts":    hosts,
			"users":    users,
			"machine":  s.licenseMachine(),
			"editions": license.Editions,
			"features": license.Features(),
			"quota": map[string]any{
				"hostsUsed": hosts, "hostsMax": st.MaxHosts,
				"usersUsed": users, "usersMax": st.MaxUsers,
			},
		})
	case http.MethodPost:
		s.importLicense(w, r)
	case http.MethodDelete:
		if err := s.store.ClearLicense(); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		s.invalidateLicense()
		// 解除后本机不再持有授权，页面不要再显示「已获取授权」。
		s.setHub(func(h *HubStatus) {
			h.LicenseState = ""
			h.LicenseMsg = "已解除授权，恢复到社区版配额"
		})
		writeJSON(w, map[string]any{"ok": true, "msg": "已解除授权，恢复到社区版配额"})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

func (s *Server) importLicense(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "请求内容解析失败"})
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "请填写授权码"})
		return
	}
	st := license.Resolve(key, s.licenseVerifier(), s.licenseMachine(), s.freeQuota())
	if !st.Valid {
		msg := "授权码校验未通过：" + st.Reason
		// 新格式验签失败最常见的原因是「本机内置公钥不是签发该码的那把」——
		// 规格第 8 节特别提醒过：日志只会说「签名校验失败」，不提公钥，运维无从下手。
		// 这里把本机内置公钥指纹一并给出，直接和授权中心「授权管理」页/启动日志对照即可定位。
		if strings.HasPrefix(strings.TrimSpace(key), license.PrefixV2) {
			msg += fmt.Sprintf("。本机内置验签公钥指纹：%s（应与授权中心「授权管理」页显示的一致）",
				s.licenseVerifier().Fingerprint())
		}
		log.Printf("授权码导入失败：%s", msg)
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": msg})
		return
	}
	// 已接入主机数超过授权上限时允许先导入，但会给出提示，避免换授权时被卡住
	warn := ""
	if st.MaxHosts > 0 && s.store.CountHosts() > st.MaxHosts {
		warn = fmt.Sprintf("当前已接入 %d 台主机，超过该授权上限 %d 台，请清理无用主机或换更高档位的授权。",
			s.store.CountHosts(), st.MaxHosts)
	}
	if err := s.store.SaveLicense(key, st.Customer, st.Edition); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	s.invalidateLicense()
	// 手工导入成功也回写一次同步状态：否则页面要等下一轮从授权系统同步
	// （默认 5 分钟）才会从「等待签发」翻篇，中间这段时间自相矛盾。
	s.setHub(func(h *HubStatus) {
		h.LicenseState = "active"
		h.LicenseMsg = "授权已生效：" + st.Summary()
	})
	writeJSON(w, map[string]any{"ok": true, "status": st, "summary": st.Summary(), "warn": warn})
}

func randomSuffix(n int) string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	seed := sha256.Sum256([]byte(fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())))
	for i := range b {
		b[i] = chars[int(seed[i])%len(chars)]
	}
	return string(b)
}

// handleHostUpgradeStatus 汇总各主机采集端版本与平台最新版本的差距，供控制台展示升级进度。
func (s *Server) handleHostUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.store.ListHosts()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	rels, err := s.store.ListReleases()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	type plat struct {
		Goos, Goarch, Version string
	}
	var plats []plat
	for _, rel := range rels {
		if rel.IsLatest {
			plats = append(plats, plat{rel.Goos, rel.Goarch, rel.Version})
		}
	}
	// 取所有平台里最高的版本作为「目标版本」，主机版本低于它即视为待升级
	target := ""
	for _, p := range plats {
		if version.IsNewer(p.Version, target) {
			target = p.Version
		}
	}
	out := []map[string]any{}
	stale, unknown := 0, 0
	for _, h := range hosts {
		need := false
		if h.AgentVersion == "" {
			unknown++
			need = target != ""
		} else if version.IsNewer(target, h.AgentVersion) {
			stale++
			need = true
		}
		out = append(out, map[string]any{
			"hostname": h.Hostname, "ip": h.IP, "os": h.Version,
			"agentVersion": h.AgentVersion, "state": h.State,
			"lastSeen": h.LastSeen, "needUpgrade": need,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		ai, _ := out[i]["agentVersion"].(string)
		aj, _ := out[j]["agentVersion"].(string)
		return ai < aj
	})
	writeJSON(w, map[string]any{
		"ok": true, "target": target, "hosts": out,
		"stale": stale, "unknown": unknown, "total": len(out),
		"platforms": plats,
	})
}
