package server

import "testing"

// TestScreenTitleOf 大屏标题的取值优先级：
// 显式配置 > 「系统名称 + 运维监控中心」> 兜底常量。
//
// 默认安装（系统名称为空或 GridPerch）必须仍然是「GridPerch 运维监控中心」——
// 这条老行为被用户的大屏投屏地址直接引用，改了就是视觉回归。
func TestScreenTitleOf(t *testing.T) {
	cases := []struct {
		name string
		kv   map[string]string
		want string
	}{
		{"空配置", map[string]string{}, "GridPerch 运维监控中心"},
		{"nil 配置", nil, "GridPerch 运维监控中心"},
		{"只有系统名称", map[string]string{"systemName": "GridPerch"}, "GridPerch 运维监控中心"},
		{"系统名称带空格", map[string]string{"systemName": "  GridPerch  "}, "GridPerch 运维监控中心"},
		{"自定义系统名称", map[string]string{"systemName": "某某科技"}, "某某科技 运维监控中心"},
		{"显式标题优先", map[string]string{"systemName": "某某科技", "screenTitle": "机房 A 大屏"}, "机房 A 大屏"},
		{"显式标题去空格", map[string]string{"screenTitle": "  机房 A 大屏  "}, "机房 A 大屏"},
		{"显式标题为空则回落", map[string]string{"systemName": "某某科技", "screenTitle": "   "}, "某某科技 运维监控中心"},
	}
	for _, c := range cases {
		if got := screenTitleOf(c.kv); got != c.want {
			t.Errorf("%s: screenTitleOf(%v) = %q，期望 %q", c.name, c.kv, got, c.want)
		}
	}
}

// TestPublicSettingsHasScreenTitle 大屏页未登录时也能拿到标题，
// 否则会先渲染 HTML 里的默认名再跳变，投屏上很难看。
func TestPublicSettingsHasScreenTitle(t *testing.T) {
	found := false
	for _, k := range publicSettingKeys {
		if k == "screenTitle" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("publicSettingKeys 里缺少 screenTitle，大屏标题将只能靠登录后的接口获取")
	}
}
