package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sangxi/gridperch/internal/model"
	"github.com/sangxi/gridperch/internal/store"
)

/* ---------------- 操作审计 ---------------- */

// auditRule 描述一个需要记录审计的动作。
type auditRule struct {
	Action   string // 人类可读动作名
	Category string // 归类
	Target   string // 请求体中用于标识操作对象的字段名，空表示从路径取
	Detail   string // 请求体中用于补充说明的字段名（多个以逗号分隔）
}

// actionCN 各归类的中文名，前端筛选下拉复用。
var auditCategoryCN = map[string]string{
	"auth": "登录认证", "monitor": "监控项", "alert": "告警处理", "command": "指令下发",
	"asset": "资产台账", "device": "设备账号", "inspection": "巡检报告", "member": "成员权限",
	"token": "开放接口", "setting": "系统设置", "mail": "邮件推送", "data": "数据管理",
	"release": "版本发布", "license": "授权管理", "upgrade": "采集端升级",
	"agentcfg": "采集端配置", "webhook": "机器人推送", "tools": "工具箱",
}

// auditRules 将「方法 + 路由模式」映射为审计动作。
var auditRules = map[string]auditRule{
	"POST /api/login":                       {Action: "登录控制台", Category: "auth", Target: "username"},
	"POST /api/logout":                      {Action: "退出登录", Category: "auth"},
	"POST /api/alerts/{id}/resolve":         {Action: "标记告警已恢复", Category: "alert", Target: ":path"},
	"POST /api/alerts/{id}/ack":             {Action: "认领告警", Category: "alert", Target: ":path"},
	"POST /api/alerts/{id}/unack":           {Action: "释放告警认领", Category: "alert", Target: ":path"},
	"POST /api/alerts/{id}/note":            {Action: "填写告警处理备注", Category: "alert", Target: ":path", Detail: "note"},
	"POST /api/alert-silences":              {Action: "新增告警静默规则", Category: "alert", Target: "hostname", Detail: "type,level,startTime,endTime,reason"},
	"DELETE /api/alert-silences/{id}":       {Action: "删除告警静默规则", Category: "alert", Target: ":path"},
	"POST /api/tasks":                       {Action: "新建监控项", Category: "monitor", Target: "name", Detail: "type,runner,hosts"},
	"PUT /api/tasks/{id}":                   {Action: "修改监控项", Category: "monitor", Target: "name", Detail: "type,runner,hosts,enabled"},
	"DELETE /api/tasks/{id}":                {Action: "删除监控项", Category: "monitor", Target: ":path"},
	"POST /api/commands":                    {Action: "下发指令", Category: "command", Target: "host", Detail: "cmd"},
	"POST /api/tools/probe":                 {Action: "执行网络探测", Category: "tools", Target: "type"},
	"DELETE /api/commands/{id}":             {Action: "删除指令记录", Category: "command", Target: ":path"},
	"POST /api/assets":                      {Action: "新增资产", Category: "asset", Target: "name", Detail: "category,ip"},
	"PUT /api/assets/{id}":                  {Action: "修改资产", Category: "asset", Target: "name", Detail: "category,ip"},
	"DELETE /api/assets/{id}":               {Action: "删除资产", Category: "asset", Target: ":path"},
	"POST /api/assets/batch":                {Action: "批量导入资产", Category: "asset"},
	"POST /api/device-accounts":             {Action: "新增设备账号", Category: "device", Target: "name", Detail: "protocol,host,port"},
	"PUT /api/device-accounts/{id}":         {Action: "修改设备账号", Category: "device", Target: "name", Detail: "protocol,host,port"},
	"DELETE /api/device-accounts/{id}":      {Action: "删除设备账号", Category: "device", Target: ":path"},
	"POST /api/device-accounts/{id}/test":   {Action: "设备连通性检测", Category: "device", Target: ":path"},
	"POST /api/inspections":                 {Action: "生成巡检报告", Category: "inspection", Target: "title", Detail: "scope"},
	"DELETE /api/inspections/{id}":          {Action: "删除巡检报告", Category: "inspection", Target: ":path"},
	"POST /api/members":                     {Action: "新建成员账号", Category: "member", Target: "username", Detail: "role,nickname"},
	"PUT /api/members/{username}":           {Action: "修改成员账号", Category: "member", Target: "username", Detail: "role,enabled,menus"},
	"DELETE /api/members/{username}":        {Action: "删除成员账号", Category: "member", Target: ":path"},
	"POST /api/tokens":                      {Action: "新建开放接口凭证", Category: "token", Target: "name", Detail: "perms"},
	"DELETE /api/tokens/{id}":               {Action: "删除开放接口凭证", Category: "token", Target: ":path"},
	"PUT /api/settings":                     {Action: "修改系统设置", Category: "setting"},
	"POST /api/test-mail":                   {Action: "发送测试邮件", Category: "mail"},
	"POST /api/releases":                    {Action: "发布采集端版本", Category: "release", Target: "version", Detail: "goos,goarch,notes"},
	"DELETE /api/releases/{id}":             {Action: "删除采集端版本", Category: "release", Target: ":path"},
	"POST /api/releases/{id}/latest":        {Action: "设为下发版本", Category: "release", Target: ":path"},
	"POST /api/license":                     {Action: "导入授权码", Category: "license"},
	"DELETE /api/license":                   {Action: "解除授权", Category: "license"},
	"DELETE /api/audit":                     {Action: "清空审计日志", Category: "data"},
	"DELETE /api/mail/logs":                 {Action: "清空邮件记录", Category: "data"},
	"PUT /api/agent-config":                 {Action: "修改采集端全局配置", Category: "agentcfg", Detail: "agentInterval,agentSkipDisk,agentUpdateMode,agentAllowCommand"},
	"PUT /api/hosts/{host}/agent-config":    {Action: "修改采集端主机配置", Category: "agentcfg", Target: ":host", Detail: "interval,skipDisk,updateMode,allowCommand"},
	"DELETE /api/hosts/{host}/agent-config": {Action: "恢复采集端默认配置", Category: "agentcfg", Target: ":host"},
	"PUT /api/hosts/{host}/rename":          {Action: "重命名主机", Category: "host", Target: ":host", Detail: "name"},
	"DELETE /api/hosts/{host}":              {Action: "删除主机", Category: "host", Target: ":host"},
	"POST /api/webhooks":                    {Action: "新增机器人", Category: "webhook", Target: "name", Detail: "kind,url,enabled,levels"},
	"PUT /api/webhooks/{id}":                {Action: "修改机器人", Category: "webhook", Target: "name", Detail: "kind,url,enabled,levels"},
	"DELETE /api/webhooks/{id}":             {Action: "删除机器人", Category: "webhook", Target: ":path"},
	"POST /api/webhooks/{id}/test":          {Action: "测试机器人", Category: "webhook", Target: ":path"},
}

