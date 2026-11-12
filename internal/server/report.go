package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sangxi/gridperch/internal/model"
	"github.com/sangxi/gridperch/internal/notify"
)

/*
巡检报告推送。

设计要点：
  - 四种频率（每小时 / 每日 / 每周 / 每月）各自用「时间桶」做幂等标记，
    重启后不会重复补发，也不会因为服务停了半小时就彻底漏掉当期报告；
  - 每小时与每日只报关键数字（总数 / 在线 / 离线 / 离线时长），
    每周与每月才展开系统分布、目录分布、告警统计与巡检评分；
  - 发送失败最多每 5 分钟重试一次，避免机器人地址挂掉时每 30 秒刷一遍日志。
*/

const (
	reportKindHour  = "hour"
	reportKindDay   = "day"
	reportKindWeek  = "week"
	reportKindMonth = "month"
)

// reportKindLabels 报告名称，用于标题、日志与前端提示。
var reportKindLabels = map[string]string{
	reportKindHour:  "每小时巡检速报",
	reportKindDay:   "每日巡检报告",
	reportKindWeek:  "每周巡检报告",
	reportKindMonth: "每月巡检报告",
}

// reportDefaultClock 未配置发送时刻时的默认值（早上 8:30）。
const reportDefaultClock = "08:30"

// reportRetryAt 记录各频率上次发送失败的时间，只在 maintenance 单个协程里读写。
var reportRetryAt = map[string]time.Time{}

// reportSection 报告的一个小节，纯文本 / Markdown / HTML 三种渲染共用同一份内容。
type reportSection struct {
	Title string
	Lines []string
}

// reportContent 一份渲染完成的报告。
type reportContent struct {
	Kind     string
	Title    string
	Markdown string
	Text     string
	HTML     string
}

// normalizeReportKind 把任意输入收敛到四种合法频率，非法值按「每日」处理。
func normalizeReportKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case reportKindHour:
		return reportKindHour
	case reportKindWeek:
		return reportKindWeek
	case reportKindMonth:
		return reportKindMonth
	default:
		return reportKindDay
	}
}

// reportKindsOf 解析「hour,day,week,month」这样的勾选串，保持固定顺序并去重。
func reportKindsOf(raw string) []string {
	want := map[string]bool{}
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			want[p] = true
		}
	}
	var out []string
	for _, k := range []string{reportKindHour, reportKindDay, reportKindWeek, reportKindMonth} {
		if want[k] {
			out = append(out, k)
		}
	}
	return out
}

// reportLastKey 返回某个频率的发送标记在 settings 里的键名。
func reportLastKey(kind string) string {
	switch kind {
	case reportKindHour:
		return "reportLastHour"
	case reportKindWeek:
		return "reportLastWeek"
	case reportKindMonth:
		return "reportLastMonth"
	default:
		return "reportLastDay"
	}
}

// parseClock 解析 HH:MM，非法时返回默认 08:30。
func parseClock(raw string) (hour, minute int) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 {
		return 8, 30
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 8, 30
	}
	return h, m
}

// reportBucket 计算当前时间所属的时间桶，以及本期的「应发时刻」。
//
// 时间桶用于去重（例如 "2026111210" 表示 11 月 12 日 10 点这一小时）；
// 应发时刻用于判断「还没到点就先别发」，日/周/月都按配置的时刻触发。
func reportBucket(kind string, now time.Time, hour, minute int) (bucket string, due time.Time) {
	loc := now.Location()
	switch kind {
	case reportKindHour:
		start := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, loc)
		return now.Format("2006010215"), start
	case reportKindWeek:
		offset := (int(now.Weekday()) + 6) % 7 // 周一 = 0
		monday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -offset)
		year, week := now.ISOWeek()
		return fmt.Sprintf("%dW%02d", year, week), monday.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
	case reportKindMonth:
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		return now.Format("200601"), first.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
	default:
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		return now.Format("20060102"), today.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
	}
}

