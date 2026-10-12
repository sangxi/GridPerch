package agent

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"github.com/sangxi/OpsMonitor/internal/model"
)

// baselinePath 保存文件指纹的本地基线文件。
const baselineFile = "opsmonitor-file-baseline.json"

// runCommand 跨平台执行一条 shell 命令并返回退出码与输出。
func runCommand(cmd string, timeout time.Duration) (int, string, error) {
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.Command("cmd", "/C", cmd)
	} else {
		c = exec.Command("/bin/sh", "-c", cmd)
	}
	var buf strings.Builder
	c.Stdout = &buf
	c.Stderr = &buf
	done := make(chan error, 1)
	if err := c.Start(); err != nil {
		return -1, buf.String(), err
	}
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				return code, buf.String(), err
			}
		}
		out := decodeConsole(buf.String())
		if len(out) > 8000 {
			return code, out[:8000] + "\n...(已截断)", nil
		}
		return code, out, nil
	case <-time.After(timeout):
		_ = c.Process.Kill()
		return -1, decodeConsole(buf.String()), fmt.Errorf("命令执行超时（%s）", timeout)
	}
}

// decodeConsole 将中文 Windows 控制台常见的 GBK 输出转换为 UTF-8，
// 避免自定义脚本与命令回显在控制台出现乱码。
func decodeConsole(s string) string {
	if s == "" || utf8.ValidString(s) {
		return s
	}
	out, _, err := transform.String(simplifiedchinese.GBK.NewDecoder(), s)
	if err != nil {
		return s
	}
	return out
}

// checkScript 执行自定义脚本或命令，退出码 0 视为正常。
func (c *Checker) checkScript(t model.TaskAssignment, res model.MonitorResult, timeout time.Duration) model.MonitorResult {
	cfg := targetOf(t.Target)
	cmd := strings.TrimSpace(cfg["cmd"])
	if cmd == "" {
		res.Status = statusFail
		res.Msg = "缺少参数 cmd"
		return res
	}
	if timeout < 5*time.Second {
		timeout = 30 * time.Second
	}
	start := time.Now()
	code, out, err := runCommand(cmd, timeout)
	if err != nil {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("执行 %q 失败: %v", cmd, err)
		return res
	}
	res.Value = fmt.Sprintf("%d", time.Since(start).Milliseconds())
	if code != 0 {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("命令退出码 %d，输出：%s", code, truncateAgent(out, 200))
		return res
	}
	res.Msg = fmt.Sprintf("脚本执行成功（%dms），输出：%s", time.Since(start).Milliseconds(), truncateAgent(out, 200))
	return res
}

/* ---------------- 文件 / 目录防篡改 ---------------- */

type fileBaseline map[string]string // 绝对路径 -> SHA256

var (
	baselineMu sync.Mutex
	baseline   fileBaseline
)

// loadBaseline 载入本地基线；首次运行自动建立基线。
func loadBaseline() fileBaseline {
	baselineMu.Lock()
	defer baselineMu.Unlock()
	if baseline != nil {
		return baseline
	}
	data, err := os.ReadFile(baselineFile)
	cur := fileBaseline{}
	if err == nil {
		_ = json.Unmarshal(data, &cur)
	}
	baseline = cur
	return baseline
}

// saveBaseline 持久化基线到磁盘。
func saveBaselineSnap(b fileBaseline) {
	baselineMu.Lock()
	baseline = b
	baselineMu.Unlock()
	data, _ := json.MarshalIndent(b, "", "  ")
	_ = os.WriteFile(baselineFile, data, 0o600)
}

