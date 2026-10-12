package agent

import (
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sangxi/OpsMonitor/internal/model"
)

// sysEventSeen 记录已上报过的系统事件，避免同一事件被反复推送到服务端。
// key = 主机|来源|级别|时间，value = 上报时刻。
var sysEventSeen = map[string]time.Time{}

// sysEventWindow 去重时间窗：同一事件在该时间窗内只上报一次。
const sysEventWindow = 30 * time.Minute

// attachSysEvents 把系统事件挂到上报数据上（仅 Windows 生效）。
// 非 Windows 直接返回，保持上报协议体积不变。
func attachSysEvents(report *model.Report) {
	if report == nil || runtime.GOOS != "windows" {
		return
	}
	events := CollectSysEvents(20)
	if len(events) == 0 {
		return
	}
	host := sysEventHost()
	now := time.Now()
	// 顺手清掉过期键，避免长时间运行后 map 无限增长
	for k, t := range sysEventSeen {
		if now.Sub(t) > sysEventWindow*4 {
			delete(sysEventSeen, k)
		}
	}
	fresh := make([]model.SysEvent, 0, len(events))
	for _, e := range events {
		key := sysEventKey(host, e)
		if last, ok := sysEventSeen[key]; ok && now.Sub(last) < sysEventWindow {
			continue
		}
		sysEventSeen[key] = now
		if strings.TrimSpace(e.Content) == "" {
			continue
		}
		fresh = append(fresh, e)
	}
	if len(fresh) > 0 {
		report.SysEvents = fresh
	}
}

// sysEventKey 去重键。必须带上 EventID：同一秒内常有多个事件 ID 不同、
// 但来源与级别相同的事件（如 DCOM 10016 会连着来好几条），
// 只按 时间+来源+级别 会把它们当成同一条，漏掉真实告警。
func sysEventKey(host string, e model.SysEvent) string {
	return host + "|" + e.Source + "|" + e.Level + "|" + strconv.Itoa(e.EventID) + "|" + e.Time
}
