package server

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/sangxi/strix/internal/model"
	"github.com/sangxi/strix/internal/store"
)

// checkAgentToken 校验 agent 侧请求的 token（GET 用查询参数，POST 用报文体）。
func (s *Server) checkAgentToken(queryToken string, bodyToken string) bool {
	return s.cfg.TokenOK(queryToken) || s.cfg.TokenOK(bodyToken)
}

// handleAgentTasks 下发分配给该主机的监控项。
func (s *Server) handleAgentTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if !s.checkAgentToken(r.URL.Query().Get("wgToken"), "") {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{"result": "error：token is invalidate"})
		return
	}
	host := r.URL.Query().Get("hostname")
	if host == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"result": "error：缺少 hostname"})
		return
	}
	host = s.resolveAgentHost(host)
	tasks, err := s.store.TasksForAgent(host)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"result": "error：" + err.Error()})
		return
	}
	if tasks == nil {
		tasks = []model.TaskAssignment{}
	}
	writeJSON(w, tasks)
}

// handleAgentMonitorResult 接收监控项检查结果。
func (s *Server) handleAgentMonitorResult(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeJSON(w, map[string]string{"result": "error：" + err.Error()})
		return
	}
	var batch model.MonitorResultBatch
	if err := json.Unmarshal(body, &batch); err != nil {
		writeJSON(w, map[string]string{"result": "error：" + err.Error()})
		return
	}
	if !s.checkAgentToken("", batch.WgToken) {
		writeJSON(w, map[string]string{"result": "error：token is invalidate"})
		return
	}
	if err := s.store.SaveMonitorResults(s.resolveAgentHost(batch.Hostname), batch.Results); err != nil {
		writeJSON(w, map[string]string{"result": "error：" + err.Error()})
		return
	}
	writeJSON(w, map[string]string{"result": "success"})
}

/* ---------------- 监控项管理 API ---------------- */

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tasks, err := s.store.ListTasks()
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, tasks)
	case http.MethodPost:
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var t model.MonitorTask
		if err := json.Unmarshal(body, &t); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败：" + err.Error()})
			return
		}
		if err := s.store.CreateTask(&t); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "id": t.ID})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "方法不支持"})
	}
}

func (s *Server) handleTaskItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodGet:
		t, err := s.store.GetTask(id)
		if err != nil {
			writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "监控项不存在"})
			return
		}
		writeJSON(w, t)
	case http.MethodPut, http.MethodPost:
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var t model.MonitorTask
		if err := json.Unmarshal(body, &t); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
			return
		}
		t.ID = id
		if err := s.store.UpdateTask(&t); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodDelete:
		if err := s.store.DeleteTask(id); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "方法不支持"})
	}
}

// handleMonitorStates 返回监控项最新状态，可按任务过滤。
func (s *Server) handleMonitorStates(w http.ResponseWriter, r *http.Request) {
	states, err := s.store.ListMonitorStates(r.URL.Query().Get("taskId"))
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, states)
}

// handleMonitorEvents 返回监控项状态变化流水。
func (s *Server) handleMonitorEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.store.MonitorEvents(limit)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, events)
}

// handleMetricOverview 返回全主机平均资源趋势，供仪表盘总览图表使用。
func (s *Server) handleMetricOverview(w http.ResponseWriter, r *http.Request) {
	minutes, _ := strconv.Atoi(r.URL.Query().Get("minutes"))
	series, err := s.store.MetricOverview(minutes)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, series)
}

// handleMonitorSummary 返回监控项健康度统计。
func (s *Server) handleMonitorSummary(w http.ResponseWriter, r *http.Request) {
	sum, err := s.store.SummaryOfMonitor()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, sum)
}

/* ---------------- 大屏数据 ---------------- */

// ScreenPayload 大屏所需的一次性数据包。
type ScreenPayload struct {
	Generated      string         `json:"generated"`
	HostTotal      int            `json:"hostTotal"`
	HostOnline     int            `json:"hostOnline"`
	HostOffline    int            `json:"hostOffline"`
	AlertActive    int            `json:"alertActive"`
	AvgCpu         float64        `json:"avgCpu"`
	AvgMem         float64        `json:"avgMem"`
	TopHosts       []model.Host   `json:"topHosts"`
	Alerts         []model.Alert  `json:"alerts"`
	MonitorSummary map[string]any `json:"monitorSummary"`
	TypeCount      map[string]int `json:"typeCount"`

	// 以下为做大屏细分补充的字段
	AvgDisk      float64 `json:"avgDisk"`
	AvgLoad      float64 `json:"avgLoad"`
	MaxCpuTemp   float64 `json:"maxCpuTemp"`
	AvgTemp      float64 `json:"avgTemp"`
	NetRxKB      float64 `json:"netRxKb"`
	NetTxKB      float64 `json:"netTxKb"`
	DiskReadKB   float64 `json:"diskReadKb"`
	DiskWriteKB  float64 `json:"diskWriteKb"`
	ReadIOPS     float64 `json:"readIops"`
	WriteIOPS    float64 `json:"writeIops"`
	ConnTotal    int     `json:"connTotal"`
	TotalProcess int     `json:"totalProcess"`
	SystemName   string  `json:"systemName"`
	// Trends 全主机平均资源趋势，供大屏折线图
	Trends map[string][]store.TrendPoint `json:"trends"`
	// Units 各趋势曲线的单位
	Units map[string]string `json:"units"`
	// Runtimes 逐主机运行时指标
	Runtimes []store.HostRuntime `json:"runtimes"`
	// AlertStats 近 7 天告警按级别 / 类型分布
	AlertStats []store.AlertStat `json:"alertStats"`
	// AlertDaily 近 7 天每日告警数
	AlertDaily []store.TrendPoint `json:"alertDaily"`
	// Catalogs 按主机目录分组统计
	Catalogs []store.CatalogStat `json:"catalogs"`
	// SmartHealth 硬盘 SMART 分布
	SmartHealth map[string]int `json:"smartHealth"`
	// TopDisk / TopNet 磁盘与网络排行
	TopDisk []store.RankItem `json:"topDisk"`
	TopNet  []store.RankItem `json:"topNet"`
	// TaskRunnerCount 执行端分布
	TaskRunnerCount map[string]int `json:"taskRunnerCount"`
}

