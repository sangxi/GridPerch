package store

import "testing"

// seedDeviceAccount 直接写一行设备账号，只关心关联相关的字段。
func seedDeviceAccount(t *testing.T, s *Store, id, name, host, bind string) {
	t.Helper()
	now := nowStr()
	execSeed(t, s, `INSERT INTO device_account(id,name,asset_id,bind_host,protocol,host,port,username,secret,remark,create_time,update_time)
		VALUES(?,?,'',?,'ssh',?,22,'root','','',?,?)`, id, name, bind, host, now, now)
}

// TestRelinkDeviceAccounts 三种口径都要能补上，且不动已关联的账号、不误连未知地址。
func TestRelinkDeviceAccounts(t *testing.T) {
	s := newTestStore(t)
	now := nowStr()
	execSeed(t, s, `INSERT INTO host(hostname,ip,version,state,last_seen,create_time) VALUES(?,?,?,?,?,?)`,
		"web-01", "10.0.0.11", "CentOS 7", "1", now, now)
	execSeed(t, s, `INSERT INTO host(hostname,ip,version,state,last_seen,create_time) VALUES(?,?,?,?,?,?)`,
		"db-01", "10.0.0.12", "CentOS 7", "1", now, now)

	seedDeviceAccount(t, s, "by-ip", "随便起的名字", "10.0.0.11", "")       // 地址=IP
	seedDeviceAccount(t, s, "by-name", "db-01", "10.9.9.9", "")       // 名称=主机名
	seedDeviceAccount(t, s, "by-hostname", "别的名字", "db-01", "")       // 地址=主机名
	seedDeviceAccount(t, s, "unknown", "未知机器", "10.0.0.99", "")       // 主机表里没有
	seedDeviceAccount(t, s, "manual", "已手工关联", "10.0.0.11", "web-01") // 已有值

	n, err := s.RelinkDeviceAccounts()
	if err != nil {
		t.Fatalf("补关联失败: %v", err)
	}
	if n != 3 {
		t.Fatalf("应补 3 条，实际 %d", n)
	}

	want := map[string]string{
		"by-ip":       "web-01",
		"by-name":     "db-01",
		"by-hostname": "db-01",
		"unknown":     "",
		"manual":      "web-01",
	}
	for id, expect := range want {
		var got string
		if err := s.rdb.QueryRow(`SELECT IFNULL(bind_host,'') FROM device_account WHERE id=?`, id).Scan(&got); err != nil {
			t.Fatalf("查询 %s 失败: %v", id, err)
		}
		if got != expect {
			t.Errorf("%s 的关联主机应为 %q，实际 %q", id, expect, got)
		}
	}

	// 幂等：再跑一次不应再改动
	if again, err := s.RelinkDeviceAccounts(); err != nil || again != 0 {
		t.Fatalf("二次补关联应为 0 且无错，实际 %d, %v", again, err)
	}
}
