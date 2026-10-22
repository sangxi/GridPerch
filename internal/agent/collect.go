// Package agent 实现主机指标采集与上报，等价于原 wgcloud-agent 的采集能力。
package agent

import (
	"fmt"
	"log"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sangxi/gridperch/internal/config"
	"github.com/sangxi/gridperch/internal/model"
	"github.com/sangxi/gridperch/internal/version"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	psnet "github.com/shirou/gopsutil/v3/net"
	psprocess "github.com/shirou/gopsutil/v3/process"
)

// netSnapshot 保存上一轮网络累计值，用于计算速率。
type netSnapshot struct {
	mu       sync.Mutex
	recv     uint64
	sent     uint64
	packRecv uint64
	packSent uint64
	time     time.Time
	hasPrev  bool
}

var snapshot netSnapshot

// Collect 采集一轮完整指标。
// 传入的是配置快照（值传递），避免与配置热更新产生数据竞争。
func Collect(cfg config.Agent, interval time.Duration) (*model.Report, error) {
	now := time.Now()
	r := &model.Report{WgToken: cfg.TokenHash, Hostname: cfg.Hostname, AgentVersion: version.Version}

	hi, err := host.Info()
	if err != nil {
		return nil, fmt.Errorf("获取主机信息失败: %w", err)
	}
	if cfg.Hostname == "" {
		cfg.Hostname = hi.Hostname
		r.Hostname = hi.Hostname
	}

	cpuCount, _ := cpu.Counts(true)
	cpuInfo, _ := cpu.Info()
	cpuModel := fmt.Sprintf("%s %s", hi.OS, runtime.GOARCH)
	if len(cpuInfo) > 0 && cpuInfo[0].ModelName != "" {
		cpuModel = cpuInfo[0].ModelName
	}
	coreNum := strconv.Itoa(cpuCount)

	sysInfo := &model.SystemInfo{
		Hostname:      cfg.Hostname,
		IP:            localIP(),
		Version:       fmt.Sprintf("%s %s", hi.Platform, hi.PlatformVersion),
		VersionDetail: fmt.Sprintf("%s / %s", hi.OS, hi.KernelVersion),
		CpuCoreNum:    coreNum,
		CpuXh:         cpuModel,
		State:         "1",
		CreateTime:    now.Format("2006-01-02 15:04:05"),
	}

	// CPU 使用率：采样 1 秒取平均值
	percent, err := cpu.Percent(time.Second, false)
	cpuUsed := 0.0
	if err == nil && len(percent) > 0 {
		cpuUsed = percent[0]
	}
	sysInfo.CpuPer = round(cpuUsed)

	// 内存
	var memUsedPercent, memTotalMB, memUsedMB, memFreeMB float64
	if vm, err := mem.VirtualMemory(); err == nil {
		memUsedPercent = vm.UsedPercent
		memTotalMB = float64(vm.Total) / 1024 / 1024
		memUsedMB = float64(vm.Used) / 1024 / 1024
		memFreeMB = float64(vm.Available) / 1024 / 1024
	}
	sysInfo.MemPer = round(memUsedPercent)

	r.SystemInfo = sysInfo
	r.CpuState = &model.CpuState{Sys: round(cpuUsed), Idle: round(100 - cpuUsed), User: "-"}
	r.MemState = &model.MemState{
		Total:  fmt.Sprintf("%.0f", memTotalMB),
		Used:   fmt.Sprintf("%.0f", memUsedMB),
		Free:   fmt.Sprintf("%.0f", memFreeMB),
		UsePer: round(memUsedPercent),
	}

	// 系统负载（Windows 无 loadavg，置 0）
	// Users 语义为「当前登录用户名列表」，供主机登录提醒比对变化，
	// 因此优先使用系统会话用户名，取不到时再回退到 who / query user 命令。
	users := loginUserNames()
	one, five, fifteen := 0.0, 0.0, 0.0
	if avg, err := load.Avg(); err == nil {
		one, five, fifteen = avg.Load1, avg.Load5, avg.Load15
	}
	r.SysLoadState = &model.SysLoadState{
		OneLoad: round(one), FiveLoad: round(five), FifteenLoad: round(fifteen), Users: users,
	}

	// 磁盘分区
	parts, err := disk.Partitions(false)
	if err != nil {
		log.Printf("获取磁盘分区失败: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range parts {
		if seen[p.Mountpoint] {
			continue
		}
		if isSkip(p.Mountpoint, cfg.SkipDisk) {
			continue
		}
		usage, err := disk.Usage(p.Mountpoint)
		if err != nil || usage.Total == 0 {
			continue
		}
		seen[p.Mountpoint] = true
		totalGB := float64(usage.Total) / 1024 / 1024 / 1024
		usedGB := float64(usage.Used) / 1024 / 1024 / 1024
		freeGB := float64(usage.Free) / 1024 / 1024 / 1024
		r.DeskStates = append(r.DeskStates, model.DeskState{
			FileSystem: p.Mountpoint,
			Size:       fmt.Sprintf("%.1fG", totalGB),
			Used:       fmt.Sprintf("%.1fG", usedGB),
			Avail:      fmt.Sprintf("%.1fG", freeGB),
			UsePer:     fmt.Sprintf("%.1f", usage.UsedPercent),
		})
	}

	// 网络速率
	r.NetIoState = collectNet(interval, now)
	// 磁盘 IO 速率
	r.DiskIoState = CollectDiskIO(interval)
	// 进程列表（按 CPU 占用排序），供「进程监控 / 异常进程」使用
	r.Processes = collectProcesses(cfg.Hostname, topProcessCount)
	// 硬件与运行时扩展：CPU 温度、SMART、连接数、MAC
	r.HostExtra = CollectHardware(cfg.Hostname)
	return r, nil
}

// topProcessCount 上报的资源占用最高的进程数量。
const topProcessCount = 20

// collectProcesses 采集进程名、PID、CPU/内存占用与命令行。
func collectProcesses(hostname string, limit int) []model.HostProcess {
	procs, err := psprocess.Processes()
	if err != nil {
		return nil
	}
	type item struct {
		p   *psprocess.Process
		cpu float64
		mem float64
	}
	var items []item
	for _, p := range procs {
		name, err := p.Name()
		if err != nil || name == "" {
			continue
		}
		cpu, err := p.CPUPercent()
		if err != nil {
			cpu = 0
		}
		memPercent, err := p.MemoryPercent()
		if err != nil {
			memPercent = 0
		}
		if cpu > 0 || memPercent > 0 {
			items = append(items, item{p, cpu, float64(memPercent)})
		}
	}
	// 简单插入排序即可，进程数量有限
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].cpu > items[j-1].cpu; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	if limit > len(items) {
		limit = len(items)
	}
	out := make([]model.HostProcess, 0, limit)
	for _, it := range items[:limit] {
		name, _ := it.p.Name()
		cmd, err := it.p.Cmdline()
		if err != nil || len(cmd) > 200 {
			cmd = ""
		}
		out = append(out, model.HostProcess{
			Hostname: hostname, PID: it.p.Pid, Name: name,
			CPUPer: round(it.cpu), MemPer: round(float64(it.mem)), Cmdline: cmd,
		})
	}
	return out
}

