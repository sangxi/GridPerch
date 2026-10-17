package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/sangxi/strix/internal/model"
	"github.com/sangxi/strix/internal/store"
)

// catalogForm 主机目录的请求体。ID 为空表示新建。
type catalogForm struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Remark string `json:"remark"`
	Sort   int    `json:"sort"`
	// Hosts 非空时批量把该目录（或未分组）下的主机移到 Hosts 指定的目录。
	Hosts      []string `json:"hosts"`
	MoveToHost string   `json:"moveToCatalogId"`
}

// hostCatalogs 主机目录列表 / 新建。
// 目录名允许中文与空格，仅去除首尾空白与控制字符。
func (s *Server) hostCatalogs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListHostCatalogs()
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		if list == nil {
			list = []store.HostCatalog{}
		}
		writeJSON(w, map[string]any{"ok": true, "list": list})
	case http.MethodPost:
		var req catalogForm
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
			return
		}
		c := &store.HostCatalog{ID: req.ID, Name: req.Name, Remark: req.Remark, Sort: req.Sort}
		if err := s.store.SaveHostCatalog(c); err != nil {
			writeJSONStatus(w, catalogStatus(err), map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		// 同一请求里可以顺带把一批主机移进来
		if len(req.Hosts) > 0 {
			if err := s.store.SetHostCatalogBatch(req.Hosts, c.ID); err != nil {
				writeJSONStatus(w, catalogStatus(err), map[string]any{"ok": false, "msg": err.Error()})
				return
			}
		}
		writeJSON(w, map[string]any{"ok": true, "catalog": c,
			"msg": "已保存目录 " + c.Name})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

// hostCatalogItem 单个目录的修改 / 删除。
func (s *Server) hostCatalogItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "缺少目录 ID"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		// 允许只改名称，所以先读出原有字段再覆盖
		cur, err := s.store.GetHostCatalog(id)
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		if cur == nil {
			writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "主机目录不存在"})
			return
		}
		var req catalogForm
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
			return
		}
		if strings.TrimSpace(req.Name) != "" {
			cur.Name = req.Name
		}
		cur.Remark = req.Remark
		cur.Sort = req.Sort
		if err := s.store.SaveHostCatalog(cur); err != nil {
			writeJSONStatus(w, catalogStatus(err), map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		if len(req.Hosts) > 0 {
			if err := s.store.SetHostCatalogBatch(req.Hosts, req.MoveToHost); err != nil {
				writeJSONStatus(w, catalogStatus(err), map[string]any{"ok": false, "msg": err.Error()})
				return
			}
		}
		writeJSON(w, map[string]any{"ok": true, "catalog": cur, "msg": "已保存目录 " + cur.Name})
	case http.MethodDelete:
		if err := s.store.DeleteHostCatalog(id); err != nil {
			writeJSONStatus(w, catalogStatus(err), map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "msg": "目录已删除，其下主机已移到「未分组」"})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

// hostCatalogMembers 返回某个目录下的主机（目录 ID 为空表示未分组）。
func (s *Server) hostCatalogMembers(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	hosts, err := s.store.ListHosts()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(hosts))
	for _, h := range hosts {
		if h.CatalogID == id {
			out = append(out, map[string]any{
				"hostname": h.Hostname, "ip": h.IP, "state": h.State,
				"cpuPer": h.CpuPer, "memPer": h.MemPer,
			})
		}
	}
	writeJSON(w, map[string]any{"ok": true, "list": out})
}

// hostCatalogAssign 把一台主机移到指定目录。
func (s *Server) hostCatalogAssign(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.PathValue("host"))
	if host == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "缺少主机名"})
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
		return
	}
	var req struct {
		CatalogID string `json:"catalogId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
		return
	}
	if err := s.store.SetHostCatalog(host, req.CatalogID); err != nil {
		writeJSONStatus(w, catalogStatus(err), map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	msg := "已移到「未分组」"
	if req.CatalogID != "" {
		if c, _ := s.store.GetHostCatalog(req.CatalogID); c != nil {
			msg = "已移到目录 " + c.Name
		}
	}
	writeJSON(w, map[string]any{"ok": true, "msg": msg})
}

// handleHostSysEvents 返回某台主机最近的系统事件。
// 当前只有 Windows 采集端会上报（读事件查看器），其他平台返回空列表。
func (s *Server) handleHostSysEvents(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.PathValue("host"))
	if host == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "缺少主机名"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.store.SysEventsOf(host, limit)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	if events == nil {
		events = []model.SysEvent{}
	}
	writeJSON(w, events)
}

// catalogStatus 把存储层错误映射成合适的 HTTP 状态码。
func catalogStatus(err error) int {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "不存在"):
		return http.StatusNotFound
	case strings.Contains(msg, "不能为空"), strings.Contains(msg, "已存在"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
