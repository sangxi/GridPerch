// Package probe 实现服务端主动探测：网络设备(SNMP/PING)、数据库、中间件、FTP/SFTP、IPMI、K8S 等。
//
// 所有探测统一返回 Result，由 poller 写入监控状态表，与 agent 上报的结果共用一套告警流程。
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Status 取值沿用既有约定：1 正常，2 异常。
const (
	StatusOK   = "1"
	StatusFail = "2"
)

// Result 一次探测的结果。
type Result struct {
	Status string
	Value  string
	Msg    string
}

// Func 是具体探测类型的实现。
type Func func(ctx context.Context, t Target, timeout time.Duration) Result

// registry 记录类型到实现的映射。
var registry = map[string]Func{}

// Register 注册探测实现，供各子包初始化时调用。
func Register(typ string, fn Func) {
	registry[typ] = fn
}

// Types 返回全部已注册的探测类型。
func Types() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}

// Run 按类型执行一次探测，未知类型返回明确错误。
func Run(typ string, target map[string]string, timeout time.Duration) Result {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	fn, ok := registry[typ]
	if !ok {
		return Result{Status: StatusFail, Msg: "不支持的探测类型：" + typ}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res := fn(ctx, Target(target), timeout)
	if res.Status == "" {
		res.Status = StatusOK
	}
	return res
}

// DecodeTarget 将监控项的 target JSON 解析为字符串参数表。
// 端口、超时等参数常以数字形式配置，直接按 map[string]string 解析会因类型不匹配丢字段，
// 因此这里先解析为 any 再逐个转换。
func DecodeTarget(raw string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	holder := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &holder); err != nil {
		return out
	}
	for k, v := range holder {
		out[k] = stringify(v)
	}
	return out
}

// stringify 将 JSON 值转为字符串，整数不带小数尾巴。
func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatFloat(t, 'f', 0, 64)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

// Target 是探测参数的键值封装。
type Target map[string]string

// Str 读取字符串参数，为空时返回默认值。
func (t Target) Str(key, def string) string {
	if v, ok := t[key]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

// Int 读取整数参数。
func (t Target) Int(key string, def int) int {
	v := t.Str(key, "")
	if v == "" {
		return def
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// Addr 拼接 host:port 形式的地址。
func (t Target) Addr(defaultPort string) string {
	host := t.Str("host", "")
	if host == "" {
		host = t.Str("ip", "")
	}
	if host == "" {
		host = t.Str("target", "")
	}
	port := t.Str("port", defaultPort)
	if port == "" {
		return host
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return host + ":" + port
	}
	return host + ":" + port
}

// ok 构造成功的探测结果。
func ok(value, msg string) Result {
	return Result{Status: StatusOK, Value: value, Msg: msg}
}

// fail 构造失败的探测结果。
func fail(value, msg string) Result {
	return Result{Status: StatusFail, Value: value, Msg: msg}
}

// 各探测类型共用的默认端口。
const (
	defaultSSHPort  = "22"
	defaultFTPPort  = "21"
	defaultSNMPPort = "161"
	defaultRedis    = "6379"
	defaultMySQL    = "3306"
	defaultKafka    = "9092"
	defaultRabbit   = "15672"
	defaultES       = "9200"
	defaultIPMI     = "623"
)
