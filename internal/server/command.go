package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/sangxi/strix/internal/model"
)

// handleCommands 查询指令历史或下发新指令。
func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list, err := s.store.ListCommands(r.URL.Query().Get("host"), limit)
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, list)
	case http.MethodPost:
		var req struct {
			Host       string `json:"host"`
			Cmd        string `json:"cmd"`
			TimeoutSec int    `json:"timeoutSec"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<18)).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
			return
		}
		if req.Host == "" || req.Cmd == "" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "目标主机与指令内容不能为空"})
			return
		}
		if len(req.Cmd) > 4000 {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "指令内容过长"})
			return
		}
		user := ""
		if m := s.currentUser(r); m != nil {
			user = m.Username
		}
		c, err := s.store.CreateCommand(req.Host, req.Cmd, user, req.TimeoutSec)
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "id": c.ID})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

// handleCommandItem 删除（撤销）指令记录。
func (s *Server) handleCommandItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
		return
	}
	if err := s.store.CancelCommand(r.PathValue("id")); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleAgentCommands 采集端拉取待执行指令。
func (s *Server) handleAgentCommands(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	hostname := r.URL.Query().Get("hostname")
	token := r.URL.Query().Get("wgToken")
	if hostname == "" || !s.cfg.TokenOK(token) {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"result": "error：token is invalidate"})
		return
	}
	list, err := s.store.PendingCommands(s.resolveAgentHost(hostname), 5)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"result": "error：" + err.Error()})
		return
	}
	writeJSON(w, list)
}

// handleAgentCommandResult 采集端回传指令执行结果。
func (s *Server) handleAgentCommandResult(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var body model.CommandResult
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body); err != nil {
		writeJSON(w, map[string]string{"result": "error：" + err.Error()})
		return
	}
	if body.Hostname == "" || !s.cfg.TokenOK(body.WgToken) {
		writeJSON(w, map[string]string{"result": "error：token is invalidate"})
		return
	}
	if err := s.store.FinishCommands(s.resolveAgentHost(body.Hostname), body.Results); err != nil {
		writeJSON(w, map[string]string{"result": "error：" + err.Error()})
		return
	}
	writeJSON(w, map[string]string{"result": "success"})
}
