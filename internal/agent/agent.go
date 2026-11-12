package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/sangxi/gridperch/internal/config"
	"github.com/sangxi/gridperch/internal/model"
	"github.com/sangxi/gridperch/internal/version"
)

// rotatingWriter 写满上限就把旧文件改名重开，防止常驻进程把日志撑爆磁盘。
type rotatingWriter struct {
	path string
	max  int64
	size int64
	f    *os.File
}

func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if st, err := f.Stat(); err == nil {
		w.size = st.Size()
	}
	w.f = f
	return nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	if w.f == nil {
		if err := w.open(); err != nil {
			// 日志写不了不能反过来把进程弄死，当作写成功继续跑
			return len(p), nil
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	if w.size >= w.max {
		w.f.Close()
		w.f = nil
		w.size = 0
		_ = os.Remove(w.path + ".1")
	}
	return n, err
}

// setupLog 把日志同时送到 stderr 和文件。
//
// 采集端常驻在计划任务里，stdout/stderr 没有任何接收方：进程崩溃时 panic 堆栈
// 和最后几条日志会全部蒸发，事后只剩「主机掉线了」这一个现象，无从查证。
// 旧系统现场排查尤其依赖这个文件，所以安装脚本必须显式指定 -log。
func setupLog(path string) error {
	if path == "" {
		return nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	rw := &rotatingWriter{path: path, max: 5 << 20}
	if err := rw.open(); err != nil {
		return err
	}
	log.SetOutput(io.MultiWriter(os.Stderr, rw))
	return nil
}

// safeGo 启动受保护的 goroutine。
//
// 采集端有五条通道并行，任何一条 panic 都会带走整个进程；计划任务的
// RestartCount 只有 3 次，重试用完主机就永久掉线了 —— 而且现场无处可查。
// 这里把 panic 拦下来记进日志，进程继续跑，下一轮再试。
func safeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[%s] 协程异常已拦截，采集端继续运行: %v\n%s", name, r, debug.Stack())
			}
		}()
		fn()
	}()
}

// safeCall 给主协程用（monitorLoop 是阻塞的，不能丢进 goroutine）。
func safeCall(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[%s] 主循环异常已拦截: %v\n%s", name, r, debug.Stack())
		}
	}()
	fn()
}

// Run 启动采集循环：配置下发、指标上报、监控项检查、指令下发、自动更新五条通道并行。
func Run(cfg *config.Agent) {
	if cfg.PrintVersion {
		fmt.Printf("gridperch-agent %s (%s/%s)", version.Version, runtime.GOOS, runtime.GOARCH)
		// 兼容版必须自报身份：否则运维在目标机上分不清手上这个 exe 能不能跑旧系统，
		// 只能靠 grep 二进制里有没有 SystemFunction036 去猜。
		if version.LegacyWindows != "" {
			fmt.Printf(" 旧系统兼容版")
		}
		if version.BuildTime != "" {
			fmt.Printf(" 构建于 %s", version.BuildTime)
		}
		fmt.Println()
		return
	}
	client := &http.Client{Timeout: 20 * time.Second}
	base := strings.TrimRight(cfg.ServerURL, "/")
	checker := NewChecker()
	wgToken := cfg.TokenHash

	// 运行期配置：以本地启动参数为初值，随后由服务端下发的配置覆盖
	rt := NewRuntime(*cfg)

	if err := setupLog(cfg.LogFile); err != nil {
		log.Printf("打开日志文件失败（继续只用 stderr）: %v", err)
	}
	log.Printf("采集端启动：版本 %s，本机名称 %s，服务端 %s，指标上报间隔 %s，更新策略 %s",
		version.Version, cfg.Hostname, cfg.ServerURL, cfg.Interval, cfg.UpdateMode)

	exe, err := prepareSelf()
	if err != nil {
		log.Printf("无法定位自身可执行文件，自动更新将不可用: %v", err)
	}

	// 先拉一次配置再起各通道，保证首轮上报就用上平台配置的间隔
	if cfg.RemoteConfig != "off" {
		if remote, ferr := fetchRemoteConfig(client, base, wgToken, cfg.Hostname); ferr == nil {
			changed, first := rt.apply(remote)
			if first {
				log.Printf("已应用服务端配置（来源 %s，指纹 %s）：上报间隔 %s，执行指令 %s，更新策略 %s",
					sourceCN(remote.Source), remote.Revision, rt.Snapshot().Interval,
					onOff(remote.AllowCommand), remote.UpdateMode)
			} else if len(changed) > 0 {
				log.Printf("服务端配置已更新（来源 %s）：%s", sourceCN(remote.Source), strings.Join(changed, "；"))
			}
		} else {
			log.Printf("拉取服务端配置失败，暂用本地启动参数运行: %v", ferr)
		}
	}

	safeGo("config", func() { configLoop(rt, client, base, wgToken) })
	safeGo("metrics", func() { metricsLoop(rt, client, base+"/agent/minTask") })
	safeGo("command", func() { commandLoop(rt, client, base, wgToken) })
	if exe != "" {
		safeGo("update", func() { updateLoop(rt, client, base, wgToken, exe) })
	}
	// monitorLoop 常驻阻塞，主协程停在这里
	safeCall("monitor", func() { monitorLoop(rt, client, base, wgToken, checker) })
}

