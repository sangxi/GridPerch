package store

import (
	"log"
	"strings"
	"time"

	"github.com/sangxi/OpsMonitor/internal/model"
)

/* ---------------- 操作审计 ---------------- */

// AuditFilter 审计流水查询条件，空字段表示不参与过滤。
type AuditFilter struct {
	Username string // 精确匹配操作账号
	Action   string // 模糊匹配动作名
	Category string // 归类精确匹配
	Result   string // ok / fail
	Keyword  string // 在动作、对象、明细、路径中模糊匹配
	From     string // 起始时间（含）
	To       string // 结束时间（含）
	OnlyAuth bool   // 只看登录/退出类
	SkipAuth bool   // 排除登录/退出类
	Limit    int
}

// AppendAudit 异步写入一条审计流水。
//
// 审计写入发生在请求链路上，若直接落盘会与指标写入争抢唯一的写连接；
// 因此投递到独立队列由后台协程批量提交，队列满时丢弃并记录日志，绝不阻塞请求。
func (s *Store) AppendAudit(e model.AuditLog) {
	if e.ID == "" {
		e.ID = newID()
	}
	if e.CreateTime == "" {
		e.CreateTime = nowStr()
	}
	if e.Result == "" {
		e.Result = "ok"
	}
	select {
	case s.auditCh <- e:
	default:
		log.Printf("审计队列已满，丢弃一条记录：%s %s", e.Username, e.Action)
	}
}

