package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLegacyEnvFallback 品牌更名后环境变量前缀改为 STRIX_，
// 但已部署机器上写的仍是 OPSM_*，必须能回落，否则老部署换二进制就全变默认值。
func TestLegacyEnvFallback(t *testing.T) {
	t.Setenv("STRIX_TEST_KEY", "")
	t.Setenv("OPSM_TEST_KEY", "old")
	if got := env("STRIX_TEST_KEY", "def"); got != "old" {
		t.Fatalf("旧前缀变量应被回落使用，得到 %q", got)
	}
	t.Setenv("STRIX_TEST_KEY", "new")
	if got := env("STRIX_TEST_KEY", "def"); got != "new" {
		t.Fatalf("新前缀应优先于旧前缀，得到 %q", got)
	}
	if got := env("STRIX_MISSING_KEY", "def"); got != "def" {
		t.Fatalf("两者都不存在时应返回默认值，得到 %q", got)
	}
}

// TestResolveLegacyDBPath 库文件默认名从 opsmonitor.db 改为 strix.db 后，
// 老部署必须继续用到原来的库，否则会新建空库、看起来像数据全丢。
func TestResolveLegacyDBPath(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "strix.db")
	oldPath := filepath.Join(dir, "opsmonitor.db")

	if p, ok := ResolveLegacyDBPath(newPath); ok || p != newPath {
		t.Fatalf("两个文件都不存在时应保持新路径，得到 %q", p)
	}
	if err := os.WriteFile(oldPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, ok := ResolveLegacyDBPath(newPath); !ok || p != oldPath {
		t.Fatalf("只有旧库时应沿用它（%s），得到 %q", oldPath, p)
	}
	if err := os.WriteFile(newPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, ok := ResolveLegacyDBPath(newPath); ok || p != newPath {
		t.Fatalf("新旧库同时存在时应优先新库，得到 %q", p)
	}
	if _, ok := ResolveLegacyDBPath(filepath.Join(dir, "custom.db")); ok {
		t.Fatal("自定义文件名不应触发旧路径兼容")
	}
}

// TestTokenOK 通信密钥默认值从 opsmonitor 改成 strix，
// 过渡期内允许仍用旧默认值的采集端继续连上。
func TestTokenOK(t *testing.T) {
	c := &Server{Token: "strix", TokenLegacy: "opsmonitor"}
	for _, got := range []string{"strix", MD5Token("strix")} {
		if !c.TokenOK(got) {
			t.Fatalf("应接受当前密钥 %q", got)
		}
	}
	for _, got := range []string{"opsmonitor", MD5Token("opsmonitor")} {
		if !c.TokenOK(got) {
			t.Fatalf("应兼容更名前的默认密钥 %q", got)
		}
	}
	for _, got := range []string{"", "bad"} {
		if c.TokenOK(got) {
			t.Fatalf("应拒绝 %q", got)
		}
	}
	// 已改成强密钥的部署不该再多留一个可用口令
	strong := &Server{Token: "a-strong-token"}
	if strong.TokenOK("opsmonitor") {
		t.Fatal("使用强密钥时不应再接受旧默认值")
	}
}