// reportWindowStart 返回统计窗口起点：小时报看最近一小时，日报看今天 0 点，
// 周报看本周一 0 点，月报看本月 1 日 0 点。
func reportWindowStart(kind string, now time.Time) time.Time {
	loc := now.Location()
	switch kind {
	case reportKindHour:
		return now.Add(-time.Hour)
	case reportKindWeek:
		offset := (int(now.Weekday()) + 6) % 7
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -offset)
	case reportKindMonth:
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	default:
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	}
}

// offlineDuration 把「最后上报时间」换算成「已离线多久」，供报告正文展示。
func offlineDuration(lastSeen string, now time.Time) string {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", lastSeen, time.Local)
	if err != nil {
		return "时长未知"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d 天 %d 小时", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d 小时 %d 分钟", hours, mins)
	default:
		return fmt.Sprintf("%d 分钟", mins)
	}
}

// hostDisplay 统一「主机名（IP）」写法，IP 为空时只显示主机名。
func hostDisplay(h model.Host) string {
	if strings.TrimSpace(h.IP) == "" {
		return h.Hostname
	}
	return fmt.Sprintf("%s（%s）", h.Hostname, h.IP)
}

// osLabelCN 把系统代号翻译成报告里用的中文标签。
func osLabelCN(osName string) string {
	switch osName {
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	case "darwin":
		return "macOS"
	case "unix", "freebsd", "openbsd", "netbsd", "solaris", "aix":
		// 报告里按大类归并，免得出现「freebsd 1 ｜ openbsd 1」这种碎行
		return "Unix"
	case "":
		return "未知"
	default:
		return osName
	}
}

// buildInspectionReport 汇总主机、目录、告警与资源数据，生成当期报告。
// 小时 / 日报走精简版，周 / 月报走详细版。
func (s *Server) buildInspectionReport(kind string) reportContent {
	settings, _ := s.store.Settings()
	now := time.Now()
	prefix := strings.TrimSpace(settings["alertPrefix"])
	if prefix == "" {
		prefix = "[GridPerch]"
	}
	label := reportKindLabels[kind]

	hosts, err := s.store.ListHosts()
	if err != nil {
		hosts = nil
	}
	total, online := len(hosts), 0
	var offline []model.Host
	osCount := map[string]int{}
	catCount := map[string]int{}
	for _, h := range hosts {
		if h.State == "1" {
			online++
		} else {
			offline = append(offline, h)
		}
		osCount[osLabelCN(h.OS)]++
		if h.CatalogName == "" {
			catCount["未分组"]++
		} else {
			catCount[h.CatalogName]++
		}
	}

	// 告警：窗口内新增（流量）按级别统计
	windowStart := reportWindowStart(kind, now)
	byLevel, _ := s.store.AlertLevelCounts(windowStart.Format("2006-01-02 15:04:05"))
	newAlerts := byLevel["critical"] + byLevel["warning"] + byLevel["info"]

	secs := make([]reportSection, 0, 8)
	secs = append(secs, reportSection{
		Title: "主机概况",
		Lines: []string{
			fmt.Sprintf("主机总数 %d 台，在线 %d 台，离线 %d 台", total, online, len(offline)),
			fmt.Sprintf("统计时间 %s", now.Format("2006-01-02 15:04")),
		},
	})

	// 离线明细：小时/日报也要给，因为「离线多久」比「离线几台」更能说明问题
	offlineLines := make([]string, 0, len(offline)+1)
	if len(offline) == 0 {
		offlineLines = append(offlineLines, "全部主机在线")
	} else {
		sort.Slice(offline, func(i, j int) bool { return offline[i].LastSeen < offline[j].LastSeen })
		for _, h := range offline {
			offlineLines = append(offlineLines, fmt.Sprintf("%s 已离线 %s（最后上报 %s）",
				hostDisplay(h), offlineDuration(h.LastSeen, now), blankDash(h.LastSeen)))
		}
	}
	secs = append(secs, reportSection{Title: "离线主机", Lines: offlineLines})

	alertLine := fmt.Sprintf("本次统计周期新增告警 %d 条（严重 %d / 警告 %d / 提示 %d）",
		newAlerts, byLevel["critical"], byLevel["warning"], byLevel["info"])
	if kind == reportKindHour || kind == reportKindDay {
		secs = append(secs, reportSection{Title: "告警", Lines: []string{alertLine}})
	} else {
		detail := []string{alertLine}
		if rep, err := s.store.BuildInspectionReport(); err == nil {
			detail = append(detail,
				fmt.Sprintf("当前未处理告警 %d 条，监控项 %d 个（异常 %d 个）",
					rep.AlertTotal, rep.TaskTotal, rep.TaskAbnormal),
				fmt.Sprintf("综合巡检评分 %d 分", rep.Score))
			for _, sg := range rep.Suggestions {
				detail = append(detail, "建议："+sg)
			}
		}
		secs = append(secs, reportSection{Title: "告警与巡检结论", Lines: detail})

		secs = append(secs, reportSection{Title: "系统分布", Lines: []string{countLine(osCount)}})
		secs = append(secs, reportSection{Title: "目录分布", Lines: []string{countLine(catCount)}})
		if tops := topHosts(hosts, 5); len(tops) > 0 {
			secs = append(secs, reportSection{Title: "资源占用 TOP5", Lines: tops})
		}
	}

	title := fmt.Sprintf("%s %s %s", prefix, label, now.Format("2006-01-02 15:04"))
	return reportContent{
		Kind:     kind,
		Title:    title,
		Markdown: reportMarkdown(title, secs),
		Text:     reportText(title, secs),
		HTML:     reportHTML(title, secs),
	}
}

