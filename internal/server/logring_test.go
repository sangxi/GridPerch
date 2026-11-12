package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// 回归测试：运行日志接口曾经因为 []*ipStat 经 normalizeJSON 反射重建而 panic，
// 请求直接 500（HTTP server 只 recover 掉这个请求，控制台整页拿不到数据）。
func TestHandleServerLogs(t *testing.T) {
	logMu.Lock()
	logRing = logRing[:0]
	logMu.Unlock()
	// 直接写缓冲，不改全局 log 输出，避免影响同包其它测试。
	logSink{}.Write([]byte("2026/11/09 14:00:00 拒绝上报：token 校验失败，来源 192.168.40.94:65429\n" +
		"2026/11/09 14:00:05 已推送 2 条告警至机器人「告警通知」\n"))

	req := httptest.NewRequest("GET", "/api/logs?q=192.168.40.94&limit=10", nil)
	w := httptest.NewRecorder()
	(&Server{}).handleServerLogs(w, req)
	if w.Code != 200 {
		t.Fatalf("状态码 = %d，期望 200；响应 %s", w.Code, w.Body.String())
	}
	var out struct {
		OK    bool           `json:"ok"`
		Total int            `json:"total"`
		Stats map[string]int `json:"stats"`
		List  []logEntry     `json:"list"`
		IPs   []ipStat       `json:"ips"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, w.Body.String())
	}
	if !out.OK || len(out.List) == 0 {
		t.Fatalf("未返回日志: %+v", out)
	}
	if len(out.IPs) == 0 || out.IPs[0].IP != "192.168.40.94" {
		t.Fatalf("IP 汇总不正确: %+v", out.IPs)
	}
	if out.Stats["refused"] == 0 {
		t.Fatalf("鉴权拒绝未被分类: %+v", out.Stats)
	}
}

// 公共序列化器：指针切片不能被解引用成值后回写，也不能 panic。
func TestNormalizeJSONPointerSlice(t *testing.T) {
	v := normalizeJSON(map[string]any{"ips": []*ipStat{{IP: "1.2.3.4", Count: 2}}})
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Fatalf("输出不是合法 JSON: %s", b)
	}
}
