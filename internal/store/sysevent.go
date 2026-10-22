package store

import (
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/sangxi/gridperch/internal/model"
)

// schemaSysEvent 系统事件表：采集端读到的系统事件（当前仅 Windows 事件查看器）。
func (s *Store) schemaSysEvent() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS sys_event (
			id TEXT PRIMARY KEY,
			hostname TEXT,
			source TEXT, level TEXT, event_id INTEGER DEFAULT 0,
			category TEXT, content TEXT,
			event_time TEXT, create_time TEXT)`,
		`CREATE INDEX IF NOT EXISTS idx_sys_event_host ON sys_event(hostname, event_time)`,
	}
	for _, st := range stmts {
		if _, err := s.wdb.Exec(st); err != nil {
			return err
		}
	}
	return nil
}

// saveSysEvents 落库采集端上报的系统事件，并把错误/严重级别转成告警，
// 这样它们会出现在主机详情页的「历史告警」格子里。
func (s *Store) saveSysEvents(host string, events []model.SysEvent, now string) {
	if host == "" || len(events) == 0 {
		return
	}
	for _, e := range events {
		content := strings.TrimSpace(e.Content)
		if content == "" {
			continue
		}
		if len(content) > 900 {
			content = content[:900]
		}
		if _, err := s.wdb.Exec(`INSERT INTO sys_event(id, hostname, source, level, event_id,
			category, content, event_time, create_time) VALUES(?,?,?,?,?,?,?,?,?)`,
			newID(), host, e.Source, e.Level, e.EventID, e.Category, content,
			orDashTime(e.Time), now); err != nil {
			log.Printf("写入系统事件失败: %v", err)
			continue
		}
		s.raiseSysEventAlert(host, e, content, now)
	}
}

// raiseSysEventAlert 把系统事件转成告警。
// 警告级别不生成告警（事件量太大，会淹掉真正的告警），只记错误与严重。
func (s *Store) raiseSysEventAlert(host string, e model.SysEvent, content, now string) {
	level := strings.ToLower(strings.TrimSpace(e.Level))
	if level != "error" && level != "critical" {
		return
	}
	alertLevel := "warning"
	if level == "critical" {
		alertLevel = "danger"
	}
	// 同一事件 ID 24 小时内只生成一次告警，避免历史错误反复刷屏
	value := "sys:" + e.Source + ":" + itoa(e.EventID)
	var id string
	err := s.wdb.QueryRow(`SELECT id FROM alert WHERE hostname=? AND type='syslog' AND value=? AND state='1'
		AND update_time >= ?`, host, value, minusHours(24)).Scan(&id)
	if err == nil {
		return
	}
	src := orDash(e.Source)
	text := "[" + src + "] " + content
	if _, err := s.wdb.Exec(`INSERT INTO alert(id, hostname, type, level, value, content, state, create_time, update_time)
		VALUES(?,?,'syslog',?,?,?,'1',?,?)`, newID(), host, alertLevel, value, text, now, now); err != nil {
		log.Printf("写入系统事件告警失败: %v", err)
	}
}

// SysEventsOf 返回某台主机最近的系统事件，limit<=0 时取 50 条。
func (s *Store) SysEventsOf(host string, limit int) ([]model.SysEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.rdb.Query(`SELECT IFNULL(source,''), IFNULL(level,''), IFNULL(event_id,0),
		IFNULL(category,''), IFNULL(content,''), IFNULL(event_time,'')
		FROM sys_event WHERE hostname=? ORDER BY event_time DESC, create_time DESC LIMIT ?`, host, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SysEvent
	for rows.Next() {
		var e model.SysEvent
		if err := rows.Scan(&e.Source, &e.Level, &e.EventID, &e.Category, &e.Content, &e.Time); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// orDashTime 事件时间为空时退回当前时间，保证排序稳定。
func orDashTime(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nowStr()
	}
	return v
}

// orDash 空串占位为短横线，便于前端直接展示。
func orDash(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "-"
	}
	return v
}

// itoa 避免为一个转换额外引入 strconv 的使用说明负担。
func itoa(n int) string {
	return strconv.Itoa(n)
}

// minusHours 返回 n 小时前的存储格式时间串。
func minusHours(n int) string {
	return time.Now().Add(-time.Duration(n) * time.Hour).Format(timeLayout)
}
