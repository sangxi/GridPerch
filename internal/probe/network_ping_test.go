package probe

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestPingByCommandLoopback 验证命令兜底路径可用：回环地址必然可达，
// 同时校验丢包率/时延解析能在真实 ping 输出上命中。
func TestPingByCommandLoopback(t *testing.T) {
	r := pingByCommand(context.Background(), "127.0.0.1", 2, 5*time.Second)
	t.Logf("status=%s value=%s msg=%s", r.Status, r.Value, r.Msg)
	if r.Status != StatusOK {
		t.Fatalf("回环地址应可达，实际 %+v", r)
	}
	if !strings.Contains(r.Msg, "丢包率") {
		t.Fatalf("消息应包含丢包率，实际 %q", r.Msg)
	}
	if r.Value == "" || r.Value == "100" {
		t.Fatalf("丢包率解析异常：%q", r.Value)
	}
}

// TestProbeICMPLoopback 验证 probeICMP 端到端（库方式失败时自动走命令兜底）。
func TestProbeICMPLoopback(t *testing.T) {
	res := Run("icmp", map[string]string{"host": "127.0.0.1", "count": "2"}, 5*time.Second)
	t.Logf("status=%s value=%s msg=%s", res.Status, res.Value, res.Msg)
	if res.Status != StatusOK {
		t.Fatalf("回环地址应可达，实际 %+v", res)
	}
}

// TestPingParsers 校验中英文 ping 输出的解析正则。
func TestPingParsers(t *testing.T) {
	cn := "    数据包: 已发送 = 4，已接收 = 4，丢失 = 0 (0% 丢失)，\n最短 = 0ms，最长 = 0ms，平均 = 0ms"
	if m := pingLossRe.FindStringSubmatch(cn); m == nil || m[1] != "0" {
		t.Fatalf("中文丢包率解析失败：%v", m)
	}
	if m := pingRTTWinRe.FindStringSubmatch(cn); m == nil || m[1] != "0" {
		t.Fatalf("中文时延解析失败：%v", m)
	}

	en := "4 packets transmitted, 4 received, 0% packet loss, time 3004ms\nrtt min/avg/max/mdev = 0.045/0.056/0.067/0.011 ms"
	if m := pingLossRe.FindStringSubmatch(en); m == nil || m[1] != "0" {
		t.Fatalf("英文丢包率解析失败：%v", m)
	}
	if m := pingRTTUnixRe.FindStringSubmatch(en); m == nil || m[1] != "0.056" {
		t.Fatalf("英文时延解析失败：%v", m)
	}
}
