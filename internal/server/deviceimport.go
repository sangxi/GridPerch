package server

import (
	"fmt"
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
		if username == "" && hostRaw == "" && bindRaw == "" {
			res.Skipped++
			continue
		}
		res.Total++

		// 关联主机：先按主机名，再按 IP；关联列本身也可能是 IP
		bind := ""
		var matched *model.Host
		for _, cand := range []string{bindRaw, hostRaw} {
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
		port, _ := strconv.Atoi(field("port"))
		protocol, port := normalizeProtocol(field("protocol"), port)
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
