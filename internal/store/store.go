// Package store 基于 SQLite 实现指标持久化，零外部依赖、单文件存储。
//
// 针对大规模场景（数千台主机、10 秒级上报）做了三类优化：
//  1. 读写连接池分离：写走单连接串行化避免锁竞争，读走多连接池提高并发；
//  2. 上报数据进入内存队列后批量事务落盘，HTTP 请求不被磁盘 IO 阻塞；
//  3. 长周期查询按分钟聚合降采样，避免回传海量原始点。
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sangxi/OpsMonitor/internal/config"
	"github.com/sangxi/OpsMonitor/internal/model"
)

const timeLayout = "2006-01-02 15:04:05"

// rawWindowMinutes 超过该窗口的历史查询走分钟级聚合。
const rawWindowMinutes = 180

// Store 封装数据库句柄、写队列与配置。
type Store struct {
	rdb     *sql.DB // 读连接池
	wdb     *sql.DB // 写连接池（单连接串行）
	cfg     *config.Server
	writeCh chan *model.Report
	auditCh chan model.AuditLog
	wg      sync.WaitGroup
	stop    chan struct{}
	once    sync.Once

	loginMu   sync.Mutex
	loginUser map[string]string // 主机 -> 上一次观测到的登录用户列表
}

// Open 打开（不存在则创建）数据库并完成建表、初始化管理员账号。
func Open(cfg *config.Server) (*Store, error) {
	if dir := filepath.Dir(cfg.DBPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	dsn := cfg.DBPath + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-65536)&_pragma=temp_store(MEMORY)"

	rdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	rdb.SetMaxOpenConns(maxInt(8, cfg.ReadConns))
	rdb.SetMaxIdleConns(4)
	rdb.SetConnMaxLifetime(time.Hour)

	wdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// 单一写连接彻底规避 SQLITE_BUSY，写吞吐由批量事务保证。
	wdb.SetMaxOpenConns(1)
	wdb.SetMaxIdleConns(1)

	s := &Store{
		rdb:       rdb,
		wdb:       wdb,
		cfg:       cfg,
		writeCh:   make(chan *model.Report, cfg.WriteQueue),
		auditCh:   make(chan model.AuditLog, 4096),
		stop:      make(chan struct{}),
		loginUser: map[string]string{},
	}
	if err = s.schema(); err != nil {
		return nil, err
	}
	if err = s.schemaExtra(); err != nil {
		return nil, err
	}
	if err = s.schemaRelease(); err != nil {
		return nil, err
	}
	if err = s.schemaWebhook(); err != nil {
		return nil, err
	}
	if err = s.schemaCatalog(); err != nil {
		return nil, err
	}
	if err = s.schemaSysEvent(); err != nil {
		return nil, err
	}
	if err = s.migrate(); err != nil {
		return nil, err
	}
	if err = s.migrateExtra(); err != nil {
		return nil, err
	}
	if err = s.EnsureUser(cfg.AdminUser, cfg.AdminPass); err != nil {
		return nil, err
	}
	s.wg.Add(1)
	go s.writeLoop()
	s.wg.Add(1)
	go s.auditLoop()
	return s, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Close 停止写队列并关闭连接。
func (s *Store) Close() error {
	s.once.Do(func() { close(s.stop) })
	s.wg.Wait()
	_ = s.rdb.Close()
	return s.wdb.Close()
}

func (s *Store) schema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS host (
			hostname TEXT PRIMARY KEY,
			ip TEXT, version TEXT, version_detail TEXT,
			cpu_core_num TEXT, cpu_xh TEXT,
			state TEXT DEFAULT '1',
			last_seen TEXT, create_time TEXT)`,
		`CREATE TABLE IF NOT EXISTS system_info (
			id TEXT PRIMARY KEY, hostname TEXT, version TEXT, version_detail TEXT,
			cpu_per REAL, mem_per REAL, cpu_core_num TEXT, cpu_xh TEXT,
			state TEXT, create_time TEXT)`,
		`CREATE TABLE IF NOT EXISTS cpu_state (
			id TEXT PRIMARY KEY, hostname TEXT, sys REAL, idle REAL, iowait REAL,
			date_str TEXT, create_time TEXT)`,
		`CREATE TABLE IF NOT EXISTS mem_state (
			id TEXT PRIMARY KEY, hostname TEXT, total TEXT, used TEXT, free TEXT,
			use_per REAL, date_str TEXT, create_time TEXT)`,
		`CREATE TABLE IF NOT EXISTS desk_state (
			id TEXT PRIMARY KEY, hostname TEXT, file_system TEXT, size TEXT, used TEXT,
			avail TEXT, use_per REAL, date_str TEXT, create_time TEXT)`,
		`CREATE TABLE IF NOT EXISTS netio_state (
			id TEXT PRIMARY KEY, hostname TEXT, rxpck TEXT, txpck TEXT,
			rxbyt REAL, txbyt REAL, date_str TEXT, create_time TEXT)`,
		`CREATE TABLE IF NOT EXISTS diskio_state (
			id TEXT PRIMARY KEY, hostname TEXT, read_kb REAL, write_kb REAL,
			read_iops REAL, write_iops REAL, date_str TEXT, create_time TEXT)`,
		`CREATE TABLE IF NOT EXISTS sys_load_state (
			id TEXT PRIMARY KEY, hostname TEXT, one_load REAL, five_load REAL,
			fifteen_load REAL, users TEXT, date_str TEXT, create_time TEXT)`,
		`CREATE TABLE IF NOT EXISTS alert (
			id TEXT PRIMARY KEY, hostname TEXT, type TEXT, level TEXT, value TEXT,
			content TEXT, state TEXT, create_time TEXT, update_time TEXT)`,
		`CREATE TABLE IF NOT EXISTS process_state (
			id TEXT PRIMARY KEY, hostname TEXT, pid INTEGER, name TEXT,
			cpu_per REAL, mem_per REAL, cmdline TEXT, create_time TEXT)`,
		`CREATE INDEX IF NOT EXISTS idx_process_host ON process_state(hostname, create_time)`,
		`CREATE TABLE IF NOT EXISTS user (
			username TEXT PRIMARY KEY, pass_hash TEXT, salt TEXT, create_time TEXT)`,
		// 监控项配置：type 决定检查器类型，hosts 为逗号分隔主机名，'*' 表示全部主机
		`CREATE TABLE IF NOT EXISTS monitor_task (
			id TEXT PRIMARY KEY, type TEXT, name TEXT, target TEXT,
			interval_sec INTEGER DEFAULT 30, timeout_sec INTEGER DEFAULT 5,
			enabled INTEGER DEFAULT 1, hosts TEXT, alert_content TEXT,
			create_time TEXT, update_time TEXT)`,
		// 监控项最新状态，按 task + host 唯一，只保留最新一条
		`CREATE TABLE IF NOT EXISTS monitor_state (
			id TEXT PRIMARY KEY, task_id TEXT, hostname TEXT, type TEXT,
			status TEXT, value TEXT, msg TEXT, check_time TEXT, update_time TEXT)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uniq_state ON monitor_state(task_id, hostname)`,
		// 状态变化流水，仅在异常发生或状态翻转时写入
		// prev_status 记录翻转前的状态，用于区分「真正的恢复」与「首次探测即正常」
		`CREATE TABLE IF NOT EXISTS monitor_event (
			id TEXT PRIMARY KEY, task_id TEXT, hostname TEXT, type TEXT, name TEXT,
			status TEXT, prev_status TEXT, value TEXT, msg TEXT, check_time TEXT)`,
		`CREATE INDEX IF NOT EXISTS idx_cpu_host_time ON cpu_state(hostname, create_time)`,
		`CREATE INDEX IF NOT EXISTS idx_mem_host_time ON mem_state(hostname, create_time)`,
		`CREATE INDEX IF NOT EXISTS idx_load_host_time ON sys_load_state(hostname, create_time)`,
		`CREATE INDEX IF NOT EXISTS idx_netio_host_time ON netio_state(hostname, create_time)`,
		`CREATE INDEX IF NOT EXISTS idx_diskio_host_time ON diskio_state(hostname, create_time)`,
		`CREATE INDEX IF NOT EXISTS idx_desk_host_time ON desk_state(hostname, create_time)`,
		`CREATE INDEX IF NOT EXISTS idx_event_time ON monitor_event(check_time)`,
		`CREATE INDEX IF NOT EXISTS idx_alert_state ON alert(state, create_time)`,
		// 硬件与运行时扩展指标的「最新有效值」，每个主机一行、随上报覆盖。
		// 温度 / SMART 这类指标不适合时序化：采集成本高且关注的是当前值，
		// 历史趋势由 monitor 监控项或外部时序库承接。
		`CREATE TABLE IF NOT EXISTS host_extra (
			hostname TEXT PRIMARY KEY,
			cpu_temp REAL DEFAULT 0,
			smart_status TEXT DEFAULT '',
			conn_count INTEGER DEFAULT 0,
			conn_established INTEGER DEFAULT 0,
			mac_addr TEXT DEFAULT '',
			ip_addr TEXT DEFAULT '',
			update_time TEXT)`,
	}
	for _, stmt := range stmts {
		if _, err := s.wdb.Exec(stmt); err != nil {
			return fmt.Errorf("建表失败: %w", err)
		}
	}
	return nil
}

// migrate 为早期版本库补充新增列。
func (s *Store) migrate() error {
	cols := map[string]map[string]string{
		"alert":         {"monitor_task_id": "ALTER TABLE alert ADD COLUMN monitor_task_id TEXT"},
		"monitor_event": {"prev_status": "ALTER TABLE monitor_event ADD COLUMN prev_status TEXT"},
		// 采集端上报的自身版本号，用于判断哪些主机需要升级
		"host": {"agent_version": "ALTER TABLE host ADD COLUMN agent_version TEXT"},
	}
	for table, wanted := range cols {
		rows, err := s.wdb.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
		if err != nil {
			return err
		}
		exist := map[string]bool{}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var def sql.NullString
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &def, &pk); err != nil {
				rows.Close()
				return err
			}
			exist[name] = true
		}
		rows.Close()
		for col, stmt := range wanted {
			if !exist[col] {
				if _, err := s.wdb.Exec(stmt); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nowStr() string { return time.Now().Format(timeLayout) }

// EnsureUser 在账号不存在时创建用户，已存在则不做任何修改。
func (s *Store) EnsureUser(username, password string) error {
	var cnt int
	if err := s.wdb.QueryRow(`SELECT COUNT(1) FROM user WHERE username=?`, username).Scan(&cnt); err != nil {
		return err
	}
	if cnt > 0 {
		return nil
	}
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	hash := hashPassword(password, hex.EncodeToString(salt))
	_, err := s.wdb.Exec(`INSERT INTO user(username, pass_hash, salt, create_time) VALUES(?,?,?,?)`,
		username, hash, hex.EncodeToString(salt), nowStr())
	return err
}

func hashPassword(password, salt string) string {
	sum := sha256.Sum256([]byte(salt + ":" + password))
	return hex.EncodeToString(sum[:])
}

// CheckUser 校验用户名密码。
func (s *Store) CheckUser(username, password string) bool {
	var hash, salt string
	err := s.rdb.QueryRow(`SELECT pass_hash, salt FROM user WHERE username=?`, username).Scan(&hash, &salt)
	if err != nil {
		return false
	}
	return hash == hashPassword(password, salt)
}

/* ---------------- 写入队列 ---------------- */

// SaveReport 将上报数据推入异步写队列，不阻塞 HTTP 请求。
func (s *Store) SaveReport(r *model.Report) error {
	if r == nil || r.SystemInfo == nil {
		return errors.New("上报数据为空")
	}
	select {
	case s.writeCh <- r:
		return nil
	default:
		return errors.New("服务端写入队列已满，请稍后重试")
	}
}

// writeLoop 累积上报数据后批量事务落盘。
func (s *Store) writeLoop() {
	defer s.wg.Done()
	batch := make([]*model.Report, 0, 512)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.flushReports(batch); err != nil {
			log.Printf("批量落盘失败: %v", err)
		}
		batch = batch[:0]
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			for {
				select {
				case r := <-s.writeCh:
					batch = append(batch, r)
				default:
					flush()
					return
				}
			}
		case r := <-s.writeCh:
			batch = append(batch, r)
			if len(batch) >= 512 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// flushReports 在一个事务内写入整批上报数据。
func (s *Store) flushReports(batch []*model.Report) error {
	start := time.Now()
	tx, err := s.wdb.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := map[string]*sql.Stmt{}
	defer func() {
		for _, st := range stmts {
			_ = st.Close()
		}
	}()
	prepare := func(key, query string) (*sql.Stmt, error) {
		if st, ok := stmts[key]; ok {
			return st, nil
		}
		st, err := tx.Prepare(query)
		if err != nil {
			return nil, err
		}
		stmts[key] = st
		return st, nil
	}

	now := nowStr()
	dateStr := time.Now().Format("01-02 15:04:05")

	for _, r := range batch {
		host := r.SystemInfo.Hostname
		if host == "" {
			host = r.Hostname
		}
		if host == "" {
			continue
		}
		r.SystemInfo.Hostname = host

		st, err := prepare("host", `INSERT INTO host(hostname, ip, version, version_detail, cpu_core_num, cpu_xh, agent_version, state, last_seen, create_time)
			VALUES(?,?,?,?,?,?,?,'1',?,?)
			ON CONFLICT(hostname) DO UPDATE SET ip=excluded.ip, version=excluded.version,
			version_detail=excluded.version_detail, cpu_core_num=excluded.cpu_core_num,
			cpu_xh=excluded.cpu_xh, agent_version=excluded.agent_version,
			state='1', last_seen=excluded.last_seen`)
		if err != nil {
			return err
		}
		if _, err = st.Exec(host, r.SystemInfo.IP, r.SystemInfo.Version, r.SystemInfo.VersionDetail,
			r.SystemInfo.CpuCoreNum, r.SystemInfo.CpuXh, r.AgentVersion, now, now); err != nil {
			return err
		}

		st, err = prepare("system", `INSERT INTO system_info(id, hostname, version, version_detail, cpu_per, mem_per, cpu_core_num, cpu_xh, state, create_time)
			VALUES(?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		if _, err = st.Exec(newID(), host, r.SystemInfo.Version, r.SystemInfo.VersionDetail, r.SystemInfo.CpuPer,
			r.SystemInfo.MemPer, r.SystemInfo.CpuCoreNum, r.SystemInfo.CpuXh, "1", now); err != nil {
			return err
		}

		if r.CpuState != nil {
			st, err = prepare("cpu", `INSERT INTO cpu_state(id, hostname, sys, idle, iowait, date_str, create_time) VALUES(?,?,?,?,?,?,?)`)
			if err != nil {
				return err
			}
			if _, err = st.Exec(newID(), host, r.CpuState.Sys, r.CpuState.Idle, r.CpuState.Iowait, dateStr, now); err != nil {
				return err
			}
		}
		if r.MemState != nil {
			st, err = prepare("mem", `INSERT INTO mem_state(id, hostname, total, used, free, use_per, date_str, create_time) VALUES(?,?,?,?,?,?,?,?)`)
			if err != nil {
				return err
			}
			if _, err = st.Exec(newID(), host, r.MemState.Total, r.MemState.Used, r.MemState.Free, r.MemState.UsePer, dateStr, now); err != nil {
				return err
			}
		}
		if r.SysLoadState != nil {
			st, err = prepare("load", `INSERT INTO sys_load_state(id, hostname, one_load, five_load, fifteen_load, users, date_str, create_time) VALUES(?,?,?,?,?,?,?,?)`)
			if err != nil {
				return err
			}
			if _, err = st.Exec(newID(), host, r.SysLoadState.OneLoad, r.SysLoadState.FiveLoad, r.SysLoadState.FifteenLoad,
				r.SysLoadState.Users, dateStr, now); err != nil {
				return err
			}
		}
		if r.NetIoState != nil {
			st, err = prepare("netio", `INSERT INTO netio_state(id, hostname, rxpck, txpck, rxbyt, txbyt, date_str, create_time) VALUES(?,?,?,?,?,?,?,?)`)
			if err != nil {
				return err
			}
			if _, err = st.Exec(newID(), host, r.NetIoState.Rxpck, r.NetIoState.Txpck, parseFloat(r.NetIoState.Rxbyt),
				parseFloat(r.NetIoState.Txbyt), dateStr, now); err != nil {
				return err
			}
		}
		if r.DiskIoState != nil {
			st, err = prepare("diskio", `INSERT INTO diskio_state(id, hostname, read_kb, write_kb, read_iops, write_iops, date_str, create_time) VALUES(?,?,?,?,?,?,?,?)`)
			if err != nil {
				return err
			}
			if _, err = st.Exec(newID(), host, r.DiskIoState.ReadKB, r.DiskIoState.WriteKB,
				r.DiskIoState.ReadIOPS, r.DiskIoState.WriteIOPS, dateStr, now); err != nil {
				return err
			}
		}
		for i := range r.DeskStates {
			d := r.DeskStates[i]
			st, err = prepare("desk", `INSERT INTO desk_state(id, hostname, file_system, size, used, avail, use_per, date_str, create_time) VALUES(?,?,?,?,?,?,?,?,?)`)
			if err != nil {
				return err
			}
			if _, err = st.Exec(newID(), host, d.FileSystem, d.Size, d.Used, d.Avail, parseFloat(d.UsePer), dateStr, now); err != nil {
				return err
			}
		}
		for i := range r.Processes {
			p := r.Processes[i]
			st, err = prepare("process", `INSERT INTO process_state(id, hostname, pid, name, cpu_per, mem_per, cmdline, create_time) VALUES(?,?,?,?,?,?,?,?)`)
			if err != nil {
				return err
			}
			cmdline := p.Cmdline
			if len(cmdline) > 500 {
				cmdline = cmdline[:500]
			}
			if _, err = st.Exec(newID(), host, p.PID, p.Name, p.CPUPer, p.MemPer, cmdline, now); err != nil {
				return err
			}
		}
		if r.HostExtra != nil {
			// 温度与 SMART 都可能因为偶发读取失败而为 0 / 空串。
			// 直接覆盖会把上一轮的有效值清掉，大屏上就出现「温度忽高忽低」的假象，
			// 因此只有采到有效值时才更新对应列，其余列保留原值。
			st, err = prepare("extra", `INSERT INTO host_extra(hostname, cpu_temp, smart_status, conn_count,
				conn_established, mac_addr, ip_addr, update_time) VALUES(?,?,?,?,?,?,?,?)
				ON CONFLICT(hostname) DO UPDATE SET
				cpu_temp=CASE WHEN excluded.cpu_temp>0 THEN excluded.cpu_temp ELSE host_extra.cpu_temp END,
				smart_status=CASE WHEN excluded.smart_status<>'' THEN excluded.smart_status ELSE host_extra.smart_status END,
				conn_count=excluded.conn_count,
				conn_established=excluded.conn_established,
				mac_addr=CASE WHEN excluded.mac_addr<>'' THEN excluded.mac_addr ELSE host_extra.mac_addr END,
				ip_addr=CASE WHEN excluded.ip_addr<>'' THEN excluded.ip_addr ELSE host_extra.ip_addr END,
				update_time=excluded.update_time`)
			if err != nil {
				return err
			}
			e := r.HostExtra
			if _, err = st.Exec(host, e.CpuTemp, e.SmartStatus, e.ConnCount, e.ConnEstablished,
				e.MacAddr, e.IpAddr, now); err != nil {
				return err
			}
		}
	}

	if err = tx.Commit(); err != nil {
		return err
	}
	// 告警判定在数据可见后执行，避免读到未提交内容
	for _, r := range batch {
		s.checkAlerts(r, r.SystemInfo.Hostname, now)
		s.saveSysEvents(r.SystemInfo.Hostname, r.SysEvents, now)
	}
	if len(batch) > 64 {
		log.Printf("批量落盘 %d 条上报，耗时 %s", len(batch), time.Since(start).Round(time.Millisecond))
	}
	return nil
}

