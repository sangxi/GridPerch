package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sangxi/strix/internal/config"
	"github.com/sangxi/strix/internal/model"
	"github.com/sangxi/strix/internal/version"
)

// Run 启动采集循环：配置下发、指标上报、监控项检查、指令下发、自动更新五条通道并行。
func Run(cfg *config.Agent) {
	if cfg.PrintVersion {
		fmt.Printf("strix-agent %s (%s/%s)", version.Version, runtime.GOOS, runtime.GOARCH)
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

	go configLoop(rt, client, base, wgToken)
	go metricsLoop(rt, client, base+"/agent/minTask")
	go commandLoop(rt, client, base, wgToken)
	if exe != "" {
		go updateLoop(rt, client, base, wgToken, exe)
	}
	// monitorLoop 常驻阻塞，主协程停在这里
	monitorLoop(rt, client, base, wgToken, checker)
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
