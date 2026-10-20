package store

import (
	"database/sql"
	"errors"
	"fmt"
)

/* ---------------- 采集端版本发布 ---------------- */

// VariantLegacy 旧系统兼容版。
//
// Windows 采集端有两种构建：正式版（当前 Go 工具链，官方要求 Windows 10 /
// Server 2016 起）与兼容版（Go 1.20 构建，可跑在 Windows 7 / 8 / 8.1 /
// Server 2008 R2 / 2012 / 2012 R2）。同一版本号、同一平台会有两份产物，
// 所以发布记录必须用 variant 区分，否则后上传的会把先上传的顶掉。
const VariantLegacy = "legacy"

// Release 一条采集端发布记录：某个平台（goos/goarch）的一个版本。
type Release struct {
	ID         string `json:"id"`
	Version    string `json:"version"`
	Goos       string `json:"goos"`
	Goarch     string `json:"goarch"`
	Variant    string `json:"variant"` // "" = 正式版；legacy = 旧系统兼容版
	Filename   string `json:"filename"`
	Path       string `json:"path"` // 二进制在服务端磁盘上的存放路径
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Notes      string `json:"notes"`
	IsLatest   bool   `json:"isLatest"`
	MinVersion string `json:"minVersion"` // 低于该版本的采集端强制升级
	Downloads  int    `json:"downloads"`
	Archived   bool   `json:"archived"` // 归档后不再对新客户端下发
	CreateTime string `json:"createTime"`
}

// schemaRelease 创建版本发布与授权相关数据表。
func (s *Store) schemaRelease() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS release (
			id TEXT PRIMARY KEY, version TEXT, goos TEXT, goarch TEXT,
			variant TEXT DEFAULT '',
			filename TEXT, path TEXT, size INTEGER DEFAULT 0, sha256 TEXT,
			notes TEXT, is_latest INTEGER DEFAULT 0, min_version TEXT,
			downloads INTEGER DEFAULT 0, archived INTEGER DEFAULT 0, create_time TEXT)`,
		// 唯一键只到 (version,goos,goarch)，variant 由 migrateExtra 补列后重建索引。
		// 不能在这里直接写四列：老库此时还没有 variant 列，CREATE INDEX 会失败并
		// 让整个升级启动不起来（建表语句带 variant 只对全新库有效）。
		`CREATE UNIQUE INDEX IF NOT EXISTS uniq_release ON release(version, goos, goarch)`,
		`CREATE INDEX IF NOT EXISTS idx_release_platform ON release(goos, goarch, is_latest)`,
		// 当前授权码，单行存放
		`CREATE TABLE IF NOT EXISTS license (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			key TEXT, customer TEXT, edition TEXT, update_time TEXT)`,
	}
	for _, stmt := range stmts {
		if _, err := s.wdb.Exec(stmt); err != nil {
			return fmt.Errorf("建表失败: %w", err)
		}
	}
	return nil
}

// AddRelease 写入一条发布记录，同版本同平台重复上传时覆盖元数据。
func (s *Store) AddRelease(r *Release) error {
	if r.ID == "" {
		r.ID = newID()
	}
	if r.CreateTime == "" {
		r.CreateTime = nowStr()
	}
	_, err := s.wdb.Exec(`INSERT INTO release
		(id, version, goos, goarch, variant, filename, path, size, sha256, notes, is_latest, min_version, downloads, archived, create_time)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(version, goos, goarch, variant) DO UPDATE SET
			filename=excluded.filename, path=excluded.path, size=excluded.size, sha256=excluded.sha256,
			notes=excluded.notes, min_version=excluded.min_version, create_time=excluded.create_time`,
		r.ID, r.Version, r.Goos, r.Goarch, r.Variant, r.Filename, r.Path, r.Size, r.SHA256, r.Notes,
		boolInt(r.IsLatest), r.MinVersion, r.Downloads, boolInt(r.Archived), r.CreateTime)
	return err
}

const releaseCols = `id, version, IFNULL(goos,''), IFNULL(goarch,''), IFNULL(variant,''), IFNULL(filename,''), IFNULL(path,''),
	IFNULL(size,0), IFNULL(sha256,''), IFNULL(notes,''), IFNULL(is_latest,0), IFNULL(min_version,''),
	IFNULL(downloads,0), IFNULL(archived,0), IFNULL(create_time,'')`

func scanRelease(row interface{ Scan(...any) error }) (*Release, error) {
	var r Release
	var latest, archived int
	if err := row.Scan(&r.ID, &r.Version, &r.Goos, &r.Goarch, &r.Variant, &r.Filename, &r.Path, &r.Size, &r.SHA256,
		&r.Notes, &latest, &r.MinVersion, &r.Downloads, &archived, &r.CreateTime); err != nil {
		return nil, err
	}
	r.IsLatest = latest == 1
	r.Archived = archived == 1
	return &r, nil
}

// ListReleases 返回全部发布记录，按版本倒序、平台排序。
func (s *Store) ListReleases() ([]Release, error) {
	rows, err := s.rdb.Query(`SELECT ` + releaseCols + ` FROM release ORDER BY create_time DESC, version DESC, goos, goarch`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Release{}
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetRelease 按 id 查发布记录。
func (s *Store) GetRelease(id string) (*Release, error) {
	return scanRelease(s.rdb.QueryRow(`SELECT `+releaseCols+` FROM release WHERE id=?`, id))
}

// LatestRelease 返回某平台某变体当前下发的版本：优先取标记为最新的记录，
// 没有标记时退回该平台该变体最新入库的一条。
// variant 传空串即正式版；store.VariantLegacy 为旧系统兼容版。
func (s *Store) LatestRelease(goos, goarch, variant string) (*Release, error) {
	r, err := scanRelease(s.rdb.QueryRow(`SELECT `+releaseCols+`
		FROM release WHERE goos=? AND goarch=? AND IFNULL(variant,'')=?
		  AND IFNULL(archived,0)=0 AND IFNULL(is_latest,0)=1
		ORDER BY create_time DESC LIMIT 1`, goos, goarch, variant))
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	r, err = scanRelease(s.rdb.QueryRow(`SELECT `+releaseCols+`
		FROM release WHERE goos=? AND goarch=? AND IFNULL(variant,'')=? AND IFNULL(archived,0)=0
		ORDER BY create_time DESC LIMIT 1`, goos, goarch, variant))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// GetReleaseByVersion 按平台、变体与版本号查发布记录，供指定版本回滚下载。
func (s *Store) GetReleaseByVersion(goos, goarch, ver, variant string) (*Release, error) {
	r, err := scanRelease(s.rdb.QueryRow(`SELECT `+releaseCols+`
		FROM release WHERE goos=? AND goarch=? AND version=? AND IFNULL(variant,'')=?`,
		goos, goarch, ver, variant))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// SetReleaseLatest 把某条记录标记为「它所在平台 + 它所在变体」的最新版本，
// 同平台同变体其余记录取消标记。
// 兼容版与正式版必须分开算，否则上传兼容版会把正式版从最新位上顶下来。
func (s *Store) SetReleaseLatest(id string) error {
	r, err := s.GetRelease(id)
	if err != nil {
		return err
	}
	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE release SET is_latest=0
		WHERE goos=? AND goarch=? AND IFNULL(variant,'')=?`, r.Goos, r.Goarch, r.Variant); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE release SET is_latest=1, archived=0 WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// BumpReleaseDownload 累加下载次数。
