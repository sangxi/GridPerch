// Package poller 负责调度服务端主动探测类监控项，如 SNMP、PING、数据库、中间件、K8S 等。
//
// 这些目标本身不安装采集端，因此必须由服务端周期性发起探测，结果写入监控状态表，
// 与 agent 上报共用同一套告警流水。
package poller

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/sangxi/OpsMonitor/internal/config"
	"github.com/sangxi/OpsMonitor/internal/model"
	"github.com/sangxi/OpsMonitor/internal/probe"
	"github.com/sangxi/OpsMonitor/internal/store"
)

// Poller 周期性执行服务端监控项。
type Poller struct {
	store *store.Store
	cfg   *config.Server

	mu   sync.Mutex
	next map[string]time.Time // key: taskID|targetLabel

	credMu sync.Mutex
	creds  map[string]credEntry // 设备账号凭据缓存，避免每个探测周期都解密一次
}

// credEntry 是带过期时间的设备账号缓存项。
type credEntry struct {
	acc *model.DeviceAccount
	exp time.Time
}

// credentialTTL 设备账号缓存有效期。改完账号后最多 30 秒生效，兼顾实时性与解密开销。
const credentialTTL = 30 * time.Second

// credential 读取设备账号（含解密口令），带短时缓存。
func (p *Poller) credential(id string) (*model.DeviceAccount, error) {
	p.credMu.Lock()
	if e, ok := p.creds[id]; ok && time.Now().Before(e.exp) {
		p.credMu.Unlock()
		return e.acc, nil
	}
	p.credMu.Unlock()

	acc, err := p.store.DeviceCredential(id)
	if err != nil {
		return nil, err
	}
	p.credMu.Lock()
	p.creds[id] = credEntry{acc: acc, exp: time.Now().Add(credentialTTL)}
	p.credMu.Unlock()
	return acc, nil
}

// New 创建调度器。
func New(cfg *config.Server, st *store.Store) *Poller {
	return &Poller{store: st, cfg: cfg, next: map[string]time.Time{}, creds: map[string]credEntry{}}
}

// Run 启动调度循环。
func (p *Poller) Run(stop <-chan struct{}) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	log.Printf("服务端主动探测已启动，支持类型：%s", strings.Join(probe.Types(), "、"))
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			p.sweep()
		}
	}
}

// sweep 取出全部服务端监控项，执行到期的探测。
func (p *Poller) sweep() {
	tasks, err := p.store.TasksForServer()
	if err != nil {
		log.Printf("读取服务端监控项失败: %v", err)
		return
	}
	now := time.Now()
	alive := map[string]bool{}
	var jobs []job

	p.mu.Lock()
	for _, t := range tasks {
		base := probe.DecodeTarget(t.Target)
		decorateCredential(p, base)
		for _, item := range expandTargets(t, base) {
			key := t.ID + "|" + item.label
			alive[key] = true
			if due, ok := p.next[key]; !ok || !now.Before(due) {
				jobs = append(jobs, job{task: t, label: item.label, target: item.params})
				p.next[key] = now.Add(time.Duration(maxInt(t.IntervalSec, 5)) * time.Second)
			}
		}
	}
	for key := range p.next {
		if !alive[key] {
			delete(p.next, key)
		}
	}
	p.mu.Unlock()

	if len(jobs) == 0 {
		return
	}
	limit := maxInt(p.cfg.ProbeWorkers, 8)
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p.run(j)
		}(j)
	}
	wg.Wait()
}

type job struct {
	task   model.MonitorTask
	label  string
	target map[string]string
}

// decorateCredential 处理监控项对设备账号的引用。
//
// 监控项 target 中配置 "deviceId" 后，会用该设备账号里保存的地址、端口与加密口令
// 补齐探测参数；显式写在 target 里的字段优先级更高，便于临时覆盖。
// 引用失败时写入 _credentialError，由 run 直接判定为异常，避免空地址产生误导性结果。
func decorateCredential(p *Poller, params map[string]string) {
	id := strings.TrimSpace(params["deviceId"])
	if id == "" {
		return
	}
	acc, err := p.credential(id)
	if err != nil {
		params["_credentialError"] = "引用的设备账号不存在或读取失败（deviceId=" + id + "）"
		return
	}
	for k, v := range probe.CredentialTarget(acc) {
		if strings.TrimSpace(params[k]) == "" {
			params[k] = v
		}
	}
	if acc.Name != "" {
		params["_deviceName"] = acc.Name
	}
}

// run 执行一次探测并写入结果。
func (p *Poller) run(j job) {
	timeout := time.Duration(maxInt(j.task.TimeoutSec, 1)) * time.Second
	var res probe.Result
	if msg := j.target["_credentialError"]; msg != "" {
		res = probe.Result{Status: probe.StatusFail, Msg: msg}
	} else {
		res = probe.Run(j.task.Type, j.target, timeout)
	}
	r := model.MonitorResult{
		TaskID:    j.task.ID,
		Type:      j.task.Type,
		Status:    res.Status,
		Value:     res.Value,
		Msg:       res.Msg,
		CheckTime: time.Now().Format("2006-01-02 15:04:05"),
	}
	if err := p.store.SaveMonitorResults(j.label, []model.MonitorResult{r}); err != nil {
		log.Printf("写入服务端探测结果失败[%s/%s]: %v", j.task.Name, j.label, err)
	}
}

// targetItem 表示一个探测目标及其展示名。
type targetItem struct {
	label  string
	params map[string]string
}

// expandTargets 将监控项配置展开为一个或多个探测目标。
// 支持在 target 中配置 targets 字段批量指定多个地址，如 {"targets":"10.0.0.1,10.0.0.2"}。
func expandTargets(t model.MonitorTask, base map[string]string) []targetItem {
	var labels []string
	if raw := strings.TrimSpace(base["targets"]); raw != "" {
		for _, h := range strings.Split(raw, ",") {
			if h = strings.TrimSpace(h); h != "" {
				labels = append(labels, h)
			}
		}
	}
	if len(labels) == 0 {
		return []targetItem{{label: labelOf(t.Type, base), params: base}}
	}
	out := make([]targetItem, 0, len(labels))
	for _, h := range labels {
		params := map[string]string{}
		for k, v := range base {
			params[k] = v
		}
		// 批量地址场景下，引用设备账号时设备名只作为展示前缀，避免覆盖具体 IP
		if name := params["_deviceName"]; name != "" {
			params["_deviceName"] = name
		}
		params["host"] = h
		delete(params, "targets")
		out = append(out, targetItem{label: h, params: params})
	}
	return out
}

// labelOf 按类型推断目标的展示名，用于在状态列表中区分不同设备。
func labelOf(typ string, params map[string]string) string {
	// 引用了设备账号且未显式填写地址时，用设备名作为展示名
	if strings.TrimSpace(params["host"]) == "" {
		if name := strings.TrimSpace(params["_deviceName"]); name != "" {
			return truncateLabel(name)
		}
	}
	for _, key := range []string{"host", "ip", "target", "url", "addr"} {
		if v := strings.TrimSpace(params[key]); v != "" {
			return truncateLabel(v)
		}
	}
	return "server"
}

func truncateLabel(v string) string {
	v = strings.TrimPrefix(v, "http://")
	v = strings.TrimPrefix(v, "https://")
	if idx := strings.Index(v, "/"); idx > 0 {
		v = v[:idx]
	}
	if len(v) > 64 {
		v = v[:64]
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