// sensitiveKeys 这些字段的值不写入审计明细，避免口令泄漏到日志。
var sensitiveKeys = []string{"password", "secret", "passwd", "pwd", "token"}

// audit 记录写操作审计。仅对 /api/ 下会产生副作用的请求生效（GET/HEAD/OPTIONS 直接放行）。
func (s *Server) audit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.Method == http.MethodGet ||
			r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		// 只读出请求体的前 2MB 供审计解析，剩余部分原样透传给业务处理器。
		// 这里绝不能用 NopCloser 重建整个 body —— 那会把上传大文件的请求截断，
		// 导致 multipart 解析报 "unexpected EOF"。
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(io.LimitReader(r.Body, 2<<20))
			r.Body = passthroughBody{io.MultiReader(bytes.NewReader(body), r.Body), r.Body}
		}
		// 请求进入时先取当前账号，登录接口此时无会话，稍后从请求体兜底
		user := s.currentUser(r)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		e := model.AuditLog{
			Method: r.Method, Path: r.URL.Path,
			IP: clientIP(r), UserAgent: truncate(r.UserAgent(), 200),
			StatusCode: rec.status, DurationMs: time.Since(start).Milliseconds(),
			CreateTime: time.Now().Format("2006-01-02 15:04:05"),
		}
		e.Result = "ok"
		if rec.status >= 400 {
			e.Result = "fail"
		}
		// r.Pattern 自带方法前缀（形如 "POST /api/tasks"），与规则表的键一致
		rule, ok := auditRules[r.Pattern]
		if !ok {
			rule = auditRule{Action: r.Method + " " + r.URL.Path, Category: "data"}
		}
		e.Action, e.Category = rule.Action, rule.Category
		if user != nil {
			e.Username, e.Role = user.Username, user.Role
		}
		if rule.Target == ":path" {
			e.Target = lastPathSegment(r.URL.Path)
		} else if rule.Target == ":host" {
			// 形如 /api/hosts/{host}/xxx 的路径，倒数第二段是主机，末段是动作名，
			// 取末段会把「rename」当成操作对象，这里直接取路径里的主机名。
			e.Target = truncate(r.PathValue("host"), 120)
		}
		// 文件上传类接口是 multipart，业务处理器已解析过，这里复用其结果
		var form map[string][]string
		if r.MultipartForm != nil {
			form = r.MultipartForm.Value
		}
		parseAuditBody(body, form, rule, &e)
		if e.Username == "" && rule.Category == "auth" && e.Target != "" {
			// 登录接口此时还没有会话，操作账号取请求体里的用户名，
			// 这样即使密码错误也能留下「谁在什么时候尝试登录」的记录。
			e.Username, e.Role = e.Target, "-"
			if m, merr := s.store.GetMember(e.Target); merr == nil {
				e.Role = m.Role
			}
		}
		if e.Username == "" {
			e.Username, e.Role = "anonymous", "-"
		}
		if e.Result == "fail" {
			e.Action += "（失败）"
		}
		s.store.AppendAudit(e)
	})
}

