package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// 主机改名 / 删除相关的错误，供上层翻译成用户能看懂的提示。
var (
	ErrHostNameEmpty = errors.New("主机名不能为空")
	ErrHostNotFound  = errors.New("主机不存在")
	ErrHostNameTaken = errors.New("目标主机名已被其他主机占用")
	ErrHostNameSame  = errors.New("新旧主机名相同")
)

// hostnameRefTables 以 hostname 作为主键或普通列的业务表。
// 改名时整体迁移、删除时整体清理，避免留下孤儿数据。
//
// 注意：这里只收「监控主机」语义的表。device_account.host 存的是被管设备的
// SSH/WinRM 地址，asset.ip / asset.mac 也不是主机名，都不属于主机身份，不参与迁移。
var hostnameRefTables = []string{
	"host",
	"host_extra",
	"host_catalog_ref",
	"agent_config",
	"system_info",
	"cpu_state",
	"mem_state",
	"desk_state",
	"netio_state",
	"diskio_state",
	"sys_load_state",
	"alert",
	// 静默规则的 hostname 为空表示「全部主机」，迁移时空值天然不会命中，
	// 不影响通配规则；但如果漏了这张表，按主机设的静默会在改名后失效。
	"alert_silence",
	"process_state",
	"monitor_state",
	"monitor_event",
	"login_event",
	"sys_event",
}

// schemaHostAdmin 建立主机改名的映射表。
//
// 为什么需要映射表：主机名由采集端 `-name` 启动参数决定并随每次上报携带，
// 平台无法远端修改已部署主机上的服务参数。所以「改名」在平台侧拆成两步：
//  1. 把该主机名下的全部数据级联迁移到新名字（用户看到的就是新名字）；
//  2. 记下「旧名 → 新名」的映射，采集端后续仍以旧名上报时自动归到新名字下。
//
// 这样任何已部署版本的采集端都不需要改动或重装即可平滑改名。
func (s *Store) schemaHostAdmin() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS host_rename (
			from_name TEXT PRIMARY KEY,
			to_name TEXT NOT NULL,
			create_time TEXT)`,
	}
	for _, stmt := range stmts {
		if _, err := s.wdb.Exec(stmt); err != nil {
			return fmt.Errorf("建表失败: %w", err)
		}
	}
	return nil
}

// hostCount 返回 host 表中该主机名的行数（0/1），用于改名/删除前的存在性与占用校验。
func (s *Store) hostCount(name string) (int, error) {
	var n int
	if err := s.rdb.QueryRow(`SELECT COUNT(1) FROM host WHERE hostname=?`, name).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ResolveHostname 把采集端可能仍在使用的旧主机名解析为当前主机名。
// 没有映射时原样返回；链式改名（A→B→C）最多回溯 8 跳，防止脏数据造成死循环。
// 任何查询异常都 fail-open（返回原值），宁可少数主机名没归位，也不能丢上报。
func (s *Store) ResolveHostname(name string) string {
	if name == "" {
		return name
	}
	cur := name
	for i := 0; i < 8; i++ {
		var next string
		if err := s.rdb.QueryRow(`SELECT to_name FROM host_rename WHERE from_name = ?`, cur).Scan(&next); err != nil {
			return cur
		}
		next = strings.TrimSpace(next)
		if next == "" || next == cur {
			return cur
		}
		cur = next
	}
	return cur
}

// RenameHost 把 oldName 主机的全部数据迁移到 newName。
//
// 迁移内容：
//   - 所有以 hostname 为键的业务表（指标、告警、系统事件、目录归属、采集端配置…）
//   - command.host（指令队列 / 历史）
//   - monitor_task.hosts（监控项的「作用主机」逗号分隔名单）
//   - asset.monitor_bind（资产管理里手填的「关联监控主机」）
//   - host_rename 映射（写入 旧名→新名，并修正原本指向旧名的映射，避免断链）
func (s *Store) RenameHost(oldName, newName string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" || newName == "" {
		return ErrHostNameEmpty
	}
	if oldName == newName {
		return ErrHostNameSame
	}
	// 目标名必须空闲，否则两主机的指标会互相覆盖
	switch n, err := s.hostCount(oldName); {
	case err != nil:
		return err
	case n == 0:
		return ErrHostNotFound
	}
	if n, err := s.hostCount(newName); err != nil {
		return err
	} else if n > 0 {
		return ErrHostNameTaken
	}

	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, t := range hostnameRefTables {
		if _, err := tx.Exec(`UPDATE `+t+` SET hostname=? WHERE hostname=?`, newName, oldName); err != nil {
			return fmt.Errorf("迁移 %s 数据失败: %w", t, err)
		}
	}
	if _, err := tx.Exec(`UPDATE command SET host=? WHERE host=?`, newName, oldName); err != nil {
		return fmt.Errorf("迁移指令记录失败: %w", err)
	}
	if _, err := tx.Exec(`UPDATE asset SET monitor_bind=? WHERE monitor_bind=?`, newName, oldName); err != nil {
		return fmt.Errorf("更新资产关联失败: %w", err)
	}
	if err := rewriteTaskHosts(tx, oldName, newName); err != nil {
		return err
	}
	// 写入 旧名→新名 映射；采集端继续以旧名上报时据此自动归位
	if _, err := tx.Exec(`DELETE FROM host_rename WHERE from_name=?`, oldName); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO host_rename(from_name,to_name,create_time) VALUES(?,?,?)`,
		oldName, newName, nowStr()); err != nil {
		return err
	}
	// 原本指向旧名的映射（X→oldName）改指向新名，避免链式改名后解析断链
	if _, err := tx.Exec(`UPDATE host_rename SET to_name=? WHERE to_name=?`, newName, oldName); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteHost 删除主机及其全部历史数据。
