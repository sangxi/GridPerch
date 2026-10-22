package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sangxi/gridperch/internal/config"
)

// RemoteConfig 服务端下发的采集端运行配置。
//
// 安装命令只带 -server / -token-hash / -name 三个必要参数，
// 采集间隔、执行指令开关、跳过磁盘、更新策略都由平台统一下发并热更新。
type RemoteConfig struct {
	Interval       int      `json:"interval"`       // 指标上报间隔（秒）
	AllowCommand   bool     `json:"allowCommand"`   // 是否允许执行平台下发的指令
	SkipDisk       []string `json:"skipDisk"`       // 跳过采集的挂载点关键字
	UpdateMode     string   `json:"updateMode"`     // auto / check / off
	UpdateInterval int      `json:"updateInterval"` // 版本检查间隔（分钟）
	Refresh        int      `json:"refresh"`        // 配置刷新间隔（秒）
	Revision       string   `json:"revision"`       // 配置指纹
	Source         string   `json:"source"`         // global / override
}

// Runtime 承载运行期可热更新的配置。
//
// 各循环每轮取一次快照，所以平台上改完配置后，采集端在下一个刷新周期
// 就会生效，既不用登录主机改 systemd 单元，也不用重启进程。
type Runtime struct {
	mu       sync.RWMutex
	cfg      config.Agent
	revision string
	synced   bool // 是否已成功拉到过服务端配置
}

// NewRuntime 用本地启动参数构造运行时配置。
func NewRuntime(cfg config.Agent) *Runtime {
	return &Runtime{cfg: cfg}
}

// Snapshot 返回当前配置的副本，调用方可随意读取。
func (r *Runtime) Snapshot() config.Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

// Hostname 返回本机在平台上的显示名。
func (r *Runtime) Hostname() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.Hostname
}

// Synced 表示是否已经成功拉取过服务端配置。
func (r *Runtime) Synced() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.synced
}

// apply 应用服务端配置，返回发生变化的字段描述与是否为首次生效。
func (r *Runtime) apply(remote RemoteConfig) ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	first := !r.synced
	var changed []string
	if remote.Interval > 0 {
		d := time.Duration(remote.Interval) * time.Second
		if d < minReportInterval {
			d = minReportInterval
		}
		if d != r.cfg.Interval {
			changed = append(changed, fmt.Sprintf("上报间隔 %s → %s", r.cfg.Interval, d))
			r.cfg.Interval = d
		}
	}
	if remote.AllowCommand != r.cfg.AllowCommand {
		changed = append(changed, fmt.Sprintf("执行指令 %s → %s", onOff(r.cfg.AllowCommand), onOff(remote.AllowCommand)))
		r.cfg.AllowCommand = remote.AllowCommand
	}
	if newSkip := strings.Join(remote.SkipDisk, ","); newSkip != strings.Join(r.cfg.SkipDisk, ",") {
		changed = append(changed, fmt.Sprintf("跳过磁盘 [%s] → [%s]",
			strings.Join(r.cfg.SkipDisk, ","), newSkip))
		r.cfg.SkipDisk = remote.SkipDisk
	}
	if remote.UpdateMode != "" && remote.UpdateMode != r.cfg.UpdateMode {
		changed = append(changed, fmt.Sprintf("更新策略 %s → %s", r.cfg.UpdateMode, remote.UpdateMode))
		r.cfg.UpdateMode = remote.UpdateMode
	}
	if remote.UpdateInterval > 0 {
		d := time.Duration(remote.UpdateInterval) * time.Minute
		if d != r.cfg.UpdateInterval {
			changed = append(changed, fmt.Sprintf("版本检查间隔 %s → %s", r.cfg.UpdateInterval, d))
			r.cfg.UpdateInterval = d
		}
	}
	r.revision = remote.Revision
	r.synced = true
	return changed, first
}

// minReportInterval 上报间隔下限，防止误配成 0 把主机和服务端一起打爆。
const minReportInterval = 5 * time.Second

// defaultConfigRefresh 服务端未指定刷新间隔时的兜底值。
const defaultConfigRefresh = 5 * time.Minute

func onOff(b bool) string {
	if b {
		return "开启"
	}
	return "关闭"
}

// configLoop 周期拉取服务端配置并热应用。
//
// 拉取失败不清空已有配置：网络抖动或服务端重启期间，
// 采集端继续按最后一次成功下发的配置运行。
func configLoop(rt *Runtime, client *http.Client, base, wgToken string) {
	if rt.Snapshot().RemoteConfig == "off" {
		log.Printf("已关闭服务端配置下发（-remote-config off），仅使用本地启动参数")
		return
	}
	refresh := defaultConfigRefresh
	applyOnce := func() {
		remote, err := fetchRemoteConfig(client, base, wgToken, rt.Hostname())
		if err != nil {
			if !rt.Synced() {
				log.Printf("拉取服务端配置失败，暂用本地启动参数运行: %v", err)
			} else {
				log.Printf("拉取服务端配置失败，继续沿用上次配置: %v", err)
			}
			return
		}
		if d := time.Duration(remote.Refresh) * time.Second; d >= 30*time.Second {
			refresh = d
		}
		changed, first := rt.apply(remote)
		if first {
			log.Printf("已应用服务端配置（来源 %s，指纹 %s）：上报间隔 %s，执行指令 %s，更新策略 %s",
				sourceCN(remote.Source), remote.Revision, rt.Snapshot().Interval,
				onOff(remote.AllowCommand), remote.UpdateMode)
		} else if len(changed) > 0 {
			log.Printf("服务端配置已更新（来源 %s，指纹 %s）：%s",
				sourceCN(remote.Source), remote.Revision, strings.Join(changed, "；"))
		}
	}

	applyOnce()
	for {
		time.Sleep(refresh)
		applyOnce()
	}
}

// sourceCN 配置来源的中文说明。
func sourceCN(src string) string {
	if src == "override" {
		return "主机级配置"
	}
	return "全局默认"
}

// fetchRemoteConfig 向服务端拉取本机的运行配置。
func fetchRemoteConfig(client *http.Client, base, wgToken, hostname string) (RemoteConfig, error) {
	var out RemoteConfig
	url := fmt.Sprintf("%s/agent/config?wgToken=%s&hostname=%s", base, wgToken, urlEscape(hostname))
	resp, err := client.Get(url)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("服务端返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("配置解析失败: %w", err)
	}
	return out, nil
}
