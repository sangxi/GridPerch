package store

import (
	"fmt"
	"time"
)

// TrendPoint 是大屏趋势曲线上的一个点。
type TrendPoint struct {
	Time  string  `json:"time"`
	Value float64 `json:"value"`
}

// HostRuntime 汇总单台主机的大屏指标。
//
// 这些值全部来自各自表的最新一条记录，用子查询一次性取出，
// 避免逐主机二次查询把大屏刷新拖成 N+1。
type HostRuntime struct {
	Hostname   string  `json:"hostname"`
	IP         string  `json:"ip"`
	State      string  `json:"state"`
	CpuPer     float64 `json:"cpuPer"`
	MemPer     float64 `json:"memPer"`
	DiskPer    float64 `json:"diskPer"`
	Load1      float64 `json:"load1"`
	NetRxKB    float64 `json:"netRxKb"`
	NetTxKB    float64 `json:"netTxKb"`
	DiskReadKB float64 `json:"diskReadKb"`
	DiskWriteK float64 `json:"diskWriteKb"`
	ReadIOPS   float64 `json:"readIops"`
	WriteIOPS  float64 `json:"writeIops"`
	CpuTemp    float64 `json:"cpuTemp"`
	ConnCount  int     `json:"connCount"`
	Smart      string  `json:"smart"`
	CoreNum    string  `json:"cpuCoreNum"`
	CpuModel   string  `json:"cpuXh"`
	Version    string  `json:"version"`
	Catalog    string  `json:"catalogName"`
	LastSeen   string  `json:"lastSeen"`
	AgentVer   string  `json:"agentVersion"`
}