func (s *Store) BumpReleaseDownload(id string) error {
	_, err := s.wdb.Exec(`UPDATE release SET downloads=IFNULL(downloads,0)+1 WHERE id=?`, id)
	return err
}

// DeleteRelease 删除发布记录，返回被删记录以便调用方清理磁盘文件。
func (s *Store) DeleteRelease(id string) (*Release, error) {
	r, err := s.GetRelease(id)
	if err != nil {
		return nil, err
	}
	if _, err := s.wdb.Exec(`DELETE FROM release WHERE id=?`, id); err != nil {
		return nil, err
	}
	return r, nil
}

// ReleaseStats 汇总发布情况，供控制台顶部展示。
func (s *Store) ReleaseStats() (map[string]any, error) {
	var total, latestVersionCount, downloads int
	if err := s.rdb.QueryRow(`SELECT COUNT(1), IFNULL(SUM(downloads),0) FROM release`).Scan(&total, &downloads); err != nil {
		return nil, err
	}
	if err := s.rdb.QueryRow(`SELECT COUNT(DISTINCT version) FROM release`).Scan(&latestVersionCount); err != nil {
		return nil, err
	}
	return map[string]any{"total": total, "versions": latestVersionCount, "downloads": downloads}, nil
}

/* ---------------- 配额统计 ---------------- */

// CountHosts 返回已接入主机数，用于授权配额校验。
func (s *Store) CountHosts() int {
	var n int
	_ = s.rdb.QueryRow(`SELECT COUNT(1) FROM host`).Scan(&n)
	return n
}

// CountMembers 返回控制台账号数，用于授权配额校验。
func (s *Store) CountMembers() int {
	var n int
	_ = s.rdb.QueryRow(`SELECT COUNT(1) FROM user`).Scan(&n)
	return n
}

// HostExists 判断主机是否已登记，新主机才需要占用授权配额。
func (s *Store) HostExists(hostname string) bool {
	var n int
	_ = s.rdb.QueryRow(`SELECT COUNT(1) FROM host WHERE hostname=?`, hostname).Scan(&n)
	return n > 0
}

/* ---------------- 授权码 ---------------- */

// SaveLicense 保存当前授权码（单行覆盖）。
func (s *Store) SaveLicense(key, customer, edition string) error {
	_, err := s.wdb.Exec(`INSERT INTO license(id, key, customer, edition, update_time) VALUES(1,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET key=excluded.key, customer=excluded.customer,
			edition=excluded.edition, update_time=excluded.update_time`, key, customer, edition, nowStr())
	return err
}

// LicenseKey 返回当前授权码原文，未导入时返回空串。
func (s *Store) LicenseKey() string {
	var key string
	_ = s.rdb.QueryRow(`SELECT IFNULL(key,'') FROM license WHERE id=1`).Scan(&key)
	return key
}

// ClearLicense 解除授权，回到社区版。
func (s *Store) ClearLicense() error {
	_, err := s.wdb.Exec(`DELETE FROM license WHERE id=1`)
	return err
}
