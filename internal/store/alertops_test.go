package store

import (
	"testing"
	"time"

	"github.com/sangxi/gridperch/internal/model"
)

// seedAlert 插入一条告警并返回 ID。
func seedAlert(t *testing.T, s *Store, host, typ, level, at string) string {
	t.Helper()
	id := newID()
	execSeed(t, s, `INSERT INTO alert(id,hostname,type,level,value,content,state,create_time,update_time)
		VALUES(?,?,?,?,?,?,'1',?,?)`, id, host, typ, level, "90", "测试告警", at, at)
	return id
}

func alertByID(t *testing.T, s *Store, id string) model.Alert {
	t.Helper()
	list, err := s.AlertsOf("", 0)
	if err != nil {
		t.Fatalf("查询告警失败: %v", err)
	}
	for _, a := range list {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("未找到告警 %s", id)
	return model.Alert{}
}

func TestAckAndReleaseAlert(t *testing.T) {
	s := newTestStore(t)
	id := seedAlert(t, s, "web-01", "cpu", "warning", nowStr())

	if err := s.AckAlert(id, "张三"); err != nil {
		t.Fatalf("认领失败: %v", err)
	}
	a := alertByID(t, s, id)
	if a.AckBy != "张三" || a.AckTime == "" {
		t.Fatalf("认领未落库: %+v", a)
	}

	// 已被别人认领时不能再认领，且错误信息要指出是谁，否则前端只能干瞪眼
	if err := s.AckAlert(id, "李四"); err == nil {
		t.Fatal("重复认领应当报错")
	}

	if err := s.UnackAlert(id); err != nil {
		t.Fatalf("释放失败: %v", err)
	}
	if a = alertByID(t, s, id); a.AckBy != "" || a.AckTime != "" {
		t.Fatalf("释放后应回到未认领: %+v", a)
	}
}

func TestSetAlertNoteTruncatesByRune(t *testing.T) {
	s := newTestStore(t)
	id := seedAlert(t, s, "web-01", "cpu", "warning", nowStr())

	if err := s.SetAlertNote(id, "  已联系机房  "); err != nil {
		t.Fatalf("写备注失败: %v", err)
	}
	if a := alertByID(t, s, id); a.Note != "已联系机房" {
		t.Fatalf("备注应去掉首尾空白，实际 %q", a.Note)
	}

	// 中文按 rune 截断，不能把字符劈成半个 UTF-8 字节
	long := make([]rune, ackAlertMaxNote+50)
	for i := range long {
		long[i] = '警'
	}
	if err := s.SetAlertNote(id, string(long)); err != nil {
		t.Fatalf("写超长备注失败: %v", err)
	}
	got := []rune(alertByID(t, s, id).Note)
	if len(got) != ackAlertMaxNote {
		t.Fatalf("超长备注应按 rune 截到 %d，实际 %d", ackAlertMaxNote, len(got))
	}

	if err := s.SetAlertNote("不存在的ID", "x"); err == nil {
		t.Fatal("对不存在的告警写备注应当报错")
	}
}

func TestSilenceHitMatching(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	start := now.Add(-time.Hour).Format(timeLayout)
	end := now.Add(time.Hour).Format(timeLayout)

	// 只盯某台主机的 cpu 类告警
	if _, err := s.AddAlertSilence(model.AlertSilence{
		Hostname: "web-01", Type: "cpu", Reason: "割接", StartTime: start, EndTime: end,
	}); err != nil {
		t.Fatalf("新增静默失败: %v", err)
	}

	at := now.Format(timeLayout)
	if !s.IsAlertSilenced("web-01", "cpu", "warning", at) {
		t.Error("应命中同主机同类告警")
	}
	if s.IsAlertSilenced("web-02", "cpu", "warning", at) {
		t.Error("不同主机不应命中")
	}
	if s.IsAlertSilenced("web-01", "disk", "warning", at) {
		t.Error("不同监控项不应命中")
	}
	// 生效期外不命中
	if s.IsAlertSilenced("web-01", "cpu", "warning", now.Add(2*time.Hour).Format(timeLayout)) {
		t.Error("生效期外不应命中")
	}
}

func TestSilenceWildcardsAndLevel(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	start := now.Add(-time.Hour).Format(timeLayout)
	end := now.Add(time.Hour).Format(timeLayout)
	at := now.Format(timeLayout)

	// 主机与类型都留空 = 全站静默，但只对 critical 生效
	if _, err := s.AddAlertSilence(model.AlertSilence{
		Level: "critical", Reason: "演练", StartTime: start, EndTime: end,
	}); err != nil {
		t.Fatalf("新增静默失败: %v", err)
	}
	if !s.IsAlertSilenced("任意主机", "disk", "critical", at) {
		t.Error("全站静默应命中任意主机的 critical 告警")
	}
	if s.IsAlertSilenced("任意主机", "disk", "warning", at) {
		t.Error("限定级别时其它级别不应命中")
	}
}

func TestAddSilenceValidation(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()

	cases := []struct {
		name string
		in   model.AlertSilence
	}{
		{"缺结束时间", model.AlertSilence{StartTime: now.Format(timeLayout)}},
		{"时间格式错", model.AlertSilence{StartTime: "2026/10/17 10:00", EndTime: "2026/10/17 12:00"}},
		{"结束早于开始", model.AlertSilence{
			StartTime: now.Format(timeLayout),
			EndTime:   now.Add(-time.Hour).Format(timeLayout),
		}},
	}
	for _, c := range cases {
		if _, err := s.AddAlertSilence(c.in); err == nil {
			t.Errorf("%s：应当报错", c.name)
		}
	}
}

func TestSilenceListsAndDelete(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	mk := func(reason string, from, to time.Duration) model.AlertSilence {
		out, err := s.AddAlertSilence(model.AlertSilence{
			Hostname: "web-01", Type: "cpu", Reason: reason,
			StartTime: now.Add(from).Format(timeLayout),
			EndTime:   now.Add(to).Format(timeLayout),
		})
		if err != nil {
			t.Fatalf("新增静默失败: %v", err)
		}
		return out
	}
	live := mk("生效中", -time.Hour, time.Hour)
	mk("已结束", -3*time.Hour, -2*time.Hour)

	all, err := s.AlertSilences()
	if err != nil {
		t.Fatalf("读取静默列表失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("应有 2 条规则，实际 %d", len(all))
	}
	active := 0
	for _, r := range all {
		if r.Active {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("应只有 1 条生效中，实际 %d", active)
	}

	if err := s.DelAlertSilence(live.ID); err != nil {
		t.Fatalf("删除静默失败: %v", err)
	}
	if s.IsAlertSilenced("web-01", "cpu", "warning", nowStr()) {
		t.Error("删除后不应再命中")
	}
	if err := s.DelAlertSilence(live.ID); err == nil {
		t.Error("重复删除应当报错")
	}
}

// TestSilenceCheckerSuppressesNotifications 验证通知链路用的批量判断函数与逐条判断一致。
func TestSilenceCheckerSuppressesNotifications(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	if _, err := s.AddAlertSilence(model.AlertSilence{
		Hostname: "web-01", Type: "cpu",
		StartTime: now.Add(-time.Hour).Format(timeLayout),
		EndTime:   now.Add(time.Hour).Format(timeLayout),
	}); err != nil {
		t.Fatalf("新增静默失败: %v", err)
	}
	check := s.SilenceChecker()
	at := now.Format(timeLayout)
	if !check("web-01", "cpu", "warning", at) {
		t.Error("应命中")
	}
	if check("web-01", "disk", "warning", at) {
		t.Error("不应命中")
	}

	// 没有规则时返回恒 false，不能因为查不到就不发通知
	empty := newTestStore(t)
	if empty.SilenceChecker()("web-01", "cpu", "warning", at) {
		t.Error("无规则时不应命中任何告警")
	}
}

func TestUnackedAlertsAndEscalation(t *testing.T) {
	s := newTestStore(t)
	old := time.Now().Add(-2 * time.Hour).Format(timeLayout)
	oldID := seedAlert(t, s, "web-01", "cpu", "warning", old)
	freshID := seedAlert(t, s, "web-01", "disk", "warning", nowStr())

	// 30 分钟窗口只应捞出 2 小时前那条
	list, err := s.UnackedAlerts(30 * time.Minute)
	if err != nil {
		t.Fatalf("查询待催办告警失败: %v", err)
	}
	if len(list) != 1 || list[0].ID != oldID {
		t.Fatalf("应只捞到旧告警 %s，实际 %+v", oldID, list)
	}

	// 已认领的不再催办
	if err := s.AckAlert(freshID, "张三"); err != nil {
		t.Fatalf("认领失败: %v", err)
	}
	if list, _ = s.UnackedAlerts(0); len(list) != 1 || list[0].ID != oldID {
		t.Fatalf("已认领的告警不应进入催办队列，实际 %+v", list)
	}

	// 打过升级标记后不再重复捞，避免机器人挂了变成刷屏
	if err := s.MarkAlertEscalated([]string{oldID}); err != nil {
		t.Fatalf("标记升级失败: %v", err)
	}
	if list, _ = s.UnackedAlerts(30 * time.Minute); len(list) != 0 {
		t.Fatalf("已升级的告警不应重复催办，实际 %+v", list)
	}
}

// TestAlertListCarriesSilenceFlag 验证告警列表能标出「静默期内产生」，前端据此显示角标。
func TestAlertListCarriesSilenceFlag(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	at := now.Format(timeLayout)
	id := seedAlert(t, s, "web-01", "cpu", "warning", at)

	if a := alertByID(t, s, id); a.Silenced {
		t.Fatal("没有静默规则时不应标记为已静默")
	}
	if _, err := s.AddAlertSilence(model.AlertSilence{
		Hostname: "web-01", Type: "cpu",
		StartTime: now.Add(-time.Hour).Format(timeLayout),
		EndTime:   now.Add(time.Hour).Format(timeLayout),
	}); err != nil {
		t.Fatalf("新增静默失败: %v", err)
	}
	if a := alertByID(t, s, id); !a.Silenced {
		t.Fatal("命中静默规则的告警应标记为已静默")
	}
}

// TestRenameHostMovesSilenceRules 主机改名后按主机设的静默规则必须跟过去。
// 漏了这张表的话，改名当场就有告警钻出来——而改名通常发生在排障过程中，
// 正是最不该被打扰的时候。
func TestRenameHostMovesSilenceRules(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	execSeed(t, s, `INSERT INTO host(hostname,ip,version,state,last_seen,create_time) VALUES(?,?,?,?,?,?)`,
		"old-host", "10.0.0.9", "v1", "1", now.Format(timeLayout), now.Format(timeLayout))
	if _, err := s.AddAlertSilence(model.AlertSilence{
		Hostname: "old-host", Type: "cpu", Reason: "排障中",
		StartTime: now.Add(-time.Hour).Format(timeLayout),
		EndTime:   now.Add(time.Hour).Format(timeLayout),
	}); err != nil {
		t.Fatalf("新增静默失败: %v", err)
	}
	// 通配规则（hostname 为空）也要留着
	if _, err := s.AddAlertSilence(model.AlertSilence{
		Type: "disk", StartTime: now.Add(-time.Hour).Format(timeLayout),
		EndTime: now.Add(time.Hour).Format(timeLayout),
	}); err != nil {
		t.Fatalf("新增通配静默失败: %v", err)
	}

	if err := s.RenameHost("old-host", "new-host"); err != nil {
		t.Fatalf("改名失败: %v", err)
	}
	rules, err := s.AlertSilences()
	if err != nil {
		t.Fatalf("读取静默失败: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("改名不应丢失静默规则，实际 %d 条", len(rules))
	}
	var byHost, wildcard int
	for _, r := range rules {
		switch r.Hostname {
		case "new-host":
			byHost++
		case "":
			wildcard++
		case "old-host":
			t.Error("静默规则仍指向旧主机名")
		}
	}
	if byHost != 1 || wildcard != 1 {
		t.Fatalf("期望 1 条跟随改名 + 1 条通配，实际 byHost=%d wildcard=%d", byHost, wildcard)
	}
	if !s.IsAlertSilenced("new-host", "cpu", "warning", now.Format(timeLayout)) {
		t.Error("改名后按主机静默应继续生效")
	}
}

// TestAlertColumnsMigrateOnExistingDB 模拟老库升级：没有 ack_by 等列的库打开后应自动补齐。
func TestAlertColumnsMigrateOnExistingDB(t *testing.T) {
	s := newTestStore(t)
	for _, col := range []string{"ack_by", "ack_time", "note", "escalated"} {
		ok, err := s.hasColumn("alert", col)
		if err != nil {
			t.Fatalf("检查列 %s 失败: %v", col, err)
		}
		if !ok {
			t.Fatalf("alert 表缺少列 %s", col)
		}
	}
	for _, tbl := range []string{"alert_silence"} {
		var n int
		if err := s.rdb.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil {
			t.Fatalf("检查表 %s 失败: %v", tbl, err)
		}
		if n != 1 {
			t.Fatalf("缺少表 %s", tbl)
		}
	}
}