// AlertStat 是按级别与类型聚合的告警统计。
type AlertStat struct {
	Level string `json:"level"`
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// CatalogStat 是按主机目录聚合的在线情况。
type CatalogStat struct {
	Name   string  `json:"name"`
	Total  int     `json:"total"`
	Online int     `json:"online"`
	AvgCpu float64 `json:"avgCpu"`
	AvgMem float64 `json:"avgMem"`
	BadCnt int     `json:"bad"`
}

// ScreenExtra 是 /api/screen 在原有聚合之上补充的大屏专用数据。
type ScreenExtra struct {
	// Trends 为全主机平均资源趋势，供大屏折线图使用
	Trends map[string][]TrendPoint `json:"trends"`
	// Runtimes 为逐主机的运行时指标，条形图与表格的数据源
	Runtimes []HostRuntime `json:"runtimes"`
	// AlertStats 为近 N 天的告警按级别 / 类型分布
	AlertStats []AlertStat `json:"alertStats"`
	// AlertDaily 为近 N 天的每日告警条数
	AlertDaily []TrendPoint `json:"alertDaily"`
	// Catalogs 为按主机目录的分组统计
	Catalogs []CatalogStat `json:"catalogs"`
	// SmartHealth 为硬盘 SMART 健康度汇总（正常 / 预警 / 异常 / 未知）
	SmartHealth map[string]int `json:"smartHealth"`
	// ConnTotal / ConnEstablished 为全平台 TCP 连接数汇总
	ConnTotal       int `json:"connTotal"`
	ConnEstablished int `json:"connEstablished"`
	// TopDisk / TopNet 为磁盘与网络占用排行
	TopDisk []RankItem `json:"topDisk"`
	TopNet  []RankItem `json:"topNet"`
	// TaskTypeCount 为监控项类型分布
	TaskTypeCount map[string]int `json:"taskTypeCount"`
	// TaskRunnerCount 为执行端分布（agent / server）
	TaskRunnerCount map[string]int `json:"taskRunnerCount"`
	// TotalProcess 为在线主机上报的进程快照总数
	TotalProcess int `json:"totalProcess"`
}

// RankItem 是排行条目。
type RankItem struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

// trendDefs 定义大屏趋势图要画的曲线。
//
// 取数全部在 SQL 层按分钟聚合，无论窗口多长都不会把原始点位全量回传。
var trendDefs = []struct {
	Key   string
	Table string
	Col   string
	Unit  string
}{
	{"cpu", "cpu_state", "sys", "%"},
	{"mem", "mem_state", "use_per", "%"},
	{"load", "sys_load_state", "one_load", ""},
	{"netRx", "netio_state", "rxbyt", "KB/s"},
	{"netTx", "netio_state", "txbyt", "KB/s"},
	{"diskRead", "diskio_state", "read_kb", "KB/s"},
	{"diskWrite", "diskio_state", "write_kb", "KB/s"},
}

// ScreenTrends 返回全主机平均资源趋势。
func (s *Store) ScreenTrends(minutes int) (map[string][]TrendPoint, map[string]string, error) {
	if minutes <= 0 {
		minutes = 180
	}
	if minutes > 7*24*60 {
		minutes = 7 * 24 * 60
	}
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).Format(timeLayout)

	out := make(map[string][]TrendPoint, len(trendDefs))
	units := make(map[string]string, len(trendDefs))
	for _, d := range trendDefs {
		q := fmt.Sprintf(`SELECT substr(create_time,1,16) || ':00' AS slot, IFNULL(AVG(%s),0) FROM %s
			WHERE create_time>=? GROUP BY slot ORDER BY slot`, d.Col, d.Table)
		rows, err := s.rdb.Query(q, since)
		if err != nil {
			return nil, nil, err
		}
		pts := make([]TrendPoint, 0, 120)
		for rows.Next() {
			var t string
			var v float64
			if err := rows.Scan(&t, &v); err != nil {
				rows.Close()
				return nil, nil, err
			}
			pts = append(pts, TrendPoint{Time: t, Value: round1(v)})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
		out[d.Key] = pts
		units[d.Key] = d.Unit
	}
	return out, units, nil
}

// ScreenRuntimes 返回逐主机的运行时指标。
//
// 每项指标都取「该主机最新一条」记录：先在子查询里取 MAX(create_time)，
// 再取该时刻的值。磁盘取所有分区中最高的使用率，网卡与磁盘 IO 取最新速率。
func (s *Store) ScreenRuntimes() ([]HostRuntime, error) {
	q := `SELECT h.hostname, IFNULL(h.ip,''), IFNULL(h.state,'2'), IFNULL(h.cpu_core_num,''),
		IFNULL(h.cpu_xh,''), IFNULL(h.version,''), IFNULL(h.last_seen,''), IFNULL(h.agent_version,''),
		IFNULL(hc.name,''),
		IFNULL((SELECT cpu_per FROM system_info s WHERE s.hostname=h.hostname ORDER BY s.create_time DESC LIMIT 1),0),
		IFNULL((SELECT use_per FROM mem_state s WHERE s.hostname=h.hostname ORDER BY s.create_time DESC LIMIT 1),0),
		IFNULL((SELECT MAX(use_per) FROM desk_state d WHERE d.hostname=h.hostname AND d.create_time=
			(SELECT MAX(create_time) FROM desk_state d2 WHERE d2.hostname=h.hostname)),0),
		IFNULL((SELECT one_load FROM sys_load_state s WHERE s.hostname=h.hostname ORDER BY s.create_time DESC LIMIT 1),0),
		IFNULL((SELECT rxbyt FROM netio_state s WHERE s.hostname=h.hostname ORDER BY s.create_time DESC LIMIT 1),0),
		IFNULL((SELECT txbyt FROM netio_state s WHERE s.hostname=h.hostname ORDER BY s.create_time DESC LIMIT 1),0),
		IFNULL((SELECT read_kb FROM diskio_state s WHERE s.hostname=h.hostname ORDER BY s.create_time DESC LIMIT 1),0),
		IFNULL((SELECT write_kb FROM diskio_state s WHERE s.hostname=h.hostname ORDER BY s.create_time DESC LIMIT 1),0),
		IFNULL((SELECT read_iops FROM diskio_state s WHERE s.hostname=h.hostname ORDER BY s.create_time DESC LIMIT 1),0),
		IFNULL((SELECT write_iops FROM diskio_state s WHERE s.hostname=h.hostname ORDER BY s.create_time DESC LIMIT 1),0),
		IFNULL((SELECT cpu_temp FROM host_extra s WHERE s.hostname=h.hostname),0),
		IFNULL((SELECT conn_count FROM host_extra s WHERE s.hostname=h.hostname),0),
		IFNULL((SELECT smart_status FROM host_extra s WHERE s.hostname=h.hostname),'')
		FROM host h
		LEFT JOIN host_catalog_ref ref ON ref.hostname = h.hostname
		LEFT JOIN host_catalog hc ON hc.id = ref.catalog_id
		ORDER BY h.hostname`

	rows, err := s.rdb.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []HostRuntime{}
	for rows.Next() {
		var r HostRuntime
		if err := rows.Scan(&r.Hostname, &r.IP, &r.State, &r.CoreNum, &r.CpuModel, &r.Version,
			&r.LastSeen, &r.AgentVer, &r.Catalog,
			&r.CpuPer, &r.MemPer, &r.DiskPer, &r.Load1,
			&r.NetRxKB, &r.NetTxKB, &r.DiskReadKB, &r.DiskWriteK,
			&r.ReadIOPS, &r.WriteIOPS, &r.CpuTemp, &r.ConnCount, &r.Smart); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ScreenAlertStats 返回近 days 天的告警分布。
func (s *Store) ScreenAlertStats(days int) ([]AlertStat, []TrendPoint, error) {
	if days <= 0 {
		days = 7
	}
	since := time.Now().AddDate(0, 0, -days).Format(timeLayout)

	// 按级别统计
	rows, err := s.rdb.Query(`SELECT IFNULL(level,'warning'), COUNT(1) FROM alert
		WHERE create_time>=? GROUP BY level ORDER BY COUNT(1) DESC`, since)
	if err != nil {
		return nil, nil, err
	}
	byLevel := []AlertStat{}
	for rows.Next() {
		var a AlertStat
		if err := rows.Scan(&a.Level, &a.Count); err != nil {
			rows.Close()
			return nil, nil, err
		}
		byLevel = append(byLevel, a)
	}
	rows.Close()

	// 按类型统计
	rows2, err := s.rdb.Query(`SELECT IFNULL(type,'other'), COUNT(1) FROM alert
		WHERE create_time>=? GROUP BY type ORDER BY COUNT(1) DESC LIMIT 12`, since)
	if err != nil {
		return nil, nil, err
	}
	byType := []AlertStat{}
	for rows2.Next() {
		var a AlertStat
		if err := rows2.Scan(&a.Type, &a.Count); err != nil {
			rows2.Close()
			return nil, nil, err
		}
		byType = append(byType, a)
	}
	rows2.Close()
	if err := rows2.Err(); err != nil {
		return nil, nil, err
	}

	// 每日趋势：补齐没有告警的日期，否则折线会跳空档
	byDay, err := s.AlertDaily(days)
	if err != nil {
		return nil, nil, err
	}
	return append(byLevel, byType...), byDay, nil
}

// AlertDaily 返回近 days 天每天的告警条数，缺失日期补 0。
func (s *Store) AlertDaily(days int) ([]TrendPoint, error) {
	if days <= 0 {
		days = 7
	}
	since := time.Now().AddDate(0, 0, -days+1).Format("2006-01-02")

	rows, err := s.rdb.Query(`SELECT substr(create_time,1,10) AS d, COUNT(1) FROM alert
		WHERE create_time>=? GROUP BY d ORDER BY d`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var d string
		var n int
		if err := rows.Scan(&d, &n); err != nil {
			return nil, err
		}
		got[d] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	now := time.Now()
	out := make([]TrendPoint, 0, days)
	for i := days - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		out = append(out, TrendPoint{Time: d, Value: float64(got[d])})
	}
	return out, nil
}

// ScreenCatalogs 返回按主机目录分组的统计，未分组的归入「未分组」。
func (s *Store) ScreenCatalogs() ([]CatalogStat, error) {
	rt, err := s.ScreenRuntimes()
	if err != nil {
		return nil, err
	}
	index := map[string]*CatalogStat{}
	order := []string{}
	for _, r := range rt {
		name := r.Catalog
		if name == "" {
			name = "未分组"
		}
		st, ok := index[name]
		if !ok {
			st = &CatalogStat{Name: name}
			index[name] = st
			order = append(order, name)
		}
		st.Total++
		if r.State == "1" {
			st.Online++
			st.AvgCpu += r.CpuPer
			st.AvgMem += r.MemPer
		}
		if r.CpuPer >= 90 || r.MemPer >= 90 || r.DiskPer >= 90 || r.State != "1" {
			st.BadCnt++
		}
	}
	out := make([]CatalogStat, 0, len(order))
	for _, name := range order {
		st := index[name]
		if st.Online > 0 {
			st.AvgCpu = round1(st.AvgCpu / float64(st.Online))
			st.AvgMem = round1(st.AvgMem / float64(st.Online))
		}
		out = append(out, *st)
	}
	return out, nil
}

// ScreenSmartHealth 汇总硬盘 SMART 健康度分布。
//
// 没有采集到 SMART 的主机记为「未知」，这在虚拟机与云主机上是常态，
// 不应该被算成异常。
func (s *Store) ScreenSmartHealth() (map[string]int, error) {
	rows, err := s.rdb.Query(`SELECT IFNULL(NULLIF(smart_status,''),'unknown'), COUNT(1)
		FROM host_extra GROUP BY IFNULL(NULLIF(smart_status,''),'unknown')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{"pass": 0, "warn": 0, "fail": 0, "unknown": 0}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// ScreenTaskStats 返回监控项的类型分布与执行端分布。
func (s *Store) ScreenTaskStats() (byType, byRunner map[string]int, err error) {
	rows, err := s.rdb.Query(`SELECT type, COUNT(1) FROM monitor_task WHERE enabled=1 GROUP BY type`)
	if err != nil {
		return nil, nil, err
	}
	byType = map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return nil, nil, err
		}
		byType[k] = n
	}
	rows.Close()

	rows2, err := s.rdb.Query(`SELECT IFNULL(NULLIF(runner,''),'agent'), COUNT(1)
		FROM monitor_task WHERE enabled=1 GROUP BY runner`)
	if err != nil {
		return nil, nil, err
	}
	defer rows2.Close()
	byRunner = map[string]int{}
	for rows2.Next() {
		var k string
		var n int
		if err := rows2.Scan(&k, &n); err != nil {
			return nil, nil, err
		}
		byRunner[k] = n
	}
	return byType, byRunner, rows2.Err()
}

// TotalProcess 返回在线主机最近一次上报的进程快照总数。
func (s *Store) TotalProcess() (int, error) {
	var n int
	err := s.rdb.QueryRow(`SELECT COUNT(1) FROM process_state WHERE hostname IN
		(SELECT hostname FROM host WHERE state='1')`).Scan(&n)
	return n, err
}

// RanksOf 把主机运行时指标按某个字段降序取前 N，生成排行数据。
// key 支持 cpu / mem / disk / load / net / temp。
func RanksOf(rt []HostRuntime, key string) []RankItem {
	items := make([]RankItem, 0, len(rt))
	for _, h := range rt {
		var v float64
		switch key {
		case "cpu":
			v = h.CpuPer
		case "mem":
			v = h.MemPer
		case "disk":
			v = h.DiskPer
		case "load":
			v = h.Load1
		case "net":
			v = h.NetRxKB + h.NetTxKB
		case "temp":
			v = h.CpuTemp
		}
		items = append(items, RankItem{Name: h.Hostname, Value: round1(v)})
	}
	// 插入排序，条数与主机数同阶且已按值排好大部分
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].Value > items[j-1].Value; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	if len(items) > 10 {
		items = items[:10]
	}
	return items
}
