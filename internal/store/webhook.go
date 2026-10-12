package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

/* ---------------- Webhook 机器人 ---------------- */

// Webhook 一个机器人推送通道。
type Webhook struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	Secret    string `json:"-"` // 加签密钥，加密存储且不回传明文
	AtMobiles string `json:"atMobiles"`
	AtAll     bool   `json:"atAll"`
	Enabled   bool   `json:"enabled"`
	Levels    string `json:"levels"` // 订阅的告警级别，空表示全部
	LastTime  string `json:"lastTime"`
	LastState string `json:"lastState"` // ok / fail
	LastError string `json:"lastError"`
	Create    string `json:"createTime"`
}

// schemaWebhook 创建 Webhook 与采集端配置相关数据表。
func (s *Store) schemaWebhook() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS webhook (
			id TEXT PRIMARY KEY, name TEXT, kind TEXT, url TEXT, secret TEXT,
			at_mobiles TEXT, at_all INTEGER DEFAULT 0, enabled INTEGER DEFAULT 1,
			levels TEXT, last_time TEXT, last_state TEXT, last_error TEXT, create_time TEXT)`,
		// 采集端配置的主机级覆盖：字段留空/为 0 表示沿用全局默认值
		`CREATE TABLE IF NOT EXISTS agent_config (
			hostname TEXT PRIMARY KEY, interval INTEGER DEFAULT 0, allow_command TEXT,
			skip_disk TEXT, update_mode TEXT, update_interval INTEGER DEFAULT 0, update_time TEXT)`,
	}
	for _, stmt := range stmts {
		if _, err := s.wdb.Exec(stmt); err != nil {
			return fmt.Errorf("建表失败: %w", err)
		}
	}
	return nil
}

const webhookCols = `id, IFNULL(name,''), IFNULL(kind,''), IFNULL(url,''), IFNULL(secret,''),
	IFNULL(at_mobiles,''), IFNULL(at_all,0), IFNULL(enabled,1), IFNULL(levels,''),
	IFNULL(last_time,''), IFNULL(last_state,''), IFNULL(last_error,''), IFNULL(create_time,'')`

func scanWebhook(row interface{ Scan(...any) error }) (*Webhook, error) {
	var w Webhook
	var atAll, enabled int
	if err := row.Scan(&w.ID, &w.Name, &w.Kind, &w.URL, &w.Secret, &w.AtMobiles,
		&atAll, &enabled, &w.Levels, &w.LastTime, &w.LastState, &w.LastError, &w.Create); err != nil {
		return nil, err
	}
	w.AtAll = atAll != 0
	w.Enabled = enabled != 0
	return &w, nil
}

// ListWebhooks 返回全部机器人配置。
func (s *Store) ListWebhooks() ([]Webhook, error) {
	rows, err := s.rdb.Query(`SELECT ` + webhookCols + ` FROM webhook ORDER BY create_time`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Webhook{}
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// GetWebhook 按 ID 读取单个机器人配置。
func (s *Store) GetWebhook(id string) (*Webhook, error) {
	row := s.rdb.QueryRow(`SELECT `+webhookCols+` FROM webhook WHERE id = ?`, id)
	w, err := scanWebhook(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return w, err
}

// SaveWebhook 新增或更新机器人配置，ID 为空时自动生成。
func (s *Store) SaveWebhook(w *Webhook) error {
	if w.ID == "" {
		w.ID = newID()
	}
	if w.Create == "" {
		w.Create = nowStr()
	}
	_, err := s.wdb.Exec(`INSERT INTO webhook
		(id, name, kind, url, secret, at_mobiles, at_all, enabled, levels, create_time)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, kind=excluded.kind, url=excluded.url, secret=excluded.secret,
			at_mobiles=excluded.at_mobiles, at_all=excluded.at_all, enabled=excluded.enabled,
			levels=excluded.levels`,
		w.ID, w.Name, w.Kind, w.URL, w.Secret, w.AtMobiles,
		boolInt(w.AtAll), boolInt(w.Enabled), w.Levels, w.Create)
	return err
}

// DeleteWebhook 删除一个机器人配置。
func (s *Store) DeleteWebhook(id string) error {
	_, err := s.wdb.Exec(`DELETE FROM webhook WHERE id = ?`, id)
	return err
}

// MarkWebhookResult 记录最近一次推送结果，便于在控制台确认通道是否还活着。
func (s *Store) MarkWebhookResult(id, state, errMsg string) error {
	_, err := s.wdb.Exec(`UPDATE webhook SET last_time = ?, last_state = ?, last_error = ? WHERE id = ?`,
		nowStr(), state, truncateStr(errMsg, 300), id)
	return err
}

/* ---------------- 采集端配置的主机级覆盖 ---------------- */

// AgentOverride 某台主机对全局采集端配置的覆盖项，零值表示沿用全局默认。
type AgentOverride struct {
	Hostname       string `json:"hostname"`
	Interval       int    `json:"interval"`
	AllowCommand   string `json:"allowCommand"` // "" / "true" / "false"
	SkipDisk       string `json:"skipDisk"`
	UpdateMode     string `json:"updateMode"` // "" / auto / check / off
	UpdateInterval int    `json:"updateInterval"`
	UpdateTime     string `json:"updateTime"`
}

// GetAgentOverride 读取某台主机的覆盖配置，不存在时返回 nil。
func (s *Store) GetAgentOverride(hostname string) (*AgentOverride, error) {
	row := s.rdb.QueryRow(`SELECT hostname, IFNULL(interval,0), IFNULL(allow_command,''),
		IFNULL(skip_disk,''), IFNULL(update_mode,''), IFNULL(update_interval,0), IFNULL(update_time,'')
		FROM agent_config WHERE hostname = ?`, hostname)
	var o AgentOverride
	if err := row.Scan(&o.Hostname, &o.Interval, &o.AllowCommand, &o.SkipDisk,
		&o.UpdateMode, &o.UpdateInterval, &o.UpdateTime); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &o, nil
}

// SaveAgentOverride 写入主机级覆盖配置，全为零值时删除该行（回到全继承）。
func (s *Store) SaveAgentOverride(o AgentOverride) error {
	empty := o.Interval == 0 && o.AllowCommand == "" && o.SkipDisk == "" &&
		o.UpdateMode == "" && o.UpdateInterval == 0
	if empty {
		_, err := s.wdb.Exec(`DELETE FROM agent_config WHERE hostname = ?`, o.Hostname)
		return err
	}
	_, err := s.wdb.Exec(`INSERT INTO agent_config
		(hostname, interval, allow_command, skip_disk, update_mode, update_interval, update_time)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(hostname) DO UPDATE SET
			interval=excluded.interval, allow_command=excluded.allow_command, skip_disk=excluded.skip_disk,
			update_mode=excluded.update_mode, update_interval=excluded.update_interval, update_time=excluded.update_time`,
		o.Hostname, o.Interval, o.AllowCommand, o.SkipDisk, o.UpdateMode, o.UpdateInterval, nowStr())
	return err
}

// DeleteAgentOverride 删除主机级覆盖配置。
func (s *Store) DeleteAgentOverride(hostname string) error {
	_, err := s.wdb.Exec(`DELETE FROM agent_config WHERE hostname = ?`, hostname)
	return err
}

// ListAgentOverrides 返回全部主机级覆盖配置。
func (s *Store) ListAgentOverrides() ([]AgentOverride, error) {
	rows, err := s.rdb.Query(`SELECT hostname, IFNULL(interval,0), IFNULL(allow_command,''),
		IFNULL(skip_disk,''), IFNULL(update_mode,''), IFNULL(update_interval,0), IFNULL(update_time,'')
		FROM agent_config ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentOverride{}
	for rows.Next() {
		var o AgentOverride
		if err := rows.Scan(&o.Hostname, &o.Interval, &o.AllowCommand, &o.SkipDisk,
			&o.UpdateMode, &o.UpdateInterval, &o.UpdateTime); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func truncateStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}
