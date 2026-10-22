package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sangxi/gridperch/internal/model"
)

// monitorEventThrottle 同一监控项持续异常时的流水写入间隔。
const monitorEventThrottle = 10 * time.Minute

// 监控项类型分组：AgentTypes 由采集端执行，ServerTypes 由服务端主动探测。
var AgentTypes = []string{"process", "port", "http", "ping", "docker", "log", "file", "nginxlog", "script"}

var ServerTypes = []string{"tcp", "httpapi", "icmp", "snmp", "mysql", "redis", "kafka", "rabbitmq",
	"elasticsearch", "ftp", "sftp", "ssh", "ipmi", "k8s"}

// TaskTypes 全部支持的监控项类型。
var TaskTypes = append(append([]string{}, AgentTypes...), ServerTypes...)

// TaskTypeGroups 供前端渲染类型下拉分组。
var TaskTypeGroups = map[string]string{
	"process": "agent", "port": "agent", "http": "agent", "ping": "agent", "docker": "agent",
	"log": "agent", "file": "agent", "nginxlog": "agent", "script": "agent",
	"tcp": "server", "httpapi": "server", "icmp": "server", "snmp": "server", "mysql": "server",
	"redis": "server", "kafka": "server", "rabbitmq": "server", "elasticsearch": "server",
	"ftp": "server", "sftp": "server", "ssh": "server", "ipmi": "server", "k8s": "server",
}

func taskSelectCols() string {
	return `id, type, name, IFNULL(target,''), IFNULL(interval_sec,30),
		IFNULL(timeout_sec,5), IFNULL(enabled,1), IFNULL(hosts,'*'), IFNULL(alert_content,''),
		IFNULL(runner,'agent'), IFNULL(heal_cmd,''), IFNULL(auto_heal,0),
		IFNULL(create_time,''), IFNULL(update_time,'')`
}

// ListTasks 返回全部监控项。
func (s *Store) ListTasks() ([]model.MonitorTask, error) {
	rows, err := s.rdb.Query(`SELECT ` + taskSelectCols() + ` FROM monitor_task ORDER BY create_time DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.MonitorTask
	for rows.Next() {
		var t model.MonitorTask
		var enabled, autoHeal int
		if err := rows.Scan(&t.ID, &t.Type, &t.Name, &t.Target, &t.IntervalSec, &t.TimeoutSec,
			&enabled, &t.Hosts, &t.AlertContent, &t.Runner, &t.HealCmd, &autoHeal,
			&t.CreateTime, &t.UpdateTime); err != nil {
			return nil, err
		}
		t.Enabled = enabled == 1
		t.AutoHeal = autoHeal == 1
		if t.Target == "" {
			t.Target = "{}"
		}
		list = append(list, t)
	}
	return list, rows.Err()
}

// GetTask 读取单个监控项。
func (s *Store) GetTask(id string) (*model.MonitorTask, error) {
	var t model.MonitorTask
	var enabled, autoHeal int
	err := s.rdb.QueryRow(`SELECT `+taskSelectCols()+` FROM monitor_task WHERE id=?`, id).
		Scan(&t.ID, &t.Type, &t.Name, &t.Target, &t.IntervalSec, &t.TimeoutSec,
			&enabled, &t.Hosts, &t.AlertContent, &t.Runner, &t.HealCmd, &autoHeal,
			&t.CreateTime, &t.UpdateTime)
	if err != nil {
		return nil, err
	}
	t.Enabled = enabled == 1
	t.AutoHeal = autoHeal == 1
	return &t, nil
}

// CreateTask 新增监控项。
func (s *Store) CreateTask(t *model.MonitorTask) error {
	if t.ID == "" {
		t.ID = newID()
	}
	if t.IntervalSec <= 0 {
		t.IntervalSec = 30
	}
	if t.TimeoutSec <= 0 {
		t.TimeoutSec = 5
	}
	if t.Hosts == "" {
		t.Hosts = "*"
	}
	if t.Runner == "" {
		t.Runner = RunnerOfType(t.Type)
	}
	now := nowStr()
	t.CreateTime, t.UpdateTime = now, now
	_, err := s.wdb.Exec(`INSERT INTO monitor_task(id, type, name, target, interval_sec, timeout_sec, enabled, hosts, alert_content, runner, heal_cmd, auto_heal, create_time, update_time)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, t.ID, t.Type, t.Name, t.Target, t.IntervalSec, t.TimeoutSec,
		boolInt(t.Enabled), t.Hosts, t.AlertContent, t.Runner, t.HealCmd, boolInt(t.AutoHeal), now, now)
	return err
}

