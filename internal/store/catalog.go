package store

import (
	"database/sql"
	"errors"
	"strings"
)

// 主机目录相关的可识别错误，HTTP 层据此映射状态码。
var (
	errCatalogNameEmpty = errors.New("目录名称不能为空")
	errCatalogNameDup   = errors.New("目录名称已存在")
	errCatalogNotFound  = errors.New("主机目录不存在")
)

// CatalogError 判断 err 是否为主机目录相关错误，便于 HTTP 层返回 400/404。
func CatalogError(err error) bool {
	return errors.Is(err, errCatalogNameEmpty) || errors.Is(err, errCatalogNameDup) ||
		errors.Is(err, errCatalogNotFound)
}

// HostCatalog 主机目录：用于按业务/机房/环境给主机分组，名称支持中文。
//
// 目录只做归类，不影响采集与告警；主机以 hostname 为主键，
// catalog_id 为空表示未归类（控制台显示在「未分组」下）。
type HostCatalog struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Remark     string `json:"remark"`
	Sort       int    `json:"sort"`
	HostCount  int    `json:"hostCount"`
	CreateTime string `json:"createTime"`
	UpdateTime string `json:"updateTime"`
}

// HostCatalogRef 主机在目录中的归属。
type HostCatalogRef struct {
	Hostname  string `json:"hostname"`
	CatalogID string `json:"catalogId"`
	Name      string `json:"name"`
}

// schemaCatalog 主机目录相关建表语句。
func (s *Store) schemaCatalog() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS host_catalog (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			remark TEXT DEFAULT '',
			sort INTEGER DEFAULT 0,
			create_time TEXT, update_time TEXT)`,
		// 目录被删除时把主机归为未分组，而不是连带删主机
		`CREATE TABLE IF NOT EXISTS host_catalog_ref (
			hostname TEXT PRIMARY KEY,
			catalog_id TEXT DEFAULT '',
			update_time TEXT)`,
		`CREATE INDEX IF NOT EXISTS idx_catalog_ref_cat ON host_catalog_ref(catalog_id)`,
	}
	for _, st := range stmts {
		if _, err := s.wdb.Exec(st); err != nil {
			return err
		}
	}
	return nil
}

// ListHostCatalogs 返回全部目录，按 sort、名称排序，带各目录下的主机数。
func (s *Store) ListHostCatalogs() ([]HostCatalog, error) {
	rows, err := s.rdb.Query(`SELECT c.id, c.name, IFNULL(c.remark,''), IFNULL(c.sort,0),
		IFNULL(c.create_time,''), IFNULL(c.update_time,''),
		(SELECT COUNT(1) FROM host_catalog_ref r JOIN host h ON h.hostname = r.hostname
		  WHERE r.catalog_id = c.id)
		FROM host_catalog c ORDER BY IFNULL(c.sort,0), c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostCatalog
	for rows.Next() {
		var c HostCatalog
		if err := rows.Scan(&c.ID, &c.Name, &c.Remark, &c.Sort, &c.CreateTime, &c.UpdateTime, &c.HostCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetHostCatalog 返回单个目录，不存在时返回 nil。
func (s *Store) GetHostCatalog(id string) (*HostCatalog, error) {
	var c HostCatalog
	err := s.rdb.QueryRow(`SELECT id, name, IFNULL(remark,''), IFNULL(sort,0),
		IFNULL(create_time,''), IFNULL(update_time,'')
		FROM host_catalog WHERE id=?`, id).
		Scan(&c.ID, &c.Name, &c.Remark, &c.Sort, &c.CreateTime, &c.UpdateTime)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// SaveHostCatalog 新建或更新目录（传 ID 为空则新建）。
// 目录名去空白后不能为空，且不允许重名（同一名字会让人在控制台上分不清）。
func (s *Store) SaveHostCatalog(c *HostCatalog) error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return errCatalogNameEmpty
	}
	if c.ID == "" {
		c.ID = newID()
	}
	var dup int
	if err := s.rdb.QueryRow(`SELECT COUNT(1) FROM host_catalog WHERE name=? AND id<>?`, c.Name, c.ID).Scan(&dup); err != nil {
		return err
	}
	if dup > 0 {
		return errCatalogNameDup
	}
	now := nowStr()
	if c.CreateTime == "" {
		c.CreateTime = now
	}
	c.UpdateTime = now
	_, err := s.wdb.Exec(`INSERT INTO host_catalog(id, name, remark, sort, create_time, update_time)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, remark=excluded.remark,
		sort=excluded.sort, update_time=excluded.update_time`,
		c.ID, c.Name, c.Remark, c.Sort, c.CreateTime, c.UpdateTime)
	return err
}

// DeleteHostCatalog 删除目录，其下主机回落为「未分组」。
func (s *Store) DeleteHostCatalog(id string) error {
	if _, err := s.wdb.Exec(`UPDATE host_catalog_ref SET catalog_id='', update_time=? WHERE catalog_id=?`,
		nowStr(), id); err != nil {
		return err
	}
	_, err := s.wdb.Exec(`DELETE FROM host_catalog WHERE id=?`, id)
	return err
}

// HostCatalogMap 返回 hostname -> 目录 ID 与目录名的映射。
// 单次取全量，避免主机列表逐台查库。
func (s *Store) HostCatalogMap() (map[string]HostCatalogRef, error) {
	rows, err := s.rdb.Query(`SELECT r.hostname, IFNULL(r.catalog_id,''), IFNULL(c.name,'')
		FROM host_catalog_ref r LEFT JOIN host_catalog c ON c.id = r.catalog_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]HostCatalogRef{}
	for rows.Next() {
		var ref HostCatalogRef
		if err := rows.Scan(&ref.Hostname, &ref.CatalogID, &ref.Name); err != nil {
			return nil, err
		}
		out[ref.Hostname] = ref
	}
	return out, rows.Err()
}

// SetHostCatalog 给单台主机指定目录，目录不存在时返回错误。
func (s *Store) SetHostCatalog(hostname, catalogID string) error {
	if catalogID != "" {
		if c, err := s.GetHostCatalog(catalogID); err != nil {
			return err
		} else if c == nil {
			return errCatalogNotFound
		}
	}
	_, err := s.wdb.Exec(`INSERT INTO host_catalog_ref(hostname, catalog_id, update_time)
		VALUES(?,?,?)
		ON CONFLICT(hostname) DO UPDATE SET catalog_id=excluded.catalog_id, update_time=excluded.update_time`,
		hostname, catalogID, nowStr())
	return err
}

// SetHostCatalogBatch 批量移动主机，用于目录页的多选操作。
func (s *Store) SetHostCatalogBatch(hostnames []string, catalogID string) error {
	if catalogID != "" {
		if c, err := s.GetHostCatalog(catalogID); err != nil {
			return err
		} else if c == nil {
			return errCatalogNotFound
		}
	}
	for _, h := range hostnames {
		if strings.TrimSpace(h) == "" {
			continue
		}
		if err := s.SetHostCatalog(h, catalogID); err != nil {
			return err
		}
	}
	return nil
}
