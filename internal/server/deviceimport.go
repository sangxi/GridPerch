package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sangxi/gridperch/internal/model"
	"github.com/sangxi/gridperch/internal/spreadsheet"
)

/*
设备账号表格导入。

现场最常见的两种来源：
 1. 表格格式——Excel 里人工维护的表，表头是「主机 / IP / 账号 / 密码 / 备注」；
 2. 数据库格式——从 CMDB 或 DBA 表里 select 出来的结果，往往没有表头，
    就是 ip,username,password 三列。

两种都吃：先按表头识别，认不出表头就按「IP / 账号 / 口令」的位置兜底。
导入时按 IP 或主机名自动关联到监控主机，同一个「协议+地址+账号」重复导入
只更新口令与备注，不会生成重复记录。
*/

// importHeaderAliases 表头别名表，键是归一化后的表头文本。
var importHeaderAliases = map[string]string{
	// 名称
	"name": "name", "名称": "name", "账号名称": "name", "设备名称": "name", "系统名称": "name", "标题": "name",
	// 关联主机
	"bindhost": "bindHost", "关联主机": "bindHost", "关联设备": "bindHost", "主机": "bindHost",
	"主机名": "bindHost", "hostname": "bindHost", "host_name": "bindHost", "asset": "bindHost",
	// 地址
	"host": "host", "ip": "host", "地址": "host", "ip地址": "host", "ipaddr": "host",
	"ip_address": "host", "ipaddress": "host", "服务器地址": "host", "登录地址": "host",
	// 协议
	"protocol": "protocol", "协议": "protocol", "类型": "protocol", "type": "protocol", "连接方式": "protocol",
	// 端口
	"port": "port", "端口": "port", "端口号": "port",
	// 用户名
	"username": "username", "user": "username", "login": "username", "account": "username",
	"用户名": "username", "账号": "username", "帐号": "username", "登录名": "username",
	"登录账号": "username", "登录用户": "username", "用户": "username",
	// 口令
	"secret": "secret", "password": "secret", "passwd": "secret", "pwd": "secret", "pass": "secret",
	"口令": "secret", "密码": "secret", "登录密码": "secret",
	// 备注
	"remark": "remark", "note": "remark", "comment": "remark", "备注": "remark", "说明": "remark", "描述": "remark",
}

// importFieldOrder 位置兜底时各列的含义（数据库导出的 ip,user,password 就是这个顺序）。
var importFieldOrder = []string{"host", "username", "secret", "port", "remark"}

// deviceImportResult 导入结果，直接回给前端展示。
type deviceImportResult struct {
	Total         int      `json:"total"`
	Created       int      `json:"created"`
	Updated       int      `json:"updated"`
	Skipped       int      `json:"skipped"`
	HostMatched   int      `json:"hostMatched"`
	HostUnmatched int      `json:"hostUnmatched"`
	NoUser        int      `json:"noUser"` // 没有用户名的行：能建档，但登录不了
	UnmatchedList []string `json:"unmatchedList"`
	Errors        []string `json:"errors"`
}

