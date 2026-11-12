package store

import "testing"

// TestDeleteHostBatched 数据量超过单批（5000 行）时也要删干净，
// 而且不能碰到别的主机——分批删除放弃了事务原子性，这两点必须守住。
func TestDeleteHostBatched(t *testing.T) {
	s := newTestStore(t)
	now := nowStr()
	for _, h := range []string{"heavy", "other"} {
		execSeed(t, s, `INSERT INTO host(hostname,ip,version,state,last_seen,create_time) VALUES(?,?,?,?,?,?)`,
			h, "10.0.0.1", "v", "1", now, now)
	}
	// 13000 行，跨 3 个批次
	tx, err := s.wdb.Begin()
	if err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}
	for i := 0; i < 13000; i++ {
		if _, err := tx.Exec(`INSERT INTO cpu_state(id,hostname,date_str,create_time) VALUES(?,?,?,?)`,
			newID(), "heavy", "2026-11-13", now); err != nil {
			t.Fatalf("写入 cpu_state 失败: %v", err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO cpu_state(id,hostname,date_str,create_time) VALUES(?,?,?,?)`,
		newID(), "other", "2026-11-13", now); err != nil {
		t.Fatalf("写入 other 失败: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	if err := s.DeleteHost("heavy"); err != nil {
		t.Fatalf("删除主机失败: %v", err)
	}

	count := func(host string) int {
		var n int
		if err := s.rdb.QueryRow(`SELECT COUNT(1) FROM cpu_state WHERE hostname=?`, host).Scan(&n); err != nil {
			t.Fatalf("统计失败: %v", err)
		}
		return n
	}
	if n := count("heavy"); n != 0 {
		t.Fatalf("heavy 的 cpu_state 应全删，剩 %d 行", n)
	}
	if n := count("other"); n != 1 {
		t.Fatalf("other 的数据不能被动到，实际 %d 行", n)
	}
	var hosts int
	if err := s.rdb.QueryRow(`SELECT COUNT(1) FROM host WHERE hostname='heavy'`).Scan(&hosts); err != nil {
		t.Fatalf("查询 host 失败: %v", err)
	}
	if hosts != 0 {
		t.Fatalf("主机行应已删除")
	}

	// 重复删除应返回「主机不存在」而不是静默成功
	if err := s.DeleteHost("heavy"); err != ErrHostNotFound {
		t.Fatalf("重复删除应返回 ErrHostNotFound，实际 %v", err)
	}
}
