// Package license 实现离线授权码的校验（以及档位、配额的计算）。
//
// 授权码是一段自包含的文本：<前缀><载荷>.<签名>，载荷为授权信息 JSON 的
// base64url 编码。校验不需要联网，也不需要额外的证书文件。
//
// 现行格式 OPSM2- 用 Ed25519：授权中心持私钥签发，本程序只内置公钥验签，
// 因此不具备伪造授权码的能力。
// 历史格式 WGC- / OPSM1- / OPSM- 用 HMAC-SHA256（对称密钥），只读兼容存量授权码。
//
// 签名的具体实现见 sign_ed25519.go。
package license

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sangxi/OpsMonitor/internal/version"
)

// Edition 版本档位。
type Edition struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	MaxHosts int    `json:"maxHosts"` // 0 表示不限
	MaxUsers int    `json:"maxUsers"` // 0 表示不限
	Features string `json:"features"`
}

// Editions 内置版本档位，控制台展示与授权档位换算用。
var Editions = []Edition{
	{Key: "standard", Name: "标准版", MaxHosts: 50, MaxUsers: 10, Features: "主机监控、告警、监控项、指令下发、资产台账、巡检报告"},
	{Key: "pro", Name: "专业版", MaxHosts: 200, MaxUsers: 30, Features: "标准版全部功能 + 大屏、数据开放接口、邮件推送、操作审计"},
	{Key: "enterprise", Name: "企业版", MaxHosts: 5000, MaxUsers: 200, Features: "专业版全部功能 + 不限主机、SNMP/数据库/中间件/K8S 全量探针"},
	{Key: "ultimate", Name: "旗舰版", MaxHosts: 0, MaxUsers: 0, Features: "全部功能，主机数与账号数不受限制"},
}

// FindEdition 按 key 查找档位，未命中返回标准版。
func FindEdition(key string) Edition {
	for _, e := range Editions {
		if e.Key == key {
			return e
		}
	}
	return Editions[0]
}

// FreeQuota 未授权（社区版）的配额上限，由服务端启动参数决定。
// 这里只是兜底默认值，实际以 -free-hosts / -free-users 为准。
type FreeQuota struct {
	MaxHosts int
	MaxUsers int
}

// DefaultFreeQuota 默认社区版配额：10 台主机、3 个控制台账号。
var DefaultFreeQuota = FreeQuota{MaxHosts: 10, MaxUsers: 3}

// FeatureScreens 等特性开关名，授权信息里以逗号分隔存放。
const (
	FeatureScreens = "screens" // 大屏
	FeatureOpenAPI = "openapi" // 数据开放接口
	FeatureMail    = "mail"    // 邮件推送
	FeatureAudit   = "audit"   // 操作审计
	FeatureProbe   = "probe"   // 服务端主动探测（SNMP/数据库/中间件/K8S 等）
)

// featureCN 特性中文名。
var featureCN = map[string]string{
	FeatureScreens: "大屏展示", FeatureOpenAPI: "数据开放接口", FeatureMail: "邮件推送",
	FeatureAudit: "操作审计", FeatureProbe: "服务端主动探测",
}

// License 授权信息。
type License struct {
	Key       string   `json:"key"`                 // 授权编号，便于对账
	Customer  string   `json:"customer"`            // 授权对象
	Edition   string   `json:"edition"`             // 版本档位 key
	MaxHosts  int      `json:"maxHosts"`            // 允许接入主机数，0 不限
	MaxUsers  int      `json:"maxUsers"`            // 允许控制台账号数，0 不限
	Features  []string `json:"features,omitempty"`  // 功能开关
	Machine   string   `json:"machine,omitempty"`   // 绑定机器码，空表示不绑定
	IssuedAt  string   `json:"issuedAt"`            // 签发时间
	NotBefore string   `json:"notBefore,omitempty"` // 生效时间
	NotAfter  string   `json:"notAfter,omitempty"`  // 到期时间，空表示永久
	Remark    string   `json:"remark,omitempty"`    // 备注
}

// Status 是授权校验后的结果，服务端各处统一按它判断配额与功能开关。
type Status struct {
	Valid      bool     `json:"valid"`     // 是否为已授权且未过期
	Community  bool     `json:"community"` // 是否为未授权的社区版
	Edition    string   `json:"edition"`   // 版本中文名
	EditionKey string   `json:"editionKey"`
	Customer   string   `json:"customer"`
	Key        string   `json:"key"`
	Signed     string   `json:"signed"`   // 授权码签名方式：ed25519 / hmac
	MaxHosts   int      `json:"maxHosts"` // 0 不限
	MaxUsers   int      `json:"maxUsers"` // 0 不限
	Features   []string `json:"features"`
	FeatureCN  []string `json:"featureCN"`
	Machine    string   `json:"machine"` // 本机机器码
	Bound      string   `json:"bound"`   // 授权码绑定的机器码
	IssuedAt   string   `json:"issuedAt"`
	NotAfter   string   `json:"notAfter"`
	DaysLeft   int      `json:"daysLeft"` // 剩余天数，-1 表示永久
	Reason     string   `json:"reason"`   // 未授权 / 失效原因
}

