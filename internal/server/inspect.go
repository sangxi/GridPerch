package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/sangxi/gridperch/internal/store"
)

// handleInspections 列出巡检报告或生成新报告。
func (s *Server) handleInspections(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list, err := s.store.ListInspections(limit)
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, list)
	case http.MethodPost:
		var req struct {
			Title string `json:"title"`
			Scope string `json:"scope"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
		if req.Title == "" {
			req.Title = time.Now().Format("2006-01-02") + " 系统巡检报告"
		}
		if req.Scope == "" {
			req.Scope = "全部主机"
		}
		author := ""
		if m := s.currentUser(r); m != nil {
			author = m.Username
		}
		rep, err := s.store.BuildInspectionReport()
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		it, err := s.store.SaveInspection(req.Title, req.Scope, author, rep)
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "id": it.ID, "score": rep.Score, "summary": rep.Summary})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

// handleInspectionItem 读取或删除巡检报告。
func (s *Server) handleInspectionItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodGet:
		it, err := s.store.GetInspection(id)
		if err != nil {
			writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "报告不存在"})
			return
		}
		writeJSON(w, it)
	case http.MethodDelete:
		if err := s.store.DeleteInspection(id); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

// handleInspectionExport 导出巡检报告为 CSV/XLSX。
func (s *Server) handleInspectionExport(w http.ResponseWriter, r *http.Request) {
	it, err := s.store.GetInspection(r.PathValue("id"))
	if err != nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "报告不存在"})
		return
	}
	var rep store.InspectionReport
	if err := json.Unmarshal([]byte(it.Content), &rep); err != nil {
		rep.Items = nil
	}
	rows := [][]string{
		{"报告标题", it.Title},
		{"巡检范围", it.Scope},
		{"生成时间", it.CreateTime},
		{"执行人", it.Author},
		{"综合评分", strconv.Itoa(rep.Score)},
		{"结论摘要", rep.Summary},
		{},
		{"检查项", "状态", "说明", "影响"},
	}
	for _, item := range rep.Items {
		rows = append(rows, []string{item.Name, statusCN(item.Status), item.Detail, item.Impact})
	}
	rows = append(rows, []string{})
	rows = append(rows, []string{"整改建议"})
	for _, sg := range rep.Suggestions {
		rows = append(rows, []string{sg})
	}
	writeTableFile(w, "巡检报告", "inspection", rows, r.URL.Query().Get("format"))
}

func statusCN(v string) string {
	switch v {
	case "ok":
		return "正常"
	case "warn":
		return "需关注"
	case "bad":
		return "异常"
	default:
		return v
	}
}
