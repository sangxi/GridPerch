package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/sangxi/gridperch/internal/model"
)

// 告警闭环相关存储：认领 / 释放 / 备注 / 静默规则。
//
// 设计要点（改之前先读）：
//
//  1. 告警是「按次数入库」的 —— checkAlerts() 每个冷却窗口（默认 10 分钟）为同一
//     故障插一行新记录。所以认领状态挂在行上只是「这条被谁看过」，真正代表
//     「这个故障有人跟」的信息要前端按 host+type 聚合后自己展示。
//  2. 静默规则匹配的是 **host + type**，不是告警 ID。按 ID 静默只能挡住一条，
//     挡不住后续每一次，实际等于没静默。
//  3. 静默必须同时压制通知（邮件 / Webhook），否则只是让面板好看。

// ackAlertMaxNote 备注长度上限（字符，按 rune 计）。
const ackAlertMaxNote = 500

// UnackedAlerts 返回超过 minAge 仍无人认领、且尚未升级过的未恢复告警。
//
// 「已升级」用独立列标记而不是时间游标：游标一旦被推进，中间补认领再释放的
// 告警就再也不会被升级提醒，反而会漏掉。
func (s *Store) UnackedAlerts(minAge time.Duration) ([]model.Alert, error) {
	deadline := time.Now().Add(-minAge).Format(timeLayout)
	rows, err := s.rdb.Query(`SELECT id, hostname, IFNULL(type,''), IFNULL(level,'warning'), IFNULL(value,''),
		IFNULL(content,''), IFNULL(state,'1'), IFNULL(create_time,''),
		IFNULL(ack_by,''), IFNULL(ack_time,''), IFNULL(note,'')
		FROM alert WHERE state='1' AND (ack_by IS NULL OR ack_by='')
		AND IFNULL(escalated,0)=0 AND create_time <= ?
		ORDER BY create_time ASC LIMIT 100`, deadline)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.Alert
	for rows.Next() {
		var a model.Alert
		if err := rows.Scan(&a.ID, &a.Hostname, &a.Type, &a.Level, &a.Value, &a.Content, &a.State,
			&a.CreateTime, &a.AckBy, &a.AckTime, &a.Note); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, rows.Err()
}

// MarkAlertEscalated 标记告警已升级提醒，避免每轮维护任务重复推送。
// 单条 UPDATE 拼接成一句，省掉 N 次写事务（wdb 只有一条写连接）。
func (s *Store) MarkAlertEscalated(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	ph := strings.Repeat("?,", len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.wdb.Exec(`UPDATE alert SET escalated=1 WHERE id IN (`+ph[:len(ph)-1]+`)`, args...)
	return err
}

// AckAlert 认领告警，记录认领人与时间。已认领时保留原认领人（后到的人不顶掉前者）。
func (s *Store) AckAlert(id, user string) error {
	user = strings.TrimSpace(user)
	if user == "" {
		user = "未知"
	}
	res, err := s.wdb.Exec(`UPDATE alert SET ack_by=?, ack_time=?, update_time=?
		WHERE id=? AND (ack_by IS NULL OR ack_by='')`, user, nowStr(), nowStr(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// 要么不存在，要么已被别人认领 —— 区分开给前端不同的提示
		var by string
		err := s.rdb.QueryRow(`SELECT IFNULL(ack_by,'') FROM alert WHERE id=?`, id).Scan(&by)
		if err != nil {
			return fmt.Errorf("告警不存在")
		}
		return fmt.Errorf("该告警已被 %s 认领", by)
	}
	return nil
}

// UnackAlert 释放认领，回到未处理状态。
func (s *Store) UnackAlert(id string) error {
	_, err := s.wdb.Exec(`UPDATE alert SET ack_by='', ack_time='', update_time=? WHERE id=?`, nowStr(), id)
	return err
}

// SetAlertNote 写入/覆盖处理备注，空串表示清空。
func (s *Store) SetAlertNote(id, note string) error {
	note = strings.TrimSpace(note)
	if r := []rune(note); len(r) > ackAlertMaxNote {
		note = string(r[:ackAlertMaxNote])
	}
	res, err := s.wdb.Exec(`UPDATE alert SET note=?, update_time=? WHERE id=?`, note, nowStr(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("告警不存在")
	}
	return nil
}

// AlertSilences 返回全部静默规则（最新在前），并标出当前是否在生效期内。
func (s *Store) AlertSilences() ([]model.AlertSilence, error) {
	rows, err := s.rdb.Query(`SELECT id, IFNULL(hostname,''), IFNULL(type,''), IFNULL(level,''),
		IFNULL(reason,''), IFNULL(start_time,''), IFNULL(end_time,''),
		IFNULL(create_by,''), IFNULL(create_time,'')
		FROM alert_silence ORDER BY create_time DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := nowStr()
	var list []model.AlertSilence
	for rows.Next() {
		var r model.AlertSilence
		if err := rows.Scan(&r.ID, &r.Hostname, &r.Type, &r.Level, &r.Reason,
			&r.StartTime, &r.EndTime, &r.CreateBy, &r.CreateTime); err != nil {
			return nil, err
		}
		r.Active = inWindow(r.StartTime, r.EndTime, now)
		list = append(list, r)
	}
	return list, rows.Err()
}

// activeSilences 只取当前生效的规则，供告警列表与通知过滤复用。
// 查询失败时返回 nil（调用方按「无静默」处理，宁可多通知也别漏通知）。
func (s *Store) activeSilences() ([]model.AlertSilence, error) {
	rows, err := s.rdb.Query(`SELECT id, IFNULL(hostname,''), IFNULL(type,''), IFNULL(level,''),
		IFNULL(reason,''), IFNULL(start_time,''), IFNULL(end_time,''),
		IFNULL(create_by,''), IFNULL(create_time,'')
		FROM alert_silence WHERE (end_time IS NULL OR end_time='' OR end_time > ?)
		AND (start_time IS NULL OR start_time='' OR start_time <= ?)`,
		nowStr(), nowStr())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.AlertSilence
	for rows.Next() {
		var r model.AlertSilence
		if err := rows.Scan(&r.ID, &r.Hostname, &r.Type, &r.Level, &r.Reason,
			&r.StartTime, &r.EndTime, &r.CreateBy, &r.CreateTime); err != nil {
			return nil, err
		}
		r.Active = true
		list = append(list, r)
	}
	return list, rows.Err()
}

// AddAlertSilence 新增一条静默规则。endTime 必须晚于 startTime。
func (s *Store) AddAlertSilence(r model.AlertSilence) (model.AlertSilence, error) {
	r.Hostname = strings.TrimSpace(r.Hostname)
	r.Type = strings.TrimSpace(r.Type)
	r.Level = strings.TrimSpace(r.Level)
	r.Reason = strings.TrimSpace(r.Reason)
	if r.StartTime == "" {
		r.StartTime = nowStr()
	}
	if r.EndTime == "" {
		return r, fmt.Errorf("请指定静默结束时间")
	}
	if !isTimeStr(r.StartTime) || !isTimeStr(r.EndTime) {
		return r, fmt.Errorf("时间格式应为 2006-01-02 15:04:05")
	}
	if r.EndTime <= r.StartTime {
		return r, fmt.Errorf("结束时间必须晚于开始时间")
	}
	r.ID = newID()
	r.CreateTime = nowStr()
	if _, err := s.wdb.Exec(`INSERT INTO alert_silence(id, hostname, type, level, reason, start_time, end_time, create_by, create_time)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Hostname, r.Type, r.Level, r.Reason, r.StartTime, r.EndTime, r.CreateBy, r.CreateTime); err != nil {
		return r, err
	}
	r.Active = inWindow(r.StartTime, r.EndTime, nowStr())
	return r, nil
}

// DelAlertSilence 删除静默规则（立即恢复通知）。
func (s *Store) DelAlertSilence(id string) error {
	res, err := s.wdb.Exec(`DELETE FROM alert_silence WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("静默规则不存在")
	}
	return nil
}

// IsAlertSilenced 判断某主机某类告警在 at 时刻是否处于静默期。
// 通知链路（邮件 / Webhook）发送前逐条过滤，避免静默期还骚扰值班人。
func (s *Store) IsAlertSilenced(host, typ, level, at string) bool {
	rules, err := s.activeSilences()
	if err != nil || len(rules) == 0 {
		return false
	}
	if at == "" {
		at = nowStr()
	}
	return silenceHit(rules, host, typ, level, at)
}

// SilenceChecker 载入当前生效的静默规则，返回可复用的匹配函数。
//
// 通知链路一次要推一批告警，逐条查库太浪费，这里一次性载入后复用。
// 载入失败时返回恒 false —— 宁可多通知，不可漏通知。
func (s *Store) SilenceChecker() func(host, typ, level, at string) bool {
	rules, err := s.activeSilences()
	if err != nil || len(rules) == 0 {
		return func(string, string, string, string) bool { return false }
	}
	return func(host, typ, level, at string) bool {
		return silenceHit(rules, host, typ, level, at)
	}
}

// silenceHit 在内存里做规则匹配：空字段表示「不限」。
func silenceHit(rules []model.AlertSilence, host, typ, level, at string) bool {
	for _, r := range rules {
		if r.Hostname != "" && !strings.EqualFold(r.Hostname, host) {
			continue
		}
		if r.Type != "" && !strings.EqualFold(r.Type, typ) {
			continue
		}
		if r.Level != "" && !strings.EqualFold(r.Level, level) {
			continue
		}
		if inWindow(r.StartTime, r.EndTime, at) {
			return true
		}
	}
	return false
}

// inWindow 判断 at 是否落在 [start, end) 内；空边界表示不限。
// 时间串统一是 "2006-01-02 15:04:05"，字典序即时间序，可直接比较。
func inWindow(start, end, at string) bool {
	if at == "" {
		at = nowStr()
	}
	if start != "" && at < start {
		return false
	}
	if end != "" && at >= end {
		return false
	}
	return true
}

func isTimeStr(s string) bool {
	_, err := time.ParseInLocation(timeLayout, s, time.Local)
	return err == nil
}