// metricsLoop 周期采集主机指标并上报。每轮重新取配置快照，
// 因此平台上改了上报间隔后无需重启采集端即刻生效。
func metricsLoop(rt *Runtime, client *http.Client, url string) {
	for {
		cfg := rt.Snapshot()
		data, err := Collect(cfg, cfg.Interval)
		if err != nil {
			log.Printf("采集失败: %v", err)
		} else {
			ensureLoginUsers(data)
			attachSysEvents(data)
			if err := postJSON(client, url, data); err != nil {
				log.Printf("指标上报失败: %v", err)
			} else {
				log.Printf("已上报 CPU %.1f%% / 内存 %.1f%% / 磁盘分区 %d 个",
					data.SystemInfo.CpuPer, data.SystemInfo.MemPer, len(data.DeskStates))
			}
		}
		interval := cfg.Interval
		if interval < minReportInterval {
			interval = minReportInterval
		}
		time.Sleep(interval)
	}
}

// monitorLoop 拉取分配给本机的监控项，按各自频率到期执行并上报结果。
func monitorLoop(rt *Runtime, client *http.Client, base, token string, checker *Checker) {
	var mu sync.Mutex
	tasks := map[string]model.TaskAssignment{}
	lastRun := map[string]time.Time{}

	refresh := func() {
		list, err := fetchTasks(client, base, token, rt.Hostname())
		if err != nil {
			log.Printf("拉取监控项失败: %v", err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		current := map[string]model.TaskAssignment{}
		for _, t := range list {
			current[t.ID] = t
		}
		for id := range lastRun {
			if _, ok := current[id]; !ok {
				delete(lastRun, id)
			}
		}
		tasks = current
		if len(tasks) > 0 {
			log.Printf("已加载 %d 个监控项", len(tasks))
		}
	}
	refresh()

	refreshTicker := time.NewTicker(60 * time.Second)
	defer refreshTicker.Stop()
	checkTicker := time.NewTicker(5 * time.Second)
	defer checkTicker.Stop()

	for {
		select {
		case <-refreshTicker.C:
			refresh()
		case <-checkTicker.C:
			now := time.Now()
			var due []model.TaskAssignment
			mu.Lock()
			for id, t := range tasks {
				interval := time.Duration(t.IntervalSec) * time.Second
				if interval <= 0 {
					interval = 30 * time.Second
				}
				if now.Sub(lastRun[id]) >= interval {
					due = append(due, t)
					lastRun[id] = now
				}
			}
			mu.Unlock()
			if len(due) == 0 {
				continue
			}
			results := runParallel(checker, due, 16)
			bad := 0
			for _, r := range results {
				if r.Status == statusFail {
					bad++
				}
			}
			if err := postJSON(client, base+"/agent/monitor", &model.MonitorResultBatch{
				WgToken:  token,
				Hostname: rt.Hostname(),
				Results:  results,
			}); err != nil {
				log.Printf("监控结果上报失败: %v", err)
			} else if bad > 0 {
				log.Printf("完成 %d 个监控项检查，其中 %d 项异常", len(results), bad)
			} else {
				log.Printf("完成 %d 个监控项检查，全部正常", len(results))
			}
		}
	}
}

// runParallel 并发执行到期监控项，限制并发避免拖累主机。
func runParallel(checker *Checker, tasks []model.TaskAssignment, limit int) []model.MonitorResult {
	if limit <= 0 {
		limit = 16
	}
	if len(tasks) < limit {
		limit = len(tasks)
	}
	ch := make(chan model.TaskAssignment, len(tasks))
	for _, t := range tasks {
		ch <- t
	}
	close(ch)

	results := make([]model.MonitorResult, len(tasks))
	var wg sync.WaitGroup
	for i := 0; i < limit; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			for t := range ch {
				// 结果按任务下标写入，避免加锁
				for idx := range tasks {
					if tasks[idx].ID == t.ID {
						results[idx] = checker.Run(t)
						break
					}
				}
			}
		}(i)
	}
	wg.Wait()
	return results
}

// fetchTasks 向服务端请求分配给本机的监控项列表。
func fetchTasks(client *http.Client, base, token, hostname string) ([]model.TaskAssignment, error) {
	url := fmt.Sprintf("%s/agent/tasks?hostname=%s&wgToken=%s", base, urlEscape(hostname), token)
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<22))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("服务端返回 HTTP %d: %s", resp.StatusCode, string(body))
	}
	var list []model.TaskAssignment
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func urlEscape(s string) string {
	return strings.NewReplacer(" ", "%20", "#", "%23", "&", "%26", "?", "%3F").Replace(s)
}

// postJSON 提交 JSON 并解析 result 字段。
func postJSON(client *http.Client, url string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var result struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(respBody, &result); err == nil && result.Result != "" && result.Result != "success" {
		return fmt.Errorf("服务端返回: %s", result.Result)
	}
	return nil
}
