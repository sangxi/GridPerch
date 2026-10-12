package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sangxi/OpsMonitor/internal/model"
)

// TestParsePSJSON 验证 PowerShell 输出能被正确解析成事件列表。
func TestParsePSJSON(t *testing.T) {
	// PowerShell ConvertTo-Json 的真实输出形态：数组 + 前后夹带 CLIXML 头尾
	raw := "#< CLIXML\n" +
		`[{"time":"2026-10-10 12:11:58","source":"Microsoft-Windows-DistributedCOM","level":"warning","id":10016,"category":"警告","content":"计算机-默认 权限设置并未..."}]` +
		"\n<Objs Version=\"1.1.0.1\"></Objs>\n"
	events, err := parsePSJSON(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("期望 1 条，实际 %d 条", len(events))
	}
	if events[0].EventID != 10016 {
		t.Errorf("EventID 期望 10016，实际 %d（JSON 键 id 未映射）", events[0].EventID)
	}
	if events[0].Source != "Microsoft-Windows-DistributedCOM" {
		t.Errorf("Source 解析错误: %q", events[0].Source)
	}
}

// TestParsePSJSONSingle 单条记录时 PowerShell 返回对象而非数组。
func TestParsePSJSONSingle(t *testing.T) {
	raw := `{"time":"2026-10-10 12:11:58","source":"Disk","level":"error","id":7,"content":"磁盘错误"}`
	events, err := parsePSJSON(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(events) != 1 || events[0].EventID != 7 || events[0].Level != "error" {
		t.Fatalf("单条解析结果不对: %+v", events)
	}
}

// TestSysEventKeyDistinct 校验去重键能区分不同事件 ID 的同一时刻事件。
func TestSysEventKeyDistinct(t *testing.T) {
	a := model.SysEvent{Time: "2026-10-10 12:11:58", Source: "S", Level: "warning", EventID: 10016}
	b := model.SysEvent{Time: "2026-10-10 12:11:58", Source: "S", Level: "warning", EventID: 10017}
	if sysEventKey("h", a) == sysEventKey("h", b) {
		t.Error("不同事件 ID 生成了相同的去重键，会被误判为重复")
	}
}

// TestParsePSJSONEmpty 空输出不应报错。
func TestParsePSJSONEmpty(t *testing.T) {
	events, err := parsePSJSON("   \n  ")
	if err != nil {
		t.Fatalf("空输出不应报错: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("期望空结果，实际 %d 条", len(events))
	}
}

var _ = strings.TrimSpace
var _ = json.Valid
