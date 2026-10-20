package server

import (
	"fmt"
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
		{"空值兜底", "", "strix-host"},
		{"全空白兜底", "   ", "strix-host"},
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

// TestInstallPS1Template installPS1 是用 fmt.Fprintf 填三个占位符的模板。
// 模板里一旦出现裸 %，Fprintf 会输出 %!x(MISSING) 之类的东西 ——
// 而这段文本是直接给 Windows 主机执行的，坏掉之后报错极难定位。
func TestInstallPS1Template(t *testing.T) {
	out := fmt.Sprintf(installPS1, "http://10.0.0.1:9999", "abcdef", "web-01")
	if strings.Contains(out, "%!") {
		t.Fatalf("安装脚本模板存在非法的格式化占位符：\n%s", out)
	}
	for _, want := range []string{
		"http://10.0.0.1:9999",
		"abcdef",
		"web-01",
		"$Legacy",     // 旧系统分支必须留在脚本里
		"&legacy=1",   // 下载与查版本都要带上
		"strix-agent", // 计划任务名
	} {
		if !strings.Contains(out, want) {
			t.Errorf("安装脚本缺少 %q", want)
		}
	}
}