// checkFile 校验文件或目录内容指纹，发现变化即为异常。
func (c *Checker) checkFile(t model.TaskAssignment, res model.MonitorResult) model.MonitorResult {
	cfg := targetOf(t.Target)
	path := strings.TrimSpace(cfg["path"])
	if path == "" {
		res.Status = statusFail
		res.Msg = "缺少参数 path"
		return res
	}
	maxFiles := 200
	if n, err := strconv.Atoi(cfg["maxFiles"]); err == nil && n > 0 {
		maxFiles = n
	}
	info, err := os.Stat(path)
	if err != nil {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("读取 %s 失败: %v", path, err)
		return res
	}
	var files []string
	if info.IsDir() {
		err := filepath.Walk(path, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if fi.IsDir() {
				return nil
			}
			if len(files) >= maxFiles {
				return filepath.SkipDir
			}
			files = append(files, p)
			return nil
		})
		if err != nil {
			res.Status = statusFail
			res.Msg = fmt.Sprintf("遍历目录 %s 失败: %v", path, err)
			return res
		}
	} else {
		files = append(files, path)
	}
	current := fileBaseline{}
	for _, f := range files {
		h, err := fileHash(f)
		if err != nil {
			continue
		}
		current[f] = h
	}
	base := loadBaseline()
	var changed, added, removed []string
	for f, h := range current {
		old, ok := base[f]
		if !ok {
			// 首次加入监控范围的文件直接纳入基线
			continue
		}
		if old != h {
			changed = append(changed, f)
		}
	}
	for f := range base {
		if strings.HasPrefix(f, path) {
			if _, ok := current[f]; !ok {
				removed = append(removed, f)
			}
		}
	}
	// 合并更新基线。默认情况下检测到变化后不更新基线，使异常持续暴露，
	// 直到文件被还原或运维人员重新基线；配置 autoRebaseline=true 时才自动接受新内容。
	autoRebase := cfg["autoRebaseline"] == "true" || cfg["autoRebaseline"] == "1"
	changedSet := map[string]bool{}
	for _, f := range changed {
		changedSet[f] = true
	}
	merged := fileBaseline{}
	for k, v := range loadBaseline() {
		merged[k] = v
	}
	firstRun := false
	for k, v := range current {
		if _, ok := merged[k]; !ok {
			// 首次纳入监控范围的文件直接建立基线
			added = append(added, k)
			firstRun = true
			merged[k] = v
			continue
		}
		if changedSet[k] && !autoRebase {
			continue
		}
		merged[k] = v
	}
	if autoRebase {
		for _, f := range removed {
			delete(merged, f)
		}
	}
	saveBaselineSnap(merged)

	res.Value = fmt.Sprintf("%d", len(current))
	switch {
	case len(changed) > 0:
		res.Status = statusFail
		res.Msg = fmt.Sprintf("检测到 %d 个文件被修改：%s", len(changed), truncateAgent(strings.Join(changed, "、"), 200))
		if autoRebase {
			res.Msg += "（已自动更新基线）"
		} else {
			res.Msg += "（基线保持不变，将持续告警至文件还原或重新基线）"
		}
	case len(removed) > 0:
		res.Status = statusFail
		res.Msg = fmt.Sprintf("检测到 %d 个文件被删除：%s", len(removed), truncateAgent(strings.Join(removed, "、"), 200))
	case firstRun && len(added) > 0:
		res.Msg = fmt.Sprintf("已建立 %d 个文件的指纹基线", len(added))
	default:
		res.Msg = fmt.Sprintf("%d 个文件内容未发生变化", len(current))
	}
	return res
}

// fileHash 计算文件 SHA256。
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

/* ---------------- Nginx 访问日志分析 ---------------- */

// nginxStat 一次统计周期的聚合结果。
type nginxStat struct {
	Total   int            `json:"total"`
	Errors  int            `json:"errors"`
	Status  map[string]int `json:"status"`
	TopIPs  map[string]int `json:"topIps"`
	TopPath map[string]int `json:"topPath"`
}

// checkNginxLog 增量解析 Nginx access log，统计请求量、错误率与热点来源。
func (c *Checker) checkNginxLog(t model.TaskAssignment, res model.MonitorResult) model.MonitorResult {
	cfg := targetOf(t.Target)
	path := strings.TrimSpace(cfg["path"])
	if path == "" {
		res.Status = statusFail
		res.Msg = "缺少参数 path"
		return res
	}
	errorRate := 10.0
	if v, err := strconv.ParseFloat(cfg["errorRate"], 64); err == nil && v >= 0 {
		errorRate = v
	}
	stat, err := c.tailNginx(path)
	if err != nil {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("解析日志 %s 失败: %v", path, err)
		return res
	}
	detail, _ := json.Marshal(map[string]any{
		"status": stat.Status, "topIps": topN(stat.TopIPs, 5), "topPath": topN(stat.TopPath, 5),
	})
	res.Value = fmt.Sprintf("%d", stat.Total)
	rate := 0.0
	if stat.Total > 0 {
		rate = float64(stat.Errors) * 100 / float64(stat.Total)
	}
	res.Msg = fmt.Sprintf("新增请求 %d 次，5xx %d 次，错误率 %.1f%%；分布：%s",
		stat.Total, stat.Errors, rate, string(detail))
	if rate > errorRate {
		res.Status = statusFail
		res.Msg = fmt.Sprintf("错误率 %.1f%% 超过阈值 %.1f%%，%s", rate, errorRate, res.Msg)
	}
	return res
}

