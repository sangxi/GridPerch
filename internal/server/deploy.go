package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/sangxi/gridperch/internal/config"
)

// 批量部署：服务端作为 SSH 客户端，逐台连上目标机执行官方安装脚本，
// 从而把采集端「推」到还没接入的主机上，省去一台台手动 RDP/登录安装。
//
// 设计取舍：
//   - 统一走 SSH（Linux 原生，Windows 需开启 OpenSSH Server 可选功能），
//     复用项目已有的 golang.org/x/crypto/ssh，不引入 WinRM 等新依赖。
//   - 不在服务端处理二进制：直接远程执行 install.sh / install.ps1，
//     由脚本自行下载、校验、注册自启，和手动安装完全同一条路径。
//   - 凭据只在本次请求内存里用，不落库（前端「用完即弃」）。
//   - 平台由人显式选择（linux / windows），不做自动探测。
//
// 凭据支持两种写法（目标清单一行一台）：
//   - 「IP」                      → 用请求里的统一账号/密码
//   - 「IP,账号,密码」或「IP 账号 密码」→ 该台用自己的账号密码（覆盖统一值）
// 这样「每台密码都不一样」的环境也能一次性批量装，不必先统一口令。

type deployReq struct {
	OS       string   `json:"os"`       // linux | windows
	Targets  []string `json:"targets"`  // 每行一台，可带该台的 账号,密码
	User     string   `json:"user"`     // 统一账号（逐行未写时使用）
	Password string   `json:"password"` // 统一密码（逐行未写时使用）
	Key      string   `json:"key"`      // 可选：全局私钥（PEM）
	Port     int      `json:"port"`     // 默认 22
}

type deployTarget struct {
	Host string
	User string
	Pass string
	From string // 凭据来源（如「设备账号：xxx」），仅用于结果展示
}

