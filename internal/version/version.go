// Package version 集中定义构建版本号与版本比较逻辑。
//
// 采集端与服务端共用同一个版本号：发布新版本时改这里，
// 两端重新构建后由 build.sh 一并打进二进制。
package version

import (
	"strconv"
	"strings"
)

// Version 当前构建版本号，形如 1.2.3。
//
// ⚠️ 发版约定：这个号必须与「上传到授权系统时登记的版本号」完全一致，否则：
//   - 登记号 ≤ 本号    → 控制台判定「已是最新」，永远提示「无需升级」，拉不下来；
//   - 登记号 >  本号    → 升级后本机仍自报旧号，下一轮同步又会重新下载一遍（每 5 分钟循环）。
//
// 二开发版时只改这里，然后让 release.sh 自动取用（它默认就读本文件）。
var Version = "1.1.6"

// BuildTime 构建时间，由 build.sh 通过 -ldflags -X 注入，未注入时为空。
var BuildTime = ""

// Protocol 采集端与服务端之间的数据协议版本。
// 两端协议不一致时，旧版采集端仍可上报，但控制台会提示升级。
const Protocol = 1

// Compare 比较两个版本号，a>b 返回 1，a<b 返回 -1，相等返回 0。
// 支持 1.2.3、v1.2.3、1.2.3-rc1 这类写法，段内非数字时按字典序比较。
func Compare(a, b string) int {
	as, bs := split(a), split(b)
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		xi, xerr := strconv.Atoi(x)
		yi, yerr := strconv.Atoi(y)
		if xerr == nil && yerr == nil {
			if xi != yi {
				if xi > yi {
					return 1
				}
				return -1
			}
			continue
		}
		// 一边是数字、一边是文字时，文字视为预发布版本（1.0.0-rc1 < 1.0.0）
		if xerr == nil && yerr != nil && y != "" {
			return 1
		}
		if xerr != nil && yerr == nil && x != "" {
			return -1
		}
		if x != y {
			return strings.Compare(x, y)
		}
	}
	return 0
}

// IsNewer 判断 remote 是否比 local 更新。
func IsNewer(remote, local string) bool {
	if strings.TrimSpace(remote) == "" {
		return false
	}
	return Compare(remote, local) > 0
}

// split 把版本号拆成可比较的段，去掉 v 前缀与构建元数据。
func split(v string) []string {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	if i := strings.IndexAny(v, "+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil
	}
	return strings.Split(v, ".")
}

// Display 返回带 v 前缀的展示用版本号。
func Display(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "未知"
	}
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}