// MachineCode 返回本机机器码：主机名 + 首块网卡 MAC + 平台的哈希。
// 授权码不与机器绑定时该值仅作展示。
func MachineCode() string {
	host, _ := os.Hostname()
	mac := ""
	if ifaces, err := net.Interfaces(); err == nil {
		for _, in := range ifaces {
			if in.Flags&net.FlagLoopback != 0 || len(in.HardwareAddr) == 0 {
				continue
			}
			mac = in.HardwareAddr.String()
			break
		}
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{host, mac, "opsmonitor-server"}, "|")))
	hexs := fmt.Sprintf("%x", sum[:8])
	parts := make([]string, 0, 4)
	for i := 0; i < len(hexs) && len(parts) < 4; i += 4 {
		parts = append(parts, hexs[i:i+4])
	}
	return strings.ToUpper(strings.Join(parts, "-"))
}

// Resolve 校验授权码并计算配额；未提供授权码时返回社区版状态。
// v 提供校验材料（Ed25519 公钥 + 可选的旧版 HMAC 密钥）。
func Resolve(raw string, v Verifier, machine string, free FreeQuota) Status {
	if free.MaxHosts <= 0 {
		free.MaxHosts = DefaultFreeQuota.MaxHosts
	}
	if free.MaxUsers <= 0 {
		free.MaxUsers = DefaultFreeQuota.MaxUsers
	}
	community := func(reason string) Status {
		return Status{
			Community: true, Edition: "社区版", EditionKey: "community",
			MaxHosts: free.MaxHosts, MaxUsers: free.MaxUsers,
			Machine: machine, DaysLeft: -1, Reason: reason,
		}
	}
	st := Status{Machine: machine, DaysLeft: -1}
	if strings.TrimSpace(raw) == "" {
		return community(fmt.Sprintf("未导入授权码，当前为社区版（最多接入 %d 台主机、%d 个控制台账号）",
			free.MaxHosts, free.MaxUsers))
	}
	l, err := v.Verify(raw)
	if err != nil {
		return community(err.Error())
	}
	st.Signed = v.Format(raw)
	st.Key, st.Customer = l.Key, l.Customer
	st.MaxHosts, st.MaxUsers = l.MaxHosts, l.MaxUsers
	st.Features = l.Features
	st.Bound, st.IssuedAt, st.NotAfter = l.Machine, l.IssuedAt, l.NotAfter
	for _, f := range l.Features {
		st.FeatureCN = append(st.FeatureCN, featureName(f))
	}
	ed := FindEdition(l.Edition)
	st.Edition, st.EditionKey = ed.Name, ed.Key
	if l.Edition == "" {
		st.Edition, st.EditionKey = "标准版", "standard"
	}
	if l.MaxHosts == 0 && ed.MaxHosts != 0 {
		st.MaxHosts = ed.MaxHosts
	}
	if l.MaxUsers == 0 && ed.MaxUsers != 0 {
		st.MaxUsers = ed.MaxUsers
	}

	now := time.Now()
	if l.NotAfter != "" {
		end, perr := parseDate(l.NotAfter)
		if perr != nil {
			st.Reason = "授权码到期时间格式不正确"
		} else {
			st.DaysLeft = int(time.Until(end).Hours() / 24)
			if st.DaysLeft < 0 {
				st.Reason = "授权已于 " + l.NotAfter + " 到期"
			}
		}
	}
	if l.NotBefore != "" {
		if begin, perr := parseDate(l.NotBefore); perr == nil && now.Before(begin) {
			st.Reason = "授权尚未生效，生效时间 " + l.NotBefore
		}
	}
	if l.Machine != "" && !strings.EqualFold(strings.TrimSpace(l.Machine), strings.TrimSpace(machine)) {
		st.Reason = "授权码绑定了其它机器（授权机器码 " + l.Machine + "，本机 " + machine + "）"
	}
	if st.Reason == "" {
		st.Valid = true
	}
	return st
}

// Allows 判断授权是否包含某功能开关；已授权但未声明任何特性时按档位放行。
func (s Status) Allows(feature string) bool {
	if !s.Valid {
		return false
	}
	if len(s.Features) == 0 {
		// 未逐项声明特性时按档位判断：旗舰版不限，其余档位放行全部内置功能
		return true
	}
	for _, f := range s.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// Summary 返回一句人话描述，供控制台与邮件展示。
func (s Status) Summary() string {
	var b strings.Builder
	b.WriteString(s.Edition)
	if s.Customer != "" {
		b.WriteString("（" + s.Customer + "）")
	}
	if s.MaxHosts > 0 {
		b.WriteString("，主机上限 " + strconv.Itoa(s.MaxHosts) + " 台")
	} else {
		b.WriteString("，主机数不限")
	}
	if s.MaxUsers > 0 {
		b.WriteString("，账号上限 " + strconv.Itoa(s.MaxUsers) + " 个")
	} else {
		b.WriteString("，账号数不限")
	}
	switch {
	case s.NotAfter == "":
		b.WriteString("，永久有效")
	case s.Valid:
		b.WriteString("，有效期至 " + s.NotAfter)
	default:
		b.WriteString("，已失效")
	}
	return b.String()
}

// featureName 特性中文名，未知特性原样返回。
func featureName(f string) string {
	if v, ok := featureCN[f]; ok {
		return v
	}
	return f
}

// Features 返回可签发的特性开关清单，供前端渲染勾选项。
func Features() []map[string]string {
	keys := make([]string, 0, len(featureCN))
	for k := range featureCN {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]string{"key": k, "name": featureCN[k]})
	}
	return out
}

// parseDate 解析授权里的日期，允许 YYYY-MM-DD 与 YYYY-MM-DD HH:MM:SS。
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("日期格式不正确")
}

// BuildInfo 返回版本展示信息。
func BuildInfo() map[string]string {
	return map[string]string{
		"version":   version.Version,
		"buildTime": version.BuildTime,
		"protocol":  strconv.Itoa(version.Protocol),
	}
}
