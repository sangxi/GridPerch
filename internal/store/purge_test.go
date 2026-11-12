package store

import (
	"testing"
	"time"
)

// TestPurgeMixedTimeColumns 回归：monitor_event 的时间列叫 check_time，
// 其余表叫 create_time。旧实现一律按 create_time 删，第一张 monitor_event
// 就报 "no such column: create_time" 并提前 return，导致保留期完全失效。
// 这里验证各表都能按自己的时间列清理，且单表异常不拖累其它表。
func TestPurgeMixedTimeColumns(t *testing.T) {
	s := newTestStore(t)
	s.cfg.Retention = 15

	old := time.Now().AddDate(0, 0, -20).Format(timeLayout)
	fresh := time.Now().Format(timeLayout)

	execSeed(t, s, `INSERT INTO monitor_event(id,task_id,hostname,type,name,status,check_time) VALUES(?,?,?,?,?,?,?)`,
		"e-old", "t1", "h1", "port", "p", "1", old)
	execSeed(t, s, `INSERT INTO monitor_event(id,task_id,hostname,type,name,status,check_time) VALUES(?,?,?,?,?,?,?)`,
		"e-new", "t1", "h1", "port", "p", "1", fresh)
	execSeed(t, s, `INSERT INTO cpu_state(id,hostname,date_str,create_time) VALUES(?,?,?,?)`,
		"c-old", "h1", "2026-10-01", old)
	execSeed(t, s, `INSERT INTO cpu_state(id,hostname,date_str,create_time) VALUES(?,?,?,?)`,
		"c-new", "h1", "2026-11-09", fresh)
	execSeed(t, s, `INSERT INTO command(id,host,cmd,status,create_time) VALUES(?,?,?,?,?)`,
		"cmd-old", "h1", "whoami", "done", time.Now().AddDate(0, 0, -40).Format(timeLayout))
	execSeed(t, s, `INSERT INTO alert(id,hostname,type,level,content,state,create_time,update_time) VALUES(?,?,?,?,?,?,?,?)`,
		"a-closed", "h1", "offline", "critical", "x", "0", old, old)
	execSeed(t, s, `INSERT INTO alert(id,hostname,type,level,content,state,create_time,update_time) VALUES(?,?,?,?,?,?,?,?)`,
		"a-open", "h1", "offline", "critical", "x", "1", old, old)

	if err := s.Purge(); err != nil {
		t.Fatalf("Purge 不应因列名差异失败: %v", err)
	}

	count := func(table, id string) int {
		var n int
		if err := s.rdb.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id=?`, id).Scan(&n); err != nil {
			t.Fatalf("查询 %s.%s 失败: %v", table, id, err)
		}
		return n
	}
	cases := []struct {
		table, id string
		want      int
	}{
		{"monitor_event", "e-old", 0}, // check_time 列，旧实现在这里就挂了
		{"monitor_event", "e-new", 1},
		{"cpu_state", "c-old", 0},
		{"cpu_state", "c-new", 1},
		{"command", "cmd-old", 0}, // 指令队列 30 天，与 Retention 无关
		{"alert", "a-closed", 0},  // 已恢复的旧告警清掉
		{"alert", "a-open", 1},    // 未恢复的旧告警必须保留
	}
	for _, c := range cases {
		if got := count(c.table, c.id); got != c.want {
			t.Errorf("%s.%s 期望 %d 行，实际 %d 行", c.table, c.id, c.want, got)
		}
	}
}

// TestPurgeDisabledRetention Retention<=0 时只清指令队列，历史数据永久保留。
func TestPurgeDisabledRetention(t *testing.T) {
	s := newTestStore(t)
	s.cfg.Retention = 0

	old := time.Now().AddDate(0, 0, -400).Format(timeLayout)
	execSeed(t, s, `INSERT INTO monitor_event(id,task_id,hostname,type,name,status,check_time) VALUES(?,?,?,?,?,?,?)`,
		"e-old", "t1", "h1", "port", "p", "1", old)
	execSeed(t, s, `INSERT INTO command(id,host,cmd,status,create_time) VALUES(?,?,?,?,?)`,
		"cmd-old", "h1", "whoami", "done", old)

	if err := s.Purge(); err != nil {
		t.Fatalf("Purge 失败: %v", err)
	}
	var events, cmds int
	_ = s.rdb.QueryRow(`SELECT COUNT(*) FROM monitor_event WHERE id='e-old'`).Scan(&events)
	_ = s.rdb.QueryRow(`SELECT COUNT(*) FROM command WHERE id='cmd-old'`).Scan(&cmds)
	if events != 1 || cmds != 0 {
		t.Errorf("Retention=0 应保留历史(%d)、清理指令(%d)", events, cmds)
	}
}
