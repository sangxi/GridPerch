package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/sangxi/strix/internal/model"
)

// CollectSysEvents 采集系统事件（当前仅 Windows 有内容，其余平台返回 nil）。
//
// Windows 上读取「事件查看器」的 System 与 Application 两个日志，取
// 警告/错误/严重级别的记录——这正是事件查看器与服务器管理器里能看到的那批告警。
// 采集失败不影响指标上报：任何一步出错都只是少一条系统事件。
func CollectSysEvents(limit int) []model.SysEvent {
	if limit <= 0 {
		limit = 20
	}
	out := make([]model.SysEvent, 0, limit)
	for _, logName := range []string{"System", "Application"} {
		if len(out) >= limit {
			break
		}
		events, err := readWindowsEventLog(logName, limit-len(out))
		if err != nil {
			continue
		}
		out = append(out, events...)
	}
	return out
}

// readWindowsEventLog 用 PowerShell 读取指定日志的警告/错误/严重事件。
//
// 走 PowerShell 而非引入 Windows API，是为了保持跨平台编译（CGO 关闭），
// 同时用 -FilterHashtable 把范围限制在最近 6 小时、Level 1/2/3，避免全量扫描。
func readWindowsEventLog(logName string, limit int) ([]model.SysEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	script := `$ErrorActionPreference='SilentlyContinue';` +
		`$ev = Get-WinEvent -FilterHashtable @{LogName='` + logName + `'; Level=1,2,3; StartTime=(Get-Date).AddHours(-6)} -MaxEvents ` +
		strconv.Itoa(limit) + ` -ErrorAction SilentlyContinue;` +
		`$out = @();` +
		`foreach ($e in $ev) {` +
		`  $msg = $e.Message; if (-not $msg) { $msg = $e.ToString() };` +
		`  $msg = ($msg -replace '\s+', ' ').Trim();` +
		`  if ($msg.Length -gt 400) { $msg = $msg.Substring(0,400) };` +
		`  $lv = 'warning';` +
		`  if ($e.Level -eq 1) { $lv = 'critical' } elseif ($e.Level -eq 2) { $lv = 'error' } else { $lv = 'warning' };` +
		`  $src = $e.ProviderName; if (-not $src) { $src = $e.Source }; if (-not $src) { $src = 'EventLog' };` +
		`  $out += [pscustomobject]@{ time=$e.TimeCreated.ToString('yyyy-MM-dd HH:mm:ss'); source=$src; level=$lv; id=$e.Id; category=$e.LevelDisplayName; content=$msg };` +
		`};` +
		`$out | ConvertTo-Json -Compress -Depth 3`

	code, out, err := runPowerShell(script, 25*time.Second)
	if err != nil || code != 0 {
		return nil, err
	}
	events, err := parsePSJSON(out)
	if err != nil {
		return nil, err
	}
	return events, nil
}

// runPowerShell 执行一段 PowerShell 脚本，返回退出码与标准输出。
//
// 用 -EncodedCommand（UTF-16LE + Base64）而不是 -Command：
// 采集端在 Windows 上是通过 cmd /C 执行外部命令的，$ 开头的变量会被 cmd 吃掉，
// 脚本里又有引号与分号，逐层转义极易出错；编码后传参不经过任何 shell 解析。
// 同时加 -OutputFormat Text，否则 PowerShell 会向 stdout 混入 CLIXML 进度流。
func runPowerShell(script string, timeout time.Duration) (int, string, error) {
	b := utf16.Encode([]rune(script))
	buf := make([]byte, len(b)*2)
	for i, c := range b {
		buf[i*2] = byte(c)
		buf[i*2+1] = byte(c >> 8)
	}
	enc := base64.StdEncoding.EncodeToString(buf)
	return runCommand("powershell -NoProfile -NonInteractive -OutputFormat Text -EncodedCommand "+enc, timeout)
}

// parsePSJSON 从 PowerShell 输出里取出 JSON 数组。
// 输出前后可能夹带 CLIXML 头尾与空行，这里按第一个 '[' 到最后一个 ']' 截取。
func parsePSJSON(out string) ([]model.SysEvent, error) {
	s := strings.TrimSpace(out)
	if s == "" {
		return nil, nil
	}
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start < 0 || end <= start {
		// 单条记录时 ConvertTo-Json 返回对象而非数组
		o := strings.Index(s, "{")
		if o < 0 {
			return nil, nil
		}
		start, end = o, strings.LastIndex(s, "}")
		if end <= start {
			return nil, nil
		}
		s = "[" + s[start:end+1] + "]"
	} else {
		s = s[start : end+1]
	}
	var raw []model.SysEvent
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// sysEventHost 取本机名，只用于事件去重键。
func sysEventHost() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "local"
	}
	return h
}