// parseAuditBody 从请求体中提取操作对象与要点明细。
// body 是原始请求体（JSON），form 是 multipart 表单（文件上传类接口）。
func parseAuditBody(body []byte, form map[string][]string, rule auditRule, e *model.AuditLog) {
	var kv map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &kv); err != nil || kv == nil {
			// 资产批量导入等数组型请求体只记录条数
			var list []any
			if err := json.Unmarshal(body, &list); err == nil {
				e.Target = strconv.Itoa(len(list)) + " 条"
			}
		}
	}
	if kv == nil && len(form) > 0 {
		kv = make(map[string]any, len(form))
		for k, vs := range form {
			if len(vs) > 0 {
				kv[k] = vs[0]
			}
		}
	}
	if kv == nil {
		return
	}
	text := func(k string) string {
		v, ok := kv[k]
		if !ok || v == nil {
			return ""
		}
		switch t := v.(type) {
		case string:
			return t
		case bool:
			if t {
				return "是"
			}
			return "否"
		case float64:
			if t == float64(int64(t)) {
				return strconv.FormatInt(int64(t), 10)
			}
			return strconv.FormatFloat(t, 'f', -1, 64)
		default:
			b, _ := json.Marshal(t)
			return string(b)
		}
	}
	if rule.Target != "" && e.Target == "" {
		e.Target = truncate(text(rule.Target), 120)
	}
	if rule.Detail != "" {
		var parts []string
		for _, k := range strings.Split(rule.Detail, ",") {
			k = strings.TrimSpace(k)
			if k == "" || isSensitive(k) {
				continue
			}
			if v := text(k); v != "" {
				parts = append(parts, k+"="+truncate(v, 120))
			}
		}
		e.Detail = strings.Join(parts, " ")
	} else if rule.Category == "setting" {
		// 设置项逐个列出被修改的键，便于回溯改了什么
		var keys []string
		for k := range kv {
			if isSensitive(k) {
				continue
			}
			keys = append(keys, k)
		}
		e.Detail = "修改项：" + strings.Join(keys, ",")
	}
}

func isSensitive(key string) bool {
	lower := strings.ToLower(key)
	for _, s := range sensitiveKeys {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// passthroughBody 把「已读出的前缀 + 原始剩余流」拼回成一个请求体，
// 关闭动作交给原始 body 处理。
type passthroughBody struct {
	io.Reader
	io.Closer
}

// statusRecorder 捕获响应状态码，供审计记录成功/失败。
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.written {
		r.status = code
		r.written = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.written = true
	return r.ResponseWriter.Write(b)
}

// clientIP 取真实来源 IP，优先使用反向代理透传的头。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func lastPathSegment(p string) string {
	p = strings.Trim(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	return truncate(p, 120)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

/* ---------------- 审计与邮件记录接口 ---------------- */

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		filter := store.AuditFilter{
			Username: q.Get("username"),
			Action:   q.Get("action"),
			Category: q.Get("category"),
			Result:   q.Get("result"),
			Keyword:  q.Get("keyword"),
			From:     q.Get("from"),
			To:       q.Get("to"),
			Limit:    limit,
		}
		switch q.Get("scope") {
		case "auth":
			filter.OnlyAuth = true
		case "operate":
			filter.SkipAuth = true
		}
		list, err := s.store.ListAudit(filter)
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, list)
	case http.MethodDelete:
		n, err := s.store.ClearAudit()
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "count": n})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

func (s *Server) handleAuditStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.AuditStats()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	users, err := s.store.DistinctAuditUsers()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	stats["users"] = users
	stats["categories"] = auditCategoryCN
	writeJSON(w, stats)
}

// handleAuditExport 导出审计流水为 CSV / XLSX。
func (s *Server) handleAuditExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.AuditFilter{
		Username: q.Get("username"), Action: q.Get("action"), Category: q.Get("category"),
		Result: q.Get("result"), Keyword: q.Get("keyword"), From: q.Get("from"), To: q.Get("to"), Limit: 2000,
	}
	switch q.Get("scope") {
	case "auth":
		filter.OnlyAuth = true
	case "operate":
		filter.SkipAuth = true
	}
	list, err := s.store.ListAudit(filter)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	rows := [][]string{{"时间", "账号", "角色", "归类", "动作", "对象", "明细", "方法", "路径",
		"结果", "状态码", "耗时(ms)", "来源IP", "User-Agent"}}
	for _, e := range list {
		rows = append(rows, []string{e.CreateTime, e.Username, roleCN(e.Role), categoryCN(e.Category),
			e.Action, e.Target, e.Detail, e.Method, e.Path, resultCN(e.Result),
			strconv.Itoa(e.StatusCode), strconv.FormatInt(e.DurationMs, 10), e.IP, e.UserAgent})
	}
	writeTableFile(w, "操作审计", "audit", rows, q.Get("format"))
}

func (s *Server) handleMailLogs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list, err := s.store.ListMailLogs(limit)
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, list)
	case http.MethodDelete:
		if err := s.store.ClearMailLogs(); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

func roleCN(role string) string {
	switch role {
	case RoleAdmin:
		return "管理员"
	case RoleOperator:
		return "运维"
	case RoleViewer:
		return "只读"
	case "-":
		return "-"
	default:
		return role
	}
}

func categoryCN(c string) string {
	if v, ok := auditCategoryCN[c]; ok {
		return v
	}
	return c
}

func resultCN(r string) string {
	if r == "fail" {
		return "失败"
	}
	return "成功"
}
