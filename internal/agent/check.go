package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-ping/ping"
	"github.com/sangxi/OpsMonitor/internal/model"
	psdisk "github.com/shirou/gopsutil/v3/disk"
	psprocess "github.com/shirou/gopsutil/v3/process"
)

const (
	statusOK   = "1"
	statusFail = "2"
)

// targetOf 解析监控项配置为通用 kv。
func targetOf(raw string) map[string]string {
	out := map[string]string{}
	if raw == "" {
		return out
	}
	holder := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &holder); err != nil {
		return out
	}
	for k, v := range holder {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// Checker 执行具体类型的可用性检查。
type Checker struct {
	mu         sync.Mutex
	logOffsets map[string]int64
	transport  *http.Transport
}

// NewChecker 创建检查器。
func NewChecker() *Checker {
	return &Checker{
		logOffsets: map[string]int64{},
		transport: &http.Transport{
			MaxIdleConns:        64,
			MaxConnsPerHost:     8,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 5 * time.Second,
			DisableKeepAlives:   true,
		},
	}
}

// Run 按类型分发检查。
func (c *Checker) Run(t model.TaskAssignment) model.MonitorResult {
	res := model.MonitorResult{
		TaskID:    t.ID,
		Type:      t.Type,
		Status:    statusOK,
		CheckTime: time.Now().Format("2006-01-02 15:04:05"),
	}
	timeout := time.Duration(t.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	defer func() {
		if r := recover(); r != nil {
			res.Status = statusFail
			res.Msg = fmt.Sprintf("检查异常: %v", r)
		}
	}()

	switch t.Type {
	case "process":
		return c.finish(t, c.checkProcess(t, res))
	case "port":
		return c.finish(t, c.checkPort(t, res, timeout))
	case "http":
		return c.finish(t, c.checkHTTP(t, res, timeout))
	case "ping":
		return c.finish(t, c.checkPing(t, res, timeout))
	case "docker":
		return c.finish(t, c.checkDocker(t, res, timeout))
	case "log":
		return c.finish(t, c.checkLog(t, res))
	case "file":
		return c.finish(t, c.checkFile(t, res))
	case "nginxlog":
		return c.finish(t, c.checkNginxLog(t, res))
	case "script":
		return c.finish(t, c.checkScript(t, res, timeout))
	default:
		res.Msg = "暂不支持的监控类型: " + t.Type
		res.Status = statusFail
		return res
	}
}

// finish 在异常且配置了自动处理命令时执行恢复动作，例如进程自愈、日志告警联动脚本。
func (c *Checker) finish(t model.TaskAssignment, res model.MonitorResult) model.MonitorResult {
	if res.Status != statusFail || !t.AutoHeal || strings.TrimSpace(t.HealCmd) == "" {
		return res
	}
	timeout := time.Duration(t.TimeoutSec) * time.Second
	if timeout < 5*time.Second {
		timeout = 30 * time.Second
	}
	code, out, err := runCommand(t.HealCmd, timeout)
	if err != nil {
		res.Msg += fmt.Sprintf("；自动处理失败: %v", err)
		return res
	}
	if code != 0 {
		res.Msg += fmt.Sprintf("；自动处理命令退出码 %d", code)
		return res
	}
	res.Msg += fmt.Sprintf("；已执行自动处理命令并返回成功：%s", truncateAgent(out, 120))
	return res
}

/* ---------------- 进程监控 ---------------- */

func (c *Checker) checkProcess(t model.TaskAssignment, res model.MonitorResult) model.MonitorResult {
	cfg := targetOf(t.Target)
	keyword := cfg["name"]
	if keyword == "" {
		keyword = cfg["keyword"]
	}
	if keyword == "" {
		res.Status = statusFail
		res.Msg = "监控项缺少进程关键字配置"
		return res
	}
	procs, err := psprocess.Processes()
	if err != nil {
		res.Status = statusFail
		res.Msg = "读取进程列表失败: " + err.Error()
		return res
	}
	matched := []string{}
	for _, p := range procs {
		name, err := p.Name()
		if err != nil || name == "" {
			continue
		}
		if strings.Contains(strings.ToLower(name), strings.ToLower(keyword)) {
			matched = append(matched, name)
			if len(matched) >= 5 {
				break
			}
		}
	}
	if len(matched) == 0 {
		res.Status = statusFail
		res.Value = "0"
		res.Msg = fmt.Sprintf("进程 %s 未运行", keyword)
		return res
	}
	res.Value = strconv.Itoa(len(matched))
	res.Msg = fmt.Sprintf("进程命中 %d 个：%s", len(matched), strings.Join(matched, ", "))
	return res
}

/* ---------------- 端口监控 ---------------- */

func (c *Checker) checkPort(t model.TaskAssignment, res model.MonitorResult, timeout time.Duration) model.MonitorResult {
	cfg := targetOf(t.Target)
	host := cfg["host"]
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg["port"]
	addr := net.JoinHostPort(host, port)
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("端口 %s 不可达: %v", addr, err)
		res.Value = "-1"
		return res
	}
	_ = conn.Close()
	res.Value = fmt.Sprintf("%.0f", float64(time.Since(start).Milliseconds()))
	res.Msg = fmt.Sprintf("端口 %s 可连通，耗时 %d ms", addr, time.Since(start).Milliseconds())
	return res
}

/* ---------------- HTTP 服务接口监控 ---------------- */

func (c *Checker) checkHTTP(t model.TaskAssignment, res model.MonitorResult, timeout time.Duration) model.MonitorResult {
	cfg := targetOf(t.Target)
	url := cfg["url"]
	if url == "" {
		res.Status = statusFail
		res.Msg = "监控项缺少 URL 配置"
		return res
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		res.Status = statusFail
		res.Msg = "构造请求失败: " + err.Error()
		return res
	}
	client := &http.Client{Timeout: timeout, Transport: c.transport}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("请求 %s 失败: %v", url, err)
		res.Value = "-1"
		return res
	}
	defer resp.Body.Close()
	cost := time.Since(start).Milliseconds()
	body := ""
	if cfg["contains"] != "" {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		body = string(data)
	}
	res.Value = fmt.Sprintf("%d", cost)

	expect := 0
	if v := cfg["expectCode"]; v != "" {
		expect, _ = strconv.Atoi(v)
	}
	if expect > 0 && resp.StatusCode != expect {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("返回状态码 %d，期望 %d，耗时 %d ms", resp.StatusCode, expect, cost)
		return res
	}
	if resp.StatusCode >= 400 {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("返回状态码 %d，耗时 %d ms", resp.StatusCode, cost)
		return res
	}
	if cfg["contains"] != "" && !strings.Contains(body, cfg["contains"]) {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("响应内容未包含关键字 %s，耗时 %d ms", cfg["contains"], cost)
		return res
	}
	res.Msg = fmt.Sprintf("状态码 %d，耗时 %d ms", resp.StatusCode, cost)
	return res
}