func parseFloat(v string) float64 {
	v = strings.TrimSuffix(strings.TrimSpace(v), "%")
	var f float64
	_, _ = fmt.Sscan(v, &f)
	return f
}

// checkAlerts 依据阈值生成告警，同类告警在冷却期内不重复记录。
func (s *Store) checkAlerts(r *model.Report, host, now string) {
	if host == "" {
		return
	}
	// 主机登录提醒：登录用户发生变化时生成一条提示级告警
	if r.SysLoadState != nil {
		s.watchLogin(host, r.SysLoadState.Users, now)
	}
	type candidate struct {
		typ, level, value, content string
	}
	var list []candidate
	cfg := s.cfg
	if r.CpuState != nil && r.CpuState.Sys >= cfg.CPUAlert {
		list = append(list, candidate{"cpu", "warning", fmt.Sprintf("%.1f%%", r.CpuState.Sys),
			fmt.Sprintf("CPU 使用率 %.1f%% 超过阈值 %.0f%%", r.CpuState.Sys, cfg.CPUAlert)})
	}
	if r.MemState != nil && r.MemState.UsePer >= cfg.MemAlert {
		list = append(list, candidate{"mem", "warning", fmt.Sprintf("%.1f%%", r.MemState.UsePer),
			fmt.Sprintf("内存使用率 %.1f%% 超过阈值 %.0f%%", r.MemState.UsePer, cfg.MemAlert)})
	}
	if r.SysLoadState != nil && r.SysLoadState.FiveLoad >= cfg.LoadAlert {
		list = append(list, candidate{"load", "warning", fmt.Sprintf("%.2f", r.SysLoadState.FiveLoad),
			fmt.Sprintf("5 分钟负载 %.2f 超过阈值 %.0f", r.SysLoadState.FiveLoad, cfg.LoadAlert)})
	}
	for _, d := range r.DeskStates {
		if p := parseFloat(d.UsePer); p >= cfg.DiskAlert {
			list = append(list, candidate{"disk", "warning", fmt.Sprintf("%.1f%%", p),
				fmt.Sprintf("分区 %s 使用率 %.1f%% 超过阈值 %.0f%%", d.FileSystem, p, cfg.DiskAlert)})
		}
	}
	for _, c := range list {
		var id string
		err := s.wdb.QueryRow(`SELECT id FROM alert WHERE hostname=? AND type=? AND value=? AND state='1'
			AND update_time >= ?`, host, c.typ, c.value,
			time.Now().Add(-cfg.AlertCoolDown).Format(timeLayout)).Scan(&id)
		if err == nil {
			continue // 冷却期内已有同类告警
		}
		if _, err = s.wdb.Exec(`INSERT INTO alert(id, hostname, type, level, value, content, state, create_time, update_time)
			VALUES(?,?,?,?,?,?,'1',?,?)`, newID(), host, c.typ, c.level, c.value, c.content, now, now); err != nil {
			log.Printf("写入告警失败: %v", err)
		}
	}
}