// UpdateTask 更新监控项。
func (s *Store) UpdateTask(t *model.MonitorTask) error {
	now := nowStr()
	if t.Runner == "" {
		t.Runner = RunnerOfType(t.Type)
	}
	_, err := s.wdb.Exec(`UPDATE monitor_task SET type=?, name=?, target=?, interval_sec=?, timeout_sec=?, enabled=?, hosts=?, alert_content=?, runner=?, heal_cmd=?, auto_heal=?, update_time=? WHERE id=?`,
		t.Type, t.Name, t.Target, t.IntervalSec, t.TimeoutSec, boolInt(t.Enabled), t.Hosts,
		t.AlertContent, t.Runner, t.HealCmd, boolInt(t.AutoHeal), now, t.ID)
	return err
}

// RunnerOfType 根据监控项类型推断执行端。
func RunnerOfType(typ string) string {
	for _, t := range ServerTypes {
		if t == typ {
			return serverRunner
		}
	}
	return agentRunner
}

// DeleteTask 删除监控项及其状态数据。
func (s *Store) DeleteTask(id string) error {
	if _, err := s.wdb.Exec(`DELETE FROM monitor_task WHERE id=?`, id); err != nil {
		return err
	}
	_, _ = s.wdb.Exec(`DELETE FROM monitor_state WHERE task_id=?`, id)
	_, _ = s.wdb.Exec(`DELETE FROM monitor_event WHERE task_id=?`, id)
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TasksForAgent 返回分配给指定主机、由采集端执行的监控项。
func (s *Store) TasksForAgent(hostname string) ([]model.TaskAssignment, error) {
	tasks, err := s.ListTasks()
	if err != nil {
		return nil, err
	}
	var list []model.TaskAssignment
	for _, t := range tasks {
		if !t.Enabled || t.Runner == serverRunner || !matchHosts(t.Hosts, hostname) {
			continue
		}
		list = append(list, model.TaskAssignment{
			ID: t.ID, Type: t.Type, Name: t.Name, Target: t.Target,
			IntervalSec: t.IntervalSec, TimeoutSec: t.TimeoutSec,
			AutoHeal: t.AutoHeal, HealCmd: t.HealCmd,
		})
	}
	return list, nil
}

// TasksForServer 返回由服务端主动探测的监控项。
func (s *Store) TasksForServer() ([]model.MonitorTask, error) {
	tasks, err := s.ListTasks()
	if err != nil {
		return nil, err
	}
	var list []model.MonitorTask
	for _, t := range tasks {
		if t.Enabled && t.Runner == serverRunner {
			list = append(list, t)
		}
	}
	return list, nil
}

// matchHosts 判断主机名是否在作用范围内，'*' 表示全部主机。
func matchHosts(hosts, hostname string) bool {
	hosts = strings.TrimSpace(hosts)
	if hosts == "" || hosts == "*" {
		return true
	}
	for _, h := range strings.Split(hosts, ",") {
		if strings.TrimSpace(h) == hostname {
			return true
		}
	}
	return false
}

// SaveMonitorResults 写入一批监控项结果：更新最新状态，状态翻转时记录流水与告警。
func (s *Store) SaveMonitorResults(hostname string, results []model.MonitorResult) error {
	if len(results) == 0 {
		return nil
	}
	now := nowStr()

	taskCache := map[string]model.MonitorTask{}
	getTask := func(id string) (model.MonitorTask, bool) {
		if t, ok := taskCache[id]; ok {
			return t, true
		}
		t, err := s.GetTask(id)
		if err != nil {
			return model.MonitorTask{}, false
		}
		taskCache[id] = *t
		return *t, true
	}

	previous := map[string]string{} // taskID -> 上一次状态
	ids := make([]any, 0, len(results))
	placeholders := make([]string, 0, len(results))
	seen := map[string]bool{}
	for _, r := range results {
		if seen[r.TaskID] {
			continue
		}
		seen[r.TaskID] = true
		ids = append(ids, r.TaskID)
		placeholders = append(placeholders, "?")
	}
	if len(ids) > 0 {
		rows, err := s.rdb.Query(fmt.Sprintf(`SELECT task_id, status FROM monitor_state WHERE hostname=? AND task_id IN (%s)`,
			strings.Join(placeholders, ",")), append([]any{hostname}, ids...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var tid, status string
			if err := rows.Scan(&tid, &status); err != nil {
				rows.Close()
				return err
			}
			previous[tid] = status
		}
		rows.Close()
	}

	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	upsertStmt, err := tx.Prepare(`INSERT INTO monitor_state(id, task_id, hostname, type, status, value, msg, check_time, update_time)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(task_id, hostname) DO UPDATE SET status=excluded.status, value=excluded.value,
		msg=excluded.msg, check_time=excluded.check_time, update_time=excluded.update_time`)
	if err != nil {
		return err
	}
	defer upsertStmt.Close()

	eventStmt, err := tx.Prepare(`INSERT INTO monitor_event(id, task_id, hostname, type, name, status, prev_status, value, msg, check_time)
		VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer eventStmt.Close()

	for _, r := range results {
		if r.Status == "" {
			r.Status = "1"
		}
		if r.CheckTime == "" {
			r.CheckTime = now
		}
		taskName, taskType := r.Type, r.Type
		if t, ok := getTask(r.TaskID); ok {
			taskName, taskType = t.Name, t.Type
		}
		if _, err = upsertStmt.Exec(newID(), r.TaskID, hostname, taskType, r.Status, r.Value, r.Msg, r.CheckTime, now); err != nil {
			return err
		}
		prev, existed := previous[r.TaskID]
		if existed && prev == r.Status {
			// 状态未变化：持续异常时按冷却期补记流水，避免频繁写入
			if r.Status == "2" && !throttled(s, r.TaskID, hostname) {
				if _, err = eventStmt.Exec(newID(), r.TaskID, hostname, taskType, taskName, r.Status, prev, r.Value, r.Msg, r.CheckTime); err != nil {
					return err
				}
			}
			continue
		}
		if _, err = eventStmt.Exec(newID(), r.TaskID, hostname, taskType, taskName, r.Status, prev, r.Value, r.Msg, r.CheckTime); err != nil {
			return err
		}
		// 由异常转为正常：自动恢复关联告警
		if existed && prev == "2" && r.Status == "1" {
			if _, err = tx.Exec(`UPDATE alert SET state='0', update_time=? WHERE monitor_task_id=? AND hostname=? AND state='1'`,
				now, r.TaskID, hostname); err != nil {
				return err
			}
			continue
		}
		// 新产生的异常：写入告警并做冷却去重
		if r.Status == "2" {
			var existID string
			err := s.rdb.QueryRow(`SELECT id FROM alert WHERE hostname=? AND monitor_task_id=? AND state='1'`, hostname, r.TaskID).Scan(&existID)
			if err != nil {
				content := r.Msg
				if t, ok := getTask(r.TaskID); ok && t.AlertContent != "" {
					content = t.AlertContent + " " + content
				}
				if _, err = tx.Exec(`INSERT INTO alert(id, hostname, type, level, value, content, state, create_time, update_time, monitor_task_id)
					VALUES(?,?,?,'warning',?,?,'1',?,?,?)`, newID(), hostname, taskType, r.Value, content, now, now, r.TaskID); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

// ListMonitorStates 返回监控项在各主机上的最新状态。
func (s *Store) ListMonitorStates(taskID string) ([]model.MonitorState, error) {
	q := `SELECT ms.task_id, IFNULL(ms.type,''), IFNULL(mt.name,''), ms.hostname, IFNULL(ms.status,'1'),
		IFNULL(ms.value,''), IFNULL(ms.msg,''), IFNULL(ms.check_time,'')
		FROM monitor_state ms LEFT JOIN monitor_task mt ON mt.id = ms.task_id`
	var rows *sql.Rows
	var err error
	if taskID != "" {
		rows, err = s.rdb.Query(q+` WHERE ms.task_id=? ORDER BY ms.check_time DESC`, taskID)
	} else {
		rows, err = s.rdb.Query(q + ` ORDER BY ms.check_time DESC LIMIT 500`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.MonitorState
	for rows.Next() {
		var st model.MonitorState
		if err := rows.Scan(&st.TaskID, &st.Type, &st.Name, &st.Hostname, &st.Status, &st.Value, &st.Msg, &st.CheckTime); err != nil {
			return nil, err
		}
		list = append(list, st)
	}
	return list, rows.Err()
}

// MonitorEvents 返回监控项状态变化流水。
func (s *Store) MonitorEvents(limit int) ([]model.MonitorState, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.rdb.Query(`SELECT task_id, IFNULL(type,''), IFNULL(name,''), hostname, IFNULL(status,'1'),
		IFNULL(value,''), IFNULL(msg,''), IFNULL(check_time,'') FROM monitor_event ORDER BY check_time DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.MonitorState
	for rows.Next() {
		var st model.MonitorState
		if err := rows.Scan(&st.TaskID, &st.Type, &st.Name, &st.Hostname, &st.Status, &st.Value, &st.Msg, &st.CheckTime); err != nil {
			return nil, err
		}
		list = append(list, st)
	}
	return list, rows.Err()
}

// MonitorSummary 统计监控项数量与异常数量，用于大屏与仪表盘。
type MonitorSummary struct {
	TaskTotal int `json:"taskTotal"`
	TaskBad   int `json:"taskBad"`
	Items     int `json:"items"`
	BadItems  int `json:"badItems"`
}

// SummaryOfMonitor 汇总监控项健康度。
func (s *Store) SummaryOfMonitor() (*MonitorSummary, error) {
	var sum MonitorSummary
	if err := s.rdb.QueryRow(`SELECT COUNT(1) FROM monitor_task WHERE enabled=1`).Scan(&sum.TaskTotal); err != nil {
		return nil, err
	}
	if err := s.rdb.QueryRow(`SELECT COUNT(1) FROM monitor_state WHERE status='2'`).Scan(&sum.BadItems); err != nil {
		return nil, err
	}
	if err := s.rdb.QueryRow(`SELECT COUNT(1) FROM monitor_state`).Scan(&sum.Items); err != nil {
		return nil, err
	}
	// 存在任意异常主机的监控项数量
	if err := s.rdb.QueryRow(`SELECT COUNT(DISTINCT task_id) FROM monitor_state WHERE status='2'`).Scan(&sum.TaskBad); err != nil {
		return nil, err
	}
	return &sum, nil
}

// decodeTarget 将监控项配置解析为 map，失败时返回空配置。
func decodeTarget(raw string) map[string]string {
	out := map[string]string{}
	if raw == "" {
		return out
	}
	holder := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &holder); err != nil {
		return out
	}
	for k, v := range holder {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// throttled 判断该监控项的持续异常流水是否处于冷却期内，避免每轮都写入。
//
// 注意：本函数会在写事务执行过程中被调用，必须使用读连接池 rdb。
// 写连接池只有一条连接（用于串行化写），在事务内再向它申请连接会造成死锁。
func throttled(s *Store, taskID, hostname string) bool {
	var last string
	err := s.rdb.QueryRow(`SELECT IFNULL(MAX(check_time),'') FROM monitor_event WHERE task_id=? AND hostname=?`,
		taskID, hostname).Scan(&last)
	if err != nil || last == "" {
		return false
	}
	t, err := time.ParseInLocation(timeLayout, last, time.Local)
	if err != nil {
		return false
	}
	return time.Since(t) < monitorEventThrottle
}

// EnsureDefaultTasks 在没有任何监控项时写入一组示例配置，便于快速体验。
func (s *Store) EnsureDefaultTasks() error {
	var cnt int
	if err := s.rdb.QueryRow(`SELECT COUNT(1) FROM monitor_task`).Scan(&cnt); err != nil {
		return err
	}
	if cnt > 0 {
		return nil
	}
	samples := []model.MonitorTask{
		{Type: "http", Name: "控制台健康页", Target: `{"url":"http://127.0.0.1:9999/","expectCode":200}`, IntervalSec: 30},
		{Type: "port", Name: "本机 SSH 端口", Target: `{"host":"127.0.0.1","port":22}`, IntervalSec: 30},
		{Type: "ping", Name: "网关连通性", Target: `{"host":"127.0.0.1","count":2}`, IntervalSec: 30},
	}
	for i := range samples {
		if err := s.CreateTask(&samples[i]); err != nil {
			return err
		}
	}
	return nil
}

// LatestTargetHosts 返回按 CPU 使用率排序的前 N 台主机，供大屏展示。
func (s *Store) LatestTargetHosts(limit int) ([]model.Host, error) {
	hosts, err := s.ListHosts()
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(hosts) > limit {
		hosts = hosts[:limit]
	}
	return hosts, nil
}
