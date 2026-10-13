package server

import (
	"strings"
	"testing"
)

// TestSanitizeAgentName 锁定安装脚本里主机名的清洗行为：
// 允许各语言字母数字（中文可正常使用），但必须拦住 shell / PowerShell 元字符。
func TestSanitizeAgentName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ASCII 原样保留", "web-01", "web-01"},
		{"中文保留（回归：曾被全部替换成 -）", "测试主机-中文", "测试主机-中文"},
		{"日文保留", "日本語サーバ", "日本語サーバ"},
		{"空白替换为 -", "我的服务器 web01", "我的服务器-web01"},
		{"空值兜底", "", "opsmonitor-host"},
		{"全空白兜底", "   ", "opsmonitor-host"},
		{"shell 元字符全部替换", `恶意;rm -rf /$x`, "恶意-rm--rf---x"},
		{"引号与反引号替换", "a\"`b`", "a--b-"},
		{"命令替换替换", "主机$(id)", "主机--id-"},
		{"换行与管道替换", "a\nb|c&d", "a-b-c-d"},
		{"按字符截断（不劈开汉字）", strings.Repeat("中", 70), strings.Repeat("中", 64)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeAgentName(c.in); got != c.want {
				t.Fatalf("sanitizeAgentName(%q) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}
}