// blankDash 空字符串显示为「-」，避免报告里出现「最后上报 」这种断句。
func blankDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

// countLine 渲染「Windows 33 ｜ Linux 10」这样的分布行，按数量倒序。
func countLine(m map[string]int) string {
	type kv struct {
		K string
		V int
	}
	list := make([]kv, 0, len(m))
	for k, v := range m {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].V != list[j].V {
			return list[i].V > list[j].V
		}
		return list[i].K < list[j].K
	})
	parts := make([]string, 0, len(list))
	for _, it := range list {
		parts = append(parts, fmt.Sprintf("%s %d", it.K, it.V))
	}
	return strings.Join(parts, " ｜ ")
}

// topHosts 取 CPU / 内存占用最高的若干台在线主机。
func topHosts(hosts []model.Host, n int) []string {
	var online []model.Host
	for _, h := range hosts {
		if h.State == "1" {
			online = append(online, h)
		}
	}
	if len(online) == 0 {
		return nil
	}
	byCPU := append([]model.Host(nil), online...)
	sort.Slice(byCPU, func(i, j int) bool { return byCPU[i].CpuPer > byCPU[j].CpuPer })
	byMem := append([]model.Host(nil), online...)
	sort.Slice(byMem, func(i, j int) bool { return byMem[i].MemPer > byMem[j].MemPer })

	cpu := make([]string, 0, n)
	mem := make([]string, 0, n)
	for i := 0; i < n && i < len(byCPU); i++ {
		if byCPU[i].CpuPer <= 0 {
			break
		}
		cpu = append(cpu, fmt.Sprintf("%s %.1f%%", byCPU[i].Hostname, byCPU[i].CpuPer))
	}
	for i := 0; i < n && i < len(byMem); i++ {
		if byMem[i].MemPer <= 0 {
			break
		}
		mem = append(mem, fmt.Sprintf("%s %.1f%%", byMem[i].Hostname, byMem[i].MemPer))
	}
	out := make([]string, 0, 2)
	if len(cpu) > 0 {
		out = append(out, "CPU："+strings.Join(cpu, " ｜ "))
	}
	if len(mem) > 0 {
		out = append(out, "内存："+strings.Join(mem, " ｜ "))
	}
	return out
}