func isSkip(mount string, skip []string) bool {
	for _, s := range skip {
		if strings.EqualFold(mount, s) {
			return true
		}
	}
	return false
}

// collectNet 按两次采样的差值计算每秒速率（KB/s）与包速（个/s）。
func collectNet(interval time.Duration, now time.Time) *model.NetIoState {
	counters, err := psnet.IOCounters(false)
	if err != nil || len(counters) == 0 {
		return nil
	}
	c := counters[0]
	state := &model.NetIoState{}
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	secs := interval.Seconds()
	if snapshot.hasPrev && secs > 0 {
		elapsed := now.Sub(snapshot.time).Seconds()
		if elapsed <= 0 {
			elapsed = secs
		}
		rx := delta(c.BytesRecv, snapshot.recv) / elapsed / 1024
		tx := delta(c.BytesSent, snapshot.sent) / elapsed / 1024
		state.Rxbyt = fmt.Sprintf("%.1f", rx)
		state.Txbyt = fmt.Sprintf("%.1f", tx)
		state.Rxpck = fmt.Sprintf("%.0f", delta(c.PacketsRecv, snapshot.packRecv)/elapsed)
		state.Txpck = fmt.Sprintf("%.0f", delta(c.PacketsSent, snapshot.packSent)/elapsed)
	} else {
		state.Rxbyt, state.Txbyt, state.Rxpck, state.Txpck = "0", "0", "0", "0"
	}
	snapshot.recv, snapshot.sent = c.BytesRecv, c.BytesSent
	snapshot.packRecv, snapshot.packSent = c.PacketsRecv, c.PacketsSent
	snapshot.time = now
	snapshot.hasPrev = true
	return state
}

func delta(cur, prev uint64) float64 {
	if cur < prev {
		return 0 // 网卡计数重置
	}
	return float64(cur - prev)
}

// localIP 获取本机对外通信使用的 IP。
func localIP() string {
	conn, err := net.DialTimeout("udp", "8.8.8.8:80", time.Second)
	if err != nil {
		if hn, err := os.Hostname(); err == nil {
			if addrs, err := net.LookupIP(hn); err == nil && len(addrs) > 0 {
				return addrs[0].String()
			}
		}
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func round(v float64) float64 {
	if v < 0 {
		return 0
	}
	return float64(int64(v*100+0.5)) / 100
}

// loginUserNames 返回当前登录本机的用户名列表（逗号分隔）。
// 优先使用系统会话接口，失败时回退到 who / query user 命令输出。
func loginUserNames() string {
	var names []string
	seen := map[string]bool{}
	if us, err := host.Users(); err == nil {
		for _, u := range us {
			name := strings.TrimSpace(u.User)
			if name != "" && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		for _, n := range strings.Split(HostLogins(), ",") {
			if n = strings.TrimSpace(n); n != "" && !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	return strings.Join(names, ",")
}