// auditLoop 批量消费审计队列。
func (s *Store) auditLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	buf := make([]model.AuditLog, 0, 64)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		if err := s.insertAudits(buf); err != nil {
			log.Printf("写入审计流水失败: %v", err)
		}
		buf = buf[:0]
	}
	for {
		select {
		case <-s.stop:
			flush()
			return
		case e := <-s.auditCh:
			buf = append(buf, e)
			if len(buf) >= 64 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (s *Store) insertAudits(list []model.AuditLog) error {
	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO audit_log(id, username, role, action, category, method, path,
		target, detail, ip, user_agent, result, status_code, duration_ms, create_time)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range list {
		if _, err := stmt.Exec(e.ID, e.Username, e.Role, e.Action, e.Category, e.Method, e.Path,
			e.Target, e.Detail, e.IP, e.UserAgent, e.Result, e.StatusCode, e.DurationMs, e.CreateTime); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListAudit 按条件查询审计流水，时间倒序。
func (s *Store) ListAudit(f AuditFilter) ([]model.AuditLog, error) {
	where := []string{"1=1"}
	args := []any{}
	if f.Username != "" {
		where = append(where, "username=?")
		args = append(args, f.Username)
	}
	if f.Action != "" {
		where = append(where, "action LIKE ?")
		args = append(args, "%"+f.Action+"%")
	}
	if f.Category != "" {
		where = append(where, "category=?")
		args = append(args, f.Category)
	}
	if f.Result != "" {
		where = append(where, "result=?")
		args = append(args, f.Result)
	}
	if f.Keyword != "" {
		kw := "%" + f.Keyword + "%"
		where = append(where, "(action LIKE ? OR target LIKE ? OR detail LIKE ? OR path LIKE ? OR username LIKE ?)")
		args = append(args, kw, kw, kw, kw, kw)
	}
	if f.From != "" {
		where = append(where, "create_time >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "create_time <= ?")
		args = append(args, f.To)
	}
	if f.OnlyAuth {
		where = append(where, "category='auth'")
	}
	if f.SkipAuth {
		where = append(where, "category<>'auth'")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 2000 {
		limit = 2000
	}
	args = append(args, limit)
	rows, err := s.rdb.Query(`SELECT id, IFNULL(username,''), IFNULL(role,''), IFNULL(action,''), IFNULL(category,''),
		IFNULL(method,''), IFNULL(path,''), IFNULL(target,''), IFNULL(detail,''), IFNULL(ip,''), IFNULL(user_agent,''),
		IFNULL(result,'ok'), IFNULL(status_code,0), IFNULL(duration_ms,0), IFNULL(create_time,'')
		FROM audit_log WHERE `+strings.Join(where, " AND ")+` ORDER BY create_time DESC, rowid DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.AuditLog
	for rows.Next() {
		var e model.AuditLog
		if err := rows.Scan(&e.ID, &e.Username, &e.Role, &e.Action, &e.Category, &e.Method, &e.Path,
			&e.Target, &e.Detail, &e.IP, &e.UserAgent, &e.Result, &e.StatusCode, &e.DurationMs, &e.CreateTime); err != nil {
			return nil, err
		}
		list = append(list, e)
	}
	return list, rows.Err()
}

// AuditStats 汇总审计概况：总数、失败数、今日条数与最近一次操作时间。
func (s *Store) AuditStats() (map[string]any, error) {
	out := map[string]any{"total": 0, "failed": 0, "today": 0, "authToday": 0, "lastTime": ""}
	var total, failed, today, authToday int
	todayStart := time.Now().Format("2006-01-02") + " 00:00:00"
	if err := s.rdb.QueryRow(`SELECT COUNT(1), SUM(CASE WHEN result='fail' THEN 1 ELSE 0 END),
		SUM(CASE WHEN create_time >= ? THEN 1 ELSE 0 END),
		SUM(CASE WHEN create_time >= ? AND category='auth' THEN 1 ELSE 0 END) FROM audit_log`, todayStart, todayStart).
		Scan(&total, &failed, &today, &authToday); err != nil {
		return nil, err
	}
	var last string
	_ = s.rdb.QueryRow(`SELECT IFNULL(MAX(create_time),'') FROM audit_log`).Scan(&last)
	out["total"] = total
	out["failed"] = failed
	out["today"] = today
	out["authToday"] = authToday
	out["lastTime"] = last
	return out, nil
}

// DistinctAuditUsers 返回出现过的操作账号，用于列表筛选下拉。
func (s *Store) DistinctAuditUsers() ([]string, error) {
	rows, err := s.rdb.Query(`SELECT DISTINCT username FROM audit_log WHERE username<>'' ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ClearAudit 清空审计流水，返回删除条数。
func (s *Store) ClearAudit() (int64, error) {
	res, err := s.wdb.Exec(`DELETE FROM audit_log`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

/* ---------------- 邮件发送记录 ---------------- */

// AppendMailLog 记录一次邮件发送结果。
func (s *Store) AppendMailLog(e model.MailLog) {
	if e.ID == "" {
		e.ID = newID()
	}
	if e.CreateTime == "" {
		e.CreateTime = nowStr()
	}
	if _, err := s.wdb.Exec(`INSERT INTO mail_log(id, kind, subject, recipients, item_count, status, error, create_time)
		VALUES(?,?,?,?,?,?,?,?)`, e.ID, e.Kind, e.Subject, e.Recipients, e.ItemCount, e.Status, e.Error, e.CreateTime); err != nil {
		log.Printf("写入邮件记录失败: %v", err)
	}
}

// ListMailLogs 返回最近的邮件发送记录。
func (s *Store) ListMailLogs(limit int) ([]model.MailLog, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.rdb.Query(`SELECT id, IFNULL(kind,''), IFNULL(subject,''), IFNULL(recipients,''),
		IFNULL(item_count,0), IFNULL(status,''), IFNULL(error,''), IFNULL(create_time,'')
		FROM mail_log ORDER BY create_time DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.MailLog
	for rows.Next() {
		var m model.MailLog
		if err := rows.Scan(&m.ID, &m.Kind, &m.Subject, &m.Recipients, &m.ItemCount, &m.Status, &m.Error, &m.CreateTime); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

// ClearMailLogs 清空邮件发送记录。
func (s *Store) ClearMailLogs() error {
	_, err := s.wdb.Exec(`DELETE FROM mail_log`)
	return err
}

// RecentRecoveriesSince 返回指定时间之后发生的「恢复」事件，用于发送恢复通知邮件。
func (s *Store) RecentRecoveriesSince(since string) ([]model.MonitorState, error) {
	if since == "" {
		// 首次运行（尚无发送游标）只回看 10 分钟，避免把历史恢复事件全部重发
		since = time.Now().Add(-10 * time.Minute).Format(timeLayout)
	}
	rows, err := s.rdb.Query(`SELECT task_id, hostname, IFNULL(type,''), IFNULL(name,''),
		IFNULL(status,''), IFNULL(value,''), IFNULL(msg,''), IFNULL(check_time,'')
		FROM monitor_event WHERE check_time > ? AND status='1' AND prev_status='2' ORDER BY check_time LIMIT 50`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.MonitorState
	for rows.Next() {
		var e model.MonitorState
		if err := rows.Scan(&e.TaskID, &e.Hostname, &e.Type, &e.Name, &e.Status, &e.Value, &e.Msg, &e.CheckTime); err != nil {
			return nil, err
		}
		list = append(list, e)
	}
	return list, rows.Err()
}