// reportMarkdown 渲染机器人用的 Markdown 正文。
func reportMarkdown(title string, secs []reportSection) string {
	var sb strings.Builder
	sb.WriteString("### " + title + "\n\n")
	for _, s := range secs {
		sb.WriteString("**" + s.Title + "**\n")
		for _, l := range s.Lines {
			sb.WriteString("> " + l + "\n")
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

// reportText 渲染不支持 Markdown 的通道（飞书文本 / Slack）用的纯文本。
func reportText(title string, secs []reportSection) string {
	var sb strings.Builder
	sb.WriteString(title + "\n")
	sb.WriteString(strings.Repeat("=", 28) + "\n")
	for _, s := range secs {
		sb.WriteString("\n【" + s.Title + "】\n")
		for _, l := range s.Lines {
			sb.WriteString("  " + l + "\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

// reportHTML 渲染邮件正文，保持和告警邮件一致的朴素样式。
func reportHTML(title string, secs []reportSection) string {
	var sb strings.Builder
	sb.WriteString(`<div style="font-family:Microsoft YaHei,Arial;font-size:14px;color:#222">`)
	sb.WriteString(`<h2 style="font-size:18px;margin:0 0 12px">` + html.EscapeString(title) + `</h2>`)
	for _, s := range secs {
		sb.WriteString(`<h3 style="font-size:15px;color:#374151;margin:18px 0 6px">` + html.EscapeString(s.Title) + `</h3>`)
		sb.WriteString(`<div style="line-height:1.8;color:#374151">`)
		for _, l := range s.Lines {
			sb.WriteString("<div>" + html.EscapeString(l) + "</div>")
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`</div>`)
	return sb.String()
}

// reportChannels 解析勾选的推送通道（webhook / mail）。
func reportChannels(settings map[string]string) map[string]bool {
	out := map[string]bool{}
	for _, p := range strings.Split(settings["reportPushChannels"], ",") {
		if p = strings.TrimSpace(p); p != "" {
			out[p] = true
		}
	}
	return out
}

// sendInspectionReport 生成并投递一份报告。
//
// strict=true 用于「立即发送测试」：没有任何可用通道时直接报错，
// 让用户知道要先配通道；定时任务走 strict=false，静默跳过即可，
// 否则每到点都会往运行日志里刷一条错误。
func (s *Server) sendInspectionReport(kind string, settings map[string]string, strict bool) error {
	content := s.buildInspectionReport(kind)
	channels := reportChannels(settings)
	var errs []error
	sent := 0

	if channels["webhook"] {
		hooks, err := s.store.ListWebhooks()
		if err != nil {
			errs = append(errs, err)
		}
		for _, h := range hooks {
			if !h.Enabled {
				continue
			}
			if err := notify.SendWebhookText(s.webhookConfig(h), content.Title, content.Markdown, content.Text); err != nil {
				errs = append(errs, fmt.Errorf("机器人「%s」: %w", h.Name, err))
				_ = s.store.MarkWebhookResult(h.ID, "fail", err.Error())
				s.store.AppendMailLog(model.MailLog{Kind: "report", Subject: content.Title, Recipients: h.Name,
					ItemCount: 1, Status: "fail", Error: err.Error()})
				continue
			}
			sent++
			_ = s.store.MarkWebhookResult(h.ID, "ok", "")
			s.store.AppendMailLog(model.MailLog{Kind: "report", Subject: content.Title, Recipients: h.Name,
				ItemCount: 1, Status: "ok"})
		}
	}

	if channels["mail"] {
		cfg := s.mailConfigFrom(settings)
		if cfg.Enabled() {
			if err := notify.SendHTML(cfg, content.Title, content.HTML); err != nil {
				errs = append(errs, fmt.Errorf("邮件: %w", err))
				s.store.AppendMailLog(model.MailLog{Kind: "report", Subject: content.Title, Recipients: cfg.To,
					ItemCount: 1, Status: "fail", Error: err.Error()})
			} else {
				sent++
				s.store.AppendMailLog(model.MailLog{Kind: "report", Subject: content.Title, Recipients: cfg.To,
					ItemCount: 1, Status: "ok"})
			}
		} else if strict {
			errs = append(errs, errors.New("邮件推送未配置完整（SMTP 服务器 / 发件人 / 收件人）"))
		}
	}

	if sent == 0 && len(errs) == 0 {
		if strict {
			return errors.New("还没有可用的推送通道：请在「巡检报告」里勾选机器人或邮件，并确认对应通道已启用")
		}
		return nil
	}
	if sent > 0 {
		// 至少有一个通道成功就算送达，失败通道的错误只记日志，不阻塞下一期
		if len(errs) > 0 {
			log.Printf("%s 部分通道推送失败: %v", reportKindLabels[kind], errors.Join(errs...))
		}
		return nil
	}
	return errors.Join(errs...)
}

// pushInspectionReports 由 maintenance 周期调用，按配置的频率与时刻发报告。
func (s *Server) pushInspectionReports() {
	settings, err := s.store.Settings()
	if err != nil {
		return
	}
	if settings["reportPushEnabled"] != "true" {
		return
	}
	kinds := reportKindsOf(settings["reportPushKinds"])
	if len(kinds) == 0 {
		return
	}
	now := time.Now()
	hour, minute := parseClock(settings["reportPushTime"])
	for _, kind := range kinds {
		bucket, due := reportBucket(kind, now, hour, minute)
		if now.Before(due) {
			continue
		}
		key := reportLastKey(kind)
		if settings[key] == bucket {
			continue
		}
		if t, ok := reportRetryAt[kind]; ok && now.Sub(t) < 5*time.Minute {
			continue
		}
		if err := s.sendInspectionReport(kind, settings, false); err != nil {
			reportRetryAt[kind] = now
			log.Printf("推送%s失败: %v", reportKindLabels[kind], err)
			continue
		}
		delete(reportRetryAt, kind)
		if err := s.store.SaveSettings(map[string]string{key: bucket}); err != nil {
			log.Printf("记录巡检报告发送标记失败: %v", err)
			continue
		}
		settings[key] = bucket
	}
}

// autoAssignCatalogByOS 在开启「新主机自动按系统归组」时，把还没有归属的主机归类。
func (s *Server) autoAssignCatalogByOS() {
	settings, err := s.store.Settings()
	if err != nil || settings["catalogAutoByOs"] != "true" {
		return
	}
	res, err := s.store.AutoAssignCatalogByOS(false)
	if err != nil {
		log.Printf("按系统自动归组失败: %v", err)
		return
	}
	if res.HostsAssigned > 0 {
		log.Printf("已按操作系统自动归组 %d 台主机", res.HostsAssigned)
	}
}

// handleReportPreview 预览一份报告正文，不发送。
func (s *Server) handleReportPreview(w http.ResponseWriter, r *http.Request) {
	kind := normalizeReportKind(r.URL.Query().Get("kind"))
	c := s.buildInspectionReport(kind)
	writeJSON(w, map[string]any{
		"ok": true, "kind": kind, "label": reportKindLabels[kind],
		"title": c.Title, "markdown": c.Markdown, "text": c.Text,
	})
}

// handleReportTest 立即发送一份报告，用于确认通道是否配好。
func (s *Server) handleReportTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
		return
	}
	var req struct {
		Kind string `json:"kind"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
	kind := normalizeReportKind(req.Kind)
	settings, err := s.store.Settings()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	if err := s.sendInspectionReport(kind, settings, true); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "发送失败：" + err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "msg": "已发送" + reportKindLabels[kind]})
}
