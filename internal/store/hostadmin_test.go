package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/sangxi/strix/internal/config"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	cfg := &config.Server{
		DBPath:     filepath.Join(t.TempDir(), "strix-test.db"),
		Token:      "test-token",
		AdminUser:  "admin",
		AdminPass:  "admin",
		ReadConns:  2,
		WriteQueue: 16,
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func execSeed(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.wdb.Exec(q, args...); err != nil {
		t.Fatalf("seed 失败\nSQL: %s\n错误: %v", q, err)
	}
}

// seedHost 在每张以主机名为键的业务表里都写入一行，用于验证改名/删除的级联覆盖度。
func seedHost(t *testing.T, s *Store, name string) {
	t.Helper()
	now := nowStr()
	execSeed(t, s, `INSERT INTO host(hostname,ip,version,state,last_seen,create_time) VALUES(?,?,?,?,?,?)`,
		name, "10.0.0.1", "v1", "1", now, now)
	execSeed(t, s, `INSERT INTO host_extra(hostname,update_time) VALUES(?,?)`, name, now)
	execSeed(t, s, `INSERT INTO host_catalog_ref(hostname,catalog_id,update_time) VALUES(?,?,?)`, name, "cat-1", now)
	execSeed(t, s, `INSERT INTO agent_config(hostname,interval,update_time) VALUES(?,?,?)`, name, 30, now)
	execSeed(t, s, `INSERT INTO system_info(id,hostname,create_time) VALUES(?,?,?)`, newID(), name, now)
	execSeed(t, s, `INSERT INTO cpu_state(id,hostname,date_str,create_time) VALUES(?,?,?,?)`, newID(), name, "2026-10-13", now)
	execSeed(t, s, `INSERT INTO mem_state(id,hostname,date_str,create_time) VALUES(?,?,?,?)`, newID(), name, "2026-10-13", now)
	execSeed(t, s, `INSERT INTO desk_state(id,hostname,file_system,date_str,create_time) VALUES(?,?,?,?,?)`, newID(), name, "/", "2026-10-13", now)
	execSeed(t, s, `INSERT INTO netio_state(id,hostname,date_str,create_time) VALUES(?,?,?,?)`, newID(), name, "2026-10-13", now)
	execSeed(t, s, `INSERT INTO diskio_state(id,hostname,date_str,create_time) VALUES(?,?,?,?)`, newID(), name, "2026-10-13", now)
	execSeed(t, s, `INSERT INTO sys_load_state(id,hostname,date_str,create_time) VALUES(?,?,?,?)`, newID(), name, "2026-10-13", now)
	execSeed(t, s, `INSERT INTO alert(id,hostname,type,level,content,state,create_time,update_time) VALUES(?,?,?,?,?,?,?,?)`,
		newID(), name, "cpu", "warn", "CPU 偏高", "1", now, now)
	// 静默规则也以主机名为键（空值表示全部主机），改名/删除必须一并处理
	execSeed(t, s, `INSERT INTO alert_silence(id,hostname,type,level,reason,start_time,end_time,create_by,create_time)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		newID(), name, "cpu", "", "割接", now, now, "admin", now)
	execSeed(t, s, `INSERT INTO process_state(id,hostname,pid,name,create_time) VALUES(?,?,?,?,?)`, newID(), name, 1, "init", now)
	execSeed(t, s, `INSERT INTO monitor_state(id,task_id,hostname,type,status,update_time) VALUES(?,?,?,?,?,?)`,
		newID(), "task-1", name, "tcp", "ok", now)
	execSeed(t, s, `INSERT INTO monitor_event(id,task_id,hostname,type,name,status,check_time) VALUES(?,?,?,?,?,?,?)`,
		newID(), "task-1", name, "tcp", "端口", "ok", now)
	execSeed(t, s, `INSERT INTO login_event(id,hostname,users,diff,create_time) VALUES(?,?,?,?,?)`, newID(), name, "root", "+1", now)
	execSeed(t, s, `INSERT INTO sys_event(id,hostname,source,level,event_time,create_time) VALUES(?,?,?,?,?,?)`,
		newID(), name, "Service Control Manager", "error", now, now)
	execSeed(t, s, `INSERT INTO command(id,host,cmd,status,create_time) VALUES(?,?,?,?,?)`, newID(), name, "whoami", "pending", now)
}

func countOf(t *testing.T, s *Store, table, name string) int {
	t.Helper()
	col := "hostname"
	if table == "command" {
		col = "host"
	}
	var n int
	if err := s.rdb.QueryRow(`SELECT COUNT(1) FROM `+table+` WHERE `+col+`=?`, name).Scan(&n); err != nil {
		t.Fatalf("统计 %s 失败: %v", table, err)
	}
	return n
}

func TestRenameHostMovesEveryRelatedTable(t *testing.T) {
	s := newTestStore(t)
	const oldName, newName = "测试主机-中文", "web-prod-01"
	seedHost(t, s, oldName)
	// 监控项的「作用主机」名单（含该主机 + 另一台）
	execSeed(t, s, `INSERT INTO monitor_task(id,type,name,target,hosts,create_time) VALUES(?,?,?,?,?,?)`,
		"task-1", "tcp", "端口", "127.0.0.1:80", oldName+",other-host", nowStr())
	// 资产管理里手填的「关联监控主机」
	execSeed(t, s, `INSERT INTO asset(id,name,monitor_bind,create_time) VALUES(?,?,?,?)`,
		"asset-1", "核心机", oldName, nowStr())

	if err := s.RenameHost(oldName, newName); err != nil {
		t.Fatalf("改名失败: %v", err)
	}

	for _, tb := range hostnameRefTables {
		if got := countOf(t, s, tb, oldName); got != 0 {
			t.Errorf("改名后 %s 仍残留 %d 行旧主机名数据", tb, got)
		}
		if got := countOf(t, s, tb, newName); got != 1 {
			t.Errorf("改名后 %s 应有 1 行新主机名数据，实际 %d", tb, got)
		}
	}
	if got := countOf(t, s, "command", oldName); got != 0 {
		t.Errorf("指令记录未迁移，仍有 %d 行", got)
	}
	if got := countOf(t, s, "command", newName); got != 1 {
		t.Errorf("指令记录应迁移到新名，实际 %d 行", got)
	}

	var hosts string
	if err := s.rdb.QueryRow(`SELECT hosts FROM monitor_task WHERE id='task-1'`).Scan(&hosts); err != nil {
		t.Fatalf("读取监控项失败: %v", err)
	}
	if want := newName + ",other-host"; hosts != want {
		t.Errorf("监控项作用主机未改写：got %q want %q", hosts, want)
	}

	var bind string
	if err := s.rdb.QueryRow(`SELECT monitor_bind FROM asset WHERE id='asset-1'`).Scan(&bind); err != nil {
		t.Fatalf("读取资产失败: %v", err)
	}
	if bind != newName {
		t.Errorf("资产关联未同步：got %q want %q", bind, newName)
	}

	// 采集端仍以旧名上报时会归位到新名
	if got := s.ResolveHostname(oldName); got != newName {
		t.Errorf("旧名应解析为新名：got %q want %q", got, newName)
	}
	if got := s.ResolveHostname(newName); got != newName {
		t.Errorf("新名应原样返回：got %q", got)
	}
	if got := s.ResolveHostname("other-host"); got != "other-host" {
		t.Errorf("未改名的主机不应受影响：got %q", got)
	}
}

func TestRenameHostValidation(t *testing.T) {
	s := newTestStore(t)
	seedHost(t, s, "alpha")
	seedHost(t, s, "beta")

	cases := []struct {
		name     string
		from, to string
		want     error
	}{
		{"目标名已被占用", "alpha", "beta", ErrHostNameTaken},
		{"源主机不存在", "ghost", "beta-2", ErrHostNotFound},
		{"新旧同名", "alpha", "alpha", ErrHostNameSame},
		{"空名", "alpha", "   ", ErrHostNameEmpty},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := s.RenameHost(c.from, c.to); !errors.Is(err, c.want) {
				t.Fatalf("期望 %v，实际 %v", c.want, err)
			}
		})
	}
	// 失败用例不应产生任何副作用
	if !s.HostExists("alpha") || !s.HostExists("beta") {
		t.Fatal("校验失败的改名不应改动主机数据")
	}
}

func TestRenameHostChains(t *testing.T) {
	s := newTestStore(t)
	seedHost(t, s, "a")
	if err := s.RenameHost("a", "b"); err != nil {
		t.Fatalf("第一次改名失败: %v", err)
	}
	if err := s.RenameHost("b", "c"); err != nil {
		t.Fatalf("第二次改名失败: %v", err)
	}
	// 链式改名后，最老的旧名也要能一路解析到最新名字
	if got := s.ResolveHostname("a"); got != "c" {
		t.Errorf("a 应解析为 c，实际 %q", got)
	}
	if got := s.ResolveHostname("b"); got != "c" {
		t.Errorf("b 应解析为 c，实际 %q", got)
	}
}

func TestDeleteHostClearsEverything(t *testing.T) {
	s := newTestStore(t)
	const name = "要被删除的主机"
	seedHost(t, s, name)
	execSeed(t, s, `INSERT INTO monitor_task(id,type,name,target,hosts,create_time) VALUES(?,?,?,?,?,?)`,
		"task-1", "tcp", "端口", "127.0.0.1:80", "keep-me,"+name, nowStr())
	execSeed(t, s, `INSERT INTO asset(id,name,monitor_bind,create_time) VALUES(?,?,?,?)`,
		"asset-1", "核心机", name, nowStr())

	if err := s.DeleteHost(name); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	for _, tb := range hostnameRefTables {
		if got := countOf(t, s, tb, name); got != 0 {
			t.Errorf("删除后 %s 仍残留 %d 行", tb, got)
		}
	}
	if got := countOf(t, s, "command", name); got != 0 {
		t.Errorf("删除后指令记录仍残留 %d 行", got)
	}

	var hosts string
	if err := s.rdb.QueryRow(`SELECT hosts FROM monitor_task WHERE id='task-1'`).Scan(&hosts); err != nil {
		t.Fatalf("读取监控项失败: %v", err)
	}
	if hosts != "keep-me" {
		t.Errorf("删除后应从监控项名单中摘除该主机，实际 %q", hosts)
	}
	var bind string
	if err := s.rdb.QueryRow(`SELECT monitor_bind FROM asset WHERE id='asset-1'`).Scan(&bind); err != nil {
		t.Fatalf("读取资产失败: %v", err)
	}
	if bind != "" {
		t.Errorf("删除后应清空资产关联，实际 %q", bind)
	}
	if err := s.DeleteHost(name); !errors.Is(err, ErrHostNotFound) {
		t.Errorf("重复删除应返回 ErrHostNotFound，实际 %v", err)
	}
}

// 改名后再删除新名字，映射必须一并清掉；
// 否则采集端用旧名上报时会被解析到一台已删除的主机上、把它凭空建回来。
func TestDeleteHostDropsRenameMapping(t *testing.T) {
	s := newTestStore(t)
	seedHost(t, s, "old-name")
	if err := s.RenameHost("old-name", "new-name"); err != nil {
		t.Fatalf("改名失败: %v", err)
	}
	if err := s.DeleteHost("new-name"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if got := s.ResolveHostname("old-name"); got != "old-name" {
		t.Errorf("映射应已清除，old-name 应解析为自身，实际 %q", got)
	}
}
