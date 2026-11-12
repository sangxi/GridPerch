package server

// 服务端运行日志：把 log 包输出在写 stderr 的同时留一份到内存环形缓冲，
// 供控制台「运行日志」页查询，免去 SSH 上机器翻 journalctl。
//
// 两条数据来源：
//   1) 本次进程启动后的实时日志：logSink 捕获，固定容量，服务重启即清空；
//   2) systemd 历史日志：只在传了 since 且当前是 Linux 时执行 journalctl 补取。
//      exec.Command 不经 shell，since 先用正则校验，避免把用户输入拼进参数。
// 两份数据按「时间 + 正文」去重后合并，接口输出的永远是最新在前。

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// logRingCap 约可覆盖数小时内中等频率的输出；超出后丢弃最旧的。
const logRingCap = 5000

type logEntry struct {
	Time string `json:"time"`
	Kind string `json:"kind"`
	IP   string `json:"ip"`
	Text string `json:"text"`
}

type ipStat struct {
	IP      string `json:"ip"`
	Count   int    `json:"count"`
	Refused int    `json:"refused"`
	Last    string `json:"last"`
}

var (
	logMu      sync.RWMutex
	logRing    = make([]logEntry, 0, logRingCap)
	logIPRe    = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	logTimeRe  = regexp.MustCompile(`^(\d{4})/(\d{2})/(\d{2}) (\d{2}:\d{2}:\d{2})`)
	logSinceRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}([ T]\d{2}:\d{2}(:\d{2})?)?$`)
)

// InstallLogCapture 在服务端进程入口调用一次，之后所有 log.Printf 都会进环形缓冲。
// stderr 仍然保留：systemd / Docker / 前台调试的日志行为不受影响。
func InstallLogCapture() {
	log.SetFlags(log.LstdFlags)
	log.SetOutput(io.MultiWriter(os.Stderr, logSink{}))
}

type logSink struct{}

func (logSink) Write(p []byte) (int, error) {
	n := len(p)
	sc := bufio.NewScanner(bytes.NewReader(p))
	sc.Buffer(make([]byte, 4096), 256<<10) // panic 堆栈可能很长，放宽单行上限
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		e := newLogEntry(line)
		logMu.Lock()
		logRing = append(logRing, e)
		if len(logRing) > logRingCap {
			logRing = append([]logEntry(nil), logRing[len(logRing)-logRingCap:]...)
		}
		logMu.Unlock()
	}
	return n, nil
}

func newLogEntry(line string) logEntry {
	t := ""
	if m := logTimeRe.FindStringSubmatch(line); len(m) == 5 {
		t = m[1] + "-" + m[2] + "-" + m[3] + " " + m[4]
	}
	if t == "" {
		t = time.Now().Format("2006-01-02 15:04:05")
	}
	return logEntry{
		Time: t,
		Text: line,
		Kind: classifyLog(line),
		IP:   logIPRe.FindString(line),
	}
}

// classifyLog 把排障时最关心的几类日志分出来，顺序不能随意调：
// 「token 校验失败」里也含「失败」，必须先判 refused，否则会被归到 error。
func classifyLog(s string) string {
	switch {
	case strings.Contains(s, "token 校验失败"), strings.Contains(s, "拒绝上报"),
		strings.Contains(s, "鉴权失败"), strings.Contains(s, "密钥校验失败"):
		return "refused"
	case strings.Contains(s, "升级"), strings.Contains(s, "回执授权系统"),
		strings.Contains(s, "autoUpdate"), strings.Contains(s, "版本"):
		return "upgrade"
	case strings.Contains(s, "告警"), strings.Contains(s, "机器人"), strings.Contains(s, "推送"):
		return "alert"
	case strings.Contains(s, "失败"), strings.Contains(s, "错误"), strings.Contains(s, "警告"),
		strings.Contains(s, "panic"), strings.Contains(s, "超时"):
		return "error"
	default:
		return "info"
	}
}

func snapshotLogs() []logEntry {
	logMu.RLock()
	defer logMu.RUnlock()
	return append([]logEntry(nil), logRing...)
}

// journalLogs 读取 systemd 历史。服务名取 gridperch（线上单元名）；
// 其它部署若单元名不同，journalctl 取不到时返回空，实时日志仍可用。
//
// 不用 journalctl -n：加 --since 时 -n 取的是「起始时间之后最早的一批」，
// 选「今天起」反而看不到最近几条，与排障预期相反。这里读完整区间后只保留
// 最近 max 条，并用 30 秒缓存兜住页面的自动刷新。
var (
	journalMu    sync.Mutex
	journalCache struct {
		since   string
		at      time.Time
		entries []logEntry
	}
)

func journalLogs(since string, max int) []logEntry {
	if runtime.GOOS != "linux" {
		return nil
	}
	journalMu.Lock()
	defer journalMu.Unlock()
	if journalCache.since == since && time.Since(journalCache.at) < 30*time.Second {
		return journalCache.entries
	}
	args := []string{"-u", "gridperch", "--no-pager", "-o", "cat"}
	if since != "" {
		args = append(args, "--since", since)
	}
	out, err := exec.Command("journalctl", args...).Output()
	if err != nil {
		return nil
	}
	res := make([]logEntry, 0, 4096)
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 4096), 256<<10)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) != "" {
			res = append(res, newLogEntry(line))
		}
	}
	if len(res) > max {
		res = res[len(res)-max:]
	}
	journalCache = struct {
		since   string
		at      time.Time
		entries []logEntry
	}{since, time.Now(), res}
	return res
}

func mergeLogs(a, b []logEntry) []logEntry {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]logEntry, 0, len(a)+len(b))
	both := append(append([]logEntry(nil), a...), b...)
	for _, e := range both {
		key := e.Time + "\x00" + e.Text
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out
}

// handleServerLogs 只读，管理员与运维可见；viewer 菜单里不开放。
func (s *Server) handleServerLogs(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	since := strings.TrimSpace(r.URL.Query().Get("since"))
	limit := 300
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	if limit > 2000 {
		limit = 2000
	}

	all := snapshotLogs()
	journalOK := false
	if since != "" && logSinceRe.MatchString(since) {
		if hist := journalLogs(since, 20000); len(hist) > 0 {
			all = mergeLogs(all, hist)
			journalOK = true
		}
	}

	stats := map[string]int{"all": len(all)}
	for _, e := range all {
		stats[e.Kind]++
	}

	byIP := map[string]*ipStat{}
	for _, e := range all {
		if e.IP == "" {
			continue
		}
		st := byIP[e.IP]
		if st == nil {
			st = &ipStat{IP: e.IP}
			byIP[e.IP] = st
		}
		st.Count++
		if e.Kind == "refused" {
			st.Refused++
		}
		if e.Time > st.Last {
			st.Last = e.Time
		}
	}
	// 用值切片：writeJSON 的 normalizeJSON 会反射重建切片，指针元素在其中
	// 被解引用成值后回写会触发 reflect.Set panic。
	ips := make([]ipStat, 0, len(byIP))
	for _, v := range byIP {
		ips = append(ips, *v)
	}
	sort.Slice(ips, func(i, j int) bool { return ips[i].Count > ips[j].Count })
	if len(ips) > 30 {
		ips = ips[:30]
	}

	list := make([]logEntry, 0, limit)
	for i := len(all) - 1; i >= 0 && len(list) < limit; i-- {
		e := all[i]
		if kind != "" && kind != "all" && e.Kind != kind {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Text), q) && !strings.Contains(strings.ToLower(e.IP), q) {
			continue
		}
		list = append(list, e)
	}

	note := "实时日志存于内存，服务重启后清空；选择起始时间可补读 systemd 历史日志（服务名 gridperch，最多保留最近 20000 条）。"
	if since != "" && !journalOK {
		// 服务端常以普通用户（如 gridperch）运行，默认无权读 journal。
		// 把该用户加入 systemd-journal 组并重启服务即可开启历史查询。
		note = "未能读取 systemd 历史日志：请把服务端运行用户加入 systemd-journal 组后重启服务；当前只显示本次启动后的实时日志。"
	}
	writeJSON(w, map[string]any{
		"ok":       true,
		"total":    len(all),
		"stats":    stats,
		"list":     list,
		"ips":      ips,
		"capacity": logRingCap,
		"journal":  journalOK,
		"note":     note,
	})
}
