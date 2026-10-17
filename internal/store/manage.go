package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/sangxi/strix/internal/model"
)

// serverRunner 表示该监控项由服务端主动探测，而非下发到 agent 执行。
const serverRunner = "server"

const agentRunner = "agent"

// schemaExtra 创建运维管理类数据表。
func (s *Store) schemaExtra() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS asset (
			id TEXT PRIMARY KEY, name TEXT, category TEXT, type TEXT,
			ip TEXT, mac TEXT, os TEXT, spec TEXT, vendor TEXT, sn TEXT,
			location TEXT, owner TEXT, department TEXT,
			buy_date TEXT, expire_date TEXT, status TEXT,
			monitor_bind TEXT, remark TEXT, create_time TEXT, update_time TEXT)`,
		`CREATE INDEX IF NOT EXISTS idx_asset_ip ON asset(ip)`,
		`CREATE TABLE IF NOT EXISTS device_account (
			id TEXT PRIMARY KEY, name TEXT, asset_id TEXT, protocol TEXT,
			host TEXT, port INTEGER DEFAULT 22, username TEXT, secret TEXT,
			remark TEXT, create_time TEXT, update_time TEXT)`,
		`CREATE TABLE IF NOT EXISTS command (
			id TEXT PRIMARY KEY, host TEXT, cmd TEXT, timeout_sec INTEGER DEFAULT 30,
			source TEXT, status TEXT, exit_code INTEGER DEFAULT 0, output TEXT,
			error_msg TEXT, user TEXT, create_time TEXT, finish_time TEXT)`,
		`CREATE INDEX IF NOT EXISTS idx_command_host ON command(host, status)`,
		`CREATE TABLE IF NOT EXISTS api_token (
			id TEXT PRIMARY KEY, name TEXT, token TEXT, perms TEXT,
			enabled INTEGER DEFAULT 1, last_used TEXT, create_time TEXT)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uniq_api_token ON api_token(token)`,
		`CREATE TABLE IF NOT EXISTS inspection (
			id TEXT PRIMARY KEY, title TEXT, scope TEXT, author TEXT,
			summary TEXT, content TEXT, score INTEGER DEFAULT 0, create_time TEXT)`,
		`CREATE TABLE IF NOT EXISTS settings (
			k TEXT PRIMARY KEY, v TEXT, update_time TEXT)`,
		`CREATE TABLE IF NOT EXISTS login_event (
			id TEXT PRIMARY KEY, hostname TEXT, users TEXT, diff TEXT, create_time TEXT)`,
		`CREATE INDEX IF NOT EXISTS idx_login_event ON login_event(hostname, create_time)`,
		// 操作审计：记录控制台每一次写操作的账号、来源 IP 与结果
		`CREATE TABLE IF NOT EXISTS audit_log (
			id TEXT PRIMARY KEY, username TEXT, role TEXT, action TEXT, category TEXT,
			method TEXT, path TEXT, target TEXT, detail TEXT, ip TEXT, user_agent TEXT,
			result TEXT, status_code INTEGER DEFAULT 0, duration_ms INTEGER DEFAULT 0,
			create_time TEXT)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_time ON audit_log(create_time)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_log(username, create_time)`,
		// 邮件发送记录，便于确认告警邮件到底有没有发出去
		`CREATE TABLE IF NOT EXISTS mail_log (
			id TEXT PRIMARY KEY, kind TEXT, subject TEXT, recipients TEXT,
			item_count INTEGER DEFAULT 0, status TEXT, error TEXT, create_time TEXT)`,
		`CREATE INDEX IF NOT EXISTS idx_mail_log_time ON mail_log(create_time)`,
	}
	for _, stmt := range stmts {
		if _, err := s.wdb.Exec(stmt); err != nil {
			return fmt.Errorf("建表失败: %w", err)
		}
	}
	return nil
}

// migrateExtra 为既有表补充新版本新增的列。
func (s *Store) migrateExtra() error {
	adds := []struct {
		table string
		col   string
		ddl   string
	}{
		{"monitor_task", "runner", "ALTER TABLE monitor_task ADD COLUMN runner TEXT DEFAULT 'agent'"},
		{"monitor_task", "heal_cmd", "ALTER TABLE monitor_task ADD COLUMN heal_cmd TEXT"},
		{"monitor_task", "auto_heal", "ALTER TABLE monitor_task ADD COLUMN auto_heal INTEGER DEFAULT 0"},
		{"user", "role", "ALTER TABLE user ADD COLUMN role TEXT DEFAULT 'admin'"},
		{"user", "nickname", "ALTER TABLE user ADD COLUMN nickname TEXT"},
		{"user", "email", "ALTER TABLE user ADD COLUMN email TEXT"},
		{"user", "phone", "ALTER TABLE user ADD COLUMN phone TEXT"},
		{"user", "menus", "ALTER TABLE user ADD COLUMN menus TEXT"},
		{"user", "enabled", "ALTER TABLE user ADD COLUMN enabled INTEGER DEFAULT 1"},
		{"user", "last_login", "ALTER TABLE user ADD COLUMN last_login TEXT"},
		{"user", "last_login_ip", "ALTER TABLE user ADD COLUMN last_login_ip TEXT"},
		{"user", "login_count", "ALTER TABLE user ADD COLUMN login_count INTEGER DEFAULT 0"},
	}
	for _, a := range adds {
		exist, err := s.hasColumn(a.table, a.col)
		if err != nil {
			return err
		}
		if exist {
			continue
		}
		if _, err := s.wdb.Exec(a.ddl); err != nil {
			return fmt.Errorf("补充列 %s.%s 失败: %w", a.table, a.col, err)
		}
	}
	// 保证管理员拥有全部权限
	if _, err := s.wdb.Exec(`UPDATE user SET role='admin', enabled=1 WHERE role IS NULL OR role=''`); err != nil {
		return err
	}
	return nil
}

func (s *Store) hasColumn(table, col string) (bool, error) {
	rows, err := s.wdb.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var def sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &def, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, col) {
			return true, nil
		}
	}
	return false, nil
}

/* ---------------- 系统设置 ---------------- */

// GetSetting 读取配置项，不存在返回空串。
func (s *Store) GetSetting(key string) string {
	var v string
	_ = s.rdb.QueryRow(`SELECT v FROM settings WHERE k=?`, key).Scan(&v)
	return v
}

// Settings 返回全部配置项。
func (s *Store) Settings() (map[string]string, error) {
	rows, err := s.rdb.Query(`SELECT k, v FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// SaveSettings 批量写入配置项。
