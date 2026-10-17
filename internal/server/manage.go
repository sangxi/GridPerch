package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/sangxi/strix/internal/model"
	"github.com/sangxi/strix/internal/notify"
	"github.com/sangxi/strix/internal/store"
)

/* ---------------- 菜单与角色 ---------------- */

const (
	// RoleAdmin 拥有全部权限。
	RoleAdmin = "admin"
	// RoleOperator 可查看监控并处理告警、下发指令。
	RoleOperator = "operator"
	// RoleViewer 只读。
	RoleViewer = "viewer"
)

// allMenus 控制台全部菜单项。
var allMenus = []string{"dashboard", "hosts", "catalogs", "onboard", "releases", "tasks", "alerts", "commands", "tools",
	"assets", "devices", "inspections", "screens", "members", "tokens", "license", "audit", "settings"}

// roleDefaultMenus 各角色的默认可见菜单。
// onboard（添加主机）会暴露采集端通信密钥，license（授权管理）涉及授权与机器码，
// 两者都不下放给只读账号；releases 只读可见（看升级进度），上传/删除仍限管理员。
// catalogs（主机目录）新建/改名/删除限管理员，operator/viewer 只读查看归类。
// tools（工具箱）会下发命令到目标主机，等同指令下发，因此只给管理员与运维，只读账号不可见。
var roleDefaultMenus = map[string][]string{
	RoleAdmin:    allMenus,
	RoleOperator: {"dashboard", "hosts", "catalogs", "onboard", "releases", "tasks", "alerts", "commands", "tools", "assets", "devices", "inspections", "screens"},
	RoleViewer:   {"dashboard", "hosts", "catalogs", "tasks", "alerts", "screens"},
}

// MenusOf 返回成员可见菜单：管理员全量，其余按账号配置或角色默认值。
func MenusOf(m *model.Member) []string {
	if m == nil {
		return []string{"dashboard"}
	}
	if m.Role == RoleAdmin {
		return allMenus
	}
	if strings.TrimSpace(m.Menus) != "" {
		var out []string
		for _, item := range strings.Split(m.Menus, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if v, ok := roleDefaultMenus[m.Role]; ok {
		return v
	}
	return roleDefaultMenus[RoleViewer]
}

// handleTaskTypes 返回全部监控项类型及其执行端分组，供前端动态渲染配置表单。
func (s *Server) handleTaskTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"agent":  store.AgentTypes,
		"server": store.ServerTypes,
		"group":  store.TaskTypeGroups,
	})
}

// handleLoginEvents 返回主机登录提醒流水。
func (s *Server) handleLoginEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.store.ListLoginEvents(limit)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, list)
}

// handleHostProcesses 返回主机最近一次上报的进程快照。
func (s *Server) handleHostProcesses(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.store.LatestProcesses(r.PathValue("host"), limit)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, list)
}

/* ---------------- 资产设备台账 ---------------- */

func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		list, err := s.store.ListAssets(q.Get("keyword"), q.Get("category"), q.Get("status"))
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, list)
	case http.MethodPost:
		var a model.Asset
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&a); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败：" + err.Error()})
			return
		}
		if err := s.store.SaveAsset(&a); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "id": a.ID})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

func (s *Server) handleAssetItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodPut:
		var a model.Asset
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&a); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
			return
		}
		a.ID = id
		if err := s.store.SaveAsset(&a); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodDelete:
		if err := s.store.DeleteAsset(id); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

// handleAssetBatch 批量导入资产台账。
func (s *Server) handleAssetBatch(w http.ResponseWriter, r *http.Request) {
	var list []model.Asset
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&list); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败：" + err.Error()})
		return
	}
	n, err := s.store.BatchCreateAssets(list)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "count": n})
}

// handleAssetStats 返回资产状态分布。
func (s *Server) handleAssetStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.AssetStats()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, stats)
}

// handleAssetExport 导出资产台账，支持 csv 与 xlsx。
func (s *Server) handleAssetExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := s.store.ListAssets(q.Get("keyword"), q.Get("category"), q.Get("status"))
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	header := []string{"名称", "类别", "型号", "IP", "MAC", "操作系统", "配置", "厂商", "序列号",
		"位置", "责任人", "部门", "购入日期", "维保到期", "状态", "关联主机", "备注"}
	rows := make([][]string, 0, len(list)+1)
	rows = append(rows, header)
	for _, a := range list {
		rows = append(rows, []string{a.Name, a.Category, a.Type, a.IP, a.Mac, a.OS, a.Spec, a.Vendor, a.SN,
			a.Location, a.Owner, a.Department, a.BuyDate, a.ExpireDate, assetStatusCN(a.Status), a.MonitorBind, a.Remark})
	}
	writeTableFile(w, "资产台账", "assets", rows, q.Get("format"))
}

func assetStatusCN(v string) string {
	switch v {
	case "inuse":
		return "在用"
	case "idle":
		return "闲置"
	case "repair":
		return "维修"
	case "scrap":
		return "报废"
	case "storage":
		return "库存"
	default:
		return v
	}
}

/* ---------------- 设备账号 ---------------- */