/* ---------------- 运维维护 ---------------- */
// RefreshHostStates 将长时间未上报的主机标记为离线。
func (s *Store) RefreshHostStates() error {
	deadline := time.Now().Add(-time.Duration(s.cfg.OfflineSec) * time.Second).Format(timeLayout)
	_, err := s.wdb.Exec(`UPDATE host SET state='2' WHERE state='1' AND last_seen < ?`, deadline)
	return err
}

// Purge 清理超过保留期的历史数据。Retention<=0 表示永久保留，仅清理队列类数据。
func (s *Store) Purge() error {
	if s.cfg.Retention > 0 {
		deadline := time.Now().AddDate(0, 0, -s.cfg.Retention).Format(timeLayout)
		tables := []string{"system_info", "cpu_state", "mem_state", "desk_state", "netio_state",
			"sys_load_state", "diskio_state", "process_state", "monitor_event", "login_event",
			"audit_log", "mail_log", "sys_event"}
		for _, t := range tables {
			if _, err := s.wdb.Exec(fmt.Sprintf(`DELETE FROM %s WHERE create_time < ?`, t), deadline); err != nil {
				return err
			}
		}
		if _, err := s.wdb.Exec(`DELETE FROM alert WHERE state='0' AND update_time < ?`, deadline); err != nil {
			return err
		}
	}
	// 指令与最近告警类队列只保留近期记录，避免无限膨胀
	queueDeadline := time.Now().AddDate(0, 0, -30).Format(timeLayout)
	if _, err := s.wdb.Exec(`DELETE FROM command WHERE create_time < ?`, queueDeadline); err != nil {
		return err
	}
	return nil
}

