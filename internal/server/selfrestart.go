package server

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 自升级重启。
//
// 「在线升级」把新二进制就位后，如果进程不退出重启，跑的还是旧代码 ——
// 表现为「平台自己没法更新自己」：文件换了、版本号没变、功能也没变。
//
// 重启策略：
//   - 被服务管理器托管时（systemd / launchd / Docker），直接退出交给管理器用
//     新二进制拉起：我们生成的 systemd 单元是 Restart=always，docker-compose
//     是 restart: unless-stopped，容器里 PID 1 退出即整容器重启；
//   - 手工 nohup 启动的场景，自己拉起一个新进程再退出。新进程启动时若端口仍被
//     旧进程占着会自动重试（见 listenAndServeWithRetry），因此无需额外等待。
//
// 若部署环境既不托管又无法拉起新进程，会打日志提示人工重启，不会静默失效。

const selfRestartDelay = 3 * time.Second

var selfRestartOnce sync.Once

// scheduleSelfRestart 只触发一次：延时若干秒后重启自身。
// 延时是为了让 HTTP 响应先写完、授权系统回执先发出去，再退出进程。
func (s *Server) scheduleSelfRestart(reason string) {
	if s == nil || strings.EqualFold(strings.TrimSpace(s.cfg.HubRestart), "off") {
		log.Printf("自动更新已配置为不重启（-hub-restart off）：%s，新版本已就位，请手工重启服务生效", reason)
		return
	}
	selfRestartOnce.Do(func() {
		go func() {
			log.Printf("平台将在 %v 后自动重启以启用新版本（%s）", selfRestartDelay, reason)
			time.Sleep(selfRestartDelay)
			restartSelfSelf(reason)
		}()
	})
}

// restartSelfSelf 让新版本接管当前进程。
func restartSelfSelf(reason string) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("自动重启失败：无法定位程序路径：%v，请手工重启服务", err)
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)

	if supervisedSelf() {
		log.Printf("已由服务管理器/容器托管，退出后将以新版本自动拉起（%s）"+
			"；若容器未配置 restart 策略，请在宿主机执行 docker compose up -d", reason)
		os.Exit(0)
	}

	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	setDetachSelf(cmd)
	if err := cmd.Start(); err != nil {
		log.Printf("拉起新版本失败：%v，请手工重启服务（新版本已就位）", err)
		return
	}
	log.Printf("新版本已拉起（PID %d），当前进程退出（%s）", cmd.Process.Pid, reason)
	os.Exit(0)
}

// supervisedSelf 判断当前进程是否由服务管理器/容器托管。
func supervisedSelf() bool {
	// systemd 会注入 INVOCATION_ID / JOURNAL_STREAM
	if os.Getenv("INVOCATION_ID") != "" || os.Getenv("JOURNAL_STREAM") != "" {
		return true
	}
	// launchd 会注入 XPC_SERVICE_NAME
	if os.Getenv("XPC_SERVICE_NAME") != "" {
		return true
	}
	// 容器：PID 1 退出即整容器退出，由 compose 的 restart 策略重新拉起
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if strings.TrimSpace(os.Getenv("container")) != "" ||
		strings.TrimSpace(os.Getenv("OPSM_IN_CONTAINER")) != "" {
		return true
	}
	return false
}
