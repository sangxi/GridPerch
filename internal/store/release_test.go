package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/sangxi/strix/internal/config"
)

// TestReleaseVariant 同一版本、同一平台要能同时存正式版与旧系统兼容版。
//
// 唯一索引以前只有 (version,goos,goarch)，上传兼容版会把正式版顶掉；
// 现在多一列 variant，两种产物并存，且「设为最新」只影响同一变体。
func TestReleaseVariant(t *testing.T) {
	st, err := Open(&config.Server{DBPath: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer st.Close()

	modern := &Release{Version: "1.2.3", Goos: "windows", Goarch: "amd64",
		Filename: "strix-agent-windows-amd64.exe", Path: "a"}
	legacy := &Release{Version: "1.2.3", Goos: "windows", Goarch: "amd64", Variant: VariantLegacy,
		Filename: "strix-agent-windows-amd64-legacy.exe", Path: "b"}
	for _, r := range []*Release{modern, legacy} {
		if err := st.AddRelease(r); err != nil {
			t.Fatalf("写入 %s 失败: %v", r.Filename, err)
		}
	}

	m, err := st.GetReleaseByVersion("windows", "amd64", "1.2.3", "")
	if err != nil || m == nil {
		t.Fatalf("正式版丢失: %v", err)
	}
	lg, err := st.GetReleaseByVersion("windows", "amd64", "1.2.3", VariantLegacy)
	if err != nil || lg == nil {
		t.Fatalf("兼容版丢失: %v", err)
	}

	// 把兼容版设为最新，正式版的 is_latest 不能被清掉
	if err := st.SetReleaseLatest(lg.ID); err != nil {
		t.Fatalf("设为最新失败: %v", err)
	}
	if err := st.SetReleaseLatest(m.ID); err != nil {
		t.Fatalf("设为最新失败: %v", err)
	}
	got, err := st.LatestRelease("windows", "amd64", VariantLegacy)
	if err != nil || got == nil || got.Variant != VariantLegacy {
		t.Fatalf("兼容版应仍是兼容版的最新记录: %v %+v", err, got)
	}
	if got, _ := st.LatestRelease("windows", "amd64", ""); got == nil || got.Variant != "" {
		t.Fatalf("正式版应仍是正式版的最新记录: %+v", got)
	}
}

// TestReleaseVariantMigration 老库升级：release 表建成时还没有 variant 列，
// 唯一索引也只有三列。升级路径必须先补列、再重建索引，
// 且不能因为「建索引时列还不存在」而让整个服务起不来。
func TestReleaseVariantMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// 造一个 1.2.2 时代的库
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS release (
		id TEXT PRIMARY KEY, version TEXT, goos TEXT, goarch TEXT,
		filename TEXT, path TEXT, size INTEGER DEFAULT 0, sha256 TEXT,
		notes TEXT, is_latest INTEGER DEFAULT 0, min_version TEXT,
		downloads INTEGER DEFAULT 0, archived INTEGER DEFAULT 0, create_time TEXT)`); err != nil {
		t.Fatalf("建旧表失败: %v", err)
	}
	if _, err := raw.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uniq_release ON release(version, goos, goarch)`); err != nil {
		t.Fatalf("建旧索引失败: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO release (id,version,goos,goarch,filename,path,is_latest,create_time)
		VALUES ('old1','1.2.2','windows','amd64','strix-agent-windows-amd64.exe','x',1,'2026-10-01 00:00:00')`); err != nil {
		t.Fatalf("写入旧记录失败: %v", err)
	}
	_ = raw.Close()

	st, err := Open(&config.Server{DBPath: path})
	if err != nil {
		t.Fatalf("升级打开失败（很可能索引重建顺序有问题）: %v", err)
	}
	defer st.Close()

	// 老记录必须还在，且默认算正式版
	old, err := st.GetReleaseByVersion("windows", "amd64", "1.2.2", "")
	if err != nil || old == nil {
		t.Fatalf("升级后老记录丢失: %v", err)
	}
	// 升级后同一版本能再存一份兼容版
	if err := st.AddRelease(&Release{Version: "1.2.2", Goos: "windows", Goarch: "amd64",
		Variant: VariantLegacy, Filename: "legacy.exe", Path: "y"}); err != nil {
		t.Fatalf("升级后写入兼容版失败（唯一索引没有带上 variant）: %v", err)
	}
}