func (s *Store) SaveSettings(kv map[string]string) error {
	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO settings(k,v,update_time) VALUES(?,?,?)
		ON CONFLICT(k) DO UPDATE SET v=excluded.v, update_time=excluded.update_time`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := nowStr()
	for k, v := range kv {
		if k == "" {
			continue
		}
		if _, err := stmt.Exec(k, v, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

/* ---------------- 成员账号与权限 ---------------- */

// ListMembers 返回全部成员账号。
func (s *Store) ListMembers() ([]model.Member, error) {
	rows, err := s.rdb.Query(`SELECT username, IFNULL(role,'viewer'), IFNULL(nickname,''), IFNULL(email,''),
		IFNULL(phone,''), IFNULL(menus,''), IFNULL(enabled,1), IFNULL(last_login,''),
		IFNULL(last_login_ip,''), IFNULL(login_count,0), IFNULL(create_time,'')
		FROM user ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.Member
	for rows.Next() {
		var m model.Member
		var enabled int
		if err := rows.Scan(&m.Username, &m.Role, &m.Nickname, &m.Email, &m.Phone, &m.Menus, &enabled,
			&m.LastLogin, &m.LastLoginIP, &m.LoginCount, &m.CreateTime); err != nil {
			return nil, err
		}
		m.Enabled = enabled == 1
		list = append(list, m)
	}
	return list, rows.Err()
}

// GetMember 读取单个成员及其权限。
func (s *Store) GetMember(username string) (*model.Member, error) {
	var m model.Member
	var enabled int
	err := s.rdb.QueryRow(`SELECT username, IFNULL(role,'viewer'), IFNULL(nickname,''), IFNULL(email,''),
		IFNULL(phone,''), IFNULL(menus,''), IFNULL(enabled,1), IFNULL(last_login,''),
		IFNULL(last_login_ip,''), IFNULL(login_count,0), IFNULL(create_time,'')
		FROM user WHERE username=?`, username).
		Scan(&m.Username, &m.Role, &m.Nickname, &m.Email, &m.Phone, &m.Menus, &enabled,
			&m.LastLogin, &m.LastLoginIP, &m.LoginCount, &m.CreateTime)
	if err != nil {
		return nil, err
	}
	m.Enabled = enabled == 1
	return &m, nil
}