//
// 注意：主机名由采集端决定，若该主机的采集端仍在运行，它会在下次上报时
// 被重新发现并“复活”。这是符合直觉的行为——有活着的采集端就是在线主机。
// 要彻底不再出现，需要先在目标主机上停用/卸载采集端。
func (s *Store) DeleteHost(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrHostNameEmpty
	}
	switch n, err := s.hostCount(name); {
	case err != nil:
		return err
	case n == 0:
		return ErrHostNotFound
	}

	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, t := range hostnameRefTables {
		if _, err := tx.Exec(`DELETE FROM `+t+` WHERE hostname=?`, name); err != nil {
			return fmt.Errorf("清理 %s 数据失败: %w", t, err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM command WHERE host=?`, name); err != nil {
		return fmt.Errorf("清理指令记录失败: %w", err)
	}
	if _, err := tx.Exec(`UPDATE asset SET monitor_bind='' WHERE monitor_bind=?`, name); err != nil {
		return fmt.Errorf("解除资产关联失败: %w", err)
	}
	if err := rewriteTaskHosts(tx, name, ""); err != nil {
		return err
	}
	// 映射一并清掉：否则旧名上报会被解析到一台已删除的主机上、把它凭空建回来
	if _, err := tx.Exec(`DELETE FROM host_rename WHERE from_name=? OR to_name=?`, name, name); err != nil {
		return err
	}
	return tx.Commit()
}

// rewriteTaskHosts 改写监控项的「作用主机」名单（逗号分隔，'*' 表示全部主机）。
// newName 为空表示把该主机从名单里摘掉（删除主机时用）。
func rewriteTaskHosts(tx *sql.Tx, oldName, newName string) error {
	rows, err := tx.Query(`SELECT id, IFNULL(hosts,'') FROM monitor_task WHERE IFNULL(hosts,'') <> ''`)
	if err != nil {
		return err
	}
	type pending struct{ id, hosts string }
	var updates []pending
	for rows.Next() {
		var id, hosts string
		if err := rows.Scan(&id, &hosts); err != nil {
			rows.Close()
			return err
		}
		parts := strings.Split(hosts, ",")
		kept := make([]string, 0, len(parts))
		changed := false
		for _, p := range parts {
			if strings.TrimSpace(p) != oldName {
				kept = append(kept, p)
				continue
			}
			changed = true
			if newName != "" {
				kept = append(kept, newName)
			}
		}
		if changed {
			updates = append(updates, pending{id, strings.Join(kept, ",")})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, u := range updates {
		if _, err := tx.Exec(`UPDATE monitor_task SET hosts=? WHERE id=?`, u.hosts, u.id); err != nil {
			return fmt.Errorf("更新监控项作用主机失败: %w", err)
		}
	}
	return nil
}
