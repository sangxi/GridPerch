package store

import "testing"

// seedAutoHost 写入一台主机，version_detail 形如 "linux / 5.15.0"。
func seedAutoHost(t *testing.T, s *Store, host, detail, version string) {
	t.Helper()
	now := nowStr()
	execSeed(t, s, `INSERT INTO host(hostname,ip,version,version_detail,state,last_seen,create_time)
		VALUES(?,?,?,?,?,?,?)`, host, "10.0.0.1", version, detail, "1", now, now)
}

// TestAutoAssignCatalogByOSOnlyUngrouped 验证默认模式只碰「从未归类」的主机：
// 人工归好类的主机不能被自动分组冲掉，这是运维整理结果不被覆盖的底线。
func TestAutoAssignCatalogByOSOnlyUngrouped(t *testing.T) {
	s := newTestStore(t)
	seedAutoHost(t, s, "win1", "windows / 10.0.17763", "Windows Server 2019")
	seedAutoHost(t, s, "lin1", "linux / 5.15.0", "CentOS 7")
	seedAutoHost(t, s, "mac1", "darwin / 23.0.0", "macOS 14")
	seedAutoHost(t, s, "bsd1", "freebsd / 13.2", "FreeBSD 13")

	// 人工把 lin1 归到「业务A」，自动分组必须跳过它
	manual := &HostCatalog{Name: "业务A", Sort: 9}
	if err := s.SaveHostCatalog(manual); err != nil {
		t.Fatalf("创建人工目录失败: %v", err)
	}
	if err := s.SetHostCatalog("lin1", manual.ID); err != nil {
		t.Fatalf("人工归类失败: %v", err)
	}

	res, err := s.AutoAssignCatalogByOS(false)
	if err != nil {
		t.Fatalf("自动分组失败: %v", err)
	}
	// lin1 已人工归类，「Linux」桶本次没有主机要归入，就不该凭空建出空目录
	if res.CatalogsCreated != 3 {
		t.Fatalf("应新建 3 个系统目录（Windows/macOS/其他），实际 %d", res.CatalogsCreated)
	}
	if res.HostsAssigned != 3 || res.HostsSkipped != 1 {
		t.Fatalf("归组数量不符：assigned=%d skipped=%d", res.HostsAssigned, res.HostsSkipped)
	}

	refs, err := s.HostCatalogMap()
	if err != nil {
		t.Fatalf("读取归属失败: %v", err)
	}
	want := map[string]string{"win1": "Windows", "mac1": "macOS", "bsd1": "其他", "lin1": "业务A"}
	for host, name := range want {
		if refs[host].Name != name {
			t.Errorf("%s 应归入 %s，实际 %q", host, name, refs[host].Name)
		}
	}

	// 再跑一次应是幂等的：没有新主机需要归组
	again, err := s.AutoAssignCatalogByOS(false)
	if err != nil {
		t.Fatalf("二次自动分组失败: %v", err)
	}
	if again.HostsAssigned != 0 {
		t.Fatalf("二次执行不应再改动主机，实际 %d", again.HostsAssigned)
	}
}

// TestAutoAssignCatalogByOSOverwrite 验证覆盖模式会把人工分类也改成系统目录。
func TestAutoAssignCatalogByOSOverwrite(t *testing.T) {
	s := newTestStore(t)
	seedAutoHost(t, s, "win1", "windows / 10.0.19045", "Windows 10")
	seedAutoHost(t, s, "lin1", "linux / 6.1.0", "Ubuntu 22.04")

	manual := &HostCatalog{Name: "生产环境"}
	if err := s.SaveHostCatalog(manual); err != nil {
		t.Fatalf("创建人工目录失败: %v", err)
	}
	if err := s.SetHostCatalog("win1", manual.ID); err != nil {
		t.Fatalf("人工归类失败: %v", err)
	}

	res, err := s.AutoAssignCatalogByOS(true)
	if err != nil {
		t.Fatalf("覆盖分组失败: %v", err)
	}
	if res.HostsAssigned != 2 {
		t.Fatalf("覆盖模式应处理全部主机，实际 %d", res.HostsAssigned)
	}
	refs, _ := s.HostCatalogMap()
	if refs["win1"].Name != "Windows" || refs["lin1"].Name != "Linux" {
		t.Fatalf("覆盖后归属不对：win1=%q lin1=%q", refs["win1"].Name, refs["lin1"].Name)
	}
}

