package store

import "testing"

// TestListHostsNoSystemInfo 回归：主机在 host 表登记但还没有任何 system_info 行时，
// 最新 CPU/内存子查询返回 NULL（标量子查询里写 IFNULL 救不了空结果集），
// 曾经导致 Scan 到 float64 报 "converting NULL to float64 is unsupported"。
func TestListHostsNoSystemInfo(t *testing.T) {
	s := newTestStore(t)
	now := nowStr()
	execSeed(t, s, `INSERT INTO host(hostname,ip,version,state,last_seen,create_time) VALUES(?,?,?,?,?,?)`,
		"has-data", "10.0.0.1", "linux", "1", now, now)
	execSeed(t, s, `INSERT INTO host(hostname,ip,version,state,last_seen,create_time) VALUES(?,?,?,?,?,?)`,
		"no-data", "10.0.0.2", "linux", "1", now, now)
	// 只有 has-data 有指标快照；no-data 故意不写 system_info
	execSeed(t, s, `INSERT INTO system_info(id,hostname,cpu_per,mem_per,create_time) VALUES(?,?,?,?,?)`,
		"s1", "has-data", 12.5, 34.5, now)

	hosts, err := s.ListHosts()
	if err != nil {
		t.Fatalf("ListHosts 不应因缺失 system_info 报错: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("期望 2 台主机，实际 %d", len(hosts))
	}
	byName := map[string]float64{}
	for _, h := range hosts {
		byName[h.Hostname] = h.CpuPer
	}
	if got := byName["has-data"]; got != 12.5 {
		t.Fatalf("has-data CpuPer 期望 12.5，实际 %v", got)
	}
	if got := byName["no-data"]; got != 0 {
		t.Fatalf("no-data 无指标应回落到 0，实际 %v", got)
	}
}
