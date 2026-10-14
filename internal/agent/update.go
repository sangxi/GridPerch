package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sangxi/OpsMonitor/internal/config"
	"github.com/sangxi/OpsMonitor/internal/version"
)

// oldSuffix 被替换下来的旧二进制后缀，启动时清理。
const oldSuffix = ".old"

// updateInfo 服务端 /agent/version 返回的版本信息。
type updateInfo struct {
	Version    string `json:"version"`
	HasUpdate  bool   `json:"hasUpdate"`
	Force      bool   `json:"force"`
	URL        string `json:"url"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Notes      string `json:"notes"`
	MinVersion string `json:"minVersion"`
	ReleaseAt  string `json:"releaseAt"`
	Msg        string `json:"msg"`
	Server     string `json:"server"`
}

// Run 启动后的入口辅助：处理 -version，清理残留旧文件。
func prepareSelf() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	// 上一轮升级留下的旧二进制，此时已不再被占用，直接删掉
	if _, err := os.Stat(exe + oldSuffix); err == nil {
		if err := os.Remove(exe + oldSuffix); err != nil {
			log.Printf("清理旧版本文件失败（可忽略）: %v", err)
		}
	}
	return exe, nil
}

// updateLoop 周期性检查服务端是否发布了更新的采集端版本。
// 策略：auto 自动下载替换并重启；check 只打印提示；off 不检查。
//
// 策略本身也可能被平台在运行期改掉，所以不在启动时直接退出，
// 而是每轮重新取配置快照再决定做什么。
func updateLoop(rt *Runtime, client *http.Client, base, wgToken string, exe string) {
	// 启动后先跑一次，但让位给首轮指标上报，避免启动风暴
	time.Sleep(20 * time.Second)
	offLogged := false
	for {
		cfg := rt.Snapshot()
		if cfg.UpdateMode == "off" {
			if !offLogged {
				log.Printf("自动更新已关闭（平台配置 updateMode=off），如需自动升级请在「采集端配置」中改为 auto")
				offLogged = true
			}
		} else {
			offLogged = false
			checkUpdate(cfg, client, base, wgToken, exe)
		}
		interval := cfg.UpdateInterval
		if interval <= 0 {
			interval = 30 * time.Minute
		}
		time.Sleep(interval)
	}
}

// checkUpdate 执行一轮版本检查，必要时升级。
func checkUpdate(cfg config.Agent, client *http.Client, base, wgToken, exe string) {
	info, err := fetchUpdateInfo(client, base, wgToken)
	if err != nil {
		log.Printf("版本检查失败: %v", err)
		return
	}
	if !info.HasUpdate {
		return
	}
	hint := ""
	if info.Force {
		hint = "（服务端要求最低版本 " + info.MinVersion + "，属于强制升级）"
	}
	if cfg.UpdateMode == "check" {
		log.Printf("发现新版本 %s%s，当前为 %s；如需自动升级请在「采集端配置」中把更新策略改为 auto",
			info.Version, hint, version.Version)
		return
	}
	log.Printf("发现新版本 %s（当前 %s）%s，开始自动升级", info.Version, version.Version, hint)
	if err := applyUpdate(cfg, client, info, exe, wgToken, base); err != nil {
		log.Printf("自动升级失败: %v", err)
		reportUpdate(client, base, wgToken, cfg.Hostname, version.Version, info.Version, "fail", err.Error())
		return
	}
	reportUpdate(client, base, wgToken, cfg.Hostname, version.Version, info.Version, "ok", "升级完成，进程即将重启")
	log.Printf("已升级到 %s，进程即将重启", info.Version)
	restartSelf(exe)
}

// fetchUpdateInfo 向服务端查询本平台的最新版本。
func fetchUpdateInfo(client *http.Client, base, wgToken string) (*updateInfo, error) {
	url := fmt.Sprintf("%s/agent/version?os=%s&arch=%s&wgToken=%s&current=%s",
		base, runtime.GOOS, runtime.GOARCH, wgToken, version.Version)
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("服务端返回 HTTP %d: %s", resp.StatusCode, truncateText(string(body), 200))
	}
	var info updateInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("解析版本信息失败: %w", err)
	}
	return &info, nil
}

// applyUpdate 下载新版本、校验完整性并替换当前二进制。
func applyUpdate(cfg config.Agent, client *http.Client, info *updateInfo, exe, wgToken, base string) error {
	if info.URL == "" {
		return fmt.Errorf("服务端未返回下载地址")
	}
	dir := filepath.Dir(exe)
	if d := strings.TrimSpace(cfg.InstallDir); d != "" {
		dir = d
		target := filepath.Join(d, filepath.Base(exe))
		exe = target
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建安装目录失败: %w", err)
	}

	// 临时文件必须与目标同目录，保证后续 rename 是原子操作
	tmp, err := os.CreateTemp(dir, ".opsmonitor-agent-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	dlClient := &http.Client{Timeout: 10 * time.Minute}
	resp, err := dlClient.Get(info.URL)
	if err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		cleanup()
		return fmt.Errorf("下载失败，服务端返回 HTTP %d", resp.StatusCode)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("写文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("落盘失败: %w", err)
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if info.SHA256 != "" && !strings.EqualFold(actual, info.SHA256) {
		cleanup()
		return fmt.Errorf("校验和不一致（期望 %s，实际 %s），文件可能在传输中损坏", info.SHA256, actual)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("设置可执行权限失败: %w", err)
	}

	// 替换之前先让新二进制自报版本。发布物的「版本号标签」可能与二进制内嵌版本
	// 不一致（典型事故：发布时用旧产物配了新版本号），那样换上去等于没换 ——
	// 下一轮检查又会「发现新版本」，变成每轮都下载 + 重启、永远不收敛的循环。
	// 与平台侧 hubPullSelf 的守卫同源：自报版本必须真比当前运行的版本新。
	// （服务端只在 IsNewer(发布版本, 本机版本) 为真时才下发升级，所以拒绝这里的
	// 情况必然意味着标签与二进制对不上，不会误伤正常升级。）
	if selfVer, verr := runSelfVersion(tmpPath); verr != nil {
		cleanup()
		return fmt.Errorf("新版本自检失败（无法执行下载回来的二进制）：%w", verr)
	} else if !version.IsNewer(selfVer, version.Version) {
		cleanup()
		return fmt.Errorf("平台发布的 %s 自报版本为 %s，并不高于当前运行的 %s，已拒绝替换。"+
			"发布方很可能把旧产物登记成了新版本号，请联系平台管理员重新构建上传",
			info.Version, selfVer, version.Version)
	}

	// 先把当前二进制改名，再落新文件：Windows 上正在运行的 exe 无法被覆盖，
	// 但可以被改名，因此这套流程在两个平台上都成立。
	oldPath := exe + oldSuffix
	_ = os.Remove(oldPath)
	if err := os.Rename(exe, oldPath); err != nil {
		cleanup()
		return fmt.Errorf("备份当前版本失败: %w", err)
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		// 回滚，避免把采集端搞丢
		_ = os.Rename(oldPath, exe)
		cleanup()
		return fmt.Errorf("替换新版本失败: %w", err)
	}
	return nil
}

// runSelfVersion 执行一个刚刚下载的 opsmonitor 二进制的 `-version`，
// 返回它「自报」的版本号（输出形如 `opsmonitor-agent 1.1.7 (linux/amd64) 构建于 …`）。
//
// 临时文件名与运行中的可执行文件同目录、已 chmod 0755；Windows 下 CreateProcess
// 只看文件头不看扩展名，因此带不带后缀都能跑。加超时是兜底：正常情况下它打印一行
// 就退出，跑不起来才需要放弃（宁可拒绝替换，也不能把采集端换成跑不起来的二进制）。
func runSelfVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return "", fmt.Errorf("输出无法解析：%s", strings.TrimSpace(string(out)))
	}
	return fields[1], nil
}

// restartSelf 让新版本接管。
//
// 由 systemd / launchd 托管时直接退出，交给服务管理器拉起（我们生成的单元文件
// 用的是 Restart=always / KeepAlive），避免同时跑起两个采集端；
// 手工 nohup 启动的场景则自己拉起新进程再退出。
func restartSelf(exe string) {
	if supervised() {
		log.Printf("已由服务管理器托管，退出后会自动以新版本启动")
		os.Exit(0)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	setDetach(cmd)
	if err := cmd.Start(); err != nil {
		log.Printf("拉起新版本失败，请手工重启采集端: %v", err)
		os.Exit(1)
	}
	log.Printf("新版本已启动（PID %d），当前进程退出", cmd.Process.Pid)
	os.Exit(0)
}

// supervised 判断当前进程是否由服务管理器托管。
func supervised() bool {
	// systemd 会注入 INVOCATION_ID / JOURNAL_STREAM
	if os.Getenv("INVOCATION_ID") != "" || os.Getenv("JOURNAL_STREAM") != "" {
		return true
	}
	// launchd 会注入 XPC_SERVICE_NAME
	if os.Getenv("XPC_SERVICE_NAME") != "" {
		return true
	}
	return false
}

// reportUpdate 把升级结果回执给服务端，写进操作审计便于回溯灰度情况。
func reportUpdate(client *http.Client, base, wgToken, hostname, from, to, result, msg string) {
	body, _ := json.Marshal(map[string]string{
		"hostname": hostname, "from": from, "to": to, "result": result, "message": msg,
	})
	resp, err := client.Post(base+"/agent/update?wgToken="+wgToken, "application/json", strings.NewReader(string(body)))
	if err != nil {
		log.Printf("上报升级结果失败: %v", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