// handleScreen 返回大屏聚合数据。
//
// 一次请求返回大屏所需的全部指标，避免前端为了拼一张图发七八个接口。
// 各子查询之间没有写操作，任一子查询失败只影响对应字段，其余照常返回。
func (s *Server) handleScreen(w http.ResponseWriter, r *http.Request) {
	dash, err := s.store.Dashboard()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	sum, err := s.store.SummaryOfMonitor()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	tasks, _ := s.store.ListTasks()
	typeCount := map[string]int{}
	enabled := 0
	for _, t := range tasks {
		if !t.Enabled {
			continue
		}
		enabled++
		typeCount[t.Type]++
	}

	minutes, _ := strconv.Atoi(r.URL.Query().Get("minutes"))
	if minutes <= 0 {
		minutes = 180
	}

	payload := ScreenPayload{
		Generated:   timeNowString(),
		HostTotal:   dash.HostTotal,
		HostOnline:  dash.HostOnline,
		HostOffline: dash.HostOffline,
		AlertActive: dash.AlertActive,
		AvgCpu:      dash.AvgCpu,
		AvgMem:      dash.AvgMem,
		TopHosts:    dash.Hosts,
		Alerts:      dash.Alerts,
		MonitorSummary: map[string]any{
			"taskTotal": enabled,
			"taskBad":   sum.TaskBad,
			"items":     sum.Items,
			"badItems":  sum.BadItems,
		},
		TypeCount:  typeCount,
		Units:      map[string]string{},
		SystemName: "Strix",
	}

	// 逐主机运行时指标：磁盘 / 网络 / IO / 温度 / 连接数的汇总都从这里算
	rt, rtErr := s.store.ScreenRuntimes()
	if rtErr == nil {
		payload.Runtimes = rt
		var online int
		for _, h := range rt {
			if h.State != "1" {
				continue
			}
			online++
			payload.AvgDisk += h.DiskPer
			payload.AvgLoad += h.Load1
			payload.NetRxKB += h.NetRxKB
			payload.NetTxKB += h.NetTxKB
			payload.DiskReadKB += h.DiskReadKB
			payload.DiskWriteKB += h.DiskWriteK
			payload.ReadIOPS += h.ReadIOPS
			payload.WriteIOPS += h.WriteIOPS
			payload.ConnTotal += h.ConnCount
			// 温度为 0 表示该主机没有温度传感器（虚拟机、云主机），
			// 不计入平均，否则会把真实温度稀释掉。
			if h.CpuTemp > 0 {
				payload.AvgTemp += h.CpuTemp
				if h.CpuTemp > payload.MaxCpuTemp {
					payload.MaxCpuTemp = h.CpuTemp
				}
			}
		}
		if online > 0 {
			f := float64(online)
			payload.AvgDisk = round2(payload.AvgDisk / f)
			payload.AvgLoad = round2(payload.AvgLoad / f)
			payload.AvgTemp = round2(payload.AvgTemp / f)
			payload.NetRxKB = round2(payload.NetRxKB / f)
			payload.NetTxKB = round2(payload.NetTxKB / f)
			payload.DiskReadKB = round2(payload.DiskReadKB / f)
			payload.DiskWriteKB = round2(payload.DiskWriteKB / f)
			payload.ReadIOPS = round2(payload.ReadIOPS / f)
			payload.WriteIOPS = round2(payload.WriteIOPS / f)
		}
		payload.TopDisk = store.RanksOf(rt, "disk")
		payload.TopNet = store.RanksOf(rt, "net")
	}

	if tr, units, terr := s.store.ScreenTrends(minutes); terr == nil {
		payload.Trends = tr
		payload.Units = units
	}
	if stats, daily, aerr := s.store.ScreenAlertStats(7); aerr == nil {
		payload.AlertStats = stats
		payload.AlertDaily = daily
	}
	if cats, cerr := s.store.ScreenCatalogs(); cerr == nil {
		payload.Catalogs = cats
	}
	if sh, serr := s.store.ScreenSmartHealth(); serr == nil {
		payload.SmartHealth = sh
	}
	if _, byRunner, rerr := s.store.ScreenTaskStats(); rerr == nil {
		payload.TaskRunnerCount = byRunner
	}
	if n, perr := s.store.TotalProcess(); perr == nil {
		payload.TotalProcess = n
	}
	// 系统名称取用户自定义，未设置时用默认品牌名
	if settings, serr := s.store.Settings(); serr == nil {
		if name := strings.TrimSpace(settings["systemName"]); name != "" {
			payload.SystemName = name
		}
	}
	writeJSON(w, payload)
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