// TestAutoAssignCatalogByOSReuseExisting 现场已经有「Windows系统」「linux系统」这类
// 手建目录时，必须复用它们，不能另建一套 Windows / Linux 让目录列表出现两套同义项。
func TestAutoAssignCatalogByOSReuseExisting(t *testing.T) {
	s := newTestStore(t)
	seedAutoHost(t, s, "win1", "windows / 10.0.17763", "Windows Server 2019")
	seedAutoHost(t, s, "lin1", "linux / 5.15.0", "CentOS 7")

	winCat := &HostCatalog{Name: "Windows系统"}
	if err := s.SaveHostCatalog(winCat); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	linCat := &HostCatalog{Name: "linux系统"}
	if err := s.SaveHostCatalog(linCat); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}

	res, err := s.AutoAssignCatalogByOS(false)
	if err != nil {
		t.Fatalf("自动分组失败: %v", err)
	}
	if res.CatalogsCreated != 0 {
		t.Fatalf("已有同义目录时不应新建，实际新建 %d", res.CatalogsCreated)
	}
	refs, _ := s.HostCatalogMap()
	if refs["win1"].CatalogID != winCat.ID {
		t.Errorf("win1 应归入「Windows系统」，实际 %q", refs["win1"].Name)
	}
	if refs["lin1"].CatalogID != linCat.ID {
		t.Errorf("lin1 应归入「linux系统」，实际 %q", refs["lin1"].Name)
	}
}

// TestAutoAssignCatalogByOSFillsUngrouped 回归：目录被删或主机被移回「未分组」后，
// host_catalog_ref 里还留着 catalog_id=” 的空记录，这类主机必须仍能被归档，
// 否则「删过目录」的机器会永远停在未分组。
func TestAutoAssignCatalogByOSFillsUngrouped(t *testing.T) {
	s := newTestStore(t)
	seedAutoHost(t, s, "lin1", "linux / 5.15.0", "CentOS 7")
	cat := &HostCatalog{Name: "linux系统"}
	if err := s.SaveHostCatalog(cat); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := s.SetHostCatalog("lin1", cat.ID); err != nil {
		t.Fatalf("归类失败: %v", err)
	}
	if err := s.DeleteHostCatalog(cat.ID); err != nil {
		t.Fatalf("删除目录失败: %v", err)
	}

	res, err := s.AutoAssignCatalogByOS(false)
	if err != nil {
		t.Fatalf("自动分组失败: %v", err)
	}
	if res.HostsAssigned != 1 {
		t.Fatalf("未分组主机应被重新归档，实际归组 %d 台", res.HostsAssigned)
	}
	refs, _ := s.HostCatalogMap()
	if refs["lin1"].Name != "Linux" {
		t.Fatalf("应归入 Linux，实际 %q", refs["lin1"].Name)
	}
}

// TestMatchCatalogAlias 别名匹配的边界：短别名只认完全相等，避免误认领。
func TestMatchCatalogAlias(t *testing.T) {
	cases := []struct {
		bucket, name string
		want         bool
	}{
		{"Windows", "Windows系统", true},
		{"Windows", "windows 系统", true},
		{"Windows", "Windows-生产", true},
		{"Windows", "win", true},
		{"Windows", "macOS", false},
		{"Linux", "linux系统", true},
		{"Linux", "Linux 测试环境", true},
		{"macOS", "macOS", true},
		{"macOS", "mac地址管理", false}, // 短别名 mac 不应命中
		{"其他", "其他系统", true},
		{"其他", "业务A", false},
	}
	for _, c := range cases {
		if got := matchCatalogAlias(c.bucket, c.name); got != c.want {
			t.Errorf("matchCatalogAlias(%q,%q)=%v，期望 %v", c.bucket, c.name, got, c.want)
		}
	}
}

// TestCatalogNameOfOS 兜底规则：未知系统一律进「其他」，不能出现空目录名。
func TestCatalogNameOfOS(t *testing.T) {
	cases := map[string]string{
		"windows": "Windows",
		"linux":   "Linux",
		"darwin":  "macOS",
		"freebsd": "其他",
		"":        "其他",
		"aix":     "其他",
	}
	for in, want := range cases {
		if got := catalogNameOfOS(in); got != want {
			t.Errorf("catalogNameOfOS(%q)=%q，期望 %q", in, got, want)
		}
	}
}