func (s *Server) handleDeviceAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListDeviceAccounts(r.URL.Query().Get("assetId"))
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, list)
	case http.MethodPost:
		var a model.DeviceAccount
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&a); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
			return
		}
		if err := s.store.SaveDeviceAccount(&a); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "id": a.ID})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

func (s *Server) handleDeviceAccountItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodPut:
		var a model.DeviceAccount
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&a); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
			return
		}
		a.ID = id
		if err := s.store.SaveDeviceAccount(&a); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodDelete:
		if err := s.store.DeleteDeviceAccount(id); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

/* ---------------- 成员账号与权限 ---------------- */

func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListMembers()
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, list)
	case http.MethodPost:
		var req struct {
			model.Member
			Password string `json:"password"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
			return
		}
		applyMemberDefaults(body, &req.Member)
		if req.Username == "" || req.Password == "" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "用户名和密码不能为空"})
			return
		}
		if req.Role == "" {
			req.Role = RoleViewer
		}
		// 授权配额：新增账号前校验上限
		if st := s.licenseStatus(); st.MaxUsers > 0 && s.store.CountMembers() >= st.MaxUsers {
			writeJSONStatus(w, http.StatusForbidden, map[string]any{"ok": false, "msg": fmt.Sprintf(
				"控制台账号数已达授权上限 %d 个（%s），请在「授权管理」中导入更高档位的授权码", st.MaxUsers, st.Edition)})
			return
		}
		if err := s.store.CreateMember(req.Username, req.Password, &req.Member); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

func (s *Server) handleMemberItem(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	switch r.Method {
	case http.MethodPut:
		var req struct {
			model.Member
			Password string `json:"password"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
			return
		}
		applyMemberDefaults(body, &req.Member)
		if req.Role == "" {
			req.Role = RoleViewer
		}
		if err := s.store.UpdateMember(username, req.Password, &req.Member); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodDelete:
		if err := s.store.DeleteMember(username); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

// applyMemberDefaults 补齐成员字段缺省值：未显式传 enabled 时默认启用。
func applyMemberDefaults(body []byte, m *model.Member) {
	keys := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &keys); err != nil {
		return
	}
	if _, ok := keys["enabled"]; !ok {
		m.Enabled = true
	}
	if _, ok := keys["role"]; !ok && m.Role == "" {
		m.Role = RoleViewer
	}
}

/* ---------------- 开放接口凭证 ---------------- */

func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListAPITokens()
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, list)
	case http.MethodPost:
		var req struct {
			Name  string `json:"name"`
			Perms string `json:"perms"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
		if req.Name == "" {
			req.Name = "未命名接口"
		}
		if req.Perms == "" {
			req.Perms = "hosts,alerts,metrics,monitors,assets"
		}
		t, err := s.store.CreateAPIToken(req.Name, req.Perms)
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "token": t.Token, "id": t.ID})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

func (s *Server) handleTokenItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
		return
	}
	if err := s.store.DeleteAPIToken(r.PathValue("id")); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

/* ---------------- 系统设置与界面定制 ---------------- */

// publicSettingKeys 无需登录即可读取的界面定制项。
var publicSettingKeys = []string{"systemName", "logoData", "hideFooter", "footerText", "alertSound"}

func (s *Server) handlePublicSettings(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.Settings()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	out := map[string]string{}
	for _, k := range publicSettingKeys {
		out[k] = all[k]
	}
	if out["systemName"] == "" {
		out["systemName"] = "Strix"
	}
	writeJSON(w, out)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.Settings()
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		// 口令类配置不回传明文，只告知前端是否已设置
		writeJSON(w, s.maskSettings(list))
	case http.MethodPut:
		kv := map[string]string{}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<22)).Decode(&kv); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
			return
		}
		if err := s.prepareSettings(kv); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		if err := s.store.SaveSettings(kv); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

// handleTestMail 发送一封测试邮件，验证邮件告警配置是否正确，并记录发送结果。
func (s *Server) handleTestMail(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.Settings()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	cfg := s.mailConfigFrom(settings)
	if !cfg.Enabled() {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "请先完整填写 SMTP 服务器、发件人与收件人"})
		return
	}
	subject := cfg.Subject(notify.Alert{Hostname: "测试主机", Level: "info"})
	alerts := []notify.Alert{{Hostname: "测试主机", Type: "info", Level: "info",
		Content: "这是一封来自 Strix 的测试邮件，说明邮件告警通道配置正确。", CreateTime: timeNowString()}}
	if err := notify.Send(cfg, alerts); err != nil {
		s.store.AppendMailLog(model.MailLog{Kind: "test", Subject: subject, Recipients: cfg.To,
			ItemCount: 1, Status: "fail", Error: err.Error()})
		_ = s.store.SaveSettings(map[string]string{"mailLastError": err.Error(), "mailLastTime": timeNowString()})
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "发送失败：" + err.Error()})
		return
	}
	s.store.AppendMailLog(model.MailLog{Kind: "test", Subject: subject, Recipients: cfg.To,
		ItemCount: 1, Status: "ok"})
	_ = s.store.SaveSettings(map[string]string{"mailLastError": "", "mailLastTime": timeNowString(),
		"mailLastCount": "1"})
	writeJSON(w, map[string]any{"ok": true, "msg": "测试邮件已发送到 " + cfg.To})
}