/* ---------------- PING 监控 ---------------- */

func (c *Checker) checkPing(t model.TaskAssignment, res model.MonitorResult, timeout time.Duration) model.MonitorResult {
	cfg := targetOf(t.Target)
	host := cfg["host"]
	if host == "" {
		res.Status = statusFail
		res.Msg = "监控项缺少主机地址配置"
		return res
	}
	count := 3
	if v := cfg["count"]; v != "" {
		count, _ = strconv.Atoi(v)
	}
	lossLimit := 50.0
	if v := cfg["lossThreshold"]; v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			lossLimit = f
		}
	}

	pinger, err := ping.NewPinger(host)
	if err != nil {
		res.Status = statusFail
		res.Msg = "创建 ping 失败: " + err.Error()
		return res
	}
	pinger.Count = count
	pinger.Timeout = timeout
	// Windows 未实现 UDP 方式的非特权 ping，需走 ICMP.DLL；其余平台优先非特权模式避免需要 root
	privileged := runtime.GOOS == "windows"
	pinger.SetPrivileged(privileged)
	if err = pinger.Run(); err != nil {
		pinger.SetPrivileged(!privileged)
		if err2 := pinger.Run(); err2 != nil {
			res.Status = statusFail
			res.Value = "100"
			errMsg := err.Error()
			if strings.Contains(errMsg, "operation not permitted") || strings.Contains(errMsg, "socket") {
				errMsg += "（ICMP 需要权限或以 TCP 方式探测）"
			}
			res.Msg = fmt.Sprintf("PING %s 失败: %s", host, errMsg)
			return res
		}
	}
	stats := pinger.Statistics()
	loss := stats.PacketLoss
	res.Value = fmt.Sprintf("%.0f", loss)
	if loss >= lossLimit {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("PING %s 丢包率 %.0f%%（阈值 %.0f%%）", host, loss, lossLimit)
		return res
	}
	if stats.AvgRtt > 0 {
		res.Msg = fmt.Sprintf("PING %s 正常，丢包 %.0f%%，平均时延 %s", host, loss, stats.AvgRtt.Round(time.Microsecond*100))
		return res
	}
	res.Msg = fmt.Sprintf("PING %s 正常，丢包 %.0f%%", host, loss)
	return res
}