// CreateMember 新建成员账号。
func (s *Store) CreateMember(username, password string, m *model.Member) error {
	if username == "" || password == "" {
		return errors.New("用户名和密码不能为空")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	_, err := s.wdb.Exec(`INSERT INTO user(username, pass_hash, salt, role, nickname, email, phone, menus, enabled, create_time)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, username, hashPassword(password, hex.EncodeToString(salt)),
		hex.EncodeToString(salt), m.Role, m.Nickname, m.Email, m.Phone, m.Menus, boolInt(m.Enabled), nowStr())
	return err
}

// UpdateMember 更新成员资料与权限，密码为空表示不修改。
func (s *Store) UpdateMember(username, password string, m *model.Member) error {
	if password != "" {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return err
		}
		if _, err := s.wdb.Exec(`UPDATE user SET pass_hash=?, salt=? WHERE username=?`,
			hashPassword(password, hex.EncodeToString(salt)), hex.EncodeToString(salt), username); err != nil {
			return err
		}
	}
	_, err := s.wdb.Exec(`UPDATE user SET role=?, nickname=?, email=?, phone=?, menus=?, enabled=? WHERE username=?`,
		m.Role, m.Nickname, m.Email, m.Phone, m.Menus, boolInt(m.Enabled), username)
	return err
}

// DeleteMember 删除成员账号，管理员账号不可删除。
func (s *Store) DeleteMember(username string) error {
	if username == s.cfg.AdminUser {
		return errors.New("初始管理员账号不可删除")
	}
	_, err := s.wdb.Exec(`DELETE FROM user WHERE username=?`, username)
	return err
}

// TouchLogin 记录成员最近登录时间、来源 IP 与累计登录次数。
func (s *Store) TouchLogin(username, ip string) {
	_, _ = s.wdb.Exec(`UPDATE user SET last_login=?, last_login_ip=?,
		login_count=IFNULL(login_count,0)+1 WHERE username=?`, nowStr(), ip, username)
}

/* ---------------- 开放接口凭证 ---------------- */

// ListAPITokens 返回全部开放接口凭证。
func (s *Store) ListAPITokens() ([]model.APIToken, error) {
	rows, err := s.rdb.Query(`SELECT id, name, token, IFNULL(perms,''), IFNULL(enabled,1),
		IFNULL(last_used,''), IFNULL(create_time,'') FROM api_token ORDER BY create_time DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.APIToken
	for rows.Next() {
		var t model.APIToken
		var enabled int
		if err := rows.Scan(&t.ID, &t.Name, &t.Token, &t.Perms, &enabled, &t.LastUsed, &t.CreateTime); err != nil {
			return nil, err
		}
		t.Enabled = enabled == 1
		list = append(list, t)
	}
	return list, rows.Err()
}

// CreateAPIToken 生成新的开放接口凭证。
func (s *Store) CreateAPIToken(name, perms string) (*model.APIToken, error) {
	t := &model.APIToken{ID: newID(), Name: name, Token: "wgk_" + newID() + newID()[:8], Perms: perms, Enabled: true, CreateTime: nowStr()}
	_, err := s.wdb.Exec(`INSERT INTO api_token(id,name,token,perms,enabled,create_time) VALUES(?,?,?,?,1,?)`,
		t.ID, t.Name, t.Token, t.Perms, t.CreateTime)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// DeleteAPIToken 删除开放接口凭证。
func (s *Store) DeleteAPIToken(id string) error {
	_, err := s.wdb.Exec(`DELETE FROM api_token WHERE id=?`, id)
	return err
}

// SetAPITokenEnabled 启停开放接口凭证。
func (s *Store) SetAPITokenEnabled(id string, enabled bool) error {
	_, err := s.wdb.Exec(`UPDATE api_token SET enabled=? WHERE id=?`, boolInt(enabled), id)
	return err
}

// CheckAPIToken 校验开放接口凭证并返回其权限列表。
func (s *Store) CheckAPIToken(token, perm string) bool {
	var perms string
	var enabled int
	if err := s.rdb.QueryRow(`SELECT IFNULL(perms,''), IFNULL(enabled,1) FROM api_token WHERE token=?`, token).
		Scan(&perms, &enabled); err != nil {
		return false
	}
	if enabled != 1 {
		return false
	}
	if perms == "" || perms == "*" {
		return true
	}
	for _, p := range strings.Split(perms, ",") {
		if strings.TrimSpace(p) == perm || strings.TrimSpace(p) == "*" {
			return true
		}
	}
	return false
}

// TouchAPIToken 刷新凭证最近使用时间。
func (s *Store) TouchAPIToken(token string) {
	_, _ = s.wdb.Exec(`UPDATE api_token SET last_used=? WHERE token=?`, nowStr(), token)
}

/* ---------------- 资产设备台账 ---------------- */

// ListAssets 支持按关键字、类别、状态过滤。
func (s *Store) ListAssets(keyword, category, status string) ([]model.Asset, error) {
	q := `SELECT id, IFNULL(name,''), IFNULL(category,''), IFNULL(type,''), IFNULL(ip,''), IFNULL(mac,''),
		IFNULL(os,''), IFNULL(spec,''), IFNULL(vendor,''), IFNULL(sn,''), IFNULL(location,''), IFNULL(owner,''),
		IFNULL(department,''), IFNULL(buy_date,''), IFNULL(expire_date,''), IFNULL(status,'inuse'),
		IFNULL(monitor_bind,''), IFNULL(remark,''), IFNULL(create_time,''), IFNULL(update_time,'')
		FROM asset`
	var where []string
	var args []any
	if keyword != "" {
		where = append(where, `(name LIKE ? OR ip LIKE ? OR sn LIKE ? OR owner LIKE ? OR location LIKE ?)`)
		for i := 0; i < 5; i++ {
			args = append(args, "%"+keyword+"%")
		}
	}
	if category != "" {
		where = append(where, `category=?`)
		args = append(args, category)
	}
	if status != "" {
		where = append(where, `status=?`)
		args = append(args, status)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY update_time DESC"
	rows, err := s.rdb.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.Asset
	for rows.Next() {
		var a model.Asset
		if err := rows.Scan(&a.ID, &a.Name, &a.Category, &a.Type, &a.IP, &a.Mac, &a.OS, &a.Spec,
			&a.Vendor, &a.SN, &a.Location, &a.Owner, &a.Department, &a.BuyDate, &a.ExpireDate,
			&a.Status, &a.MonitorBind, &a.Remark, &a.CreateTime, &a.UpdateTime); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, rows.Err()
}

// SaveAsset 新增或更新资产，ID 为空表示新增。
func (s *Store) SaveAsset(a *model.Asset) error {
	now := nowStr()
	if a.ID == "" {
		a.ID = newID()
		a.CreateTime = now
		a.UpdateTime = now
		_, err := s.wdb.Exec(`INSERT INTO asset(id,name,category,type,ip,mac,os,spec,vendor,sn,location,owner,department,
			buy_date,expire_date,status,monitor_bind,remark,create_time,update_time)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.ID, a.Name, a.Category, a.Type, a.IP, a.Mac, a.OS, a.Spec, a.Vendor, a.SN,
			a.Location, a.Owner, a.Department, a.BuyDate, a.ExpireDate, a.Status, a.MonitorBind, a.Remark, a.CreateTime, a.UpdateTime)
		return err
	}
	a.UpdateTime = now
	_, err := s.wdb.Exec(`UPDATE asset SET name=?,category=?,type=?,ip=?,mac=?,os=?,spec=?,vendor=?,sn=?,
		location=?,owner=?,department=?,buy_date=?,expire_date=?,status=?,monitor_bind=?,remark=?,update_time=? WHERE id=?`,
		a.Name, a.Category, a.Type, a.IP, a.Mac, a.OS, a.Spec, a.Vendor, a.SN,
		a.Location, a.Owner, a.Department, a.BuyDate, a.ExpireDate, a.Status, a.MonitorBind, a.Remark, a.UpdateTime, a.ID)
	return err
}

// DeleteAsset 删除资产及其关联账号。
func (s *Store) DeleteAsset(id string) error {
	if _, err := s.wdb.Exec(`DELETE FROM device_account WHERE asset_id=?`, id); err != nil {
		return err
	}
	_, err := s.wdb.Exec(`DELETE FROM asset WHERE id=?`, id)
	return err
}

// BatchCreateAssets 批量导入资产。
func (s *Store) BatchCreateAssets(list []model.Asset) (int, error) {
	tx, err := s.wdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO asset(id,name,category,type,ip,mac,os,spec,vendor,sn,location,owner,department,
		buy_date,expire_date,status,monitor_bind,remark,create_time,update_time)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	now := nowStr()
	n := 0
	for _, a := range list {
		if a.Name == "" {
			continue
		}
		id := a.ID
		if id == "" {
			id = newID()
		}
		if _, err := stmt.Exec(id, a.Name, a.Category, a.Type, a.IP, a.Mac, a.OS, a.Spec, a.Vendor, a.SN,
			a.Location, a.Owner, a.Department, a.BuyDate, a.ExpireDate, defaultStr(a.Status, "inuse"),
			a.MonitorBind, a.Remark, now, now); err != nil {
			return n, err
		}
		n++
	}
	return n, tx.Commit()
}

func defaultStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// AssetStats 返回资产分类统计。
func (s *Store) AssetStats() (map[string]int, error) {
	rows, err := s.rdb.Query(`SELECT IFNULL(status,'inuse'), COUNT(1) FROM asset GROUP BY IFNULL(status,'inuse')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var c int
		if err := rows.Scan(&k, &c); err != nil {
			return nil, err
		}
		out[k] = c
	}
	return out, rows.Err()
}

/* ---------------- 设备账号 ---------------- */

// LatestProcesses 返回某主机最近一次上报的进程快照。
func (s *Store) LatestProcesses(host string, limit int) ([]model.HostProcess, error) {
	if limit <= 0 {
		limit = 30
	}
	rows, err := s.rdb.Query(`SELECT hostname, pid, IFNULL(name,''), IFNULL(cpu_per,0), IFNULL(mem_per,0),
		IFNULL(cmdline,''), IFNULL(create_time,'') FROM process_state
		WHERE hostname=? AND create_time=(SELECT MAX(create_time) FROM process_state WHERE hostname=?)
		ORDER BY cpu_per DESC LIMIT ?`, host, host, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.HostProcess
	for rows.Next() {
		var p model.HostProcess
		if err := rows.Scan(&p.Hostname, &p.PID, &p.Name, &p.CPUPer, &p.MemPer, &p.Cmdline, &p.CreateTime); err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

// AlertsSince 返回指定时间点之后新增的告警，供邮件推送使用。
func (s *Store) AlertsSince(since string) ([]model.Alert, error) {
	if since == "" {
		since = time.Now().Add(-10 * time.Minute).Format(timeLayout)
	}
	rows, err := s.rdb.Query(`SELECT id, hostname, IFNULL(type,''), IFNULL(level,'warning'), IFNULL(value,''),
		IFNULL(content,''), IFNULL(state,'1'), IFNULL(create_time,'') FROM alert
		WHERE create_time > ? ORDER BY create_time LIMIT 50`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.Alert
	for rows.Next() {
		var a model.Alert
		if err := rows.Scan(&a.ID, &a.Hostname, &a.Type, &a.Level, &a.Value, &a.Content, &a.State, &a.CreateTime); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, rows.Err()
}

// secretKey 由服务端 token 派生，用于加密保存设备口令。
func (s *Store) secretKey() []byte {
	// ⚠️ "opsmonitor-device-secret" 是与机器码盐同性质的历史值，**不随品牌更名而改**：
	// 改了之后已入库的设备口令就再也解不开。
	sum := sha256.Sum256([]byte(s.cfg.Token + "|opsmonitor-device-secret"))
	return sum[:]
}

// EncryptSecret 加密明文口令。
func (s *Store) EncryptSecret(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	block, err := aes.NewCipher(s.secretKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return hex.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

// DecryptSecret 解密口令，失败返回空串。
func (s *Store) DecryptSecret(cipherText string) string {
	if cipherText == "" {
		return ""
	}
	raw, err := hex.DecodeString(cipherText)
	if err != nil {
		return ""
	}
	block, err := aes.NewCipher(s.secretKey())
	if err != nil {
		return ""
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(raw) < gcm.NonceSize() {
		return ""
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return ""
	}
	return string(plain)
}

// ListDeviceAccounts 返回设备账号，secret 字段不回传明文。
func (s *Store) ListDeviceAccounts(assetID string) ([]model.DeviceAccount, error) {
	q := `SELECT id, IFNULL(name,''), IFNULL(asset_id,''), IFNULL(protocol,'ssh'), IFNULL(host,''),
		IFNULL(port,22), IFNULL(username,''), IFNULL(secret,''), IFNULL(remark,''),
		IFNULL(create_time,''), IFNULL(update_time,'') FROM device_account`
	var args []any
	if assetID != "" {
		q += " WHERE asset_id=?"
		args = append(args, assetID)
	}
	q += " ORDER BY create_time DESC"
	rows, err := s.rdb.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.DeviceAccount
	for rows.Next() {
		var a model.DeviceAccount
		if err := rows.Scan(&a.ID, &a.Name, &a.AssetID, &a.Protocol, &a.Host, &a.Port, &a.Username,
			&a.Secret, &a.Remark, &a.CreateTime, &a.UpdateTime); err != nil {
			return nil, err
		}
		a.HasSecret = a.Secret != ""
		a.Secret = ""
		list = append(list, a)
	}
	return list, rows.Err()
}

// SaveDeviceAccount 新增或更新设备账号，口令加密保存。
func (s *Store) SaveDeviceAccount(a *model.DeviceAccount) error {
	now := nowStr()
	var enc string
	var err error
	if a.Secret != "" {
		if enc, err = s.EncryptSecret(a.Secret); err != nil {
			return err
		}
	}
	if a.ID == "" {
		a.ID = newID()
		a.CreateTime = now
		a.UpdateTime = now
		_, err = s.wdb.Exec(`INSERT INTO device_account(id,name,asset_id,protocol,host,port,username,secret,remark,create_time,update_time)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`, a.ID, a.Name, a.AssetID, a.Protocol, a.Host, a.Port, a.Username, enc, a.Remark, a.CreateTime, a.UpdateTime)
		return err
	}
	a.UpdateTime = now
	if a.Secret == "" {
		// 未提供新口令时保持原值不变
		_, err = s.wdb.Exec(`UPDATE device_account SET name=?,asset_id=?,protocol=?,host=?,port=?,username=?,remark=?,update_time=? WHERE id=?`,
			a.Name, a.AssetID, a.Protocol, a.Host, a.Port, a.Username, a.Remark, a.UpdateTime, a.ID)
		return err
	}
	_, err = s.wdb.Exec(`UPDATE device_account SET name=?,asset_id=?,protocol=?,host=?,port=?,username=?,secret=?,remark=?,update_time=? WHERE id=?`,
		a.Name, a.AssetID, a.Protocol, a.Host, a.Port, a.Username, enc, a.Remark, a.UpdateTime, a.ID)
	return err
}

// DeleteDeviceAccount 删除设备账号。
func (s *Store) DeleteDeviceAccount(id string) error {
	_, err := s.wdb.Exec(`DELETE FROM device_account WHERE id=?`, id)
	return err
}

// DeviceCredential 返回可实际用于连接的凭据（含解密后的口令）。
func (s *Store) DeviceCredential(id string) (*model.DeviceAccount, error) {
	var a model.DeviceAccount
	var secret string
	err := s.rdb.QueryRow(`SELECT id, IFNULL(name,''), IFNULL(asset_id,''), IFNULL(protocol,'ssh'), IFNULL(host,''),
		IFNULL(port,22), IFNULL(username,''), IFNULL(secret,''), IFNULL(remark,'') FROM device_account WHERE id=?`, id).
		Scan(&a.ID, &a.Name, &a.AssetID, &a.Protocol, &a.Host, &a.Port, &a.Username, &secret, &a.Remark)
	if err != nil {
		return nil, err
	}
	a.Secret = s.DecryptSecret(secret)
	a.HasSecret = secret != ""
	return &a, nil
}

/* ---------------- 指令下发 ---------------- */

// CreateCommand 将指令写入待执行队列。
func (s *Store) CreateCommand(host, cmd, user string, timeoutSec int) (*model.Command, error) {
	c := &model.Command{
		ID: newID(), Host: host, Cmd: cmd, User: user,
		TimeoutSec: timeoutSec, Status: "pending", CreateTime: nowStr(), Source: "console",
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 30
	}
	_, err := s.wdb.Exec(`INSERT INTO command(id,host,cmd,timeout_sec,source,status,user,create_time)
		VALUES(?,?,?,?,?,?,?,?)`, c.ID, c.Host, c.Cmd, c.TimeoutSec, c.Source, c.Status, c.User, c.CreateTime)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// PendingCommands 采集端拉取待执行指令并标记为执行中。
func (s *Store) PendingCommands(hostname string, limit int) ([]model.CommandPull, error) {
	if limit <= 0 {
		limit = 5
	}
	tx, err := s.wdb.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id, cmd, IFNULL(timeout_sec,30) FROM command
		WHERE host=? AND status='pending' ORDER BY create_time LIMIT ?`, hostname, limit)
	if err != nil {
		return nil, err
	}
	var list []model.CommandPull
	var ids []any
	for rows.Next() {
		var c model.CommandPull
		if err := rows.Scan(&c.ID, &c.Cmd, &c.TimeoutSec); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, c)
		ids = append(ids, c.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(ids) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		if _, err := tx.Exec(`UPDATE command SET status='running' WHERE id IN (`+placeholders+`)`, ids...); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return list, nil
}

// FinishCommands 记录指令执行结果。
func (s *Store) FinishCommands(hostname string, outcomes []model.CommandOutcome) error {
	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`UPDATE command SET status=?, exit_code=?, output=?, error_msg=?, finish_time=? WHERE id=? AND host=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := nowStr()
	for _, o := range outcomes {
		status := "done"
		if o.ErrorMsg != "" || o.ExitCode != 0 {
			status = "failed"
		}
		if len(o.Output) > 16000 {
			o.Output = o.Output[:16000] + "\n...(输出已截断)"
		}
		if _, err := stmt.Exec(status, o.ExitCode, o.Output, o.ErrorMsg, now, o.ID, hostname); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListCommands 返回指令历史。
func (s *Store) ListCommands(host string, limit int) ([]model.Command, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id, IFNULL(host,''), cmd, IFNULL(timeout_sec,30), IFNULL(source,''), IFNULL(status,''),
		IFNULL(exit_code,0), IFNULL(output,''), IFNULL(error_msg,''), IFNULL(user,''),
		IFNULL(create_time,''), IFNULL(finish_time,'') FROM command`
	var args []any
	if host != "" {
		q += " WHERE host=?"
		args = append(args, host)
	}
	q += " ORDER BY create_time DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.rdb.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.Command
	for rows.Next() {
		var c model.Command
		if err := rows.Scan(&c.ID, &c.Host, &c.Cmd, &c.TimeoutSec, &c.Source, &c.Status, &c.ExitCode,
			&c.Output, &c.ErrorMsg, &c.User, &c.CreateTime, &c.FinishTime); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// CommandByID 按 ID 取单条指令，供工具箱「本机执行」轮询回包。
func (s *Store) CommandByID(id string) (*model.Command, error) {
	var c model.Command
	err := s.rdb.QueryRow(`SELECT id, IFNULL(host,''), cmd, IFNULL(timeout_sec,30), IFNULL(source,''), IFNULL(status,''),
		IFNULL(exit_code,0), IFNULL(output,''), IFNULL(error_msg,''), IFNULL(user,''),
		IFNULL(create_time,''), IFNULL(finish_time,'') FROM command WHERE id = ?`, id).
		Scan(&c.ID, &c.Host, &c.Cmd, &c.TimeoutSec, &c.Source, &c.Status, &c.ExitCode,
			&c.Output, &c.ErrorMsg, &c.User, &c.CreateTime, &c.FinishTime)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// TimeoutCommands 将长时间未回包的指令标记为超时。
func (s *Store) TimeoutCommands(after time.Duration) int {
	res, err := s.wdb.Exec(`UPDATE command SET status='timeout', finish_time=?, error_msg='采集端未在规定时间内回包'
		WHERE status='running' AND create_time < ?`, nowStr(), time.Now().Add(-after).Format(timeLayout))
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}

// CancelCommand 撤销尚未执行的指令。
func (s *Store) CancelCommand(id string) error {
	res, err := s.wdb.Exec(`DELETE FROM command WHERE id=? AND status IN ('pending')`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("指令不存在或已在执行中，无法撤销")
	}
	return nil
}

// BuildInspectionReport 汇总主机、监控项、告警与资产状况，生成一份巡检报告。
func (s *Store) BuildInspectionReport() (*InspectionReport, error) {
	rep := &InspectionReport{GeneratedAt: nowStr()}
	hosts, err := s.ListHosts()
	if err != nil {
		return nil, err
	}
	rep.HostTotal = len(hosts)
	for _, h := range hosts {
		if h.State == "1" {
			rep.HostOnline++
		}
	}
	tasks, err := s.ListTasks()
	if err != nil {
		return nil, err
	}
	rep.TaskTotal = len(tasks)
	for _, t := range tasks {
		if !t.Enabled {
			continue
		}
		states, err := s.ListMonitorStates(t.ID)
		if err != nil {
			continue
		}
		for _, st := range states {
			if st.Status == "2" {
				rep.TaskAbnormal++
			}
		}
	}
	alerts, err := s.Alerts(200)
	if err != nil {
		return nil, err
	}
	for _, a := range alerts {
		if a.State == "1" {
			rep.AlertTotal++
		}
	}
	assets, err := s.ListAssets("", "", "")
	if err == nil {
		rep.AssetTotal = len(assets)
	}
	// 最近一次指令执行情况
	var cmdFailed int
	_ = s.rdb.QueryRow(`SELECT COUNT(1) FROM command WHERE status='failed'`).Scan(&cmdFailed)

	score := 100
	addItem := func(name, status, detail, impact string, deduct int) {
		rep.Items = append(rep.Items, InspectItem{Name: name, Status: status, Detail: detail, Impact: impact})
		if status != "ok" {
			score -= deduct
		}
	}
	if rep.HostTotal == 0 {
		addItem("主机接入", "warn", "当前尚未有任何主机上报数据", "无法掌握主机运行状态", 10)
		rep.Suggestions = append(rep.Suggestions, "请在被监控主机上部署 strix-agent 并确认服务端地址与密钥配置正确。")
	} else {
		offline := rep.HostTotal - rep.HostOnline
		addItem("主机在线状态", boolStatus(offline == 0), fmt.Sprintf("共 %d 台，在线 %d 台，离线 %d 台", rep.HostTotal, rep.HostOnline, offline),
			"离线主机无法采集指标", 10)
		if offline > 0 {
			rep.Suggestions = append(rep.Suggestions, "排查离线主机上的采集端进程、网络连通性与服务端地址配置。")
		}
	}
	addItem("监控项健康度", boolStatus(rep.TaskAbnormal == 0),
		fmt.Sprintf("共 %d 个监控项，当前异常 %d 项", rep.TaskTotal, rep.TaskAbnormal), "异常监控项代表对应服务不可用", 15)
	if rep.TaskAbnormal > 0 {
		rep.Suggestions = append(rep.Suggestions, "检查异常监控项对应的进程或服务，并在监控项管理中调整阈值或自动恢复策略。")
	}
	addItem("未处理告警", boolStatus(rep.AlertTotal == 0), fmt.Sprintf("当前未处理告警 %d 条", rep.AlertTotal),
		"告警长期未处理可能演变为故障", 15)
	if rep.AlertTotal > 0 {
		rep.Suggestions = append(rep.Suggestions, "及时在告警中心确认并处理未恢复告警，必要时配置邮件推送。")
	}
	addItem("资产台账完整度", boolStatus(rep.AssetTotal > 0), fmt.Sprintf("资产记录 %d 条", rep.AssetTotal),
		"台账缺失会影响故障定位与维保", 5)
	if rep.AssetTotal == 0 {
		rep.Suggestions = append(rep.Suggestions, "录入设备台账（型号、序列号、维保期、责任人），便于故障定位与到期提醒。")
	}
	addItem("指令执行成功率", boolStatus(cmdFailed == 0), fmt.Sprintf("历史执行失败 %d 次", cmdFailed),
		"失败指令可能意味着批量运维动作未生效", 5)
	if cmdFailed > 0 {
		rep.Suggestions = append(rep.Suggestions, "核对失败指令的目标主机权限与命令内容，避免批量操作遗漏。")
	}
	if retention := s.cfg.Retention; retention > 0 {
		addItem("数据保留策略", "ok", fmt.Sprintf("监控数据保留 %d 天，过期自动清理", retention), "保留期过短不利于趋势回溯", 0)
	} else {
		addItem("数据保留策略", "ok", "监控数据永久保留（无保留天数限制）", "数据量持续增长，请关注磁盘容量", 0)
	}
	if len(rep.Suggestions) == 0 {
		rep.Suggestions = append(rep.Suggestions, "当前系统整体运行良好，建议保持周期性巡检与告警策略复核。")
	}
	if score < 0 {
		score = 0
	}
	rep.Score = score
	switch {
	case score >= 90:
		rep.Summary = fmt.Sprintf("系统整体健康（评分 %d）：主机在线率 %.0f%%，监控项与告警均无异常积压。",
			score, percentOf(rep.HostOnline, rep.HostTotal))
	case score >= 70:
		rep.Summary = fmt.Sprintf("系统基本可用（评分 %d）：存在部分需关注项，详见巡检明细与整改建议。", score)
	default:
		rep.Summary = fmt.Sprintf("系统存在较多风险（评分 %d）：请优先处理明细中标记为异常的检查项。", score)
	}
	return rep, nil
}

// boolStatus 将布尔值转为巡检状态标识。
func boolStatus(okValue bool) string {
	if okValue {
		return "ok"
	}
	return "bad"
}

// percentOf 计算百分比，分母为 0 时返回 100。
func percentOf(a, b int) float64 {
	if b == 0 {
		return 100
	}
	return float64(a) * 100 / float64(b)
}

/* ---------------- 巡检报告 ---------------- */

// InspectionReport 是巡检报告的结构化内容。
type InspectionReport struct {
	GeneratedAt  string        `json:"generatedAt"`
	Summary      string        `json:"summary"`
	HostTotal    int           `json:"hostTotal"`
	HostOnline   int           `json:"hostOnline"`
	TaskTotal    int           `json:"taskTotal"`
	TaskAbnormal int           `json:"taskAbnormal"`
	AlertTotal   int           `json:"alertTotal"`
	AssetTotal   int           `json:"assetTotal"`
	Items        []InspectItem `json:"items"`
	Suggestions  []string      `json:"suggestions"`
	Score        int           `json:"score"`
}

// InspectItem 巡检明细项。
type InspectItem struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Impact string `json:"impact"`
}

// ListInspections 返回巡检报告摘要列表（不含正文）。
func (s *Store) ListInspections(limit int) ([]model.Inspection, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.rdb.Query(`SELECT id, title, IFNULL(scope,''), IFNULL(author,''), IFNULL(summary,''),
		IFNULL(score,0), IFNULL(create_time,'') FROM inspection ORDER BY create_time DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.Inspection
	for rows.Next() {
		var it model.Inspection
		if err := rows.Scan(&it.ID, &it.Title, &it.Scope, &it.Author, &it.Summary, &it.Score, &it.CreateTime); err != nil {
			return nil, err
		}
		list = append(list, it)
	}
	return list, rows.Err()
}

// GetInspection 读取巡检报告全文。
func (s *Store) GetInspection(id string) (*model.Inspection, error) {
	var it model.Inspection
	err := s.rdb.QueryRow(`SELECT id, title, IFNULL(scope,''), IFNULL(author,''), IFNULL(summary,''),
		IFNULL(content,''), IFNULL(score,0), IFNULL(create_time,'') FROM inspection WHERE id=?`, id).
		Scan(&it.ID, &it.Title, &it.Scope, &it.Author, &it.Summary, &it.Content, &it.Score, &it.CreateTime)
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// SaveInspection 保存巡检报告。
func (s *Store) SaveInspection(title, scope, author string, rep *InspectionReport) (*model.Inspection, error) {
	raw, err := json.Marshal(rep)
	if err != nil {
		return nil, err
	}
	it := &model.Inspection{
		ID: newID(), Title: title, Scope: scope, Author: author, Summary: rep.Summary,
		Content: string(raw), Score: rep.Score, CreateTime: nowStr(),
	}
	_, err = s.wdb.Exec(`INSERT INTO inspection(id,title,scope,author,summary,content,score,create_time) VALUES(?,?,?,?,?,?,?,?)`,
		it.ID, it.Title, it.Scope, it.Author, it.Summary, it.Content, it.Score, it.CreateTime)
	if err != nil {
		return nil, err
	}
	return it, nil
}

// DeleteInspection 删除巡检报告。
func (s *Store) DeleteInspection(id string) error {
	_, err := s.wdb.Exec(`DELETE FROM inspection WHERE id=?`, id)
	return err
}

/* ---------------- 主机登录事件 ---------------- */

// SaveLoginEvent 记录一次主机登录用户变化。
func (s *Store) SaveLoginEvent(hostname, users, diff string) error {
	_, err := s.wdb.Exec(`INSERT INTO login_event(id,hostname,users,diff,create_time) VALUES(?,?,?,?,?)`,
		newID(), hostname, users, diff, nowStr())
	return err
}

// watchLogin 比对最近一次登录用户列表，出现新登录会话时记录事件与提醒。
func (s *Store) watchLogin(hostname, users, now string) {
	users = strings.TrimSpace(users)
	if users == "" {
		return
	}
	if s.cfg.LoginAlert != "true" {
		return
	}
	s.loginMu.Lock()
	prev, existed := s.loginUser[hostname]
	s.loginUser[hostname] = users
	s.loginMu.Unlock()
	if !existed {
		return
	}
	prevSet := map[string]bool{}
	for _, u := range strings.Split(prev, ",") {
		if u = strings.TrimSpace(u); u != "" {
			prevSet[u] = true
		}
	}
	var fresh []string
	for _, u := range strings.Split(users, ",") {
		if u = strings.TrimSpace(u); u != "" && !prevSet[u] {
			fresh = append(fresh, u)
		}
	}
	if len(fresh) == 0 {
		return
	}
	content := fmt.Sprintf("主机 %s 新增登录用户：%s（当前在线：%s）", hostname, strings.Join(fresh, "、"), users)
	if err := s.SaveLoginEvent(hostname, users, strings.Join(fresh, ",")); err != nil {
		log.Printf("记录登录事件失败: %v", err)
	}
	if _, err := s.wdb.Exec(`INSERT INTO alert(id, hostname, type, level, value, content, state, create_time, update_time)
		VALUES(?,?,?,?,?,?,'1',?,?)`, newID(), hostname, "login", "info", strings.Join(fresh, ","), content, now, now); err != nil {
		log.Printf("写入登录提醒失败: %v", err)
	}
}

// ListLoginEvents 返回最近的主机登录事件。
func (s *Store) ListLoginEvents(limit int) ([]model.LoginEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.rdb.Query(`SELECT id, hostname, IFNULL(users,''), IFNULL(diff,''), IFNULL(create_time,'')
		FROM login_event ORDER BY create_time DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.LoginEvent
	for rows.Next() {
		var e model.LoginEvent
		if err := rows.Scan(&e.ID, &e.Hostname, &e.Users, &e.Diff, &e.CreateTime); err != nil {
			return nil, err
		}
		list = append(list, e)
	}
	return list, rows.Err()
}
