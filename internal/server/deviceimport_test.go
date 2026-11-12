package server

import "testing"

// TestMapHeaderRow 表头识别要能吃中英文两种写法；命中不足两列则视为无表头。
func TestMapHeaderRow(t *testing.T) {
	m := mapHeaderRow([]string{"地址", "账号", "密码", "备注"})
	if m[0] != "host" || m[1] != "username" || m[2] != "secret" || m[3] != "remark" {
		t.Fatalf("中文表头映射错误: %#v", m)
	}
	m = mapHeaderRow([]string{"IP", "hostname", "user", "password", "port"})
	if m[0] != "host" || m[1] != "bindHost" || m[2] != "username" || m[3] != "secret" || m[4] != "port" {
		t.Fatalf("英文表头映射错误: %#v", m)
	}
	// 带空格/下划线/括号的写法也要认
	m = mapHeaderRow([]string{"关联主机 ", "登录_账号", "登录密码（必填）"})
	if m[0] != "bindHost" || m[1] != "username" || m[2] != "secret" {
		t.Fatalf("变形表头映射错误: %#v", m)
	}
	// 只有一列像字段名时不当表头，交给位置兜底
	if m := mapHeaderRow([]string{"地址", "abc", "def"}); m != nil {
		t.Fatalf("命中不足两列不应判为表头: %#v", m)
	}
}

// TestNormalizeProtocol 协议写法归一与默认端口。
func TestNormalizeProtocol(t *testing.T) {
	cases := []struct {
		raw      string
		port     int
		wantP    string
		wantPort int
	}{
		{"", 0, "ssh", 22},
		{"SSH", 0, "ssh", 22},
		{"远程桌面", 0, "rdp", 3389},
		{"rdp", 0, "rdp", 3389},
		{"Windows", 0, "rdp", 3389},
		{"Sftp", 0, "sftp", 22},
		{"ssh", 2222, "ssh", 2222},
		{"rdp", 13389, "rdp", 13389},
		{"奇怪协议", 0, "ssh", 22},
	}
	for _, c := range cases {
		p, port := normalizeProtocol(c.raw, c.port)
		if p != c.wantP || port != c.wantPort {
			t.Errorf("normalizeProtocol(%q,%d)=(%q,%d)，期望 (%q,%d)",
				c.raw, c.port, p, port, c.wantP, c.wantPort)
		}
	}
}

// TestSplitHostPort 地址列里带端口要能拆开，否则会拼出 IP:9801:22 这种非法地址。
func TestSplitHostPort(t *testing.T) {
	cases := []struct {
		in, host, port string
		ok             bool
	}{
		{"192.168.40.69:9801", "192.168.40.69", "9801", true},
		{"192.168.40.69", "", "", false},
		{"host:abc", "", "", false},
		{"host:70000", "", "", false},
		{"::1", "", "", false},
		{":22", "", "", false},
		{"host:", "", "", false},
	}
	for _, c := range cases {
		h, p, ok := splitHostPort(c.in)
		if ok != c.ok || h != c.host || p != c.port {
			t.Errorf("splitHostPort(%q)=(%q,%q,%v)，期望 (%q,%q,%v)", c.in, h, p, ok, c.host, c.port, c.ok)
		}
	}
}

// TestRDPFile 生成的 .rdp 必须带地址与端口，且是 CRLF（mstsc 对换行敏感）。
func TestRDPFile(t *testing.T) {
	body := rdpFile("192.168.40.88", 13389, "administrator")
	if !containsSub(body, "full address:s:192.168.40.88:13389") {
		t.Fatalf("缺少地址行: %s", body)
	}
	if !containsSub(body, "username:s:administrator") {
		t.Fatalf("缺少用户名行: %s", body)
	}
	if !containsSub(body, "\r\n") {
		t.Fatalf("应为 CRLF 换行")
	}
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