// Checkpoint 执行 WAL 检查点，控制 wal 文件体积。
func (s *Store) Checkpoint() error {
	_, err := s.wdb.Exec(`PRAGMA wal_checkpoint(PASSIVE)`)
	return err
}

/* ---------------- 查询 ---------------- */

// ListHosts 返回主机列表及最新快照。
func (s *Store) ListHosts() ([]model.Host, error) {
	if err := s.RefreshHostStates(); err != nil {
		return nil, err
	}
	// 目录归属一次取全量，避免逐主机二次查询
	cats, err := s.HostCatalogMap()
	if err != nil {
		return nil, err
	}
	// 用子查询一次性取出最新指标，避免逐主机二次查询
	rows, err := s.rdb.Query(`SELECT hostname, IFNULL(ip,''), IFNULL(version,''), IFNULL(version_detail,''), IFNULL(state,'2'),
		IFNULL(cpu_core_num,''), IFNULL(cpu_xh,''), IFNULL(last_seen,''),
		(SELECT IFNULL(cpu_per,0) FROM system_info s WHERE s.hostname = h.hostname ORDER BY s.create_time DESC LIMIT 1),
		(SELECT IFNULL(mem_per,0) FROM system_info s WHERE s.hostname = h.hostname ORDER BY s.create_time DESC LIMIT 1),
		IFNULL(agent_version,'')
		FROM host h ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hosts []model.Host
	for rows.Next() {
		var h model.Host
		var lastSeen, versionDetail string
		if err := rows.Scan(&h.Hostname, &h.IP, &h.Version, &versionDetail, &h.State, &h.CpuCoreNum, &h.CpuXh,
			&lastSeen, &h.CpuPer, &h.MemPer, &h.AgentVersion); err != nil {
			return nil, err
		}
		h.OS = hostOS(versionDetail, h.Version)
		h.LastSeen = lastSeen
		if ref, ok := cats[h.Hostname]; ok {
			h.CatalogID, h.CatalogName = ref.CatalogID, ref.Name
		}
		if t, err := time.ParseInLocation(timeLayout, lastSeen, time.Local); err == nil {
			h.UptimeDesc = humanDuration(time.Since(t))
		}
		hosts = append(hosts, h)
	}
	return hosts, rows.Err()
}

// hostOS 推导主机操作系统代号，供控制台「工具箱」按系统过滤本机命令。
//
// 首选采集端上报的 version_detail（形如 "linux / 5.15.0"，取自 runtime.GOOS），
// 缺失时（老版本采集端）退化为对美化版本名做关键字匹配。
func hostOS(versionDetail, version string) string {
	d := strings.ToLower(strings.TrimSpace(versionDetail))
	if i := strings.Index(d, "/"); i > 0 {
		d = strings.TrimSpace(d[:i])
	}
	switch d {
	case "linux", "windows", "darwin", "freebsd", "openbsd", "netbsd", "solaris", "aix":
		return d
	}
	v := strings.ToLower(version)
	switch {
	case strings.Contains(v, "mac os"), strings.Contains(v, "macos"), strings.Contains(v, "darwin"):
		return "darwin"
	case strings.Contains(v, "windows"):
		return "windows"
	}
	for _, kw := range []string{"ubuntu", "centos", "debian", "red hat", "redhat", "rocky", "almalinux",
		"alma", "fedora", "suse", "opensuse", "alpine", "kali", "arch", "oracle linux", "linux", "openwrt"} {
		if strings.Contains(v, kw) {
			return "linux"
		}
	}
	return ""
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%d 秒前", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	}
	return fmt.Sprintf("%d 天前", int(d.Hours()/24))
}

// Dashboard 返回仪表盘聚合数据。
func (s *Store) Dashboard() (*model.Dashboard, error) {
	hosts, err := s.ListHosts()
	if err != nil {
		return nil, err
	}
	d := &model.Dashboard{HostTotal: len(hosts), Hosts: hosts}
	var cpuSum, memSum float64
	var online int
	for _, h := range hosts {
		if h.State == "1" {
			online++
			cpuSum += h.CpuPer
			memSum += h.MemPer
		} else {
			d.HostOffline++
		}
	}
	d.HostOnline = online
	if online > 0 {
		d.AvgCpu = round1(cpuSum / float64(online))
		d.AvgMem = round1(memSum / float64(online))
	}
	if err = s.rdb.QueryRow(`SELECT COUNT(1) FROM alert WHERE state='1'`).Scan(&d.AlertActive); err != nil {
		return nil, err
	}
	d.Alerts, err = s.Alerts(8)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// Alerts 返回最近的告警记录。
func (s *Store) Alerts(limit int) ([]model.Alert, error) {
	return s.AlertsOf("", limit)
}

// AlertsOf 返回告警列表，host 非空时只看该主机（主机详情页用，避免前端拉全量再过滤）。
func (s *Store) AlertsOf(host string, limit int) ([]model.Alert, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, hostname, IFNULL(type,''), IFNULL(level,'warning'), IFNULL(value,''),
		IFNULL(content,''), IFNULL(state,'1'), IFNULL(create_time,'') FROM alert`
	var args []any
	if host != "" {
		q += ` WHERE hostname=?`
		args = append(args, host)
	}
	q += ` ORDER BY create_time DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.rdb.Query(q, args...)
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

// ResolveAlert 将告警置为已恢复。
func (s *Store) ResolveAlert(id string) error {
	_, err := s.wdb.Exec(`UPDATE alert SET state='0', update_time=? WHERE id=?`, nowStr(), id)
	return err
}

// LatestDisks 返回主机最近一次采集的磁盘分区列表。
func (s *Store) LatestDisks(host string) ([]model.DeskState, error) {
	lastTime := ""
	if err := s.rdb.QueryRow(`SELECT IFNULL(MAX(create_time),'') FROM desk_state WHERE hostname=?`, host).Scan(&lastTime); err != nil {
		return nil, err
	}
	if lastTime == "" {
		return nil, nil
	}
	rows, err := s.rdb.Query(`SELECT hostname, IFNULL(file_system,''), IFNULL(size,''), IFNULL(used,''), IFNULL(avail,''), IFNULL(use_per,0), date_str
		FROM desk_state WHERE hostname=? AND create_time=? ORDER BY file_system`, host, lastTime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.DeskState
	for rows.Next() {
		var d model.DeskState
		var per float64
		if err := rows.Scan(&d.Hostname, &d.FileSystem, &d.Size, &d.Used, &d.Avail, &per, &d.DateStr); err != nil {
			return nil, err
		}
		d.UsePer = fmt.Sprintf("%.1f", per)
		list = append(list, d)
	}
	return list, rows.Err()
}

// metricQuery 描述一类指标的取数方式，agg 非空时按时间分组聚合（多分区场景）。
type metricQuery struct {
	name  string
	unit  string
	table string
	col   string
	agg   string
}

var metricDefs = map[string][]metricQuery{
	"cpu":    {{"CPU 使用率", "%", "cpu_state", "sys", ""}},
	"mem":    {{"内存使用率", "%", "mem_state", "use_per", ""}},
	"load":   {{"1 分钟负载", "", "sys_load_state", "one_load", ""}, {"5 分钟负载", "", "sys_load_state", "five_load", ""}, {"15 分钟负载", "", "sys_load_state", "fifteen_load", ""}},
	"net":    {{"下行速率", "KB/s", "netio_state", "rxbyt", ""}, {"上行速率", "KB/s", "netio_state", "txbyt", ""}},
	"disk":   {{"磁盘使用率（最高分区）", "%", "desk_state", "use_per", "MAX"}},
	"diskio": {{"磁盘读速率", "KB/s", "diskio_state", "read_kb", ""}, {"磁盘写速率", "KB/s", "diskio_state", "write_kb", ""}},
}

// Metrics 返回指定主机、指定类型、指定时间窗口内的时序数据。
// 窗口超过 rawWindowMinutes 时在 SQL 层按分钟聚合，避免回传海量原始点。
func (s *Store) Metrics(host, typ string, minutes int) ([]model.Series, error) {
	defs, ok := metricDefs[typ]
	if !ok {
		return nil, fmt.Errorf("不支持的指标类型: %s", typ)
	}
	if minutes <= 0 {
		minutes = 60
	}
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).Format(timeLayout)
	groupByMinute := minutes > rawWindowMinutes

	var out []model.Series
	for _, d := range defs {
		expr := d.col
		group := ""
		if groupByMinute {
			expr = fmt.Sprintf("AVG(%s)", d.col)
			group = " GROUP BY minute_slot"
		} else if d.agg != "" {
			expr = fmt.Sprintf("%s(%s)", d.agg, d.col)
			group = " GROUP BY create_time"
		}
		timeExpr := "create_time"
		if groupByMinute {
			timeExpr = "substr(create_time,1,16) || ':00'"
		}
		q := fmt.Sprintf(`SELECT %s AS minute_slot, IFNULL(%s,0) FROM %s WHERE hostname=? AND create_time>=?%s ORDER BY minute_slot`,
			timeExpr, expr, d.table, group)

		rows, err := s.rdb.Query(q, host, since)
		if err != nil {
			return nil, err
		}
		pts := []model.Point{}
		for rows.Next() {
			var t string
			var v float64
			if err := rows.Scan(&t, &v); err != nil {
				rows.Close()
				return nil, err
			}
			pts = append(pts, model.Point{Time: t, Value: round1(v)})
		}
		rows.Close()
		if rows.Err() != nil {
			return nil, rows.Err()
		}
		out = append(out, model.Series{Name: d.name, Unit: d.unit, Points: downsample(pts, 240)})
	}
	return out, nil
}

// metricOverviewDef 描述仪表盘总览趋势中的一条平均曲线。
type metricOverviewDef struct {
	name  string
	unit  string
	table string
	col   string
}

var metricOverviewDefs = []metricOverviewDef{
	{"平均 CPU 使用率", "%", "cpu_state", "sys"},
	{"平均内存使用率", "%", "mem_state", "use_per"},
}

// MetricOverview 返回全部主机在指定窗口内的平均资源趋势，供仪表盘总览图表使用。
// 无论窗口多长都在 SQL 层按分钟聚合，避免主机数量多时点位数爆炸。
func (s *Store) MetricOverview(minutes int) ([]model.Series, error) {
	if minutes <= 0 {
		minutes = 180
	}
	if minutes > 7*24*60 {
		minutes = 7 * 24 * 60
	}
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).Format(timeLayout)

	out := make([]model.Series, 0, len(metricOverviewDefs))
	for _, d := range metricOverviewDefs {
		q := fmt.Sprintf(`SELECT substr(create_time,1,16) || ':00' AS slot, IFNULL(AVG(%s),0) FROM %s
			WHERE create_time>=? GROUP BY slot ORDER BY slot`, d.col, d.table)
		rows, err := s.rdb.Query(q, since)
		if err != nil {
			return nil, err
		}
		pts := make([]model.Point, 0, 120)
		for rows.Next() {
			var t string
			var v float64
			if err := rows.Scan(&t, &v); err != nil {
				rows.Close()
				return nil, err
			}
			pts = append(pts, model.Point{Time: t, Value: round1(v)})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		out = append(out, model.Series{Name: d.name, Unit: d.unit, Points: downsample(pts, 240)})
	}
	return out, nil
}

// downsample 将点数限制在 max 以内，采用等间隔抽样保留趋势。
func downsample(pts []model.Point, max int) []model.Point {
	if len(pts) <= max {
		return pts
	}
	step := float64(len(pts)) / float64(max)
	res := make([]model.Point, 0, max)
	for i := 0; i < max; i++ {
		res = append(res, pts[int(float64(i)*step)])
	}
	return res
}