type deployResult struct {
	Host    string `json:"host"`
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"msg"`
}

const (
	deployMaxTargets = 200
	deployParallel   = 5
	deployInstallTO  = 200 * time.Second
)

// parseTargets 把目标清单解析成「主机 + 该台凭据」。
// 支持「IP」「IP,账号,密码」「IP 账号 密码」三种写法；带逗号的优先按逗号切
// （密码里含空格时用逗号分隔才不会把密码截断）。
func parseTargets(lines []string, defUser, defPass string) []deployTarget {
	seen := map[string]bool{}
	out := make([]deployTarget, 0, len(lines))
	for _, raw := range lines {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		var parts []string
		if strings.Contains(s, ",") {
			parts = strings.SplitN(s, ",", 3)
		} else {
			parts = strings.Fields(s)
		}
		if len(parts) == 0 {
			continue
		}
		t := deployTarget{Host: strings.TrimSpace(parts[0]), User: defUser, Pass: defPass}
		if len(parts) >= 2 && strings.TrimSpace(parts[1]) != "" {
			t.User = strings.TrimSpace(parts[1])
		}
		if len(parts) >= 3 {
			// 逗号分隔时 parts[2] 是密码全串；空格分隔时只取到第三段（密码含空格请用逗号）
			t.Pass = strings.TrimSpace(parts[2])
		}
		if t.Host == "" || seen[t.Host] {
			continue
		}
		seen[t.Host] = true
		out = append(out, t)
	}
	return out
}

// handleBatchDeploy 批量下发采集端安装。
func (s *Server) handleBatchDeploy(w http.ResponseWriter, r *http.Request) {
	var req deployReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
		return
	}
	osKind := strings.ToLower(strings.TrimSpace(req.OS))
	if osKind != "linux" && osKind != "windows" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "请选择目标平台：Linux 或 Windows"})
		return
	}
	targets := parseTargets(req.Targets, strings.TrimSpace(req.User), req.Password)
	if len(targets) == 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "目标清单为空"})
		return
	}
	if len(targets) > deployMaxTargets {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": fmt.Sprintf("单次最多 %d 台", deployMaxTargets)})
		return
	}
	// 凑凭据顺序：本台显式（清单里写了）> 设备账号（按地址自动匹配）> 全局默认。
	hasKey := strings.TrimSpace(req.Key) != ""
	for i := range targets {
		t := &targets[i]
		if !hasKey && strings.TrimSpace(t.Pass) == "" {
			if acc, err := s.store.DeviceCredentialForHost(t.Host); err == nil && acc != nil && strings.TrimSpace(acc.Secret) != "" {
				t.User = acc.Username
				t.Pass = acc.Secret
				t.From = acc.Name
			}
		}
		if strings.TrimSpace(t.User) == "" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "主机 " + t.Host + " 缺少账号：请填统一账号、在清单里写「IP,账号,密码」，或在「设备账号」里为该地址配 SSH 账号"})
			return
		}
		if !hasKey && strings.TrimSpace(t.Pass) == "" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "主机 " + t.Host + " 缺少密码：请填统一密码、在清单里写「IP,账号,密码」，或在「设备账号」里为该地址配 SSH 账号"})
			return
		}
	}

	port := req.Port
	if port <= 0 {
		port = 22
	}
	base := s.baseURL(r)
	tokenHash := config.MD5Token(s.cfg.Token)

	results := make([]deployResult, len(targets))
	sem := make(chan struct{}, deployParallel)
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t deployTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = deployOne(t, osKind, req.Key, port, base, tokenHash)
		}(i, t)
	}
	wg.Wait()

	success := 0
	for _, r := range results {
		if r.OK {
			success++
		}
	}
	writeJSON(w, map[string]any{"ok": true, "total": len(results), "success": success, "results": results})
}

// deployOne 处理单台目标：SSH 连接 → 取主机名 → 执行对应平台的安装脚本。
func deployOne(t deployTarget, osKind, key string, port int, base, tokenHash string) deployResult {
	res := deployResult{Host: t.Host}

	var auths []ssh.AuthMethod
	if k := strings.TrimSpace(key); k != "" {
		if signer, err := ssh.ParsePrivateKey([]byte(k)); err == nil {
			auths = append(auths, ssh.PublicKeys(signer))
		}
	}
	if t.Pass != "" {
		auths = append(auths, ssh.Password(t.Pass))
		auths = append(auths, ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = t.Pass
			}
			return answers, nil
		}))
	}
	if len(auths) == 0 {
		res.Message = "未提供可用的密码或私钥"
		return res
	}

	addr := net.JoinHostPort(t.Host, fmt.Sprint(port))
	cfg := &ssh.ClientConfig{User: t.User, Auth: auths, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 15 * time.Second}
	cli, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		res.Message = "SSH 连接失败：" + err.Error()
		return res
	}
	defer cli.Close()

	// 先取目标机主机名，作为采集端注册名（避免用 IP 当主机名）
	nameCmd := "hostname"
	if osKind == "windows" {
		nameCmd = "powershell -NoProfile -Command \"$env:COMPUTERNAME\""
	}
	name, _ := runSSH(cli, nameCmd, 15*time.Second)
	name = sanitizeAgentName(strings.TrimSpace(name))
	if name == "" {
		name = sanitizeAgentName(t.Host)
	}
	res.Name = name

	q := "wgToken=" + url.QueryEscape(tokenHash) + "&name=" + url.QueryEscape(name)
	var cmd string
	if osKind == "linux" {
		cmd = fmt.Sprintf("curl -fsSL '%s/install.sh?%s' | bash", base, q)
	} else {
		cmd = fmt.Sprintf("powershell -NoProfile -ExecutionPolicy Bypass -Command \"iwr -useb '%s/install.ps1?%s' | iex\"", base, q)
	}

	out, err := runSSH(cli, cmd, deployInstallTO)
	if err != nil {
		res.Message = strings.TrimSpace(tail(out, 300))
		if res.Message == "" {
			res.Message = err.Error()
		} else {
			res.Message = err.Error() + "｜" + res.Message
		}
		return res
	}
	low := strings.ToLower(out)
	if strings.Contains(low, "安装中止") || strings.Contains(low, "权限不足") || strings.Contains(low, "无法连接平台") {
		res.Message = strings.TrimSpace(tail(out, 300))
		return res
	}
	res.OK = true
	res.Message = "已安装并注册开机自启"
	if t.From != "" {
		res.Message += "（凭据取自设备账号：" + t.From + "）"
	}
	return res
}

// runSSH 在已建立的连接上执行一条命令，带超时（超时关闭会话，避免卡死整批）。
func runSSH(cli *ssh.Client, cmd string, timeout time.Duration) (string, error) {
	sess, err := cli.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	type outcome struct {
		out []byte
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		o, e := sess.CombinedOutput(cmd)
		ch <- outcome{o, e}
	}()
	select {
	case r := <-ch:
		return string(r.out), r.err
	case <-time.After(timeout):
		_ = sess.Close()
		return "", fmt.Errorf("执行超时（%s）", timeout)
	}
}

// tail 截取输出的最后 n 个字符，前端提示够用即可，避免整段刷屏。
func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}