// normalizeHeader 归一化表头：去空白、转小写、去掉常见分隔符与括号说明。
func normalizeHeader(s string) string {
	var sb strings.Builder
	depth := 0 // 括号内的说明文字整段丢掉：「登录密码（必填）」→「登录密码」
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch r {
		case '（', '(':
			depth++
			continue
		case '）', ')':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth > 0 {
			continue
		}
		switch r {
		case ' ', '\t', '_', '-', '·', '/', '\\', '：', ':':
			continue
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// mapHeaderRow 判断一行是否表头，并返回列下标到字段名的映射。
// 命中不足两列视为「不是表头」，交给位置兜底。
func mapHeaderRow(row []string) map[int]string {
	m := map[int]string{}
	for i, cell := range row {
		if f, ok := importHeaderAliases[normalizeHeader(cell)]; ok {
			if _, dup := m[i]; !dup {
				m[i] = f
			}
		}
	}
	if len(m) < 2 {
		return nil
	}
	return m
}

// osDefaults 按主机操作系统给出默认的协议、端口与用户名。
//
// 现场约定：Linux 一律 root + SSH 22，Windows 一律 administrator + 远程桌面 3389。
// 只在用户没写的时候补，写了就尊重表里的值 —— 有些 Windows 装了 OpenSSH，
// 有些人 Linux 用普通账号，不能被规则覆盖。
func osDefaults(osName string) (protocol string, port int, username string) {
	switch strings.ToLower(strings.TrimSpace(osName)) {
	case "linux", "unix", "freebsd", "openbsd", "netbsd", "solaris", "aix":
		return "ssh", 22, "root"
	case "windows":
		return "rdp", 3389, "administrator"
	}
	return "", 0, ""
}

// resolveAccountHost 按「关联主机 → 地址 → 名称」的顺序找到账号对应的监控主机。
func resolveAccountHost(a model.DeviceAccount, byName, byIP map[string]model.Host) *model.Host {
	for _, cand := range []string{a.BindHost, a.Host, a.Name} {
		cand = strings.TrimSpace(cand)
		if cand == "" {
			continue
		}
		key := strings.ToLower(cand)
		if h, ok := byName[key]; ok {
			return &h
		}
		if h, ok := byIP[key]; ok {
			return &h
		}
	}
	return nil
}

// handleDeviceSyncOS 按关联主机的操作系统，补全用户名并（可选）纠正协议。
func (s *Server) handleDeviceSyncOS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
		return
	}
	var req struct {
		SwitchProtocol bool `json:"switchProtocol"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)

	res, err := s.syncDeviceAccounts(req.SwitchProtocol)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	msg := fmt.Sprintf("已补全 %d 条用户名", res["userFilled"])
	if req.SwitchProtocol {
		msg += fmt.Sprintf("，按系统调整协议 %d 条", res["protocolSwitched"])
	}
	if res["unknownHost"] > 0 {
		msg += fmt.Sprintf("；%d 条找不到对应主机，未处理", res["unknownHost"])
	}
	writeJSON(w, map[string]any{"ok": true, "msg": msg, "result": res})
}

// autoFillDeviceAccounts 由后台定期调用，只补用户名、不碰协议，
// 让「先导入账号、后装采集端」的机器在主机出现后自动具备可登录的用户名。
func (s *Server) autoFillDeviceAccounts() {
	if res, err := s.syncDeviceAccounts(false); err == nil && res["userFilled"] > 0 {
		log.Printf("已按主机系统补全 %d 条设备账号的用户名", res["userFilled"])
	}
}

// syncDeviceAccounts 是「按系统补全」的实现，handler 与定时任务共用。
func (s *Server) syncDeviceAccounts(switchProtocol bool) (map[string]int, error) {
	hosts, err := s.store.ListHosts()
	if err != nil {
		return nil, err
	}
	byName, byIP := map[string]model.Host{}, map[string]model.Host{}
	for _, h := range hosts {
		byName[strings.ToLower(h.Hostname)] = h
		if h.IP != "" {
			byIP[strings.ToLower(h.IP)] = h
		}
	}
	accounts, err := s.store.ListDeviceAccounts("")
	if err != nil {
		return nil, err
	}
	var userFilled, protoSwitched, unknownHost int
	for _, a := range accounts {
		h := resolveAccountHost(a, byName, byIP)
		if h == nil {
			unknownHost++
			continue
		}
		defProto, defPort, defUser := osDefaults(h.OS)
		if defProto == "" {
			unknownHost++
			continue
		}
		changed := false
		if strings.TrimSpace(a.Username) == "" {
			a.Username = defUser
			changed = true
			userFilled++
		}
		if switchProtocol && !strings.EqualFold(a.Protocol, defProto) {
			// 端口只在「还是另一套默认值」时才改，避免把用户自定义的端口冲掉
			if a.Port == 0 || a.Port == 22 || a.Port == 3389 {
				a.Port = defPort
			}
			a.Protocol = defProto
			changed = true
			protoSwitched++
		}
		if !changed {
			continue
		}
		if a.BindHost == "" {
			a.BindHost = h.Hostname
		}
		if err := s.store.SaveDeviceAccount(&a); err != nil {
			log.Printf("按系统补全 %s 失败: %v", a.Name, err)
		}
	}
	return map[string]int{
		"scanned": len(accounts), "userFilled": userFilled,
		"protocolSwitched": protoSwitched, "unknownHost": unknownHost,
	}, nil
}

// normalizeProtocol 统一协议写法，返回协议代号与默认端口。
func normalizeProtocol(raw string, port int) (string, int) {
	p := normalizeHeader(raw)
	switch {
	case p == "":
		p = "ssh"
	case strings.Contains(p, "rdp"), strings.Contains(p, "远程桌面"), strings.Contains(p, "桌面"),
		strings.Contains(p, "windows"), strings.Contains(p, "3389"):
		p = "rdp"
	case strings.Contains(p, "sftp"):
		p = "sftp"
	case strings.Contains(p, "ssh"), strings.Contains(p, "linux"), strings.Contains(p, "unix"):
		p = "ssh"
	default:
		// 未知写法按 SSH 处理，避免整行被丢弃
		p = "ssh"
	}
	if port <= 0 {
		if p == "rdp" {
			port = 3389
		} else {
			port = 22
		}
	}
	return p, port
}

// handleDeviceAccountImport 接收上传的表格并批量写入设备账号。
func (s *Server) handleDeviceAccountImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "解析上传文件失败：" + err.Error()})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "请选择要导入的表格文件"})
		return
	}
	defer file.Close()
	buf := make([]byte, 0, 64<<10)
	tmp := make([]byte, 32<<10)
	for {
		n, rerr := file.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if rerr != nil {
			break
		}
	}
	rows, err := spreadsheet.Read(header.Filename, buf)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	res, err := s.importDeviceRows(rows)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	msg := fmt.Sprintf("导入完成：新增 %d 条，更新 %d 条", res.Created, res.Updated)
	if res.HostMatched > 0 {
		msg += fmt.Sprintf("，关联主机 %d 条", res.HostMatched)
	}
	if res.Skipped > 0 {
		msg += fmt.Sprintf("，跳过 %d 行", res.Skipped)
	}
	if res.NoUser > 0 {
		msg += fmt.Sprintf("；其中 %d 条没有用户名，补全前无法登录", res.NoUser)
	}
	writeJSON(w, map[string]any{"ok": true, "msg": msg, "result": res})
}

// importDeviceRows 是导入的核心：识别表头、拼装账号、关联主机、去重写入。
func (s *Server) importDeviceRows(rows [][]string) (*deviceImportResult, error) {
	res := &deviceImportResult{}
	hosts, err := s.store.ListHosts()
	if err != nil {
		return nil, fmt.Errorf("读取主机列表失败: %w", err)
	}
	byName := map[string]model.Host{}
	byIP := map[string]model.Host{}
	for _, h := range hosts {
		byName[strings.ToLower(h.Hostname)] = h
		if h.IP != "" {
			byIP[strings.ToLower(h.IP)] = h
		}
	}
	// 已有账号按「协议|地址|账号」去重
	existing, err := s.store.ListDeviceAccounts("")
	if err != nil {
		return nil, fmt.Errorf("读取已有账号失败: %w", err)
	}
	seen := map[string]model.DeviceAccount{}
	for _, a := range existing {
		seen[accountKey(a.Protocol, a.Host, a.Username)] = a
	}

	start := 0
	var cols map[int]string
	// 表头可能被标题行/空行顶下去，前 5 行里找一个像表头的
	for i := 0; i < len(rows) && i < 5; i++ {
		if m := mapHeaderRow(rows[i]); m != nil {
			cols, start = m, i+1
			break
		}
	}
	if cols == nil {
		cols = map[int]string{}
		for i, f := range importFieldOrder {
			cols[i] = f
		}
	}

	for idx := start; idx < len(rows); idx++ {
		row := rows[idx]
		lineNo := idx + 1
		if isBlankRow(row) {
			continue
		}
		field := func(name string) string {
			for i, f := range cols {
				if f == name && i < len(row) {
					return strings.TrimSpace(row[i])
				}
			}
			return ""
		}
		hostRaw := field("host")
		bindRaw := field("bindHost")
		username := field("username")
		secret := field("secret")
		portRaw := field("port")
		// 地址列里常把端口写在一起（192.168.40.69:9801），直接当主机名会拼成
		// 「192.168.40.69:9801:22」这种非法地址，这里拆开
		if portRaw == "" {
			if h, p, ok := splitHostPort(hostRaw); ok {
				hostRaw, portRaw = h, p
			}
		}
		if username == "" && hostRaw == "" && bindRaw == "" {
			res.Skipped++
			continue
		}
		res.Total++

		// 关联主机：先按主机名，再按 IP；关联列本身也可能是 IP
		bind := ""
		var matched *model.Host
		// 第三个候选是「名称」：现场常把主机名直接写进名称列
		for _, cand := range []string{bindRaw, hostRaw, field("name")} {
			if cand == "" {
				continue
			}
			key := strings.ToLower(cand)
			if h, ok := byName[key]; ok {
				matched = &h
				break
			}
			if h, ok := byIP[key]; ok {
				matched = &h
				break
			}
		}
		if matched != nil {
			bind = matched.Hostname
			res.HostMatched++
			if hostRaw == "" {
				hostRaw = matched.IP
			}
		} else {
			res.HostUnmatched++
			label := hostRaw
			if label == "" {
				label = bindRaw
			}
			if label != "" && len(res.UnmatchedList) < 20 && !containsString(res.UnmatchedList, label) {
				res.UnmatchedList = append(res.UnmatchedList, label)
			}
		}
		if hostRaw == "" {
			res.Skipped++
			res.Total--
			if len(res.Errors) < 20 {
				res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行：缺少地址与关联主机，已跳过", lineNo))
			}
			continue
		}
		port, _ := strconv.Atoi(portRaw)
		rawProto := field("protocol")
		protocol, port := normalizeProtocol(rawProto, port)
		// 表里没写协议 / 用户名时，按关联主机的系统补默认值：
		// Linux → root + SSH 22，Windows → administrator + 远程桌面 3389。
		// 表里写了的以表为准。
		if matched != nil {
			if defProto, defPort, defUser := osDefaults(matched.OS); defProto != "" {
				if strings.TrimSpace(rawProto) == "" {
					protocol = defProto
					if portRaw == "" {
						port = defPort
					}
				}
				if username == "" {
					username = defUser
				}
			}
		}
		if username == "" {
			res.NoUser++
		}
		name := field("name")
		if name == "" {
			name = fmt.Sprintf("%s-%s", hostRaw, username)
			if username == "" {
				name = hostRaw
			}
		}
		acc := model.DeviceAccount{
			Name:     name,
			BindHost: bind,
			Protocol: protocol,
			Host:     hostRaw,
			Port:     port,
			Username: username,
			Secret:   secret,
			Remark:   field("remark"),
		}
		key := accountKey(protocol, hostRaw, username)
		if old, ok := seen[key]; ok {
			acc.ID = old.ID
			// 表格里没写备注时保留原备注，避免导入把人工补充的说明冲掉
			if acc.Remark == "" {
				acc.Remark = old.Remark
			}
			if acc.BindHost == "" {
				acc.BindHost = old.BindHost
			}
			if err := s.store.SaveDeviceAccount(&acc); err != nil {
				if len(res.Errors) < 20 {
					res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行更新失败：%v", lineNo, err))
				}
				continue
			}
			res.Updated++
			continue
		}
		if err := s.store.SaveDeviceAccount(&acc); err != nil {
			if len(res.Errors) < 20 {
				res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行写入失败：%v", lineNo, err))
			}
			continue
		}
		seen[key] = acc
		res.Created++
	}
	return res, nil
}

// accountKey 设备账号去重键：协议 + 地址 + 用户名，全部小写。
func accountKey(protocol, host, username string) string {
	return strings.ToLower(strings.TrimSpace(protocol)) + "|" +
		strings.ToLower(strings.TrimSpace(host)) + "|" +
		strings.ToLower(strings.TrimSpace(username))
}

// splitHostPort 把「192.168.40.69:9801」拆成主机与端口。
// 只在冒号后是纯数字且端口在合法范围内时才拆，避免把 IPv6 或带冒号的
// 主机别名误拆。
func splitHostPort(s string) (host, port string, ok bool) {
	s = strings.TrimSpace(s)
	i := strings.LastIndex(s, ":")
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	p := strings.TrimSpace(s[i+1:])
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return "", "", false
	}
	h := strings.TrimSpace(s[:i])
	if h == "" || strings.Contains(h, ":") {
		return "", "", false
	}
	return h, p, true
}

func isBlankRow(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// confirmCurrentUser 校验「当前登录账号的密码」，用于敏感操作前的二次确认。
//
// 只比对会话里的用户名，不接受请求里传来的用户名 —— 否则就成了任意账号的
// 口令爆破入口。校验失败返回 false，调用方负责回 403。
//
// 注意：这里拿到的明文口令只用于比对，**绝不写日志、绝不回传、绝不入库**。
func (s *Server) confirmCurrentUser(r *http.Request, password string) bool {
	m := s.currentUser(r)
	if m == nil || strings.TrimSpace(password) == "" {
		return false
	}
	return s.store.CheckUser(m.Username, password)
}

// handleDeviceSecret 返回解密后的口令，供「复制密码」按钮使用。
//
// 刻意用 POST 而不是 GET：审计中间件只记录写操作，而「查看明文口令」是敏感动作，
// 必须留下「谁在什么时候看了哪条账号」的记录。响应也带 no-store，避免被缓存。
//
// 调用前必须带上当前登录账号的密码做二次确认：会话 cookie 被旁人拿到时，
// 至少还要知道本人的口令才能把明文密码取走。
func (s *Server) handleDeviceSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
	if !s.confirmCurrentUser(r, req.Password) {
		writeJSONStatus(w, http.StatusForbidden, map[string]any{"ok": false, "msg": "当前账号密码不正确，无法查看口令"})
		return
	}
	acc, err := s.store.DeviceCredential(r.PathValue("id"))
	if err != nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "设备账号不存在"})
		return
	}
	if acc.Secret == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "该账号还没有保存口令"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{
		"ok": true, "secret": acc.Secret, "username": acc.Username,
		"host": acc.Host, "port": acc.Port, "protocol": acc.Protocol, "name": acc.Name,
	})
}

// handleDeviceRelink 给尚未关联主机的账号补关联。
//
// 导入时的匹配只反映「当时」的主机表；之后新装采集端的机器、改过 IP 的机器，
// 都需要再兜一次。用户也可以点「一键关联主机」立即触发。
func (s *Server) handleDeviceRelink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
		return
	}
	n, err := s.store.RelinkDeviceAccounts()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	msg := fmt.Sprintf("已为 %d 条账号补上关联主机", n)
	if n == 0 {
		msg = "没有可补的账号：未关联的账号在主机表里找不到相同 IP 或主机名"
	}
	writeJSON(w, map[string]any{"ok": true, "linked": n, "msg": msg})
}

// relinkDeviceAccounts 由后台定期调用，静默补关联。
func (s *Server) relinkDeviceAccounts() {
	n, err := s.store.RelinkDeviceAccounts()
	if err != nil {
		log.Printf("设备账号补关联失败: %v", err)
		return
	}
	if n > 0 {
		log.Printf("已为 %d 条设备账号补上关联主机", n)
	}
}

// handleDeviceImportTemplate 下载一份 CSV 模板，省得用户猜列名。
func (s *Server) handleDeviceImportTemplate(w http.ResponseWriter, r *http.Request) {
	body := "\xEF\xBB\xBF" + "名称,关联主机,地址,协议,端口,账号,密码,备注\r\n" +
		"核心交换机 SSH,,192.168.40.1,ssh,22,admin,******,机房 A 区\r\n" +
		"财务系统远程桌面,财务服务器,192.168.40.88,rdp,3389,administrator,******,月度结账用\r\n"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	// 中文文件名走 RFC 5987：QueryEscape 会把空格转成 +，这里换回 %20
	w.Header().Set("Content-Disposition", "attachment; filename=device-accounts-template.csv; filename*=UTF-8''"+
		strings.ReplaceAll(url.QueryEscape("设备账号导入模板.csv"), "+", "%20"))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
}
