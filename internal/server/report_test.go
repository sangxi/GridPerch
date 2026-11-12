package server

import (
	"strings"
	"testing"
	"time"
)

// TestReportBucketDay 验证日报的时间桶与应发时刻：同一自然日同桶，
// 未到配置时刻不发（避免服务凌晨重启就补一条「今天」的报告）。
func TestReportBucketDay(t *testing.T) {
	loc := time.Local
	early := time.Date(2026, 11, 12, 7, 0, 0, 0, loc)
	bucket, due := reportBucket(reportKindDay, early, 8, 30)
	if bucket != "20261112" {
		t.Fatalf("日报桶应为 20261112，实际 %q", bucket)
	}
	if early.Before(due) != true {
		t.Fatalf("07:00 应早于应发时刻 08:30")
	}
	late := time.Date(2026, 11, 12, 9, 0, 0, 0, loc)
	if bucket2, _ := reportBucket(reportKindDay, late, 8, 30); bucket2 != bucket {
		t.Fatalf("同一自然日应同桶，实际 %q vs %q", bucket, bucket2)
	}
}

// TestReportBucketWeek 验证周报按「本周一 + 时刻」触发，且跨周换桶。
func TestReportBucketWeek(t *testing.T) {
	loc := time.Local
	// 2026-11-12 是周四；应发时刻是「本周一 08:30」，所以周一 08:29 还没到点
	thu := time.Date(2026, 11, 12, 12, 0, 0, 0, loc)
	bucket, due := reportBucket(reportKindWeek, thu, 8, 30)
	if !strings.Contains(bucket, "W") {
		t.Fatalf("周报桶应带周序号，实际 %q", bucket)
	}
	if due.Weekday() != time.Monday || due.Hour() != 8 || due.Minute() != 30 {
		t.Fatalf("周报应发时刻应为周一 08:30，实际 %v", due)
	}
	mon829 := time.Date(2026, 11, 9, 8, 29, 0, 0, loc)
	if !mon829.Before(due) {
		t.Fatalf("周一 08:29 应早于应发时刻")
	}
	mon9 := time.Date(2026, 11, 9, 9, 0, 0, 0, loc)
	if b2, _ := reportBucket(reportKindWeek, mon9, 8, 30); b2 != bucket {
		t.Fatalf("同一周应同桶，实际 %q vs %q", bucket, b2)
	}
	next := time.Date(2026, 11, 16, 9, 0, 0, 0, loc) // 下周一
	if b3, _ := reportBucket(reportKindWeek, next, 8, 30); b3 == bucket {
		t.Fatalf("跨周应换桶，仍为 %q", b3)
	}
}

// TestReportBucketMonth 验证月报跨月换桶。
func TestReportBucketMonth(t *testing.T) {
	loc := time.Local
	a, due := reportBucket(reportKindMonth, time.Date(2026, 11, 12, 9, 0, 0, 0, loc), 8, 30)
	if a != "202611" {
		t.Fatalf("月报桶应为 202611，实际 %q", a)
	}
	if due.Day() != 1 || due.Hour() != 8 || due.Minute() != 30 {
		t.Fatalf("月报应发时刻应为 1 日 08:30，实际 %v", due)
	}
	if b, _ := reportBucket(reportKindMonth, time.Date(2026, 12, 1, 9, 0, 0, 0, loc), 8, 30); b != "202612" {
		t.Fatalf("跨月应换桶，实际 %q", b)
	}
}

// TestReportBucketHour 验证小时报每小时换桶、且到点即发。
func TestReportBucketHour(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 11, 12, 10, 0, 5, 0, loc)
	bucket, due := reportBucket(reportKindHour, now, 8, 30)
	if bucket != "2026111210" {
		t.Fatalf("小时报桶应为 2026111210，实际 %q", bucket)
	}
	if now.Before(due) {
		t.Fatalf("整点后应立即到期")
	}
	if b2, _ := reportBucket(reportKindHour, now.Add(time.Hour), 8, 30); b2 == bucket {
		t.Fatalf("跨小时应换桶")
	}
}

// TestReportKindsOf 验证勾选串解析：非法值丢弃、顺序固定、去重。
func TestReportKindsOf(t *testing.T) {
	got := reportKindsOf("month, hour ,day,hour,bogus")
	want := []string{reportKindHour, reportKindDay, reportKindMonth}
	if len(got) != len(want) {
		t.Fatalf("解析结果长度不符：%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项应为 %s，实际 %s", i, want[i], got[i])
		}
	}
	if len(reportKindsOf("")) != 0 {
		t.Fatalf("空配置不应产生任何频率")
	}
}

// TestParseClock 验证时刻解析的容错。
func TestParseClock(t *testing.T) {
	cases := []struct {
		in   string
		h, m int
	}{
		{"09:05", 9, 5},
		{"23:59", 23, 59},
		{"", 8, 30},
		{"abc", 8, 30},
		{"25:00", 8, 30},
		{"8", 8, 30},
	}
	for _, c := range cases {
		h, m := parseClock(c.in)
		if h != c.h || m != c.m {
			t.Errorf("parseClock(%q)=(%d,%d)，期望 (%d,%d)", c.in, h, m, c.h, c.m)
		}
	}
}

// TestOfflineDuration 验证离线时长文案：日/时/分三档。
func TestOfflineDuration(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.Local)
	cases := map[string]string{
		"2026-11-12 11:30:00": "30 分钟",
		"2026-11-12 09:20:00": "2 小时 40 分钟",
		"2026-11-09 08:00:00": "3 天 4 小时",
		"bad":                 "时长未知",
	}
	for in, want := range cases {
		if got := offlineDuration(in, now); got != want {
			t.Errorf("offlineDuration(%q)=%q，期望 %q", in, got, want)
		}
	}
}

// TestNormalizeReportKind 验证非法频率兜底为日报，避免前端传空串导致不发送。
func TestNormalizeReportKind(t *testing.T) {
	if normalizeReportKind("WEEK") != reportKindWeek {
		t.Fatalf("大小写应忽略")
	}
	if normalizeReportKind("") != reportKindDay {
		t.Fatalf("空值应兜底为日报")
	}
	if normalizeReportKind("bogus") != reportKindDay {
		t.Fatalf("非法值应兜底为日报")
	}
}

// TestReportMarkdown 验证三种渲染都包含关键数字，避免某一通道少内容。
func TestReportMarkdown(t *testing.T) {
	secs := []reportSection{{Title: "主机概况", Lines: []string{"主机总数 3 台，在线 2 台，离线 1 台"}}}
	title := "[GridPerch] 每日巡检报告 2026-11-12 08:30"
	md := reportMarkdown(title, secs)
	txt := reportText(title, secs)
	h := reportHTML(title, secs)
	for name, body := range map[string]string{"markdown": md, "text": txt, "html": h} {
		if !strings.Contains(body, "主机总数 3 台") {
			t.Errorf("%s 缺少正文内容", name)
		}
		if !strings.Contains(body, "主机概况") {
			t.Errorf("%s 缺少小节标题", name)
		}
	}
	if !strings.Contains(md, "### ") {
		t.Errorf("markdown 应带标题层级")
	}
}