// tailNginx 从上次偏移量继续读取日志并解析。
func (c *Checker) tailNginx(path string) (*nginxStat, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	offset := c.logOffsets[path]
	c.mu.Unlock()
	// 日志被切割重建时从头读取
	if info.Size() < offset {
		offset = 0
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	stat := &nginxStat{Status: map[string]int{}, TopIPs: map[string]int{}, TopPath: map[string]int{}}
	reader := bufio.NewReaderSize(f, 1<<16)
	read := int64(0)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			read += int64(len(line))
			stat.Total++
			fields := strings.Fields(line)
			if len(fields) >= 7 {
				stat.TopIPs[fields[0]]++
				// 请求行被双引号包裹且可能含有空格（"GET /path HTTP/1.1"），
				// 先定位整个请求行，其后紧跟的字段才是状态码与响应大小，
				// 这样 combined 格式（尾部带 referer 与 user-agent）也能正确解析。
				start, end := -1, -1
				for i, f2 := range fields {
					if start < 0 && strings.HasPrefix(f2, "\"") {
						start = i
					}
					if start >= 0 && i >= start && strings.HasSuffix(f2, "\"") {
						end = i
						break
					}
				}
				if start >= 0 && end >= start && end+1 < len(fields) {
					code := fields[end+1]
					if n, convErr := strconv.Atoi(code); convErr == nil && n >= 100 && n <= 599 {
						stat.Status[code]++
						if strings.HasPrefix(code, "5") {
							stat.Errors++
						}
					}
					req := strings.Trim(strings.Join(fields[start:end+1], " "), "\"")
					if parts := strings.Fields(req); len(parts) > 1 {
						stat.TopPath[parts[1]]++
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	c.mu.Lock()
	c.logOffsets[path] = offset + read
	c.mu.Unlock()
	return stat, nil
}

// topN 取出现次数最多的若干项。
func topN(m map[string]int, n int) []map[string]any {
	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range m {
		list = append(list, kv{k, v})
	}
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].v > list[j-1].v; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	if n > len(list) {
		n = len(list)
	}
	out := make([]map[string]any, 0, n)
	for _, item := range list[:n] {
		out = append(out, map[string]any{"key": item.k, "count": item.v})
	}
	return out
}

func truncateAgent(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// HostLogins 采集当前登录本机的用户列表，用于主机登录提醒。
func HostLogins() string {
	// 注意：query user 在没有活动会话时会返回退出码 1，但输出仍然有效，
	// 因此这里只看命令是否成功启动，不以退出码判定失败。
	_, out, err := runCommand(loginCommand(), 5*time.Second)
	if err != nil {
		return ""
	}
	var users []string
	seen := map[string]bool{}
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	skipHeader := runtime.GOOS == "windows" // query user 首行为表头
	for i, line := range lines {
		if i == 0 && skipHeader {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// 去掉 query user 标记当前会话的 '>' 前缀
		name := strings.TrimPrefix(strings.TrimSpace(fields[0]), ">")
		// 跳过表头行，避免把「用户名/USERNAME」误当成登录账号
		if name == "" || isLoginHeader(fields) {
			continue
		}
		if !seen[name] {
			seen[name] = true
			users = append(users, name)
		}
	}
	if len(users) == 0 {
		// 兜底：至少给出运行 agent 的账号，保证登录提醒有可比对基线
		if u, uerr := user.Current(); uerr == nil && u.Username != "" {
			users = append(users, u.Username)
		}
	}
	return strings.Join(users, ",")
}

// isLoginHeader 判断是否为 who / query user 输出的表头行。
func isLoginHeader(fields []string) bool {
	head := strings.ToUpper(fields[0])
	switch head {
	case "USERNAME", "用户名", "USER", "用户", "NAME", "LOGIN":
		return true
	}
	for _, f := range fields {
		switch strings.ToUpper(f) {
		case "SESSIONNAME", "会话名", "STATE", "状态", "IDLE", "空闲时间", "LOGONTIME", "登录时间":
			return true
		}
	}
	return false
}

func loginCommand() string {
	if runtime.GOOS == "windows" {
		return "query user"
	}
	return "who"
}

// ReportLogins 保证 metrics 结构体可直接透传登录用户名。
func ensureLoginUsers(report *model.Report) {
	if report == nil || report.SysLoadState == nil {
		return
	}
	if report.SysLoadState.Users == "" {
		report.SysLoadState.Users = HostLogins()
	}
}