/* ---------------- Docker 监控 ---------------- */

// dockerEndpoints 按优先级返回待探测的 Docker Engine 地址。
func dockerEndpoints() []string {
	list := []string{"unix:///var/run/docker.sock"}
	if ep := os.Getenv("DOCKER_HOST"); ep != "" {
		list = append([]string{ep}, list...)
	}
	list = append(list, "tcp://127.0.0.1:2375")
	return list
}

func dockerTransport(endpoint string) (*http.Transport, string) {
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		switch {
		case strings.HasPrefix(endpoint, "unix://"):
			return d.DialContext(ctx, "unix", strings.TrimPrefix(endpoint, "unix://"))
		case strings.HasPrefix(endpoint, "npipe://"):
			// Windows 命名管道依赖 go-winio，此处不做额外依赖，直接失败由调用方降级
			return nil, fmt.Errorf("windows 命名管道需以 tcp 方式暴露 Docker API")
		default:
			return d.DialContext(ctx, network, addr)
		}
	}
	base := "http://docker"
	if strings.HasPrefix(endpoint, "tcp://") {
		base = "http://" + strings.TrimPrefix(endpoint, "tcp://")
	}
	return &http.Transport{DialContext: dial, DisableKeepAlives: true}, base
}

func (c *Checker) checkDocker(t model.TaskAssignment, res model.MonitorResult, timeout time.Duration) model.MonitorResult {
	cfg := targetOf(t.Target)
	var lastErr error
	for _, endpoint := range dockerEndpoints() {
		tr, base := dockerTransport(endpoint)
		client := &http.Client{Timeout: timeout, Transport: tr}
		list, err := listContainers(client, base)
		if err != nil {
			lastErr = err
			continue
		}
		running := 0
		names := []string{}
		for _, ct := range list {
			if strings.EqualFold(ct.State, "running") {
				running++
			}
			names = append(names, ct.displayName())
		}
		// 指定了容器名时检查其存活状态
		if want := cfg["container"]; want != "" {
			found := false
			for _, ct := range list {
				for _, n := range ct.Names {
					if strings.Contains(n.Name, want) {
						found = true
						if strings.EqualFold(ct.State, "running") {
							stats, statErr := containerStats(client, base, ct.ID)
							if statErr != nil {
								res.Value = fmt.Sprintf("%d", len(list))
								res.Msg = fmt.Sprintf("容器 %s 运行中（无法采集 CPU：%v）", want, statErr)
								return res
							}
							res.Value = fmt.Sprintf("%.1f", stats.CPUPercent)
							res.Msg = fmt.Sprintf("容器 %s 运行中，CPU %.1f%%，内存 %.1fMB / %.1fMB",
								want, stats.CPUPercent, float64(stats.MemUsage)/1024/1024, float64(stats.MemLimit)/1024/1024)
							// 配置了 CPU 阈值时，超限直接判定为异常
							if max := parseFloatIn(cfg["maxCpuPercent"]); max > 0 && stats.CPUPercent > max {
								res.Status = statusFail
								res.Msg = fmt.Sprintf("容器 %s CPU %.1f%% 超过阈值 %.1f%%", want, stats.CPUPercent, max)
							}
							return res
						}
						res.Status = statusFail
						res.Msg = fmt.Sprintf("容器 %s 状态为 %s", want, ct.State)
						return res
					}
				}
			}
			if !found {
				res.Status = statusFail
				res.Msg = fmt.Sprintf("未找到容器 %s", want)
				return res
			}
		}
		res.Value = fmt.Sprintf("%d", len(list))
		res.Msg = fmt.Sprintf("容器 %d 个（运行中 %d）：%s", len(list), running, strings.Join(names, ", "))
		return res
	}
	res.Status = statusFail
	if lastErr != nil {
		res.Msg = fmt.Sprintf("无法连接 Docker Engine: %v", lastErr)
	} else {
		res.Msg = "无法连接 Docker Engine"
	}
	return res
}

// dockerContainerStats 保存单个容器的 CPU 与内存占用。
type dockerContainerStats struct {
	CPUPercent float64
	MemUsage   float64
	MemLimit   float64
}

type dockerStatsResponse struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage  float64 `json:"total_usage"`
			PercpuUsage []int   `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage float64 `json:"system_cpu_usage"`
		OnlineCPUs     float64 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage float64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage float64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage float64 `json:"usage"`
		Limit float64 `json:"limit"`
	} `json:"memory_stats"`
}

// containerStats 调用 /containers/{id}/stats?stream=0 计算容器 CPU 使用率。
func containerStats(client *http.Client, base, id string) (*dockerContainerStats, error) {
	url := fmt.Sprintf("%s/containers/%s/stats?stream=0", base, id)
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var data dockerStatsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil {
		return nil, err
	}
	cpuDelta := data.CPUStats.CPUUsage.TotalUsage - data.PreCPUStats.CPUUsage.TotalUsage
	sysDelta := data.CPUStats.SystemCPUUsage - data.PreCPUStats.SystemCPUUsage
	cpus := data.CPUStats.OnlineCPUs
	if cpus <= 0 {
		cpus = float64(len(data.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpus <= 0 {
		cpus = 1
	}
	percent := 0.0
	if sysDelta > 0 && cpuDelta > 0 {
		percent = (cpuDelta / sysDelta) * cpus * 100
	}
	return &dockerContainerStats{CPUPercent: roundFloat(percent), MemUsage: data.MemoryStats.Usage, MemLimit: data.MemoryStats.Limit}, nil
}

func parseFloatIn(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var v float64
	if _, err := fmt.Sscan(s, &v); err != nil {
		return 0
	}
	return v
}

type containerItem struct {
	ID    string
	State string
	Names []struct {
		Name string `json:"Name"`
	} `json:"Names"`
}

// displayName 返回容器可读名称。
func (ci containerItem) displayName() string {
	if len(ci.Names) > 0 {
		return strings.TrimPrefix(ci.Names[0].Name, "/")
	}
	if len(ci.ID) > 12 {
		return ci.ID[:12]
	}
	return ci.ID
}

func listContainers(client *http.Client, base string) ([]containerItem, error) {
	resp, err := client.Get(base + "/containers/json?all=1")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker api 返回 %d", resp.StatusCode)
	}
	var list []containerItem
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<22)).Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
}

/* ---------------- 日志关键字监控 ---------------- */

func (c *Checker) checkLog(t model.TaskAssignment, res model.MonitorResult) model.MonitorResult {
	cfg := targetOf(t.Target)
	path := cfg["path"]
	keyword := cfg["keyword"]
	if path == "" || keyword == "" {
		res.Status = statusFail
		res.Msg = "监控项缺少日志路径或关键字配置"
		return res
	}
	file, err := os.Open(path)
	if err != nil {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("打开日志文件失败: %v", err)
		return res
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("读取日志文件失败: %v", err)
		return res
	}
	c.mu.Lock()
	offset, seen := c.logOffsets[path]
	if !seen {
		// 首次检查从文件末尾开始，避免历史内容造成重复告警
		offset = stat.Size()
	}
	c.mu.Unlock()

	if stat.Size() < offset {
		offset = 0 // 日志被轮转或清空
	}
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("定位日志位置失败: %v", err)
		return res
	}
	data, err := io.ReadAll(io.LimitReader(file, 2<<20))
	if err != nil {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("读取日志内容失败: %v", err)
		return res
	}
	newOffset := offset + int64(len(data))
	c.mu.Lock()
	c.logOffsets[path] = newOffset
	c.mu.Unlock()

	content := string(data)
	if content == "" {
		res.Value = "0"
		res.Msg = "本轮无新增日志"
		return res
	}
	var hitCount int
	var firstLine string
	if cfg["regex"] == "true" {
		re, err := regexp.Compile(keyword)
		if err != nil {
			res.Status = statusFail
			res.Msg = "关键字正则非法: " + err.Error()
			return res
		}
		matches := re.FindAllString(content, -1)
		hitCount = len(matches)
		if hitCount > 0 {
			firstLine = matches[0]
		}
	} else {
		hitCount = strings.Count(content, keyword)
		if hitCount > 0 {
			if idx := strings.Index(content, keyword); idx >= 0 {
				start := idx - 80
				if start < 0 {
					start = 0
				}
				end := idx + len(keyword) + 80
				if end > len(content) {
					end = len(content)
				}
				firstLine = strings.TrimSpace(content[start:end])
			}
		}
	}
	res.Value = strconv.Itoa(hitCount)
	if hitCount > 0 {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("日志命中关键字 %s 共 %d 次，样例：%s", keyword, hitCount, firstLine)
		return res
	}
	res.Msg = fmt.Sprintf("新增 %d 字节日志，未命中关键字 %s", len(data), keyword)
	return res
}

/* ---------------- 磁盘 IO 速率采集 ---------------- */

type diskSnapshot struct {
	mu       sync.Mutex
	readB    uint64
	writeB   uint64
	readOps  uint64
	writeOps uint64
	time     time.Time
	hasPrev  bool
}

var lastDiskIO diskSnapshot

// CollectDiskIO 返回自上次采样以来的磁盘读写速率（KB/s、IOPS）。
func CollectDiskIO(interval time.Duration) *model.DiskIoState {
	counters, err := psdisk.IOCounters()
	if err != nil || len(counters) == 0 {
		return nil
	}
	var readBytes, writeBytes, readOps, writeOps uint64
	for _, c := range counters {
		readBytes += c.ReadBytes
		writeBytes += c.WriteBytes
		readOps += c.ReadCount
		writeOps += c.WriteCount
	}
	now := time.Now()
	lastDiskIO.mu.Lock()
	defer lastDiskIO.mu.Unlock()
	secs := now.Sub(lastDiskIO.time).Seconds()
	if !lastDiskIO.hasPrev || secs <= 0 {
		secs = interval.Seconds()
	}
	if secs <= 0 {
		secs = 1
	}
	state := &model.DiskIoState{}
	if lastDiskIO.hasPrev {
		state.ReadKB = roundFloat(delta64(readBytes, lastDiskIO.readB) / secs / 1024)
		state.WriteKB = roundFloat(delta64(writeBytes, lastDiskIO.writeB) / secs / 1024)
		state.ReadIOPS = roundFloat(delta64(readOps, lastDiskIO.readOps) / secs)
		state.WriteIOPS = roundFloat(delta64(writeOps, lastDiskIO.writeOps) / secs)
	}
	lastDiskIO.readB, lastDiskIO.writeB = readBytes, writeBytes
	lastDiskIO.readOps, lastDiskIO.writeOps = readOps, writeOps
	lastDiskIO.time = now
	lastDiskIO.hasPrev = true
	return state
}

func delta64(cur, prev uint64) float64 {
	if cur < prev {
		return 0
	}
	return float64(cur - prev)
}

func roundFloat(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
